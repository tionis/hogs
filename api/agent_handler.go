package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	"github.com/tionis/hogs/agent"
	"github.com/tionis/hogs/database"
)

type AgentHandler struct {
	Store   *database.Store
	Service *agent.AgentService
	Hub     *agent.Hub
}

func NewAgentHandler(store *database.Store, service *agent.AgentService, hub *agent.Hub) *AgentHandler {
	return &AgentHandler{Store: store, Service: service, Hub: hub}
}

func (h *AgentHandler) ListAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := h.Store.ListAgents()
	if err != nil {
		http.Error(w, "Failed to list agents", http.StatusInternalServerError)
		return
	}
	if agents == nil {
		agents = []database.Agent{}
	}

	type agentWithStatus struct {
		database.Agent
		Connected bool `json:"connected"`
	}

	var result []agentWithStatus
	for _, a := range agents {
		connected := false
		if h.Hub != nil {
			connected = h.Hub.GetConn(a.ID) != nil
		}
		result = append(result, agentWithStatus{Agent: a, Connected: connected})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *AgentHandler) GetAgent(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		http.Error(w, "Invalid agent ID", http.StatusBadRequest)
		return
	}

	a, err := h.Store.GetAgent(id)
	if err != nil {
		http.Error(w, "Failed to get agent", http.StatusInternalServerError)
		return
	}
	if a == nil {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}

	connected := false
	if h.Hub != nil {
		connected = h.Hub.GetConn(a.ID) != nil
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":           a.ID,
		"name":         a.Name,
		"keyPrefix":    a.TokenPrefix,
		"nodeName":     a.NodeName,
		"capabilities": a.Capabilities,
		"createdAt":    a.CreatedAt,
		"lastSeen":     a.LastSeen,
		"online":       a.Online,
		"connected":    connected,
	})
}

func (h *AgentHandler) CreateAgent(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	token := r.FormValue("token")
	if token == "" {
		token = generateAgentToken()
		if token == "" {
			http.Error(w, "Failed to generate token", http.StatusInternalServerError)
			return
		}
	}

	nodeName := r.FormValue("node_name")
	if nodeName == "" {
		nodeName = name
	}

	a := &database.Agent{
		Name:     name,
		Token:    token,
		NodeName: nodeName,
	}

	if err := h.Store.CreateAgent(a); err != nil {
		http.Error(w, "Failed to create agent: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":        a.ID,
		"name":      a.Name,
		"token":     a.Token,
		"keyPrefix": a.TokenPrefix,
		"nodeName":  a.NodeName,
	})
}

func (h *AgentHandler) UpdateAgent(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		http.Error(w, "Invalid agent ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	a, err := h.Store.GetAgent(id)
	if err != nil {
		http.Error(w, "Failed to get agent", http.StatusInternalServerError)
		return
	}
	if a == nil {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}

	if name := r.FormValue("name"); name != "" {
		a.Name = name
	}
	if nodeName := r.FormValue("node_name"); nodeName != "" {
		a.NodeName = nodeName
	}
	if caps := r.FormValue("capabilities"); caps != "" {
		if !json.Valid([]byte(caps)) {
			http.Error(w, "Invalid JSON in capabilities", http.StatusBadRequest)
			return
		}
		if len(caps) > 64*1024 {
			http.Error(w, "Capabilities JSON too large", http.StatusBadRequest)
			return
		}
		a.Capabilities = json.RawMessage(caps)
	}

	if err := h.Store.UpdateAgent(a); err != nil {
		http.Error(w, "Failed to update agent: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":        a.ID,
		"name":      a.Name,
		"nodeName":  a.NodeName,
		"keyPrefix": a.TokenPrefix,
	})
}

func (h *AgentHandler) RegenerateToken(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		http.Error(w, "Invalid agent ID", http.StatusBadRequest)
		return
	}

	a, err := h.Store.GetAgent(id)
	if err != nil {
		http.Error(w, "Failed to get agent", http.StatusInternalServerError)
		return
	}
	if a == nil {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}

	newToken := generateAgentToken()
	if newToken == "" {
		http.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}
	a.Token = newToken
	a.TokenHash = database.HashAPIKey(newToken)
	a.TokenPrefix = newToken[:8]

	_, err = h.Store.DB.Exec("UPDATE agents SET token = ?, token_hash = ?, token_prefix = ? WHERE id = ?",
		a.Token, a.TokenHash, a.TokenPrefix, a.ID)
	if err != nil {
		http.Error(w, "Failed to regenerate token", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":        a.ID,
		"name":      a.Name,
		"token":     newToken,
		"keyPrefix": a.TokenPrefix,
	})
}

func (h *AgentHandler) DeleteAgent(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid agent ID", http.StatusBadRequest)
		return
	}

	if err := h.Store.DeleteAgent(id); err != nil {
		http.Error(w, "Failed to delete agent", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (h *AgentHandler) AgentFileList(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "."
	}
	if !isValidAgentPath(path) {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	result, err := h.Service.FileList(serverName, path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *AgentHandler) AgentFileRead(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	if !isValidAgentPath(path) {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	result, err := h.Service.FileRead(serverName, path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *AgentHandler) AgentFileWrite(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]

	r.Body = http.MaxBytesReader(w, r.Body, 10<<20) // 10 MB limit

	var req struct {
		Path       string `json:"path"`
		ContentB64 string `json:"contentBase64"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Path == "" || req.ContentB64 == "" {
		http.Error(w, "path and contentBase64 are required", http.StatusBadRequest)
		return
	}
	if !isValidAgentPath(req.Path) {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	result, err := h.Service.FileWrite(serverName, req.Path, req.ContentB64)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *AgentHandler) AgentFileDelete(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	if !isValidAgentPath(path) {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	result, err := h.Service.FileDelete(serverName, path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *AgentHandler) AgentMkdir(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	if !isValidAgentPath(path) {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	result, err := h.Service.Mkdir(serverName, path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *AgentHandler) AgentBackupCreate(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB limit

	var req struct {
		Repo     string   `json:"repo"`
		Password string   `json:"password"`
		Paths    []string `json:"paths"`
		Tags     []string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Repo == "" || req.Password == "" {
		http.Error(w, "repo and password are required", http.StatusBadRequest)
		return
	}

	result, err := h.Service.BackupCreate(serverName, req.Repo, req.Password, req.Paths, req.Tags)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *AgentHandler) AgentBackupRestore(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB limit

	var req struct {
		Repo     string `json:"repo"`
		Password string `json:"password"`
		Snapshot string `json:"snapshot"`
		Target   string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Repo == "" || req.Password == "" || req.Snapshot == "" {
		http.Error(w, "repo, password, and snapshot are required", http.StatusBadRequest)
		return
	}

	result, err := h.Service.BackupRestore(serverName, req.Repo, req.Password, req.Snapshot, req.Target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *AgentHandler) AgentBackupList(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB limit

	var req struct {
		Repo     string `json:"repo"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Repo == "" || req.Password == "" {
		http.Error(w, "repo and password are required", http.StatusBadRequest)
		return
	}

	result, err := h.Service.BackupList(serverName, req.Repo, req.Password)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func generateAgentToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return "hogs_" + hex.EncodeToString(b)
}

func isValidAgentPath(path string) bool {
	if path == "" {
		return false
	}
	if filepath.IsAbs(path) {
		return false
	}
	clean := filepath.Clean(path)
	return clean != ".." && !strings.HasPrefix(clean, "..")
}
