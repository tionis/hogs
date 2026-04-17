package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"
	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/pterodactyl"
)

type PterodactylHandler struct {
	Store  *database.Store
	Config *config.Config
}

func NewPterodactylHandler(store *database.Store, cfg *config.Config) *PterodactylHandler {
	return &PterodactylHandler{Store: store, Config: cfg}
}

func (h *PterodactylHandler) client() *pterodactyl.Client {
	if h.Config.PterodactylURL == "" || h.Config.PterodactylAppKey == "" {
		return nil
	}
	c := pterodactyl.NewClient(h.Config.PterodactylURL, h.Config.PterodactylAppKey)
	c.ClientKey = h.Config.PterodactylClientKey
	return c
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

	link := &database.PterodactylLink{
		ServerID:        serverID,
		PteroServerID:   pteroServerID,
		PteroIdentifier: pteroIdentifier,
		AllowedActions:  allowedActions,
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

	http.Redirect(w, r, "/admin", http.StatusFound)
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

	http.Redirect(w, r, "/admin", http.StatusFound)
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

	http.Redirect(w, r, "/admin", http.StatusFound)
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
		http.Error(w, "Server not linked to Pterodactyl", http.StatusNotFound)
		return
	}

	if !isActionAllowed(link.AllowedActions, action) {
		http.Error(w, "Action not permitted for this server", http.StatusForbidden)
		return
	}

	c := h.client()
	if c == nil {
		http.Error(w, "Pterodactyl not configured", http.StatusServiceUnavailable)
		return
	}

	var pteroErr error
	switch action {
	case "start":
		pteroErr = c.StartServer(link.PteroServerID)
	case "stop":
		pteroErr = c.StopServer(link.PteroServerID)
	case "restart":
		pteroErr = c.RestartServer(link.PteroServerID)
	default:
		http.Error(w, fmt.Sprintf("Unknown action: %s", action), http.StatusBadRequest)
		return
	}

	if pteroErr != nil {
		http.Error(w, "Pterodactyl action failed: "+pteroErr.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (h *PterodactylHandler) SendCommand(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	serverName := vars["serverName"]
	command := r.FormValue("command")

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
		http.Error(w, "Server not linked to Pterodactyl", http.StatusNotFound)
		return
	}

	if !isActionAllowed(link.AllowedActions, "command:"+command) {
		http.Error(w, "Command not permitted for this server", http.StatusForbidden)
		return
	}

	c := h.client()
	if c == nil {
		http.Error(w, "Pterodactyl not configured", http.StatusServiceUnavailable)
		return
	}

	if c.ClientKey == "" {
		http.Error(w, "Pterodactyl client key not configured. Set PTERODACTYL_CLIENT_KEY to send commands.", http.StatusServiceUnavailable)
		return
	}

	if link.PteroIdentifier == "" {
		http.Error(w, "Pterodactyl server identifier not set. Re-link the server with identifier.", http.StatusBadRequest)
		return
	}

	if err := c.SendCommand(link.PteroIdentifier, command); err != nil {
		http.Error(w, "Pterodactyl command failed: "+err.Error(), http.StatusInternalServerError)
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
		http.Error(w, "Server not linked to Pterodactyl", http.StatusNotFound)
		return
	}

	if !isActionAllowed(link.AllowedActions, "whitelist") {
		http.Error(w, "Whitelist action not permitted for this server", http.StatusForbidden)
		return
	}

	c := h.client()
	if c == nil {
		http.Error(w, "Pterodactyl not configured", http.StatusServiceUnavailable)
		return
	}

	if c.ClientKey == "" {
		http.Error(w, "Pterodactyl client key not configured. Set PTERODACTYL_CLIENT_KEY to send commands.", http.StatusServiceUnavailable)
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
			c.SendCommand(link.PteroIdentifier, removeCmd)
		}
	}

	addCmd := whitelistAddCommand(server.GameType, username)
	if addCmd == "" {
		http.Error(w, "Whitelist not supported for game type: "+server.GameType, http.StatusBadRequest)
		return
	}

	if err := c.SendCommand(link.PteroIdentifier, addCmd); err != nil {
		http.Error(w, "Whitelist add failed: "+err.Error(), http.StatusInternalServerError)
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
