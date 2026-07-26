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
	"github.com/tionis/hogs/gametypes"
	"github.com/tionis/hogs/query"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/tionis/hogs/access"
	"github.com/tionis/hogs/agent"
)

//go:embed templates/*.html assets/*
var templateFS embed.FS

// WebHandler handles frontend requests.
type WebHandler struct {
	Store             *database.Store
	Config            *config.Config
	Auth              *auth.Authenticator
	Engine            *engine.Engine
	AgentConnected    func(int) bool
	AgentNodeInfo     func(string) (agent.NodeSummary, bool)
	AgentNodeUpdate   func(string, string, string, string) error
	AfterAccessChange func(int)
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
	seen := make(map[string]struct{})
	for _, server := range servers {
		if server.GameType != "" {
			// Keep current assignments visible even after a type is disabled.
			seen[server.GameType] = struct{}{}
		}
	}
	gameTypes, _ := h.Store.ListGameTypes()
	for _, info := range gameTypes {
		if info.Enabled {
			seen[info.Slug] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
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

func (h *WebHandler) ensureGameType(slug string, allowDisabled bool) error {
	existing, err := h.Store.GetGameType(slug)
	if err != nil {
		return err
	}
	if existing != nil {
		if !existing.Enabled && !allowDisabled {
			return fmt.Errorf("game type %q is disabled", slug)
		}
		return nil
	}
	info := query.GetGameInfo(slug)
	return h.Store.SetGameType(&database.GameType{
		Slug: slug, DisplayName: info.DisplayName, PlayerNoun: info.PlayerNoun,
		AccentColor: "#666666", Kind: gametypes.KindGeneric, Enabled: true,
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
	username := "anonymous"
	role := "user"
	if h.Auth != nil {
		username = h.Auth.GetUsername(r)
		role = h.Auth.GetUserRole(r)
	}
	if username == "" {
		username = "anonymous"
	}
	if role == "" {
		role = "user"
	}

	var groups []string
	if username != "anonymous" && h.Store != nil {
		user, _ := h.Store.GetUserByUsername(username)
		if user != nil {
			scimGroups, _ := h.Store.GetSCIMGroupsForUser(user.ID)
			for _, g := range scimGroups {
				groups = append(groups, g.DisplayName)
			}
		}
	}

	return &engine.UserEnv{
		Username: username, Role: role, Groups: groups,
		ClientIP:    auth.ClientIP(r, h.Config != nil && h.Config.TrustProxyHeaders),
		CountryCode: auth.ClientCountry(r, h.Config != nil && h.Config.TrustProxyHeaders),
	}
}

type PterodactylLinkData struct {
	ServerID        int                           `json:"serverId"`
	PteroServerID   string                        `json:"pteroServerId"`
	PteroIdentifier string                        `json:"pteroIdentifier"`
	AllowedActions  []string                      `json:"allowedActions"`
	ACLRule         string                        `json:"aclRule"`
	Node            string                        `json:"node"`
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

// FileManager preserves old administrator bookmarks while routing them to the
// capability-aware server file page.
func (h *WebHandler) FileManager(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]
	http.Redirect(w, r, "/servers/"+serverName+"/files", http.StatusFound)
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
		UserUsername   string
		SiteName       string
		BackgroundURLs BackgroundURLs
	}{
		Authenticated:  true,
		UserRole:       h.Auth.GetUserRole(r),
		UserUsername:   h.Auth.GetUsername(r),
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
				view, _ = h.Store.EvaluateServerAccess(s.ID, userEnv.Username, userEnv.Groups, access.View)
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
	h.renderServerPage(w, r, "dashboard")
}

func (h *WebHandler) ServerConsole(w http.ResponseWriter, r *http.Request) {
	h.renderServerPage(w, r, "console")
}

// ServerFiles renders the managed file browser separately from the operational
// server detail page.
func (h *WebHandler) ServerFiles(w http.ResponseWriter, r *http.Request) {
	h.renderServerPage(w, r, "files")
}

func (h *WebHandler) ServerWhitelist(w http.ResponseWriter, r *http.Request) {
	h.renderServerPage(w, r, "whitelist")
}

func (h *WebHandler) ServerAccess(w http.ResponseWriter, r *http.Request) {
	h.renderServerPage(w, r, "access")
}

func (h *WebHandler) ServerBackups(w http.ResponseWriter, r *http.Request) {
	h.renderServerPage(w, r, "backups")
}

func (h *WebHandler) ServerAutomation(w http.ResponseWriter, r *http.Request) {
	h.renderServerPage(w, r, "automation")
}

func (h *WebHandler) ServerSettings(w http.ResponseWriter, r *http.Request) {
	h.renderServerPage(w, r, "settings")
}

func (h *WebHandler) renderServerPage(w http.ResponseWriter, r *http.Request, page string) {
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
		view, _ = h.Store.EvaluateServerAccess(server.ID, userEnv.Username, userEnv.Groups, access.View)
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
		Server                      *database.Server
		Authenticated               bool
		UserRole                    string
		UserUsername                string
		SiteName                    string
		BackgroundURLs              BackgroundURLs
		PteroConfigured             bool
		PteroLink                   *PterodactylLinkData
		PteroCommands               []database.PterodactylCommand
		GameTypes                   []string
		ServerTags                  []string
		Agents                      []database.Agent
		AllowedActions              []string
		HasAgent                    bool
		ShowConsole                 bool
		ConsoleWrite                bool
		ShowFiles                   bool
		FileWrite                   bool
		ShowResources               bool
		EffectiveAccess             []EffectiveAccessEntry
		ManageAccess                bool
		AccessGrants                []database.ServerAccessGrant
		ServerConstraints           []database.Constraint
		ServerConstraintMaxPriority int
		AccessCatalog               []access.Capability
		ServerJoin                  bool
		WhitelistManage             bool
		IdentityCaseSensitive       bool
		IdentityLabel               string
		GameIdentitySettingsURL     string
		BackupList                  bool
		BackupCreate                bool
		BackupRestore               bool
		AutomationManage            bool
		AutomationJobs              []database.CronJob
		AutomationLogs              map[int][]database.CronJobLog
		CanRevealSecrets            bool
		Page                        string
		FilesPage                   bool
	}{
		Server:                      server,
		Authenticated:               isAuthenticated,
		UserRole:                    userRole,
		UserUsername:                h.Auth.GetUsername(r),
		SiteName:                    h.siteName(),
		BackgroundURLs:              h.pickBackgrounds([]string{server.GameType}),
		PteroConfigured:             h.Config.PterodactylURL != "",
		PteroLink:                   nil,
		PteroCommands:               nil,
		GameTypes:                   []string{},
		ServerTags:                  []string{},
		Agents:                      []database.Agent{},
		AllowedActions:              nil,
		HasAgent:                    hasAgent,
		ShowConsole:                 false,
		ConsoleWrite:                false,
		ShowFiles:                   false,
		FileWrite:                   false,
		ShowResources:               false,
		EffectiveAccess:             []EffectiveAccessEntry{},
		AccessGrants:                []database.ServerAccessGrant{},
		ServerConstraints:           []database.Constraint{},
		ServerConstraintMaxPriority: h.Config.ServerConstraintMaxPriority,
		AccessCatalog:               access.Capabilities,
		AutomationJobs:              []database.CronJob{},
		AutomationLogs:              map[int][]database.CronJobLog{},
		Page:                        page,
		FilesPage:                   page == "files",
		IdentityCaseSensitive:       h.Store.ResolveGameDriver(server.GameType).IdentityCaseSensitive,
		IdentityLabel:               h.Store.ResolveGameDriver(server.GameType).IdentityFieldLabel(),
		GameIdentitySettingsURL:     h.Config.GameIdentitySettingsURL,
	}
	if isAuthenticated {
		for _, capability := range access.Capabilities {
			decision := database.ServerAccessDecision{Allowed: true, Reason: "instance administrator"}
			if userRole != "admin" {
				decision, _ = h.Store.EvaluateServerAccess(server.ID, userEnv.Username, userEnv.Groups, capability.Name)
			}
			data.EffectiveAccess = append(data.EffectiveAccess, EffectiveAccessEntry{
				Name: capability.Name, Label: capability.Label, Allowed: decision.Allowed, Reason: decision.Reason,
			})
		}
		manageDecision := database.ServerAccessDecision{Allowed: userRole == "admin"}
		if userRole != "admin" {
			manageDecision, _ = h.Store.EvaluateServerAccess(server.ID, userEnv.Username, userEnv.Groups, access.AccessManage)
		}
		data.ManageAccess = manageDecision.Allowed
		if data.ManageAccess {
			data.AccessGrants, _ = h.Store.ListServerAccessGrants(server.ID)
			if data.AccessGrants == nil {
				data.AccessGrants = []database.ServerAccessGrant{}
			}
			data.ServerConstraints, _ = h.Store.ListServerConstraints(server.ID)
			if data.ServerConstraints == nil {
				data.ServerConstraints = []database.Constraint{}
			}
		}
		whitelistDecision := database.ServerAccessDecision{Allowed: userRole == "admin"}
		if userRole != "admin" {
			whitelistDecision, _ = h.Store.EvaluateServerAccess(server.ID, userEnv.Username, userEnv.Groups, access.WhitelistManage)
		}
		data.WhitelistManage = whitelistDecision.Allowed

		serverJoinDecision := database.ServerAccessDecision{Allowed: userRole == "admin"}
		backupListDecision := database.ServerAccessDecision{Allowed: userRole == "admin"}
		backupCreateDecision := database.ServerAccessDecision{Allowed: userRole == "admin"}
		backupRestoreDecision := database.ServerAccessDecision{Allowed: userRole == "admin"}
		secretReadDecision := database.ServerAccessDecision{Allowed: userRole == "admin"}
		automationDecision := database.ServerAccessDecision{Allowed: userRole == "admin"}
		if userRole != "admin" {
			serverJoinDecision, _ = h.Store.EvaluateServerAccess(server.ID, userEnv.Username, userEnv.Groups, access.ServerJoin)
			backupListDecision, _ = h.Store.EvaluateServerAccess(server.ID, userEnv.Username, userEnv.Groups, access.BackupList)
			backupCreateDecision, _ = h.Store.EvaluateServerAccess(server.ID, userEnv.Username, userEnv.Groups, access.BackupCreate)
			backupRestoreDecision, _ = h.Store.EvaluateServerAccess(server.ID, userEnv.Username, userEnv.Groups, access.BackupRestore)
			secretReadDecision, _ = h.Store.EvaluateServerAccess(server.ID, userEnv.Username, userEnv.Groups, access.SecretRead)
			automationDecision, _ = h.Store.EvaluateServerAccess(server.ID, userEnv.Username, userEnv.Groups, access.AutomationManage)
		}
		data.ServerJoin = serverJoinDecision.Allowed
		data.BackupList = backupListDecision.Allowed
		data.BackupCreate = backupCreateDecision.Allowed
		data.BackupRestore = backupRestoreDecision.Allowed
		data.CanRevealSecrets = secretReadDecision.Allowed
		if !h.Store.ResolveGameDriver(server.GameType).SupportsWhitelist() {
			data.CanRevealSecrets = data.CanRevealSecrets || data.ServerJoin
		}
		data.AutomationManage = automationDecision.Allowed
		if !h.Store.ResolveGameDriver(server.GameType).SupportsWhitelist() {
			data.ServerJoin = false
			data.WhitelistManage = false
		}
		management, _ := h.Store.GetServerManagement(server.ID)
		if management == nil || !management.RestoreEnabled {
			data.BackupRestore = false
		}
	}
	if isAuthenticated && hasAgent {
		if userRole == "admin" {
			data.ShowConsole, data.ConsoleWrite = true, true
			data.ShowFiles, data.FileWrite = true, true
			data.ShowResources = true
		} else {
			consoleRead, _ := h.Store.EvaluateServerAccess(server.ID, userEnv.Username, userEnv.Groups, access.ConsoleRead)
			consoleWrite, _ := h.Store.EvaluateServerAccess(server.ID, userEnv.Username, userEnv.Groups, access.ConsoleWrite)
			fileRead, _ := h.Store.EvaluateServerAccess(server.ID, userEnv.Username, userEnv.Groups, access.FileRead)
			fileWrite, _ := h.Store.EvaluateServerAccess(server.ID, userEnv.Username, userEnv.Groups, access.FileWrite)
			status, _ := h.Store.EvaluateServerAccess(server.ID, userEnv.Username, userEnv.Groups, access.Status)
			data.ShowConsole, data.ConsoleWrite = consoleRead.Allowed, consoleWrite.Allowed
			data.ShowFiles, data.FileWrite = fileRead.Allowed, fileWrite.Allowed
			data.ShowResources = status.Allowed
		}
	}
	if page == "files" {
		if !data.ShowFiles {
			http.Error(w, "File access denied", http.StatusForbidden)
			return
		}
	}

	if link != nil {
		commands, _ := h.Store.ListPterodactylCommands(server.ID)
		if commands == nil {
			commands = []database.PterodactylCommand{}
		}
		data.PteroCommands = commands
		var configuredActions []string
		_ = json.Unmarshal([]byte(link.AllowedActions), &configuredActions)
		data.PteroLink = &PterodactylLinkData{
			ServerID:        server.ID,
			PteroServerID:   link.PteroServerID,
			PteroIdentifier: link.PteroIdentifier,
			AllowedActions:  configuredActions,
			ACLRule:         link.ACLRule,
			Node:            link.Node,
			Commands:        commands,
		}
	}

	var allowedActions []string
	if link != nil && data.PteroLink != nil {
		for _, action := range data.PteroLink.AllowedActions {
			allowed, evalErr := h.Engine.EvaluateACL(link, server, action, userEnv)
			if evalErr == nil && allowed {
				allowedActions = append(allowedActions, action)
			}
		}
	}
	data.AllowedActions = allowedActions

	switch page {
	case "dashboard":
	case "console":
		if !data.ShowConsole {
			http.Error(w, "Console access denied", http.StatusForbidden)
			return
		}
	case "files":
	case "whitelist":
		if !data.ServerJoin && !data.WhitelistManage {
			http.Error(w, "Whitelist access denied", http.StatusForbidden)
			return
		}
	case "access":
		if !isAuthenticated {
			http.Error(w, "Authentication required", http.StatusUnauthorized)
			return
		}
	case "backups":
		if !data.BackupList && !data.BackupCreate && !data.BackupRestore {
			http.Error(w, "Backup access denied", http.StatusForbidden)
			return
		}
	case "automation":
		if !data.AutomationManage {
			http.Error(w, "Automation access denied", http.StatusForbidden)
			return
		}
		data.AutomationJobs, _ = h.Store.ListCronJobsForServer(server.ID)
		if data.AutomationJobs == nil {
			data.AutomationJobs = []database.CronJob{}
		}
		for _, job := range data.AutomationJobs {
			if entries, logErr := h.Store.ListCronJobLogs(job.ID, 8); logErr == nil {
				data.AutomationLogs[job.ID] = entries
			}
		}
	case "settings":
		if userRole != "admin" {
			http.Error(w, "Instance administrator access required", http.StatusForbidden)
			return
		}
		allServers, _ := h.Store.ListServers()
		data.GameTypes = h.adminGameTypes(allServers)
		data.ServerTags, _ = h.Store.GetServerTags(server.ID)
		if data.ServerTags == nil {
			data.ServerTags = []string{}
		}
		data.Agents, _ = h.Store.ListAgents()
		if data.Agents == nil {
			data.Agents = []database.Agent{}
		}
	default:
		http.NotFound(w, r)
		return
	}

	tmpl, err := template.New("base.html").Funcs(sharedFuncMap(h.Store)).ParseFS(
		templateFS, "templates/base.html", "templates/server.html", "templates/server_edit.html",
		"templates/server_automation.html",
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
	buf.WriteTo(w)
}

// RevealServerField returns one user-facing secret only after an explicit,
// capability-checked POST. Secret values are never embedded into page HTML.
func (h *WebHandler) RevealServerField(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("Pragma", "no-cache")
	server, err := h.Store.GetServerByName(mux.Vars(r)["serverName"])
	if err != nil || server == nil {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}
	fieldID, err := strconv.Atoi(mux.Vars(r)["fieldID"])
	if err != nil || fieldID <= 0 {
		http.Error(w, "Field not found", http.StatusNotFound)
		return
	}
	user := h.getUserEnv(r)
	allowed := user.Role == "admin"
	reason := "instance administrator"
	revealCapability := access.SecretRead
	if !allowed {
		view, viewErr := h.Store.EvaluateServerAccess(server.ID, user.Username, user.Groups, access.View)
		decision, decisionErr := h.Store.EvaluateServerAccess(server.ID, user.Username, user.Groups, access.SecretRead)
		if viewErr != nil || decisionErr != nil {
			http.Error(w, "Could not evaluate server access", http.StatusInternalServerError)
			return
		}
		allowed, reason = view.Allowed && decision.Allowed, decision.Reason
		if !decision.Allowed && !h.Store.ResolveGameDriver(server.GameType).SupportsWhitelist() {
			join, joinErr := h.Store.EvaluateServerAccess(server.ID, user.Username, user.Groups, access.ServerJoin)
			if joinErr != nil {
				http.Error(w, "Could not evaluate server access", http.StatusInternalServerError)
				return
			}
			if join.Allowed {
				allowed, reason, revealCapability = view.Allowed, join.Reason, access.ServerJoin
			}
		}
		if !view.Allowed {
			reason = view.Reason
		}
	}
	if allowed {
		constraint, constraintErr := h.Engine.EvaluateConstraints(server, revealCapability, user)
		if constraintErr != nil {
			h.auditServerFieldReveal(server, user, fieldID, "", "error", "could not evaluate operational constraints")
			http.Error(w, "Could not evaluate server access", http.StatusInternalServerError)
			return
		}
		if !constraint.Allowed {
			allowed, reason = false, constraint.Reason
		}
	}
	if !allowed {
		h.auditServerFieldReveal(server, user, fieldID, "", "denied", reason)
		http.Error(w, "Secret access denied", http.StatusForbidden)
		return
	}
	field, err := h.Store.GetServerField(server.ID, fieldID)
	if err != nil || field == nil || field.Disclosure != database.FieldDisclosureReveal {
		h.auditServerFieldReveal(server, user, fieldID, "", "error", "field is not revealable")
		http.Error(w, "Field not found", http.StatusNotFound)
		return
	}
	value, err := h.Store.GetServerFieldValue(server.ID, field.ID)
	if err != nil {
		h.auditServerFieldReveal(server, user, fieldID, field.Key, "error", "stored secret could not be opened")
		http.Error(w, "Could not reveal server secret", http.StatusInternalServerError)
		return
	}
	h.auditServerFieldReveal(server, user, fieldID, field.Key, "success", "secret revealed after explicit request")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"value": value})
}

func (h *WebHandler) auditServerFieldReveal(server *database.Server, user *engine.UserEnv, fieldID int, fieldKey, result, reason string) {
	params, _ := json.Marshal(map[string]interface{}{"fieldId": fieldID, "fieldKey": fieldKey})
	entry := &database.AuditLogEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339), UserUsername: user.Username,
		ServerName: server.Name, Action: access.SecretRead, Params: params,
		Result: result, Reason: reason, Source: "web", ClientIP: user.ClientIP,
		CountryCode: user.CountryCode,
	}
	if err := h.Store.CreateAuditLog(entry); err != nil {
		log.Printf("Warning: failed to audit server secret reveal: %v", err)
	}
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
		UserUsername    string
		BackgroundURLs  BackgroundURLs
	}{
		Servers:         servers,
		ServerTemplates: templates,
		GameTypes:       h.adminGameTypes(servers),
		Authenticated:   true,
		UserRole:        "admin",
		SiteName:        h.siteName(),
		UserUsername:    h.Auth.GetUsername(r),
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
	if err := h.ensureGameType(gameType, false); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	state, validState := normalizePresentationState(r.FormValue("state"))
	if !validState {
		http.Error(w, "Invalid presentation state", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if !validServerDisplayName(name) {
		http.Error(w, "Server name must contain 1-120 printable characters", http.StatusBadRequest)
		return
	}
	server := &database.Server{
		Name:        name,
		Address:     r.FormValue("address"),
		Description: r.FormValue("description"),
		MapURL:      r.FormValue("map_url"),
		ModURL:      r.FormValue("mod_url"),
		GameType:    gameType,
		State:       state,
		ShowMOTD:    r.FormValue("show_motd") == "on",
		Metadata:    h.parseMetadata(r),
	}
	fields, err := h.parseServerFields(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.Store.CreateServer(server); err != nil {
		http.Error(w, "Failed to create server: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.Store.ReplaceServerFields(server.ID, fields); err != nil {
		_ = h.Store.DeleteServer(server.ID)
		http.Error(w, "Failed to save server fields: "+err.Error(), http.StatusBadRequest)
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
	id, err := strconv.Atoi(mux.Vars(r)["id"])
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
	http.Redirect(w, r, "/servers/"+url.PathEscape(server.Name)+"/settings", http.StatusMovedPermanently)
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
	current, currentErr := h.Store.GetServer(id)
	if currentErr != nil || current == nil {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}
	if err := h.ensureGameType(gameType, current.GameType == gameType); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	state, validState := normalizePresentationState(r.FormValue("state"))
	if !validState {
		http.Error(w, "Invalid presentation state", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if !validServerDisplayName(name) {
		http.Error(w, "Server name must contain 1-120 printable characters", http.StatusBadRequest)
		return
	}
	server := &database.Server{
		ID:          id,
		Name:        name,
		Address:     r.FormValue("address"),
		Description: r.FormValue("description"),
		MapURL:      r.FormValue("map_url"),
		ModURL:      r.FormValue("mod_url"),
		GameType:    gameType,
		State:       state,
		ShowMOTD:    r.FormValue("show_motd") == "on",
		Metadata:    h.parseMetadata(r),
	}
	fields, err := h.parseServerFields(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.Store.UpdateServer(server); err != nil {
		http.Error(w, "Failed to update server: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.Store.ReplaceServerFields(server.ID, fields); err != nil {
		http.Error(w, "Failed to save server fields: "+err.Error(), http.StatusBadRequest)
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

	http.Redirect(w, r, "/servers/"+url.PathEscape(server.Name)+"/settings", http.StatusFound)
}

func validServerDisplayName(name string) bool {
	return name != "" && len([]rune(name)) <= 120 && !strings.ContainsAny(name, "\r\n\x00")
}

func normalizePresentationState(state string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "", "online", "auto":
		return "online", true
	case "offline", "planned", "maintenance":
		return strings.ToLower(strings.TrimSpace(state)), true
	default:
		return "", false
	}
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
		if k != "" && k != "api_token" && k != "rcon_password" {
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

func (h *WebHandler) parseServerFields(r *http.Request) ([]database.ServerField, error) {
	ids := r.Form["field_id"]
	keys := r.Form["field_key"]
	labels := r.Form["field_label"]
	values := r.Form["field_value"]
	placements := r.Form["field_placement"]
	disclosures := r.Form["field_disclosure"]
	count := len(keys)
	for _, length := range []int{len(labels), len(values), len(placements), len(disclosures)} {
		if length < count {
			count = length
		}
	}
	fields := make([]database.ServerField, 0, count+2)
	seen := make(map[string]bool, count+2)
	for i := 0; i < count; i++ {
		if strings.TrimSpace(keys[i]) == "" {
			continue
		}
		id := 0
		if i < len(ids) && strings.TrimSpace(ids[i]) != "" {
			parsed, err := strconv.Atoi(ids[i])
			if err != nil || parsed < 0 {
				return nil, fmt.Errorf("invalid server field ID")
			}
			id = parsed
		}
		field := database.ServerField{
			ID: id, Key: keys[i], Label: labels[i], Value: values[i],
			Placement: placements[i], Disclosure: disclosures[i],
		}
		if err := database.ValidateServerField(field); err != nil {
			return nil, fmt.Errorf("invalid server field %q: %w", field.Key, err)
		}
		if seen[field.Key] {
			return nil, fmt.Errorf("server field key %q is duplicated", field.Key)
		}
		seen[field.Key] = true
		fields = append(fields, field)
	}

	// Compatibility for administrators who still submit established backend
	// credential keys through the advanced metadata editor.
	metaKeys, metaValues := r.Form["meta_key"], r.Form["meta_value"]
	for i, key := range metaKeys {
		if (key != "api_token" && key != "rcon_password") || i >= len(metaValues) || seen[key] {
			continue
		}
		value := metaValues[i]
		label := "API token"
		if key == "rcon_password" {
			label = "RCON password"
		}
		fields = append(fields, database.ServerField{
			Key: key, Label: label, Value: value,
			Placement:  database.FieldPlacementInternal,
			Disclosure: database.FieldDisclosureWriteOnly,
		})
	}
	return fields, nil
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
		UserUsername   string
		BackgroundURLs BackgroundURLs
	}{
		Backgrounds:    backgrounds,
		AvailableTags:  availableTags,
		Authenticated:  true,
		UserRole:       "admin",
		SiteName:       h.siteName(),
		UserUsername:   h.Auth.GetUsername(r),
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
		UserUsername   string
		BackgroundURLs BackgroundURLs
	}{
		SiteName:       siteName,
		Authenticated:  true,
		UserRole:       "admin",
		UserUsername:   h.Auth.GetUsername(r),
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
		identities, _ := h.Store.ListGameIdentities(u.Username)
		scimIdentities := make([]database.GameIdentity, 0, len(identities))
		for _, identity := range identities {
			if identity.Source == "scim" {
				scimIdentities = append(scimIdentities, identity)
			}
		}
		usersWithGroups = append(usersWithGroups, UserWithGroups{
			User:       u,
			Groups:     groups,
			Identities: scimIdentities,
		})
	}

	data := struct {
		Users          []UserWithGroups
		Authenticated  bool
		UserRole       string
		SiteName       string
		UserUsername   string
		BackgroundURLs BackgroundURLs
	}{
		Users:          usersWithGroups,
		Authenticated:  true,
		UserRole:       "admin",
		SiteName:       h.siteName(),
		UserUsername:   h.Auth.GetUsername(r),
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
		UserUsername   string
		BackgroundURLs BackgroundURLs
	}{
		GameTypes: gameTypes, Authenticated: true, UserRole: "admin",
		SiteName: h.siteName(), UserUsername: h.Auth.GetUsername(r),
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
		Kind: gametypes.KindGeneric, Enabled: true,
	}
	if existing != nil {
		item.Builtin = existing.Builtin
		item.Kind = existing.Kind
		item.Enabled = !existing.Builtin || r.FormValue("enabled") == "on"
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
	if h.AfterAccessChange != nil {
		h.AfterAccessChange(serverID)
	}
	http.Redirect(w, r, "/servers/"+server.Name+"/access#access-control", http.StatusFound)
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
	if h.AfterAccessChange != nil {
		h.AfterAccessChange(serverID)
	}
	http.Redirect(w, r, "/servers/"+server.Name+"/access#access-control", http.StatusFound)
}

func (h *WebHandler) HandleServerConstraintSet(w http.ResponseWriter, r *http.Request) {
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
	priority, err := strconv.Atoi(r.FormValue("priority"))
	if err != nil || priority > h.Config.ServerConstraintMaxPriority {
		http.Error(w, fmt.Sprintf("Server constraint priority must not exceed %d", h.Config.ServerConstraintMaxPriority), http.StatusBadRequest)
		return
	}
	mode := r.FormValue("mode")
	if mode != "require" && mode != "exempt" {
		http.Error(w, "Mode must be require or exempt", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	condition := strings.TrimSpace(r.FormValue("condition"))
	if name == "" || condition == "" {
		http.Error(w, "Name and condition are required", http.StatusBadRequest)
		return
	}
	constraint := &database.Constraint{
		ServerID:    &serverID,
		Name:        name,
		Description: strings.TrimSpace(r.FormValue("description")),
		Condition:   condition,
		Mode:        mode,
		Strategy:    "deny",
		Priority:    priority,
		Enabled:     r.FormValue("enabled") == "on",
	}
	if idText := r.FormValue("id"); idText != "" {
		constraint.ID, err = strconv.Atoi(idText)
		if err != nil {
			http.Error(w, "Invalid constraint ID", http.StatusBadRequest)
			return
		}
		existing, getErr := h.Store.GetConstraint(constraint.ID)
		if getErr != nil || existing == nil || existing.ServerID == nil || *existing.ServerID != serverID {
			http.Error(w, "Server constraint not found", http.StatusNotFound)
			return
		}
		err = h.Store.UpdateConstraint(constraint)
	} else {
		err = h.Store.CreateConstraint(constraint)
	}
	if err != nil {
		http.Error(w, "Failed to save server constraint: "+err.Error(), http.StatusConflict)
		return
	}
	http.Redirect(w, r, "/servers/"+server.Name+"/access#server-constraints", http.StatusFound)
}

func (h *WebHandler) HandleServerConstraintDelete(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	serverID, serverErr := strconv.Atoi(r.FormValue("server_id"))
	constraintID, constraintErr := strconv.Atoi(r.FormValue("constraint_id"))
	if serverErr != nil || constraintErr != nil {
		http.Error(w, "Invalid server constraint", http.StatusBadRequest)
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
	if err := h.Store.DeleteServerConstraint(constraintID, serverID); err != nil {
		http.Error(w, "Failed to delete server constraint", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/servers/"+server.Name+"/access#server-constraints", http.StatusFound)
}

func (h *WebHandler) canManageServerAccess(r *http.Request, serverID int) bool {
	user := h.getUserEnv(r)
	if user != nil && (user.Role == "admin" || user.Role == "system") {
		return true
	}
	if user == nil {
		return false
	}
	decision, err := h.Store.EvaluateServerAccess(serverID, user.Username, user.Groups, access.AccessManage)
	return err == nil && decision.Allowed
}

var safeGameUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9_. -]{1,192}$`)

func validGameUsername(gameType, username string) bool {
	return safeGameUsernamePattern.MatchString(username)
}

func (h *WebHandler) HandleGameIdentitySet(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	role := h.userRole(r)
	panelUsername := h.Auth.GetUsername(r)
	source := "self"
	if role == "admin" && strings.TrimSpace(r.FormValue("user_username")) != "" {
		panelUsername = strings.TrimSpace(r.FormValue("user_username"))
		source = "admin"
	}
	gameType := normalizeGameType(r.FormValue("game_type"))
	username := strings.TrimSpace(r.FormValue("username"))
	driver := h.Store.ResolveGameDriver(gameType)
	if panelUsername == "" || !validGameType(gameType) || !validGameUsername(gameType, username) ||
		!driver.IdentityValid(username) {
		http.Error(w, "Invalid game identity", http.StatusBadRequest)
		return
	}
	externalID := ""
	if existing, _ := h.Store.GetGameIdentity(panelUsername, gameType); existing != nil &&
		driver.IdentitiesEqual(existing.Username, username) {
		externalID = existing.ExternalID
	}
	if err := h.Store.SetGameIdentity(&database.GameIdentity{
		UserUsername: panelUsername, GameType: gameType, Username: username,
		ExternalID: externalID, Source: source,
	}); err != nil {
		http.Error(w, "Failed to save game identity", http.StatusInternalServerError)
		return
	}
	if role == "admin" {
		http.Redirect(w, r, "/admin/users", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/account/settings#linked-game-accounts", http.StatusFound)
}

func (h *WebHandler) HandleGameIdentityDelete(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	role := h.userRole(r)
	panelUsername := h.Auth.GetUsername(r)
	if role == "admin" && strings.TrimSpace(r.FormValue("user_username")) != "" {
		panelUsername = strings.TrimSpace(r.FormValue("user_username"))
	}
	gameType := normalizeGameType(r.FormValue("game_type"))
	if err := h.Store.DeleteGameIdentity(panelUsername, gameType); err != nil {
		http.Error(w, "Failed to delete game identity", http.StatusInternalServerError)
		return
	}
	if role == "admin" {
		http.Redirect(w, r, "/admin/users", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/account/settings#linked-game-accounts", http.StatusFound)
}

func (h *WebHandler) HandleUserUpdate(w http.ResponseWriter, r *http.Request) {
	// User updates are disabled — OIDC is the authoritative source.
	// This endpoint is kept for backwards compatibility but does nothing.
	http.Redirect(w, r, "/admin/users", http.StatusFound)
}

func (h *WebHandler) MyServers(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/account/settings", http.StatusFound)
}

func (h *WebHandler) UserSettings(w http.ResponseWriter, r *http.Request) {
	user := h.getUserEnv(r)
	identities, _ := h.Store.ListGameIdentities(user.Username)
	scimIdentities := make([]database.GameIdentity, 0, len(identities))
	for _, identity := range identities {
		if identity.Source == "scim" {
			scimIdentities = append(scimIdentities, identity)
		}
	}

	data := struct {
		Identities              []database.GameIdentity
		GameIdentitySettingsURL string
		Authenticated           bool
		UserRole                string
		UserUsername            string
		SiteName                string
		BackgroundURLs          BackgroundURLs
	}{
		Identities:              scimIdentities,
		GameIdentitySettingsURL: h.Config.GameIdentitySettingsURL,
		Authenticated:           true,
		UserRole:                h.userRole(r),
		UserUsername:            user.Username,
		SiteName:                h.siteName(),
		BackgroundURLs:          h.pickBackgrounds([]string{"home"}),
	}

	tmpl, err := template.New("base.html").Funcs(sharedFuncMap(h.Store)).ParseFS(templateFS, "templates/base.html", "templates/user_settings.html")
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
		UserUsername   string
		BackgroundURLs BackgroundURLs
	}{
		Server:         server,
		CommandSchemas: schemas,
		Authenticated:  true,
		UserRole:       "admin",
		SiteName:       h.siteName(),
		UserUsername:   h.Auth.GetUsername(r),
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
	constraints, _ := h.Store.ListInstanceConstraints()
	if constraints == nil {
		constraints = []database.Constraint{}
	}

	data := struct {
		Constraints    []database.Constraint
		Authenticated  bool
		UserRole       string
		SiteName       string
		UserUsername   string
		BackgroundURLs BackgroundURLs
	}{
		Constraints:    constraints,
		Authenticated:  true,
		UserRole:       "admin",
		SiteName:       h.siteName(),
		UserUsername:   h.Auth.GetUsername(r),
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
	md += "| `user.Username` | `string` | Requesting user's Authentik username |\n"
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
		UserUsername       string
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
		UserUsername:       h.Auth.GetUsername(r),
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
