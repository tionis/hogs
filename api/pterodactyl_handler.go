package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/tionis/hogs/agent"
	"github.com/tionis/hogs/auth"
	"github.com/tionis/hogs/backend"
	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/engine"
	"github.com/tionis/hogs/pterodactyl"
)

type PterodactylHandler struct {
	Store    *database.Store
	Config   *config.Config
	Engine   *engine.Engine
	AgentHub *agent.Hub
	Auth     *auth.Authenticator
}

func NewPterodactylHandler(store *database.Store, cfg *config.Config, eng *engine.Engine, hub *agent.Hub, auth *auth.Authenticator) *PterodactylHandler {
	return &PterodactylHandler{Store: store, Config: cfg, Engine: eng, AgentHub: hub, Auth: auth}
}

func (h *PterodactylHandler) client() *pterodactyl.Client {
	if h.Config.PterodactylURL == "" || h.Config.PterodactylAppKey == "" {
		return nil
	}
	c := pterodactyl.NewClient(h.Config.PterodactylURL, h.Config.PterodactylAppKey)
	c.ClientKey = h.Config.PterodactylClientKey
	return c
}

func (h *PterodactylHandler) resolveIdentifier(c *pterodactyl.Client, uuid string) (string, error) {
	srv, err := c.GetServer(uuid)
	if err != nil {
		return "", err
	}
	return srv.Identifier, nil
}

func (h *PterodactylHandler) ListPteroServers(w http.ResponseWriter, r *http.Request) {
	c := h.client()
	if c == nil {
		http.Error(w, "Pterodactyl not configured", http.StatusServiceUnavailable)
		return
	}

	servers, err := c.ListServers()
	if err != nil {
		http.Error(w, "Failed to list Pterodactyl servers: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(servers)
}

func (h *PterodactylHandler) GetLink(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	serverID, err := strconv.Atoi(vars["serverId"])
	if err != nil {
		http.Error(w, "Invalid server ID", http.StatusBadRequest)
		return
	}

	link, err := h.Store.GetPterodactylLink(serverID)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if link == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"linked": false})
		return
	}

	commands, _ := h.Store.ListPterodactylCommands(serverID)
	if commands == nil {
		commands = []database.PterodactylCommand{}
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"linked":         true,
		"pteroServerId":  link.PteroServerID,
		"allowedActions": link.AllowedActions,
		"commands":       commands,
	})
}

func (h *PterodactylHandler) LinkServer(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	serverID, err := strconv.Atoi(r.FormValue("server_id"))
	if err != nil {
		http.Error(w, "Invalid server ID", http.StatusBadRequest)
		return
	}

	pteroServerID := r.FormValue("ptero_server_id")
	if pteroServerID == "" {
		http.Error(w, "Pterodactyl server ID is required", http.StatusBadRequest)
		return
	}

	pteroIdentifier := r.FormValue("ptero_identifier")
	allowedActions := r.FormValue("allowed_actions")
	if allowedActions == "" {
		allowedActions = "[]"
	}
	node := r.FormValue("node")

	link := &database.PterodactylLink{
		ServerID:        serverID,
		PteroServerID:   pteroServerID,
		PteroIdentifier: pteroIdentifier,
		AllowedActions:  allowedActions,
		Node:            node,
	}

	existing, err := h.Store.GetPterodactylLink(serverID)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if existing != nil {
		link.ID = existing.ID
		if err := h.Store.UpdatePterodactylLink(link); err != nil {
			http.Error(w, "Failed to update link", http.StatusInternalServerError)
			return
		}
	} else {
		if err := h.Store.CreatePterodactylLink(link); err != nil {
			http.Error(w, "Failed to create link", http.StatusInternalServerError)
			return
		}
	}

	srv, _ := h.Store.GetServer(serverID)
	if srv != nil {
		http.Redirect(w, r, "/admin/servers/"+strconv.Itoa(serverID), http.StatusFound)
	} else {
		http.Redirect(w, r, "/admin", http.StatusFound)
	}
}

func (h *PterodactylHandler) UnlinkServer(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	serverID, err := strconv.Atoi(r.FormValue("server_id"))
	if err != nil {
		http.Error(w, "Invalid server ID", http.StatusBadRequest)
		return
	}

	if err := h.Store.DeletePterodactylLink(serverID); err != nil {
		http.Error(w, "Failed to unlink server", http.StatusInternalServerError)
		return
	}

	srv, _ := h.Store.GetServer(serverID)
	if srv != nil {
		http.Redirect(w, r, "/admin/servers/"+strconv.Itoa(serverID), http.StatusFound)
	} else {
		http.Redirect(w, r, "/admin", http.StatusFound)
	}
}

func (h *PterodactylHandler) AddCommand(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	serverID, err := strconv.Atoi(r.FormValue("server_id"))
	if err != nil {
		http.Error(w, "Invalid server ID", http.StatusBadRequest)
		return
	}

	cmd := &database.PterodactylCommand{
		ServerID:    serverID,
		Command:     r.FormValue("command"),
		DisplayName: r.FormValue("display_name"),
	}

	if cmd.Command == "" || cmd.DisplayName == "" {
		http.Error(w, "Command and display name are required", http.StatusBadRequest)
		return
	}

	if err := h.Store.CreatePterodactylCommand(cmd); err != nil {
		http.Error(w, "Failed to create command", http.StatusInternalServerError)
		return
	}

	srv, _ := h.Store.GetServer(serverID)
	if srv != nil {
		http.Redirect(w, r, "/admin/servers/"+strconv.Itoa(serverID), http.StatusFound)
	} else {
		http.Redirect(w, r, "/admin", http.StatusFound)
	}
}

func (h *PterodactylHandler) DeleteCommand(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	commandID, err := strconv.Atoi(r.FormValue("command_id"))
	if err != nil {
		http.Error(w, "Invalid command ID", http.StatusBadRequest)
		return
	}

	if err := h.Store.DeletePterodactylCommand(commandID); err != nil {
		http.Error(w, "Failed to delete command", http.StatusInternalServerError)
		return
	}

	serverIDStr := r.FormValue("server_id")
	if serverIDStr != "" {
		if sid, err := strconv.Atoi(serverIDStr); err == nil {
			http.Redirect(w, r, "/admin/servers/"+strconv.Itoa(sid), http.StatusFound)
			return
		}
	}
	http.Redirect(w, r, "/admin", http.StatusFound)
}

func (h *PterodactylHandler) ServerAction(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	serverName := vars["serverName"]
	action := r.FormValue("action")

	server, err := h.Store.GetServerByName(serverName)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if server == nil {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	link, err := h.Store.GetPterodactylLink(server.ID)
	if err != nil || link == nil {
		http.Error(w, "Server not linked to any backend", http.StatusNotFound)
		return
	}

	user := h.getUserEnv(r)
	if h.Engine != nil {
		result := h.Engine.Evaluate(server, action, nil, user)
		if !result.Allowed {
			http.Error(w, result.Reason, result.Status)
			return
		}
	} else {
		if !isActionAllowed(link.AllowedActions, action) {
			http.Error(w, "Action not permitted for this server", http.StatusForbidden)
			return
		}
	}

	b, err := h.resolveBackend(server, link)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	switch action {
	case "start":
		err = b.Start(ctx)
	case "stop":
		err = b.Stop(ctx)
	case "restart":
		err = b.Restart(ctx)
	default:
		http.Error(w, fmt.Sprintf("Unknown action: %s", action), http.StatusBadRequest)
		return
	}

	if err != nil {
		http.Error(w, fmt.Sprintf("%s action failed: %s", b.Name(), err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (h *PterodactylHandler) resolveBackend(server *database.Server, link *database.PterodactylLink) (backend.Backend, error) {
	if link.Node != "" && h.AgentHub != nil {
		ag, err := h.Store.GetAgentByNodeName(link.Node)
		if err == nil && ag != nil {
			return agent.NewAgentBackend(ag.ID, ag.NodeName, h.AgentHub), nil
		}
	}

	c := h.client()
	if c == nil {
		return nil, fmt.Errorf("no backend available (Pterodactyl not configured, no agent on node %q)", link.Node)
	}

	if c.ClientKey == "" {
		return nil, fmt.Errorf("Pterodactyl client key not configured")
	}

	identifier := link.PteroIdentifier
	if identifier == "" {
		id, err := h.resolveIdentifier(c, link.PteroServerID)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve Pterodactyl identifier: %w", err)
		}
		identifier = id
		link.PteroIdentifier = identifier
		h.Store.UpdatePterodactylLink(link)
	}

	return backend.NewPterodactylBackend(h.Config, link.PteroServerID, identifier), nil
}

func (h *PterodactylHandler) getUserEnv(r *http.Request) *engine.UserEnv {
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

	// Fetch user's SCIM groups
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

func (h *PterodactylHandler) evaluateACLEnabled(link *database.PterodactylLink, server *database.Server, action string, user *engine.UserEnv) bool {
	result := h.Engine.Evaluate(server, action, nil, user)
	return result.Allowed
}

func (h *PterodactylHandler) SendCommand(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	serverName := vars["serverName"]
	command := r.FormValue("command")

	if command == "" {
		http.Error(w, "Command is required", http.StatusBadRequest)
		return
	}
	if !isValidCommand(command) {
		http.Error(w, "Invalid command format", http.StatusBadRequest)
		return
	}

	server, err := h.Store.GetServerByName(serverName)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if server == nil {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	link, err := h.Store.GetPterodactylLink(server.ID)
	if err != nil || link == nil {
		http.Error(w, "Server not linked to any backend", http.StatusNotFound)
		return
	}

	action := "command:" + command
	user := h.getUserEnv(r)
	if h.Engine != nil {
		result := h.Engine.Evaluate(server, action, nil, user)
		if !result.Allowed {
			http.Error(w, result.Reason, result.Status)
			return
		}
	} else {
		if !isActionAllowed(link.AllowedActions, action) {
			http.Error(w, "Command not permitted for this server", http.StatusForbidden)
			return
		}
	}

	b, err := h.resolveBackend(server, link)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	if err := b.SendCommand(ctx, command); err != nil {
		http.Error(w, fmt.Sprintf("%s command failed: %s", b.Name(), err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (h *PterodactylHandler) WhitelistSet(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	serverName := vars["serverName"]
	username := r.FormValue("username")

	if username == "" {
		http.Error(w, "Username is required", http.StatusBadRequest)
		return
	}
	if !isValidMinecraftUsername(username) {
		http.Error(w, "Invalid username format", http.StatusBadRequest)
		return
	}

	server, err := h.Store.GetServerByName(serverName)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if server == nil {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	link, err := h.Store.GetPterodactylLink(server.ID)
	if err != nil || link == nil {
		http.Error(w, "Server not linked to any backend", http.StatusNotFound)
		return
	}

	if !isActionAllowed(link.AllowedActions, "whitelist") {
		user := h.getUserEnv(r)
		if h.Engine == nil || !h.evaluateACLEnabled(link, server, "whitelist", user) {
			http.Error(w, "Whitelist action not permitted for this server", http.StatusForbidden)
			return
		}
	}

	addCmd := whitelistAddCommand(server.GameType, username)
	if addCmd == "" {
		http.Error(w, "Whitelist not supported for game type: "+server.GameType, http.StatusBadRequest)
		return
	}

	userEmail := r.FormValue("user_email")

	existing, _ := h.Store.GetUserWhitelist(userEmail, server.ID)
	if existing != nil && existing.Username == username {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "already whitelisted"})
		return
	}

	if existing != nil && existing.Username != "" {
		removeCmd := whitelistRemoveCommand(server.GameType, existing.Username)
		if removeCmd != "" {
			b, bErr := h.resolveBackend(server, link)
			if bErr != nil {
				http.Error(w, bErr.Error(), http.StatusServiceUnavailable)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			b.SendCommand(ctx, removeCmd)
		}
	}

	b, bErr := h.resolveBackend(server, link)
	if bErr != nil {
		http.Error(w, bErr.Error(), http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	if err := b.SendCommand(ctx, addCmd); err != nil {
		http.Error(w, fmt.Sprintf("%s whitelist failed: %s", b.Name(), err.Error()), http.StatusInternalServerError)
		return
	}

	if err := h.Store.SetUserWhitelist(userEmail, server.ID, username); err != nil {
		http.Error(w, "Failed to save whitelist entry", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "username": username})
}

func (h *PterodactylHandler) WhitelistStatus(w http.ResponseWriter, r *http.Request) {
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

	userEmail := r.URL.Query().Get("user_email")
	if userEmail == "" {
		userEmail = r.FormValue("user_email")
	}

	existing, _ := h.Store.GetUserWhitelist(userEmail, server.ID)

	w.Header().Set("Content-Type", "application/json")
	if existing != nil {
		json.NewEncoder(w).Encode(map[string]string{"username": existing.Username})
	} else {
		json.NewEncoder(w).Encode(map[string]string{"username": ""})
	}
}

var minecraftUsernameRegex = regexp.MustCompile(`^[a-zA-Z0-9_]{3,16}$`)

func isValidMinecraftUsername(name string) bool {
	return minecraftUsernameRegex.MatchString(name)
}

func whitelistAddCommand(gameType, player string) string {
	switch gameType {
	case "minecraft":
		return fmt.Sprintf("whitelist add %s", player)
	default:
		return ""
	}
}

func whitelistRemoveCommand(gameType, player string) string {
	switch gameType {
	case "minecraft":
		return fmt.Sprintf("whitelist remove %s", player)
	default:
		return ""
	}
}

func isActionAllowed(allowedActionsJSON string, action string) bool {
	var actions []string
	if err := json.Unmarshal([]byte(allowedActionsJSON), &actions); err != nil {
		return false
	}
	for _, a := range actions {
		if a == action {
			return true
		}
	}
	return false
}

// isValidCommand checks if a command is safe to send to the backend.
// It rejects commands with shell metacharacters.
func isValidCommand(command string) bool {
	if command == "" {
		return false
	}
	// Reject commands containing shell metacharacters
	dangerousChars := []string{";", "&", "|", "`", "$", "(", ")", "<", ">", "\\", "\n", "\r"}
	for _, ch := range dangerousChars {
		if strings.Contains(command, ch) {
			return false
		}
	}
	return true
}
