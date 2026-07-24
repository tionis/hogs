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
	"github.com/tionis/hogs/access"
	"github.com/tionis/hogs/agent"
	"github.com/tionis/hogs/auth"
	"github.com/tionis/hogs/backend"
	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/engine"
	"github.com/tionis/hogs/pterodactyl"
)

type PterodactylHandler struct {
	Store        *database.Store
	Config       *config.Config
	Engine       *engine.Engine
	AgentManager *agent.Manager
	Auth         *auth.Authenticator
}

func NewPterodactylHandler(store *database.Store, cfg *config.Config, eng *engine.Engine, manager *agent.Manager, auth *auth.Authenticator) *PterodactylHandler {
	return &PterodactylHandler{Store: store, Config: cfg, Engine: eng, AgentManager: manager, Auth: auth}
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
	if link.Node != "" && h.AgentManager != nil {
		ag, err := h.Store.GetAgentByNodeName(link.Node)
		if err == nil && ag != nil {
			return agent.NewAgentBackend(ag.NodeName, server.Name, h.AgentManager), nil
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
			// Fallback: PteroServerID may already be the short identifier
			// (e.g. user typed it manually instead of selecting from the
			// datalist). The Pterodactyl client API accepts both UUID and
			// short identifier, so use it directly.
			identifier = link.PteroServerID
		} else {
			identifier = id
		}
		link.PteroIdentifier = identifier
		h.Store.UpdatePterodactylLink(link)
	}

	return backend.NewPterodactylBackend(h.Config, link.PteroServerID, identifier), nil
}

func (h *PterodactylHandler) getUserEnv(r *http.Request) *engine.UserEnv {
	return userEnvFromRequest(h.Store, h.Auth, r, h.Config != nil && h.Config.TrustProxyHeaders)
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
	username := strings.TrimSpace(r.FormValue("username"))

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
		result := h.Engine.Evaluate(server, "whitelist", nil, user)
		if !result.Allowed {
			http.Error(w, result.Reason, result.Status)
			return
		}
	} else if !isActionAllowed(link.AllowedActions, "whitelist") {
		http.Error(w, "Whitelist action not permitted for this server", http.StatusForbidden)
		return
	}

	userEmail := user.Email
	if userEmail == "" || userEmail == "anonymous" {
		http.Error(w, "Authenticated identity is required", http.StatusUnauthorized)
		return
	}
	existing, _ := h.Store.GetUserWhitelist(userEmail, server.ID)
	if r.FormValue("op") == "remove" {
		if existing == nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "not whitelisted"})
			return
		}
		removeCmd := whitelistRemoveCommand(server.GameType, existing.Username)
		b, bErr := h.resolveBackend(server, link)
		if bErr != nil {
			http.Error(w, bErr.Error(), http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if !whitelistBackendReady(w, ctx, b) {
			return
		}
		if err := b.SendCommand(ctx, removeCmd); err != nil {
			http.Error(w, fmt.Sprintf("%s whitelist removal failed: %s", b.Name(), err.Error()), http.StatusInternalServerError)
			return
		}
		if err := h.Store.DeleteUserWhitelist(userEmail, server.ID); err != nil {
			http.Error(w, "Failed to update whitelist entry", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "removed from whitelist"})
		return
	}
	identity, _ := h.Store.GetGameIdentity(userEmail, server.GameType)
	if username == "" && identity != nil {
		username = identity.Username
	}
	if username == "" || (server.GameType == "minecraft" && !isValidMinecraftUsername(username)) {
		http.Error(w, "A valid linked in-game username is required", http.StatusBadRequest)
		return
	}
	addCmd := whitelistAddCommand(server.GameType, username)
	if addCmd == "" {
		http.Error(w, "Whitelist not supported for game type: "+server.GameType, http.StatusBadRequest)
		return
	}
	if existing != nil && existing.Username == username {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "already whitelisted"})
		return
	}

	b, bErr := h.resolveBackend(server, link)
	if bErr != nil {
		http.Error(w, bErr.Error(), http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if !whitelistBackendReady(w, ctx, b) {
		return
	}

	if existing != nil && existing.Username != "" {
		removeCmd := whitelistRemoveCommand(server.GameType, existing.Username)
		if removeCmd != "" {
			b.SendCommand(ctx, removeCmd)
		}
	}

	if err := b.SendCommand(ctx, addCmd); err != nil {
		http.Error(w, fmt.Sprintf("%s whitelist failed: %s", b.Name(), err.Error()), http.StatusInternalServerError)
		return
	}

	if identity == nil || identity.Username != username {
		if err := h.Store.SetGameIdentity(&database.GameIdentity{
			UserEmail: userEmail, GameType: server.GameType, Username: username, Source: "self",
		}); err != nil {
			http.Error(w, "Failed to save linked game identity", http.StatusInternalServerError)
			return
		}
	}
	if err := h.Store.SetUserWhitelist(userEmail, server.ID, username); err != nil {
		http.Error(w, "Failed to save whitelist entry", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "ok", "username": username,
		"message": username + " is now whitelisted",
	})
}

func (h *PterodactylHandler) AdminWhitelist(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]
	server, err := h.Store.GetServerByName(serverName)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if server == nil {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}
	user := h.getUserEnv(r)
	if user.Role != "admin" && user.Role != "system" {
		decision, accessErr := h.Store.EvaluateServerAccess(server.ID, user.Email, user.Groups, access.WhitelistManage)
		if accessErr != nil {
			http.Error(w, "Failed to evaluate server access", http.StatusInternalServerError)
			return
		}
		if !decision.Allowed {
			if h.Engine != nil {
				h.Engine.LogAction(server.Name, access.WhitelistManage, user.Email, "denied", decision.Reason, "web", nil)
			}
			http.Error(w, "Whitelist management permission required: "+decision.Reason, http.StatusForbidden)
			return
		}
	}
	link, err := h.Store.GetPterodactylLink(server.ID)
	if err != nil || link == nil {
		http.Error(w, "Server not linked to a management backend", http.StatusNotFound)
		return
	}
	if server.GameType != "minecraft" {
		http.Error(w, "Direct whitelist management is not supported for "+server.GameType, http.StatusBadRequest)
		return
	}
	backend, err := h.resolveBackend(server, link)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if !whitelistBackendReady(w, ctx, backend) {
		return
	}

	if r.Method == http.MethodGet {
		output := ""
		if resultBackend, ok := backend.(interface {
			SendCommandOutput(context.Context, string) (string, error)
		}); ok {
			output, err = resultBackend.SendCommandOutput(ctx, "whitelist list")
		} else {
			err = backend.SendCommand(ctx, "whitelist list")
		}
		if err != nil {
			http.Error(w, fmt.Sprintf("%s whitelist query failed: %s", backend.Name(), err), http.StatusBadGateway)
			return
		}
		linked, _ := h.Store.ListUserWhitelists(server.ID)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "output": output, "linked": linked})
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	op := r.FormValue("op")
	if !isValidMinecraftUsername(username) || (op != "add" && op != "remove") {
		http.Error(w, "A valid Minecraft username and operation are required", http.StatusBadRequest)
		return
	}
	command := whitelistAddCommand(server.GameType, username)
	if op == "remove" {
		command = whitelistRemoveCommand(server.GameType, username)
	}
	if err := backend.SendCommand(ctx, command); err != nil {
		http.Error(w, fmt.Sprintf("%s whitelist %s failed: %s", backend.Name(), op, err), http.StatusBadGateway)
		return
	}
	email := strings.TrimSpace(r.FormValue("user_email"))
	if email != "" {
		if op == "add" {
			_ = h.Store.SetGameIdentity(&database.GameIdentity{
				UserEmail: email, GameType: server.GameType, Username: username, Source: "admin",
			})
			_ = h.Store.SetUserWhitelist(email, server.ID, username)
		} else if existing, _ := h.Store.GetUserWhitelist(email, server.ID); existing != nil && existing.Username == username {
			_ = h.Store.DeleteUserWhitelist(email, server.ID)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	verb := "added to"
	if op == "remove" {
		verb = "removed from"
	}
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "ok", "message": fmt.Sprintf("%s was %s the whitelist", username, verb),
	})
}

func whitelistBackendReady(w http.ResponseWriter, ctx context.Context, backend backend.Backend) bool {
	status, err := backend.Status(ctx)
	if err != nil {
		http.Error(w, "Could not determine whether the game server is running. Try again shortly.", http.StatusServiceUnavailable)
		return false
	}
	if status == nil || !status.Online {
		http.Error(w, "The game server is stopped. Start it before managing the whitelist.", http.StatusConflict)
		return false
	}
	return true
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

	user := h.getUserEnv(r)
	userEmail := user.Email
	if userEmail == "" || userEmail == "anonymous" {
		http.Error(w, "Authenticated identity is required", http.StatusUnauthorized)
		return
	}

	existing, _ := h.Store.GetUserWhitelist(userEmail, server.ID)
	identity, _ := h.Store.GetGameIdentity(userEmail, server.GameType)

	w.Header().Set("Content-Type", "application/json")
	response := map[string]string{"username": "", "linkedUsername": ""}
	if identity != nil {
		response["linkedUsername"] = identity.Username
	}
	if existing != nil {
		response["username"] = existing.Username
	}
	json.NewEncoder(w).Encode(response)
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
