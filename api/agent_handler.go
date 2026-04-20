package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"
	"github.com/tionis/hogs/agent"
	"github.com/tionis/hogs/database"
)

type AgentHandler struct {
	Store   *database.Store
	Service *agent.AgentService
}

func NewAgentHandler(store *database.Store, service *agent.AgentService) *AgentHandler {
	return &AgentHandler{Store: store, Service: service}
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
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(agents)
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
		token = generateToken()
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
	json.NewEncoder(w).Encode(a)
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

	ok, msg := h.Service.FileList(serverName, path)
	if !ok {
		http.Error(w, msg, http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "request_sent", "message": msg})
}

func (h *AgentHandler) AgentFileRead(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}

	ok, msg := h.Service.FileRead(serverName, path)
	if !ok {
		http.Error(w, msg, http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "request_sent", "message": msg})
}

func (h *AgentHandler) AgentFileWrite(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]

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

	ok, msg := h.Service.FileWrite(serverName, req.Path, req.ContentB64)
	if !ok {
		http.Error(w, msg, http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "request_sent", "message": msg})
}

func (h *AgentHandler) AgentFileDelete(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}

	ok, msg := h.Service.FileDelete(serverName, path)
	if !ok {
		http.Error(w, msg, http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "request_sent", "message": msg})
}

func (h *AgentHandler) AgentMkdir(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}

	ok, msg := h.Service.Mkdir(serverName, path)
	if !ok {
		http.Error(w, msg, http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "request_sent", "message": msg})
}

func (h *AgentHandler) AgentBackupCreate(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]

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

	ok, msg := h.Service.BackupCreate(serverName, req.Repo, req.Password, req.Paths, req.Tags)
	if !ok {
		http.Error(w, msg, http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "request_sent", "message": msg})
}

func (h *AgentHandler) AgentBackupRestore(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]

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

	ok, msg := h.Service.BackupRestore(serverName, req.Repo, req.Password, req.Snapshot, req.Target)
	if !ok {
		http.Error(w, msg, http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "request_sent", "message": msg})
}

func (h *AgentHandler) AgentBackupList(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]

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

	ok, msg := h.Service.BackupList(serverName, req.Repo, req.Password)
	if !ok {
		http.Error(w, msg, http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "request_sent", "message": msg})
}

func generateToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}
