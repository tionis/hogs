package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"github.com/tionis/hogs/auth"
	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/modmanager"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
)

//go:embed templates/*.html assets/*
var templateFS embed.FS

// WebHandler handles frontend requests.
type WebHandler struct {
	Store  *database.Store
	Config *config.Config
	Auth   *auth.Authenticator
}

// NewWebHandler creates a new WebHandler.
func NewWebHandler(store *database.Store, cfg *config.Config, auth *auth.Authenticator) *WebHandler {
	return &WebHandler{Store: store, Config: cfg, Auth: auth}
}

type BackgroundURLs struct {
	Dark  string
	Light string
}

func (h *WebHandler) pickBackgrounds(gameType string) BackgroundURLs {
	urls := BackgroundURLs{}

	dark, err := h.Store.GetRandomBackground("dark", gameType)
	if err == nil && dark != nil {
		urls.Dark = dark.URL()
	} else {
		dark, err = h.Store.GetRandomBackground("all", gameType)
		if err == nil && dark != nil {
			urls.Dark = dark.URL()
		}
	}

	light, err := h.Store.GetRandomBackground("light", gameType)
	if err == nil && light != nil {
		urls.Light = light.URL()
	} else {
		light, err = h.Store.GetRandomBackground("all", gameType)
		if err == nil && light != nil {
			urls.Light = light.URL()
		}
	}

	return urls
}

func (h *WebHandler) userRole(r *http.Request) string {
	if h.Auth == nil {
		return ""
	}
	return h.Auth.GetUserRole(r)
}

type PterodactylLinkData struct {
	ServerID       int                           `json:"serverId"`
	PteroServerID  string                        `json:"pteroServerId"`
	AllowedActions []string                      `json:"allowedActions"`
	Commands       []database.PterodactylCommand `json:"commands"`
}

type AdminServerRow struct {
	Server    database.Server
	PteroLink *PterodactylLinkData
}

func (h *WebHandler) adminServerRows(servers []database.Server) []AdminServerRow {
	rows := make([]AdminServerRow, len(servers))
	for i, srv := range servers {
		row := AdminServerRow{Server: srv}
		link, err := h.Store.GetPterodactylLink(srv.ID)
		if err == nil && link != nil {
			var actions []string
			json.Unmarshal([]byte(link.AllowedActions), &actions)
			commands, _ := h.Store.ListPterodactylCommands(srv.ID)
			if commands == nil {
				commands = []database.PterodactylCommand{}
			}
			row.PteroLink = &PterodactylLinkData{
				ServerID:       srv.ID,
				PteroServerID:  link.PteroServerID,
				AllowedActions: actions,
				Commands:       commands,
			}
		}
		rows[i] = row
	}
	return rows
}

func (h *WebHandler) siteName() string {
	name, err := h.Store.GetSetting("site_name")
	if err != nil || name == "" {
		return "HOGS"
	}
	return name
}

// ... (Home, ServerDetail, Admin handlers remain unchanged) ...

// FileManager renders the file manager for a specific server.
func (h *WebHandler) FileManager(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	serverName := vars["serverName"]

	server, err := h.Store.GetServerByName(serverName)
	if err != nil || server == nil {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	modTree, err := modmanager.ScanModDirectory(h.Config.GameDataPath, serverName)
	// If dir not found, maybe just empty tree or create it?
	// Create if not exists to allow uploading
	if err != nil && strings.Contains(err.Error(), "not found") {
		os.MkdirAll(filepath.Join(h.Config.GameDataPath, serverName), 0755)
		modTree = &modmanager.ModItem{Name: serverName, Type: modmanager.TypeDir, Path: ""}
	} else if err != nil {
		http.Error(w, "Error scanning files: "+err.Error(), http.StatusInternalServerError)
		return
	}

	data := struct {
		Server         *database.Server
		Authenticated  bool
		UserRole       string
		SiteName       string
		UserEmail      string
		Files          *modmanager.ModItem
		BackgroundURLs BackgroundURLs
	}{
		Server:         server,
		Authenticated:  true,
		UserRole:       "admin",
		SiteName:       h.siteName(),
		UserEmail:      h.Auth.GetUserEmail(r),
		Files:          modTree,
		BackgroundURLs: h.pickBackgrounds(""),
	}

	tmpl, err := template.New("base.html").Funcs(sharedFuncMap()).ParseFS(templateFS, "templates/base.html", "templates/filemanager.html")
	if err != nil {
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		http.Error(w, "Render error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	buf.WriteTo(w)
}

// HandleFileUpload handles uploading files.
func (h *WebHandler) HandleFileUpload(w http.ResponseWriter, r *http.Request) {
	// Limit 1GB (adjust as needed)
	r.ParseMultipartForm(1024 << 20)

	serverName := r.FormValue("serverName")
	relPath := r.FormValue("path")

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Error retrieving file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	if !isValidPath(serverName, relPath) {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	targetDir := filepath.Join(h.Config.GameDataPath, serverName, relPath)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		http.Error(w, "Error creating directory", http.StatusInternalServerError)
		return
	}

	targetPath := filepath.Join(targetDir, header.Filename)
	out, err := os.Create(targetPath)
	if err != nil {
		http.Error(w, "Error saving file", http.StatusInternalServerError)
		return
	}
	defer out.Close()

	if _, err := io.Copy(out, file); err != nil {
		http.Error(w, "Error writing file", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/files/"+serverName, http.StatusFound)
}

// HandleFileDelete handles deleting files or directories.
func (h *WebHandler) HandleFileDelete(w http.ResponseWriter, r *http.Request) {
	serverName := r.FormValue("serverName")
	relPath := r.FormValue("path") // full relative path including filename

	if !isValidPath(serverName, relPath) {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	targetPath := filepath.Join(h.Config.GameDataPath, serverName, relPath)
	if err := os.RemoveAll(targetPath); err != nil {
		http.Error(w, "Error deleting file", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/files/"+serverName, http.StatusFound)
}

// HandleMkdir handles creating directories.
func (h *WebHandler) HandleMkdir(w http.ResponseWriter, r *http.Request) {
	serverName := r.FormValue("serverName")
	relPath := r.FormValue("path") // parent dir
	dirName := r.FormValue("dirname")

	if !isValidPath(serverName, relPath) || !isValidPath(serverName, dirName) {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	targetPath := filepath.Join(h.Config.GameDataPath, serverName, relPath, dirName)
	if err := os.MkdirAll(targetPath, 0755); err != nil {
		http.Error(w, "Error creating directory", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/files/"+serverName, http.StatusFound)
}

// ServeAssets serves static assets embedded in the binary.
func (h *WebHandler) ServeAssets(w http.ResponseWriter, r *http.Request) {
	http.FileServer(http.FS(templateFS)).ServeHTTP(w, r)
}

func isValidPath(serverName, path string) bool {
	// prevent .. traversal
	if strings.Contains(path, "..") || strings.Contains(serverName, "..") {
		return false
	}
	// serverName restricted chars check
	for _, r := range serverName {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// ... (existing Create/Update/Delete handlers) ...

// Home renders the main server list page.
func (h *WebHandler) Home(w http.ResponseWriter, r *http.Request) {
	allServers, err := h.Store.ListServers()
	if err != nil {
		http.Error(w, "Failed to load servers", http.StatusInternalServerError)
		return
	}

	isAuthenticated := h.Auth != nil && h.Auth.IsAuthenticated(r)

	// Filter servers
	var visibleServers []database.Server
	for _, s := range allServers {
		// "offline" state hides the server from public view.
		// "auto" state shows it, and the frontend determines the badge status.
		if s.State != "offline" || isAuthenticated {
			visibleServers = append(visibleServers, s)
		}
	}

	gameTypeSet := make(map[string]bool)
	for _, s := range visibleServers {
		gameTypeSet[s.GameType] = true
	}
	var gameTypes []string
	for gt := range gameTypeSet {
		gameTypes = append(gameTypes, gt)
	}

	data := struct {
		Servers        []database.Server
		GameTypes      []string
		Authenticated  bool
		UserRole       string
		SiteName       string
		BackgroundURLs BackgroundURLs
	}{
		Servers:        visibleServers,
		GameTypes:      gameTypes,
		Authenticated:  isAuthenticated,
		UserRole:       h.userRole(r),
		SiteName:       h.siteName(),
		BackgroundURLs: h.pickBackgrounds(""),
	}

	tmpl, err := template.New("base.html").Funcs(sharedFuncMap()).ParseFS(templateFS, "templates/base.html", "templates/index.html")
	if err != nil {
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		http.Error(w, "Render error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	buf.WriteTo(w)
}

// ServerDetail renders the detail page for a specific server.
func (h *WebHandler) ServerDetail(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	serverName := vars["serverName"]

	server, err := h.Store.GetServerByName(serverName)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if server == nil {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	isAuthenticated := h.Auth != nil && h.Auth.IsAuthenticated(r)

	// Access control: if offline and not admin, return 404
	if server.State == "offline" && !isAuthenticated {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	data := struct {
		Server          *database.Server
		Authenticated   bool
		UserRole        string
		SiteName        string
		BackgroundURLs  BackgroundURLs
		PteroConfigured bool
		PteroLink       *database.PterodactylLink
		PteroCommands   []database.PterodactylCommand
		AllowedActions  []string
	}{
		Server:          server,
		Authenticated:   isAuthenticated,
		UserRole:        h.userRole(r),
		SiteName:        h.siteName(),
		BackgroundURLs:  h.pickBackgrounds(server.GameType),
		PteroConfigured: h.Config.PterodactylURL != "",
		PteroLink:       nil,
		PteroCommands:   nil,
		AllowedActions:  nil,
	}

	if h.Config.PterodactylURL != "" {
		link, _ := h.Store.GetPterodactylLink(server.ID)
		data.PteroLink = link
		if link != nil {
			commands, _ := h.Store.ListPterodactylCommands(server.ID)
			data.PteroCommands = commands
		}
	}

	var allowedActions []string
	if data.PteroLink != nil {
		json.Unmarshal([]byte(data.PteroLink.AllowedActions), &allowedActions)
	}
	data.AllowedActions = allowedActions

	tmpl, err := template.New("base.html").Funcs(sharedFuncMap()).ParseFS(templateFS, "templates/base.html", "templates/server.html")
	if err != nil {
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		http.Error(w, "Render error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	buf.WriteTo(w)
}

// Admin renders the admin dashboard.
func (h *WebHandler) Admin(w http.ResponseWriter, r *http.Request) {
	servers, err := h.Store.ListServers()
	if err != nil {
		http.Error(w, "Failed to load servers", http.StatusInternalServerError)
		return
	}

	data := struct {
		Servers         []AdminServerRow
		Authenticated   bool
		UserRole        string
		SiteName        string
		UserEmail       string
		BackgroundURLs  BackgroundURLs
		PteroConfigured bool
	}{
		Servers:         h.adminServerRows(servers),
		Authenticated:   true,
		UserRole:        "admin",
		SiteName:        h.siteName(),
		UserEmail:       h.Auth.GetUserEmail(r),
		BackgroundURLs:  h.pickBackgrounds(""),
		PteroConfigured: h.Config.PterodactylURL != "",
	}

	tmpl, err := template.New("base.html").Funcs(sharedFuncMap()).ParseFS(templateFS, "templates/base.html", "templates/admin.html")
	if err != nil {
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		http.Error(w, "Render error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	buf.WriteTo(w)
}

// HandleServerCreate handles the creation of a new server.
func (h *WebHandler) HandleServerCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	server := &database.Server{
		Name:        r.FormValue("name"),
		Address:     r.FormValue("address"),
		Description: r.FormValue("description"),
		MapURL:      r.FormValue("map_url"),
		ModURL:      r.FormValue("mod_url"),
		GameType:    r.FormValue("game_type"),
		State:       r.FormValue("state"),
		ShowMOTD:    r.FormValue("show_motd") == "on",
		Metadata:    h.parseMetadata(r),
	}

	if err := h.Store.CreateServer(server); err != nil {
		http.Error(w, "Failed to create server: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin", http.StatusFound)
}

// HandleServerUpdate handles updating an existing server.
func (h *WebHandler) HandleServerUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid server ID", http.StatusBadRequest)
		return
	}

	server := &database.Server{
		ID:          id,
		Name:        r.FormValue("name"),
		Address:     r.FormValue("address"),
		Description: r.FormValue("description"),
		MapURL:      r.FormValue("map_url"),
		ModURL:      r.FormValue("mod_url"),
		GameType:    r.FormValue("game_type"),
		State:       r.FormValue("state"),
		ShowMOTD:    r.FormValue("show_motd") == "on",
		Metadata:    h.parseMetadata(r),
	}

	if err := h.Store.UpdateServer(server); err != nil {
		http.Error(w, "Failed to update server: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin", http.StatusFound)
}

// parseMetadata helper to extract metadata from form
func (h *WebHandler) parseMetadata(r *http.Request) map[string]string {
	meta := make(map[string]string)
	keys := r.Form["meta_key"]
	values := r.Form["meta_value"]

	// Ensure same length
	count := len(keys)
	if len(values) < count {
		count = len(values)
	}

	for i := 0; i < count; i++ {
		k := keys[i]
		v := values[i]
		if k != "" {
			meta[k] = v
		}
	}
	return meta
}

// HandleServerDelete handles deleting a server.
func (h *WebHandler) HandleServerDelete(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid server ID", http.StatusBadRequest)
		return
	}

	if err := h.Store.DeleteServer(id); err != nil {
		http.Error(w, "Failed to delete server: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin", http.StatusFound)
}

func (h *WebHandler) BackgroundManager(w http.ResponseWriter, r *http.Request) {
	backgrounds, err := h.Store.ListBackgrounds()
	if err != nil {
		http.Error(w, "Failed to load backgrounds", http.StatusInternalServerError)
		return
	}

	data := struct {
		Backgrounds    []database.Background
		Authenticated  bool
		UserRole       string
		SiteName       string
		UserEmail      string
		BackgroundURLs BackgroundURLs
	}{
		Backgrounds:    backgrounds,
		Authenticated:  true,
		UserRole:       "admin",
		SiteName:       h.siteName(),
		UserEmail:      h.Auth.GetUserEmail(r),
		BackgroundURLs: h.pickBackgrounds(""),
	}

	var buf bytes.Buffer
	tmpl, err := template.New("base.html").Funcs(sharedFuncMap()).ParseFS(templateFS, "templates/base.html", "templates/backgrounds.html")
	if err != nil {
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := tmpl.Execute(&buf, data); err != nil {
		http.Error(w, "Render error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	buf.WriteTo(w)
}

func (h *WebHandler) Settings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		siteName := r.FormValue("site_name")
		if siteName == "" {
			siteName = "HOGS"
		}
		if err := h.Store.SetSetting("site_name", siteName); err != nil {
			http.Error(w, "Failed to save settings", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/settings", http.StatusFound)
		return
	}

	siteName, _ := h.Store.GetSetting("site_name")
	if siteName == "" {
		siteName = "HOGS"
	}

	data := struct {
		SiteName       string
		Authenticated  bool
		UserRole       string
		UserEmail      string
		BackgroundURLs BackgroundURLs
	}{
		SiteName:       siteName,
		Authenticated:  true,
		UserRole:       "admin",
		UserEmail:      h.Auth.GetUserEmail(r),
		BackgroundURLs: h.pickBackgrounds(""),
	}

	tmpl, err := template.New("base.html").Funcs(sharedFuncMap()).ParseFS(templateFS, "templates/base.html", "templates/settings.html")
	if err != nil {
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		http.Error(w, "Render error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	buf.WriteTo(w)
}

func (h *WebHandler) Users(w http.ResponseWriter, r *http.Request) {
	users, err := h.Store.ListUsers()
	if err != nil {
		http.Error(w, "Failed to load users", http.StatusInternalServerError)
		return
	}

	data := struct {
		Users          []database.User
		Authenticated  bool
		UserRole       string
		SiteName       string
		UserEmail      string
		BackgroundURLs BackgroundURLs
	}{
		Users:          users,
		Authenticated:  true,
		UserRole:       "admin",
		SiteName:       h.siteName(),
		UserEmail:      h.Auth.GetUserEmail(r),
		BackgroundURLs: h.pickBackgrounds(""),
	}

	tmpl, err := template.New("base.html").Funcs(sharedFuncMap()).ParseFS(templateFS, "templates/base.html", "templates/users.html")
	if err != nil {
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		http.Error(w, "Render error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	buf.WriteTo(w)
}

func (h *WebHandler) HandleUserUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	userID, err := strconv.Atoi(r.FormValue("user_id"))
	if err != nil {
		http.Error(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	role := r.FormValue("role")
	if role != "admin" && role != "user" {
		http.Error(w, "Invalid role", http.StatusBadRequest)
		return
	}

	if err := h.Store.UpdateUserRole(userID, role); err != nil {
		http.Error(w, "Failed to update user role", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/users", http.StatusFound)
}

type MyServerRow struct {
	Server         database.Server
	PteroLink      *database.PterodactylLink
	PteroCommands  []database.PterodactylCommand
	AllowedActions []string
}

func (h *WebHandler) MyServers(w http.ResponseWriter, r *http.Request) {
	servers, err := h.Store.ListServers()
	if err != nil {
		http.Error(w, "Failed to load servers", http.StatusInternalServerError)
		return
	}

	var rows []MyServerRow
	for _, srv := range servers {
		link, _ := h.Store.GetPterodactylLink(srv.ID)
		if link == nil {
			continue
		}
		var actions []string
		json.Unmarshal([]byte(link.AllowedActions), &actions)
		if len(actions) == 0 {
			continue
		}
		commands, _ := h.Store.ListPterodactylCommands(srv.ID)
		if commands == nil {
			commands = []database.PterodactylCommand{}
		}
		rows = append(rows, MyServerRow{
			Server:         srv,
			PteroLink:      link,
			PteroCommands:  commands,
			AllowedActions: actions,
		})
	}

	data := struct {
		Servers        []MyServerRow
		Authenticated  bool
		UserRole       string
		SiteName       string
		BackgroundURLs BackgroundURLs
	}{
		Servers:        rows,
		Authenticated:  true,
		UserRole:       h.userRole(r),
		SiteName:       h.siteName(),
		BackgroundURLs: h.pickBackgrounds(""),
	}

	tmpl, err := template.New("base.html").Funcs(sharedFuncMap()).ParseFS(templateFS, "templates/base.html", "templates/my_servers.html")
	if err != nil {
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		http.Error(w, "Render error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	buf.WriteTo(w)
}
