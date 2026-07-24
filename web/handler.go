package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"github.com/tionis/hogs/auth"
	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/engine"
	"github.com/tionis/hogs/query"
	"html/template"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	"github.com/tionis/hogs/access"
	"github.com/tionis/hogs/agent"
)

//go:embed templates/*.html assets/*
var templateFS embed.FS

// WebHandler handles frontend requests.
type WebHandler struct {
	Store           *database.Store
	Config          *config.Config
	Auth            *auth.Authenticator
	Engine          *engine.Engine
	AgentConnected  func(int) bool
	AgentNodeInfo   func(string) (agent.NodeSummary, bool)
	AgentNodeUpdate func(string, string, string, string) error
}

// NewWebHandler creates a new WebHandler.
func NewWebHandler(store *database.Store, cfg *config.Config, auth *auth.Authenticator, eng *engine.Engine) *WebHandler {
	return &WebHandler{Store: store, Config: cfg, Auth: auth, Engine: eng}
}

type BackgroundURLs struct {
	Dark  string
	Light string
}

type BackgroundTagOption struct {
	Value       string
	DisplayName string
	Group       string
}

type EffectiveAccessEntry struct {
	Name    string
	Label   string
	Allowed bool
	Reason  string
}

func AvailableBackgroundTags(gameTypes []string) []BackgroundTagOption {
	options := []BackgroundTagOption{
		{Value: "dark", DisplayName: "Dark", Group: "Theme"},
		{Value: "light", DisplayName: "Light", Group: "Theme"},
		{Value: "home", DisplayName: "Home", Group: "Page"},
	}
	for _, gameType := range gameTypes {
		options = append(options, BackgroundTagOption{
			Value: gameType, DisplayName: query.GetGameInfo(gameType).DisplayName, Group: "Game",
		})
	}
	return options
}

func adminGameTypes(servers []database.Server) []string {
	seen := make(map[string]struct{})
	for _, info := range query.AllGameInfo() {
		seen[info.Type] = struct{}{}
	}
	for _, server := range servers {
		if server.GameType != "" {
			seen[server.GameType] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for gameType := range seen {
		result = append(result, gameType)
	}
	sort.Strings(result)
	return result
}

func (h *WebHandler) adminGameTypes(servers []database.Server) []string {
	result := adminGameTypes(servers)
	seen := make(map[string]struct{}, len(result))
	for _, gameType := range result {
		seen[gameType] = struct{}{}
	}
	gameTypes, _ := h.Store.ListGameTypes()
	for _, info := range gameTypes {
		seen[info.Slug] = struct{}{}
	}
	result = result[:0]
	for gameType := range seen {
		result = append(result, gameType)
	}
	sort.Strings(result)
	return result
}

var gameTypePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
var gameTypeColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func normalizeGameType(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validGameType(value string) bool {
	return gameTypePattern.MatchString(value)
}

func (h *WebHandler) ensureGameType(slug string) error {
	existing, err := h.Store.GetGameType(slug)
	if err != nil || existing != nil {
		return err
	}
	info := query.GetGameInfo(slug)
	return h.Store.SetGameType(&database.GameType{
		Slug: slug, DisplayName: info.DisplayName, PlayerNoun: info.PlayerNoun,
		AccentColor: "#666666",
	})
}

func configuredGameTypes(servers []database.Server) []string {
	seen := make(map[string]struct{})
	for _, server := range servers {
		if server.GameType != "" {
			seen[server.GameType] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for gameType := range seen {
		result = append(result, gameType)
	}
	sort.Strings(result)
	return result
}

func (h *WebHandler) pickBackgrounds(contextTags []string) BackgroundURLs {
	urls := BackgroundURLs{}

	darkTags := append([]string{"dark"}, contextTags...)
	dark, err := h.Store.GetRandomBackground(darkTags)
	if err == nil && dark != nil {
		urls.Dark = dark.URL()
	} else {
		dark, err = h.Store.GetRandomBackground([]string{"dark"})
		if err == nil && dark != nil {
			urls.Dark = dark.URL()
		}
	}

	lightTags := append([]string{"light"}, contextTags...)
	light, err := h.Store.GetRandomBackground(lightTags)
	if err == nil && light != nil {
		urls.Light = light.URL()
	} else {
		light, err = h.Store.GetRandomBackground([]string{"light"})
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

func (h *WebHandler) getUserEnv(r *http.Request) *engine.UserEnv {
	email := "anonymous"
	role := "user"
	if h.Auth != nil {
		email = h.Auth.GetUserEmail(r)
		role = h.Auth.GetUserRole(r)
	}
	if email == "" {
		email = "anonymous"
	}
	if role == "" {
		role = "user"
	}

	var groups []string
	if email != "anonymous" && h.Store != nil {
		user, _ := h.Store.GetUserByEmail(email)
		if user != nil {
			scimGroups, _ := h.Store.GetSCIMGroupsForUser(user.ID)
			for _, g := range scimGroups {
				groups = append(groups, g.DisplayName)
			}
		}
	}

	return &engine.UserEnv{Email: email, Role: role, Groups: groups}
}

type PterodactylLinkData struct {
	ServerID        int                           `json:"serverId"`
	PteroServerID   string                        `json:"pteroServerId"`
	PteroIdentifier string                        `json:"pteroIdentifier"`
	AllowedActions  []string                      `json:"allowedActions"`
	ACLRule         string                        `json:"aclRule"`
	Commands        []database.PterodactylCommand `json:"commands"`
}

func (h *WebHandler) siteName() string {
	name, err := h.Store.GetSetting("site_name")
	if err != nil || name == "" {
		return "HOGS"
	}
	return name
}

// ... (Home, ServerDetail, Admin handlers remain unchanged) ...

// FileManager preserves old bookmarks while routing operators to the managed
// agent-backed file browser on the server page.
func (h *WebHandler) FileManager(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]
	http.Redirect(w, r, "/"+serverName+"#file-browser-card", http.StatusFound)
}

// ServeAssets serves static assets embedded in the binary.
func (h *WebHandler) ServeAssets(w http.ResponseWriter, r *http.Request) {
	http.FileServer(http.FS(templateFS)).ServeHTTP(w, r)
}

// Forbidden renders a useful HTML response for authenticated users who lack
// the role required by a page.
func (h *WebHandler) Forbidden(w http.ResponseWriter, r *http.Request) {
	data := struct {
		Authenticated  bool
		UserRole       string
		UserEmail      string
		SiteName       string
		BackgroundURLs BackgroundURLs
	}{
		Authenticated:  true,
		UserRole:       h.Auth.GetUserRole(r),
		UserEmail:      h.Auth.GetUserEmail(r),
		SiteName:       h.siteName(),
		BackgroundURLs: h.pickBackgrounds([]string{"home"}),
	}

	tmpl, err := template.New("base.html").Funcs(sharedFuncMap(h.Store)).ParseFS(
		templateFS,
		"templates/base.html",
		"templates/forbidden.html",
	)
	if err != nil {
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		http.Error(w, "Render error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_, _ = buf.WriteTo(w)
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
	userEnv := h.getUserEnv(r)

	// Filter servers by visibility constraints
	var visibleServers []database.Server
	for _, s := range allServers {
		// "offline" state hides the server from public view.
		// "auto" state shows it, and the frontend determines the badge status.
		if s.State != "offline" || isAuthenticated {
			view := database.ServerAccessDecision{Allowed: userEnv.Role == "admin" || userEnv.Role == "system"}
			if !view.Allowed {
				view, _ = h.Store.EvaluateServerAccess(s.ID, userEnv.Email, userEnv.Groups, access.View)
			}
			if !view.Allowed {
				continue
			}
			if h.Engine.EvaluateVisibility(&s, userEnv) {
				visibleServers = append(visibleServers, s)
			}
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
	sort.Strings(gameTypes)

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
		BackgroundURLs: h.pickBackgrounds([]string{"home"}),
	}

	tmpl, err := template.New("base.html").Funcs(sharedFuncMap(h.Store)).ParseFS(templateFS, "templates/base.html", "templates/index.html")
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
	userRole := h.userRole(r)
	userEnv := h.getUserEnv(r)
	view := database.ServerAccessDecision{Allowed: userEnv.Role == "admin" || userEnv.Role == "system"}
	if !view.Allowed {
		view, _ = h.Store.EvaluateServerAccess(server.ID, userEnv.Email, userEnv.Groups, access.View)
	}
	if !view.Allowed {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	// Check visibility constraints
	if !h.Engine.EvaluateVisibility(server, userEnv) {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	// Access control: if offline and not admin, return 404
	if server.State == "offline" && !isAuthenticated {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	// Check if agent is configured for this server
	hasAgent := false
	link, _ := h.Store.GetPterodactylLink(server.ID)
	if link != nil && link.Node != "" {
		agent, _ := h.Store.GetAgentByNodeName(link.Node)
		if agent != nil {
			hasAgent = true
		}
	}

	data := struct {
		Server          *database.Server
		Authenticated   bool
		UserRole        string
		UserEmail       string
		SiteName        string
		BackgroundURLs  BackgroundURLs
		PteroConfigured bool
		PteroLink       *database.PterodactylLink
		PteroCommands   []database.PterodactylCommand
		AllowedActions  []string
		HasAgent        bool
		ShowConsole     bool
		ConsoleWrite    bool
		ShowFiles       bool
		FileWrite       bool
		EffectiveAccess []EffectiveAccessEntry
		ManageAccess    bool
		AccessGrants    []database.ServerAccessGrant
		AccessCatalog   []access.Capability
		WhitelistManage bool
	}{
		Server:          server,
		Authenticated:   isAuthenticated,
		UserRole:        userRole,
		UserEmail:       h.Auth.GetUserEmail(r),
		SiteName:        h.siteName(),
		BackgroundURLs:  h.pickBackgrounds([]string{server.GameType}),
		PteroConfigured: h.Config.PterodactylURL != "",
		PteroLink:       nil,
		PteroCommands:   nil,
		AllowedActions:  nil,
		HasAgent:        hasAgent,
		ShowConsole:     false,
		ConsoleWrite:    false,
		ShowFiles:       false,
		FileWrite:       false,
		EffectiveAccess: []EffectiveAccessEntry{},
		AccessGrants:    []database.ServerAccessGrant{},
		AccessCatalog:   access.Capabilities,
	}
	if isAuthenticated {
		for _, capability := range access.Capabilities {
			decision := database.ServerAccessDecision{Allowed: true, Reason: "instance administrator"}
			if userRole != "admin" {
				decision, _ = h.Store.EvaluateServerAccess(server.ID, userEnv.Email, userEnv.Groups, capability.Name)
			}
			data.EffectiveAccess = append(data.EffectiveAccess, EffectiveAccessEntry{
				Name: capability.Name, Label: capability.Label, Allowed: decision.Allowed, Reason: decision.Reason,
			})
		}
		manageDecision := database.ServerAccessDecision{Allowed: userRole == "admin"}
		if userRole != "admin" {
			manageDecision, _ = h.Store.EvaluateServerAccess(server.ID, userEnv.Email, userEnv.Groups, access.AccessManage)
		}
		data.ManageAccess = manageDecision.Allowed
		if data.ManageAccess {
			data.AccessGrants, _ = h.Store.ListServerAccessGrants(server.ID)
			if data.AccessGrants == nil {
				data.AccessGrants = []database.ServerAccessGrant{}
			}
		}
		whitelistDecision := database.ServerAccessDecision{Allowed: userRole == "admin"}
		if userRole != "admin" {
			whitelistDecision, _ = h.Store.EvaluateServerAccess(server.ID, userEnv.Email, userEnv.Groups, access.WhitelistManage)
		}
		data.WhitelistManage = whitelistDecision.Allowed
	}
	if isAuthenticated && hasAgent {
		if userRole == "admin" {
			data.ShowConsole, data.ConsoleWrite = true, true
			data.ShowFiles, data.FileWrite = true, true
		} else {
			consoleRead, _ := h.Store.EvaluateServerAccess(server.ID, userEnv.Email, userEnv.Groups, access.ConsoleRead)
			consoleWrite, _ := h.Store.EvaluateServerAccess(server.ID, userEnv.Email, userEnv.Groups, access.ConsoleWrite)
			fileRead, _ := h.Store.EvaluateServerAccess(server.ID, userEnv.Email, userEnv.Groups, access.FileRead)
			fileWrite, _ := h.Store.EvaluateServerAccess(server.ID, userEnv.Email, userEnv.Groups, access.FileWrite)
			data.ShowConsole, data.ConsoleWrite = consoleRead.Allowed, consoleWrite.Allowed
			data.ShowFiles, data.FileWrite = fileRead.Allowed, fileWrite.Allowed
		}
	}

	data.PteroLink = link
	if link != nil {
		commands, _ := h.Store.ListPterodactylCommands(server.ID)
		data.PteroCommands = commands
	}

	var allowedActions []string
	if data.PteroLink != nil {
		var configuredActions []string
		json.Unmarshal([]byte(data.PteroLink.AllowedActions), &configuredActions)
		for _, action := range configuredActions {
			allowed, evalErr := h.Engine.EvaluateACL(data.PteroLink, server, action, userEnv)
			if evalErr == nil && allowed {
				allowedActions = append(allowedActions, action)
			}
		}
	}
	data.AllowedActions = allowedActions

	tmpl, err := template.New("base.html").Funcs(sharedFuncMap(h.Store)).ParseFS(templateFS, "templates/base.html", "templates/server.html")
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

	templates, _ := h.Store.ListServerTemplates()
	if templates == nil {
		templates = []database.ServerTemplate{}
	}

	data := struct {
		Servers         []database.Server
		ServerTemplates []database.ServerTemplate
		GameTypes       []string
		Authenticated   bool
		UserRole        string
		SiteName        string
		UserEmail       string
		BackgroundURLs  BackgroundURLs
	}{
		Servers:         servers,
		ServerTemplates: templates,
		GameTypes:       h.adminGameTypes(servers),
		Authenticated:   true,
		UserRole:        "admin",
		SiteName:        h.siteName(),
		UserEmail:       h.Auth.GetUserEmail(r),
		BackgroundURLs:  h.pickBackgrounds([]string{"home"}),
	}

	tmpl, err := template.New("base.html").Funcs(sharedFuncMap(h.Store)).ParseFS(templateFS, "templates/base.html", "templates/admin.html")
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

	gameType := normalizeGameType(r.FormValue("game_type"))
	if !validGameType(gameType) {
		http.Error(w, "Game type must be a lowercase slug using letters, numbers, dashes, or underscores", http.StatusBadRequest)
		return
	}
	if err := h.ensureGameType(gameType); err != nil {
		http.Error(w, "Failed to register game type", http.StatusInternalServerError)
		return
	}
	server := &database.Server{
		Name:        r.FormValue("name"),
		Address:     r.FormValue("address"),
		Description: r.FormValue("description"),
		MapURL:      r.FormValue("map_url"),
		ModURL:      r.FormValue("mod_url"),
		GameType:    gameType,
		State:       r.FormValue("state"),
		ShowMOTD:    r.FormValue("show_motd") == "on",
		Metadata:    h.parseMetadata(r),
	}

	if err := h.Store.CreateServer(server); err != nil {
		http.Error(w, "Failed to create server: "+err.Error(), http.StatusInternalServerError)
		return
	}

	tagsInput := strings.TrimSpace(r.FormValue("tags"))
	if tagsInput != "" {
		var cleanTags []string
		for _, t := range strings.Split(tagsInput, ",") {
			if t = strings.TrimSpace(t); t != "" {
				cleanTags = append(cleanTags, t)
			}
		}
		if len(cleanTags) > 0 {
			if err := h.Store.SetServerTags(server.ID, cleanTags); err != nil {
				log.Printf("Warning: failed to set tags for new server %s: %v", server.Name, err)
			}
		}
	}

	http.Redirect(w, r, "/admin", http.StatusFound)
}

// HandleServerUpdate handles updating an existing server.
func (h *WebHandler) ServerEdit(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	idStr := vars["id"]
	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "Invalid server ID", http.StatusBadRequest)
		return
	}

	server, err := h.Store.GetServer(id)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if server == nil {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	var pteroLink *PterodactylLinkData
	if h.Config.PterodactylURL != "" {
		link, _ := h.Store.GetPterodactylLink(server.ID)
		if link != nil {
			var actions []string
			json.Unmarshal([]byte(link.AllowedActions), &actions)
			commands, _ := h.Store.ListPterodactylCommands(server.ID)
			if commands == nil {
				commands = []database.PterodactylCommand{}
			}
			pteroLink = &PterodactylLinkData{
				ServerID:        server.ID,
				PteroServerID:   link.PteroServerID,
				PteroIdentifier: link.PteroIdentifier,
				AllowedActions:  actions,
				ACLRule:         link.ACLRule,
				Commands:        commands,
			}
		}
	}

	serverTags, _ := h.Store.GetServerTags(server.ID)
	if serverTags == nil {
		serverTags = []string{}
	}
	accessGrants, _ := h.Store.ListServerAccessGrants(server.ID)
	if accessGrants == nil {
		accessGrants = []database.ServerAccessGrant{}
	}
	whitelistEntries, _ := h.Store.ListUserWhitelists(server.ID)
	if whitelistEntries == nil {
		whitelistEntries = []database.UserWhitelist{}
	}

	agents, _ := h.Store.ListAgents()
	if agents == nil {
		agents = []database.Agent{}
	}
	allServers, _ := h.Store.ListServers()

	data := struct {
		Server             *database.Server
		GameTypes          []string
		PteroConfigured    bool
		PteroLink          *PterodactylLinkData
		ServerTags         []string
		AccessGrants       []database.ServerAccessGrant
		AccessCapabilities []access.Capability
		WhitelistEntries   []database.UserWhitelist
		Agents             []database.Agent
		Authenticated      bool
		UserRole           string
		SiteName           string
		UserEmail          string
		BackgroundURLs     BackgroundURLs
	}{
		Server:             server,
		GameTypes:          h.adminGameTypes(allServers),
		PteroConfigured:    h.Config.PterodactylURL != "",
		PteroLink:          pteroLink,
		ServerTags:         serverTags,
		AccessGrants:       accessGrants,
		AccessCapabilities: access.Capabilities,
		WhitelistEntries:   whitelistEntries,
		Agents:             agents,
		Authenticated:      true,
		UserRole:           "admin",
		SiteName:           h.siteName(),
		UserEmail:          h.Auth.GetUserEmail(r),
		BackgroundURLs:     h.pickBackgrounds([]string{"home"}),
	}

	tmpl, err := template.New("base.html").Funcs(sharedFuncMap(h.Store)).ParseFS(templateFS, "templates/base.html", "templates/server_edit.html")
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

	gameType := normalizeGameType(r.FormValue("game_type"))
	if !validGameType(gameType) {
		http.Error(w, "Game type must be a lowercase slug using letters, numbers, dashes, or underscores", http.StatusBadRequest)
		return
	}
	if err := h.ensureGameType(gameType); err != nil {
		http.Error(w, "Failed to register game type", http.StatusInternalServerError)
		return
	}
	server := &database.Server{
		ID:          id,
		Name:        r.FormValue("name"),
		Address:     r.FormValue("address"),
		Description: r.FormValue("description"),
		MapURL:      r.FormValue("map_url"),
		ModURL:      r.FormValue("mod_url"),
		GameType:    gameType,
		State:       r.FormValue("state"),
		ShowMOTD:    r.FormValue("show_motd") == "on",
		Metadata:    h.parseMetadata(r),
	}

	if err := h.Store.UpdateServer(server); err != nil {
		http.Error(w, "Failed to update server: "+err.Error(), http.StatusInternalServerError)
		return
	}

	tagsInput := strings.TrimSpace(r.FormValue("tags"))
	if tagsInput != "" {
		var cleanTags []string
		for _, t := range strings.Split(tagsInput, ",") {
			if t = strings.TrimSpace(t); t != "" {
				cleanTags = append(cleanTags, t)
			}
		}
		if err := h.Store.SetServerTags(server.ID, cleanTags); err != nil {
			log.Printf("Warning: failed to update tags for server %s: %v", server.Name, err)
		}
	} else {
		if err := h.Store.SetServerTags(server.ID, []string{}); err != nil {
			log.Printf("Warning: failed to clear tags for server %s: %v", server.Name, err)
		}
	}
	if err := h.Store.PruneUnusedBackgroundGameTags(); err != nil {
		log.Printf("Warning: failed to prune unused background tags: %v", err)
	}

	http.Redirect(w, r, "/admin/servers/"+strconv.Itoa(id), http.StatusFound)
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
	lifecycle := r.FormValue("map_lifecycle")
	if lifecycle != "independent" {
		lifecycle = "game"
	}
	meta["map_lifecycle"] = lifecycle
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
	if err := h.Store.PruneUnusedBackgroundGameTags(); err != nil {
		log.Printf("Warning: failed to prune unused background tags: %v", err)
	}

	http.Redirect(w, r, "/admin", http.StatusFound)
}

func (h *WebHandler) BackgroundManager(w http.ResponseWriter, r *http.Request) {
	backgrounds, err := h.Store.ListBackgrounds()
	if err != nil {
		http.Error(w, "Failed to load backgrounds", http.StatusInternalServerError)
		return
	}

	servers, err := h.Store.ListServers()
	if err != nil {
		http.Error(w, "Failed to load servers", http.StatusInternalServerError)
		return
	}
	gameTypes := configuredGameTypes(servers)
	availableTags := AvailableBackgroundTags(gameTypes)
	allowedTags := make(map[string]struct{}, len(availableTags))
	for _, option := range availableTags {
		allowedTags[option.Value] = struct{}{}
	}
	for i := range backgrounds {
		filtered := backgrounds[i].Tags[:0]
		for _, tag := range backgrounds[i].Tags {
			if _, allowed := allowedTags[tag]; allowed {
				filtered = append(filtered, tag)
			}
		}
		backgrounds[i].Tags = filtered
	}

	data := struct {
		Backgrounds    []database.Background
		AvailableTags  []BackgroundTagOption
		Authenticated  bool
		UserRole       string
		SiteName       string
		UserEmail      string
		BackgroundURLs BackgroundURLs
	}{
		Backgrounds:    backgrounds,
		AvailableTags:  availableTags,
		Authenticated:  true,
		UserRole:       "admin",
		SiteName:       h.siteName(),
		UserEmail:      h.Auth.GetUserEmail(r),
		BackgroundURLs: h.pickBackgrounds([]string{"home"}),
	}

	var buf bytes.Buffer
	tmpl, err := template.New("base.html").Funcs(sharedFuncMap(h.Store)).ParseFS(templateFS, "templates/base.html", "templates/backgrounds.html")
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
		BackgroundURLs: h.pickBackgrounds([]string{"home"}),
	}

	tmpl, err := template.New("base.html").Funcs(sharedFuncMap(h.Store)).ParseFS(templateFS, "templates/base.html", "templates/settings.html")
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

	type UserWithGroups struct {
		database.User
		Groups     []database.SCIMGroup
		Identities []database.GameIdentity
	}

	var usersWithGroups []UserWithGroups
	for _, u := range users {
		groups, _ := h.Store.GetSCIMGroupsForUser(u.ID)
		identities, _ := h.Store.ListGameIdentities(u.Email)
		usersWithGroups = append(usersWithGroups, UserWithGroups{
			User:       u,
			Groups:     groups,
			Identities: identities,
		})
	}

	data := struct {
		Users          []UserWithGroups
		Authenticated  bool
		UserRole       string
		SiteName       string
		UserEmail      string
		BackgroundURLs BackgroundURLs
	}{
		Users:          usersWithGroups,
		Authenticated:  true,
		UserRole:       "admin",
		SiteName:       h.siteName(),
		UserEmail:      h.Auth.GetUserEmail(r),
		BackgroundURLs: h.pickBackgrounds([]string{"home"}),
	}

	tmpl, err := template.New("base.html").Funcs(sharedFuncMap(h.Store)).ParseFS(templateFS, "templates/base.html", "templates/users.html")
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

func (h *WebHandler) GameTypes(w http.ResponseWriter, r *http.Request) {
	gameTypes, err := h.Store.ListGameTypes()
	if err != nil {
		http.Error(w, "Failed to load game types", http.StatusInternalServerError)
		return
	}
	data := struct {
		GameTypes      []database.GameType
		Authenticated  bool
		UserRole       string
		SiteName       string
		UserEmail      string
		BackgroundURLs BackgroundURLs
	}{
		GameTypes: gameTypes, Authenticated: true, UserRole: "admin",
		SiteName: h.siteName(), UserEmail: h.Auth.GetUserEmail(r),
		BackgroundURLs: h.pickBackgrounds([]string{"home"}),
	}
	tmpl, err := template.New("base.html").Funcs(sharedFuncMap(h.Store)).ParseFS(
		templateFS, "templates/base.html", "templates/game_types.html")
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

func (h *WebHandler) HandleGameTypeSet(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	slug := normalizeGameType(r.FormValue("slug"))
	displayName := strings.TrimSpace(r.FormValue("display_name"))
	playerNoun := strings.TrimSpace(r.FormValue("player_noun"))
	icon := strings.TrimSpace(r.FormValue("icon"))
	accentColor := strings.TrimSpace(r.FormValue("accent_color"))
	if !validGameType(slug) || displayName == "" || len(displayName) > 64 ||
		playerNoun == "" || len(playerNoun) > 32 || len(icon) > 8 ||
		!gameTypeColorPattern.MatchString(accentColor) {
		http.Error(w, "Invalid game type fields", http.StatusBadRequest)
		return
	}
	existing, err := h.Store.GetGameType(slug)
	if err != nil {
		http.Error(w, "Failed to load game type", http.StatusInternalServerError)
		return
	}
	item := &database.GameType{
		Slug: slug, DisplayName: displayName, PlayerNoun: playerNoun,
		Icon: icon, AccentColor: strings.ToLower(accentColor),
	}
	if existing != nil {
		item.Builtin = existing.Builtin
	}
	if err := h.Store.SetGameType(item); err != nil {
		http.Error(w, "Failed to save game type", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/game-types", http.StatusFound)
}

func (h *WebHandler) HandleGameTypeDelete(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	slug := normalizeGameType(r.FormValue("slug"))
	if err := h.Store.DeleteGameType(slug); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	http.Redirect(w, r, "/admin/game-types", http.StatusFound)
}

func (h *WebHandler) HandleAccessGrantSet(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	serverID, err := strconv.Atoi(r.FormValue("server_id"))
	if err != nil {
		http.Error(w, "Invalid server ID", http.StatusBadRequest)
		return
	}
	server, err := h.Store.GetServer(serverID)
	if err != nil || server == nil {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}
	if !h.canManageServerAccess(r, serverID) {
		http.Error(w, "Server access management permission required", http.StatusForbidden)
		return
	}
	subjectType := r.FormValue("subject_type")
	subject := strings.TrimSpace(r.FormValue("subject"))
	effect := r.FormValue("effect")
	if subjectType != "user" && subjectType != "group" && subjectType != "authenticated" && subjectType != "everyone" {
		http.Error(w, "Subject type must be user, group, authenticated, or everyone", http.StatusBadRequest)
		return
	}
	if subjectType == "authenticated" || subjectType == "everyone" {
		subject = "*"
	}
	if subject == "" {
		http.Error(w, "Subject is required", http.StatusBadRequest)
		return
	}
	if effect != "allow" && effect != "deny" {
		http.Error(w, "Effect must be allow or deny", http.StatusBadRequest)
		return
	}
	var capabilities []string
	for _, capability := range r.Form["capability"] {
		if access.Known(capability) {
			capabilities = append(capabilities, capability)
		}
	}
	if len(capabilities) == 0 {
		http.Error(w, "At least one capability is required", http.StatusBadRequest)
		return
	}
	if err := h.Store.SetServerAccessGrant(&database.ServerAccessGrant{
		ServerID: serverID, SubjectType: subjectType, Subject: subject, Effect: effect, Capabilities: capabilities,
	}); err != nil {
		http.Error(w, "Failed to save access grant", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/"+server.Name+"#access-control", http.StatusFound)
}

func (h *WebHandler) HandleAccessGrantDelete(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	serverID, serverErr := strconv.Atoi(r.FormValue("server_id"))
	grantID, grantErr := strconv.Atoi(r.FormValue("grant_id"))
	if serverErr != nil || grantErr != nil {
		http.Error(w, "Invalid access grant", http.StatusBadRequest)
		return
	}
	server, err := h.Store.GetServer(serverID)
	if err != nil || server == nil {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}
	if !h.canManageServerAccess(r, serverID) {
		http.Error(w, "Server access management permission required", http.StatusForbidden)
		return
	}
	if err := h.Store.DeleteServerAccessGrant(grantID, serverID); err != nil {
		http.Error(w, "Failed to delete access grant", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/"+server.Name+"#access-control", http.StatusFound)
}

func (h *WebHandler) canManageServerAccess(r *http.Request, serverID int) bool {
	user := h.getUserEnv(r)
	if user != nil && (user.Role == "admin" || user.Role == "system") {
		return true
	}
	if user == nil {
		return false
	}
	decision, err := h.Store.EvaluateServerAccess(serverID, user.Email, user.Groups, access.AccessManage)
	return err == nil && decision.Allowed
}

var safeGameUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

func validGameUsername(gameType, username string) bool {
	if gameType == "minecraft" {
		return regexp.MustCompile(`^[A-Za-z0-9_]{3,16}$`).MatchString(username)
	}
	return safeGameUsernamePattern.MatchString(username)
}

func (h *WebHandler) HandleGameIdentitySet(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	role := h.userRole(r)
	email := h.Auth.GetUserEmail(r)
	source := "self"
	if role == "admin" && strings.TrimSpace(r.FormValue("user_email")) != "" {
		email = strings.TrimSpace(r.FormValue("user_email"))
		source = "admin"
	}
	gameType := normalizeGameType(r.FormValue("game_type"))
	username := strings.TrimSpace(r.FormValue("username"))
	if email == "" || !validGameType(gameType) || !validGameUsername(gameType, username) {
		http.Error(w, "Invalid game identity", http.StatusBadRequest)
		return
	}
	if err := h.Store.SetGameIdentity(&database.GameIdentity{
		UserEmail: email, GameType: gameType, Username: username, Source: source,
	}); err != nil {
		http.Error(w, "Failed to save game identity", http.StatusInternalServerError)
		return
	}
	if role == "admin" {
		http.Redirect(w, r, "/admin/users", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/my-servers", http.StatusFound)
}

func (h *WebHandler) HandleGameIdentityDelete(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	role := h.userRole(r)
	email := h.Auth.GetUserEmail(r)
	if role == "admin" && strings.TrimSpace(r.FormValue("user_email")) != "" {
		email = strings.TrimSpace(r.FormValue("user_email"))
	}
	gameType := normalizeGameType(r.FormValue("game_type"))
	if err := h.Store.DeleteGameIdentity(email, gameType); err != nil {
		http.Error(w, "Failed to delete game identity", http.StatusInternalServerError)
		return
	}
	if role == "admin" {
		http.Redirect(w, r, "/admin/users", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/my-servers", http.StatusFound)
}

func (h *WebHandler) HandleUserUpdate(w http.ResponseWriter, r *http.Request) {
	// User updates are disabled — OIDC is the authoritative source.
	// This endpoint is kept for backwards compatibility but does nothing.
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

	user := h.getUserEnv(r)
	var rows []MyServerRow
	for _, srv := range servers {
		link, _ := h.Store.GetPterodactylLink(srv.ID)
		if link == nil {
			continue
		}
		var configuredActions []string
		json.Unmarshal([]byte(link.AllowedActions), &configuredActions)
		var actions []string
		for _, action := range configuredActions {
			allowed, evalErr := h.Engine.EvaluateACL(link, &srv, action, user)
			if evalErr == nil && allowed {
				actions = append(actions, action)
			}
		}
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
	identities, _ := h.Store.ListGameIdentities(user.Email)
	if identities == nil {
		identities = []database.GameIdentity{}
	}

	data := struct {
		Servers        []MyServerRow
		Identities     []database.GameIdentity
		GameTypes      []string
		Authenticated  bool
		UserRole       string
		SiteName       string
		BackgroundURLs BackgroundURLs
	}{
		Servers:        rows,
		Identities:     identities,
		GameTypes:      configuredGameTypes(servers),
		Authenticated:  true,
		UserRole:       h.userRole(r),
		SiteName:       h.siteName(),
		BackgroundURLs: h.pickBackgrounds([]string{"home"}),
	}

	tmpl, err := template.New("base.html").Funcs(sharedFuncMap(h.Store)).ParseFS(templateFS, "templates/base.html", "templates/my_servers.html")
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

func (h *WebHandler) CommandManager(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	serverID, err := strconv.Atoi(vars["serverId"])
	if err != nil {
		http.Error(w, "Invalid server ID", http.StatusBadRequest)
		return
	}

	server, err := h.Store.GetServer(serverID)
	if err != nil || server == nil {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	schemas, _ := h.Store.ListCommandSchemas(serverID)
	if schemas == nil {
		schemas = []database.CommandSchema{}
	}

	data := struct {
		Server         *database.Server
		CommandSchemas []database.CommandSchema
		Authenticated  bool
		UserRole       string
		SiteName       string
		UserEmail      string
		BackgroundURLs BackgroundURLs
	}{
		Server:         server,
		CommandSchemas: schemas,
		Authenticated:  true,
		UserRole:       "admin",
		SiteName:       h.siteName(),
		UserEmail:      h.Auth.GetUserEmail(r),
		BackgroundURLs: h.pickBackgrounds([]string{"home"}),
	}

	tmpl, err := template.New("base.html").Funcs(sharedFuncMap(h.Store)).ParseFS(templateFS, "templates/base.html", "templates/commands.html")
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

func (h *WebHandler) ConstraintManager(w http.ResponseWriter, r *http.Request) {
	constraints, _ := h.Store.ListConstraints()
	if constraints == nil {
		constraints = []database.Constraint{}
	}

	data := struct {
		Constraints    []database.Constraint
		Authenticated  bool
		UserRole       string
		SiteName       string
		UserEmail      string
		BackgroundURLs BackgroundURLs
	}{
		Constraints:    constraints,
		Authenticated:  true,
		UserRole:       "admin",
		SiteName:       h.siteName(),
		UserEmail:      h.Auth.GetUserEmail(r),
		BackgroundURLs: h.pickBackgrounds([]string{"home"}),
	}

	tmpl, err := template.New("base.html").Funcs(sharedFuncMap(h.Store)).ParseFS(templateFS, "templates/base.html", "templates/constraints.html")
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

func (h *WebHandler) CronManager(w http.ResponseWriter, r *http.Request) {
	jobs, _ := h.Store.ListCronJobs()
	if jobs == nil {
		jobs = []database.CronJob{}
	}

	servers, _ := h.Store.ListServers()
	if servers == nil {
		servers = []database.Server{}
	}

	data := struct {
		CronJobs       []database.CronJob
		Servers        []database.Server
		Authenticated  bool
		UserRole       string
		SiteName       string
		UserEmail      string
		BackgroundURLs BackgroundURLs
	}{
		CronJobs:       jobs,
		Servers:        servers,
		Authenticated:  true,
		UserRole:       "admin",
		SiteName:       h.siteName(),
		UserEmail:      h.Auth.GetUserEmail(r),
		BackgroundURLs: h.pickBackgrounds([]string{"home"}),
	}

	tmpl, err := template.New("base.html").Funcs(sharedFuncMap(h.Store)).ParseFS(templateFS, "templates/base.html", "templates/cron.html")
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

func (h *WebHandler) Help(w http.ResponseWriter, r *http.Request) {
	constraints, _ := h.Store.ListConstraints()

	data := struct {
		Constraints    []database.Constraint
		SiteName       string
		Authenticated  bool
		UserRole       string
		BackgroundURLs BackgroundURLs
	}{
		Constraints:    constraints,
		SiteName:       h.siteName(),
		Authenticated:  h.Auth != nil && h.Auth.IsAuthenticated(r),
		UserRole:       h.userRole(r),
		BackgroundURLs: h.pickBackgrounds([]string{"home"}),
	}

	tmpl, err := template.New("base.html").Funcs(sharedFuncMap(h.Store)).ParseFS(templateFS, "templates/base.html", "templates/help.html")
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

func (h *WebHandler) HelpMarkdown(w http.ResponseWriter, r *http.Request) {
	constraints, _ := h.Store.ListConstraints()
	jobs, _ := h.Store.ListCronJobs()

	md := "# HOGS Automation API Reference\n\n"
	md += "## Available Actions\n\n"
	md += "| Action | Description |\n|--------|-------------|\n"
	md += "| `start` | Start a server |\n"
	md += "| `stop` | Stop a server |\n"
	md += "| `restart` | Restart a server |\n"
	md += "| `whitelist` | Add player to whitelist |\n"
	md += "| `command:<name>` | Execute a parameterized command |\n\n"

	md += "## Expression Language\n\n"
	md += "Expressions use [expr](https://expr-lang.org/) syntax.\n\n"
	md += "### Available Variables\n\n"
	md += "| Variable | Type | Description |\n|----------|------|-------------|\n"
	md += "| `action` | `string` | The requested action |\n"
	md += "| `server.ID` | `int` | Server ID |\n"
	md += "| `server.Name` | `string` | Server name |\n"
	md += "| `server.GameType` | `string` | Game type |\n"
	md += "| `server.Tags` | `[]string` | Server tags |\n"
	md += "| `server.Node` | `string` | Pterodactyl node |\n"
	md += "| `server.Running` | `bool` | Is server online |\n"
	md += "| `user.Email` | `string` | Requesting user email |\n"
	md += "| `user.Role` | `string` | User role (admin/user) |\n"
	md += "| `time.Hour` | `int` | Current hour (0-23) |\n"
	md += "| `time.Weekday` | `time.Weekday` | Current weekday |\n\n"

	md += "### Helper Functions\n\n"
	md += "| Function | Signature | Description |\n|----------|-----------|-------------|\n"
	md += "| `hasTag` | `(ServerEnv, string) bool` | Check if server has a tag |\n"
	md += "| `serversOnNode` | `(string) []ServerEnv` | Get servers on a node |\n"
	md += "| `runningOnNode` | `(string) []ServerEnv` | Get running servers on a node |\n"
	md += "| `countRunning` | `([]ServerEnv) int` | Count running servers |\n"
	md += "| `filterByTag` | `([]ServerEnv, string) []ServerEnv` | Filter servers by tag |\n"
	md += "| `weekday` | `(string) time.Weekday` | Parse weekday name |\n\n"

	md += "## Active Constraints\n\n"
	if len(constraints) == 0 {
		md += "No constraints configured.\n\n"
	} else {
		md += "| Name | Condition | Strategy | Priority | Enabled |\n|------|-----------|----------|----------|---------|\n"
		for _, c := range constraints {
			enabledStr := "Yes"
			if !c.Enabled {
				enabledStr = "No"
			}
			md += fmt.Sprintf("| %s | `%s` | %s | %d | %s |\n", c.Name, c.Condition, c.Strategy, c.Priority, enabledStr)
		}
		md += "\n"
	}

	md += "## Cron Jobs\n\n"
	if len(jobs) == 0 {
		md += "No cron jobs configured.\n\n"
	} else {
		md += "| Name | Schedule | Server | Action | Enabled |\n|------|----------|--------|--------|---------|\n"
		for _, j := range jobs {
			enabledStr := "Yes"
			if !j.Enabled {
				enabledStr = "No"
			}
			md += fmt.Sprintf("| %s | `%s` | %s | %s | %s |\n", j.Name, j.Schedule, j.ServerName, j.Action, enabledStr)
		}
		md += "\n"
	}

	md += "## Parameter Types\n\n"
	md += "| Type | Validation |\n|------|------------|\n"
	md += "| `string` | Optional `pattern` (regex), `minLength`, `maxLength` |\n"
	md += "| `int` | Optional `min`, `max` |\n"
	md += "| `float` | Optional `min`, `max` |\n"
	md += "| `enum` | Required `values` array |\n"
	md += "| `bool` | Accepts `true`/`false`/`1`/`0` |\n"

	md += "\n## Cron Syntax\n\n"
	md += "Standard cron format: `minute hour day-of-month month day-of-week`\n"
	md += "Example: `0 4 * * *` runs at 4:00 AM every day.\n"

	contentHash := fmt.Sprintf("%x", sha256.Sum256([]byte(md)))[:16]

	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Server-Hogs-Help-Version", contentHash)
	w.Write([]byte(md))
}

func (h *WebHandler) Agents(w http.ResponseWriter, r *http.Request) {
	agents, err := h.Store.ListAgents()
	if err != nil {
		http.Error(w, "Failed to list agents", http.StatusInternalServerError)
		return
	}
	if agents == nil {
		agents = []database.Agent{}
	}

	type agentView struct {
		database.Agent
		Connected        bool
		CapabilityLabels []string
		Mode             string
		WorkerAPI        string
		ControlURL       string
		PublicURL        string
		AssignedServers  []database.Server
	}
	servers, _ := h.Store.ListServers()
	views := make([]agentView, 0, len(agents))
	for _, current := range agents {
		connected := false
		if h.AgentConnected != nil {
			connected = h.AgentConnected(current.ID)
		}
		var capabilities []string
		if err := json.Unmarshal(current.Capabilities, &capabilities); err != nil {
			capabilities = nil
		}
		sort.Strings(capabilities)
		var assigned []database.Server
		for _, server := range servers {
			link, _ := h.Store.GetPterodactylLink(server.ID)
			if link != nil && link.Node == current.NodeName {
				assigned = append(assigned, server)
			}
		}
		mode, workerAPI, controlURL, publicURL := "", "", "", ""
		if h.AgentNodeInfo != nil {
			if info, ok := h.AgentNodeInfo(current.NodeName); ok {
				mode, controlURL, publicURL = info.Mode, info.ControlURL, info.PublicURL
				workerAPI = publicURL
				if workerAPI == "" {
					workerAPI = controlURL
				}
			}
		}
		views = append(views, agentView{
			Agent:            current,
			Connected:        connected,
			CapabilityLabels: capabilities,
			Mode:             mode,
			WorkerAPI:        workerAPI,
			ControlURL:       controlURL,
			PublicURL:        publicURL,
			AssignedServers:  assigned,
		})
	}

	data := struct {
		Agents         []agentView
		Authenticated  bool
		UserRole       string
		SiteName       string
		BackgroundURLs BackgroundURLs
	}{
		Agents:         views,
		Authenticated:  true,
		UserRole:       "admin",
		SiteName:       h.siteName(),
		BackgroundURLs: h.pickBackgrounds([]string{"home"}),
	}

	tmpl, err := template.New("base.html").Funcs(sharedFuncMap(h.Store)).ParseFS(templateFS, "templates/base.html", "templates/agents.html")
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

func (h *WebHandler) HandleAgentLabelUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid worker ID", http.StatusBadRequest)
		return
	}
	current, err := h.Store.GetAgent(id)
	if err != nil || current == nil {
		http.Error(w, "Worker not found", http.StatusNotFound)
		return
	}
	label := strings.TrimSpace(r.FormValue("display_name"))
	if label == "" {
		label = current.NodeName
	}
	if len(label) > 64 {
		http.Error(w, "Display name is too long", http.StatusBadRequest)
		return
	}
	current.Name = label
	if err := h.Store.UpdateAgent(current); err != nil {
		http.Error(w, "Failed to update worker", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/agents", http.StatusFound)
}

func (h *WebHandler) HandleAgentTransportUpdate(w http.ResponseWriter, r *http.Request) {
	if h.AgentNodeUpdate == nil {
		http.Error(w, "Worker manager is unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	node := strings.TrimSpace(r.FormValue("node"))
	mode := strings.TrimSpace(r.FormValue("mode"))
	controlURL := strings.TrimSpace(r.FormValue("control_url"))
	publicURL := strings.TrimSpace(r.FormValue("public_url"))
	if err := h.AgentNodeUpdate(node, mode, controlURL, publicURL); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/agents", http.StatusFound)
}

func (h *WebHandler) AuditLog(w http.ResponseWriter, r *http.Request) {
	data := struct {
		Authenticated  bool
		UserRole       string
		SiteName       string
		BackgroundURLs BackgroundURLs
	}{
		Authenticated:  true,
		UserRole:       "admin",
		SiteName:       h.siteName(),
		BackgroundURLs: h.pickBackgrounds([]string{"home"}),
	}

	tmpl, err := template.New("base.html").Funcs(sharedFuncMap(h.Store)).ParseFS(templateFS, "templates/base.html", "templates/audit.html")
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

func (h *WebHandler) Dashboard(w http.ResponseWriter, r *http.Request) {
	servers, err := h.Store.ListServers()
	if err != nil {
		http.Error(w, "Failed to load servers", http.StatusInternalServerError)
		return
	}

	totalServers := len(servers)
	onlineServers := 0
	offlineServers := 0
	maintenanceServers := 0
	plannedServers := 0

	gameTypes := make(map[string]int)
	for _, s := range servers {
		switch s.State {
		case "online":
			onlineServers++
		case "offline":
			offlineServers++
		case "maintenance":
			maintenanceServers++
		case "planned":
			plannedServers++
		}
		gameTypes[s.GameType]++
	}

	agents, _ := h.Store.ListAgents()
	if agents == nil {
		agents = []database.Agent{}
	}
	connectedAgents := 0
	for _, a := range agents {
		if a.Online {
			connectedAgents++
		}
	}

	recentAudit, err := h.Store.ListAuditLog(10, 0)
	if err != nil {
		recentAudit = []database.AuditLogEntry{}
	}

	cronEnabled := h.Config.CronEnabled

	data := struct {
		Servers            []database.Server
		TotalServers       int
		OnlineServers      int
		OfflineServers     int
		MaintenanceServers int
		PlannedServers     int
		GameTypes          map[string]int
		Agents             []database.Agent
		ConnectedAgents    int
		RecentAudit        []database.AuditLogEntry
		CronEnabled        bool
		Authenticated      bool
		UserRole           string
		SiteName           string
		UserEmail          string
		BackgroundURLs     BackgroundURLs
	}{
		Servers:            servers,
		TotalServers:       totalServers,
		OnlineServers:      onlineServers,
		OfflineServers:     offlineServers,
		MaintenanceServers: maintenanceServers,
		PlannedServers:     plannedServers,
		GameTypes:          gameTypes,
		Agents:             agents,
		ConnectedAgents:    connectedAgents,
		RecentAudit:        recentAudit,
		CronEnabled:        cronEnabled,
		Authenticated:      true,
		UserRole:           "admin",
		SiteName:           h.siteName(),
		UserEmail:          h.Auth.GetUserEmail(r),
		BackgroundURLs:     h.pickBackgrounds([]string{"home"}),
	}

	tmpl, err := template.New("base.html").Funcs(sharedFuncMap(h.Store)).ParseFS(templateFS, "templates/base.html", "templates/dashboard.html")
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

func (h *WebHandler) Backups(w http.ResponseWriter, r *http.Request) {
	servers, err := h.Store.ListServers()
	if err != nil {
		http.Error(w, "Failed to load servers", http.StatusInternalServerError)
		return
	}

	data := struct {
		Servers        []database.Server
		Authenticated  bool
		UserRole       string
		SiteName       string
		UserEmail      string
		BackgroundURLs BackgroundURLs
	}{
		Servers:        servers,
		Authenticated:  true,
		UserRole:       "admin",
		SiteName:       h.siteName(),
		UserEmail:      h.Auth.GetUserEmail(r),
		BackgroundURLs: h.pickBackgrounds([]string{"home"}),
	}

	tmpl, err := template.New("base.html").Funcs(sharedFuncMap(h.Store)).ParseFS(templateFS, "templates/base.html", "templates/backups.html")
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
