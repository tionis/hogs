package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/tionis/hogs/agent"
	"github.com/tionis/hogs/auth"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/engine"
)

type AgentHandler struct {
	Store   *database.Store
	Service *agent.AgentService
	Manager *agent.Manager
	Auth    *auth.Authenticator
	Engine  *engine.Engine
}

func NewAgentHandler(store *database.Store, service *agent.AgentService, manager *agent.Manager, authenticator *auth.Authenticator, eng *engine.Engine) *AgentHandler {
	return &AgentHandler{Store: store, Service: service, Manager: manager, Auth: authenticator, Engine: eng}
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
		if h.Manager != nil {
			connected = h.Manager.ConnectedNode(a.NodeName)
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
	if h.Manager != nil {
		connected = h.Manager.ConnectedNode(a.NodeName)
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

func (h *AgentHandler) AgentResources(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]
	server, _, status, err := authorizeManagedCapability(h.Store, h.Engine, h.Auth, r, serverName, managedStatus)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	if h.Manager == nil {
		http.Error(w, "agent manager is unavailable", http.StatusServiceUnavailable)
		return
	}
	resources, found := h.Manager.ServerResources(server.ID)
	if !found {
		http.Error(w, "resource observation is not available", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resources)
}

func (h *AgentHandler) AgentResourceHistory(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]
	server, _, status, err := authorizeManagedCapability(h.Store, h.Engine, h.Auth, r, serverName, managedStatus)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	hours := 24
	if requested, err := strconv.Atoi(r.URL.Query().Get("hours")); err == nil && requested > 0 {
		hours = requested
	}
	if hours > 24*7 {
		hours = 24 * 7
	}
	points := 800
	if requested, err := strconv.Atoi(r.URL.Query().Get("points")); err == nil && requested > 0 && requested <= 1200 {
		points = requested
	}
	samples, err := h.Store.ListServerResourceSamples(
		server.ID, time.Now().Add(-time.Duration(hours)*time.Hour), points,
	)
	if err != nil {
		http.Error(w, "failed to load resource history", http.StatusInternalServerError)
		return
	}
	if samples == nil {
		samples = []database.ServerResourceSample{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(samples)
}

func (h *AgentHandler) AgentFileAccess(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]
	filePath := r.URL.Query().Get("path")
	if filePath == "" || !isValidAgentPath(filePath) {
		http.Error(w, "valid path is required", http.StatusBadRequest)
		return
	}
	operation := r.URL.Query().Get("operation")
	capability := managedFileRead
	if operation != "list" && operation != "read" && operation != "download" {
		capability = managedFileWrite
	}
	if status, err := authorizeManagedPath(h.Store, h.Engine, h.Auth, r, serverName, filePath, capability); err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	server, err := h.Store.GetServerByName(serverName)
	if err != nil || server == nil {
		http.Error(w, "server not found", http.StatusNotFound)
		return
	}
	backendType, node := agent.ResolveBackend(server.ID, h.Store)
	if backendType != "agent" || node == "" || h.Manager == nil {
		http.Error(w, "managed agent is unavailable", http.StatusServiceUnavailable)
		return
	}

	targetPath := r.URL.Query().Get("target")
	method, route, maxBytes := "", "", int64(0)
	switch operation {
	case "list":
		method = http.MethodGet
		route = "/v1/servers/" + url.PathEscape(server.ManagementID) + "/files?path=" + url.QueryEscape(filePath)
	case "read", "download":
		method = http.MethodGet
		route = "/v1/servers/" + url.PathEscape(server.ManagementID) + "/file?path=" + url.QueryEscape(filePath)
	case "write":
		method = http.MethodPut
		route = "/v1/servers/" + url.PathEscape(server.ManagementID) + "/file?path=" + url.QueryEscape(filePath)
		maxBytes = 16 << 30
		if requested, err := strconv.ParseInt(r.URL.Query().Get("size"), 10, 64); err == nil && requested > 0 {
			if requested > maxBytes {
				http.Error(w, "file exceeds the 16 GiB transfer limit", http.StatusRequestEntityTooLarge)
				return
			}
			maxBytes = requested
		}
	case "delete":
		method = http.MethodDelete
		route = "/v1/servers/" + url.PathEscape(server.ManagementID) + "/file?path=" + url.QueryEscape(filePath)
	case "mkdir":
		method = http.MethodPost
		route = "/v1/servers/" + url.PathEscape(server.ManagementID) + "/directories?path=" + url.QueryEscape(filePath)
	case "rename", "copy", "move":
		if targetPath == "" || !isValidAgentPath(targetPath) {
			http.Error(w, "valid target path is required", http.StatusBadRequest)
			return
		}
		if status, err := authorizeManagedPath(h.Store, h.Engine, h.Auth, r, serverName, targetPath, managedFileWrite); err != nil {
			http.Error(w, err.Error(), status)
			return
		}
		method = http.MethodPost
		query := url.Values{
			"operation": []string{operation},
			"path":      []string{filePath},
			"target":    []string{targetPath},
		}
		route = "/v1/servers/" + url.PathEscape(server.ManagementID) + "/file-operations?" + query.Encode()
	default:
		http.Error(w, "unsupported file operation", http.StatusBadRequest)
		return
	}

	user := userEnvFromRequest(h.Store, h.Auth, r)
	access, err := h.Manager.DirectAccess(node, user.Email, method, route, filePath, targetPath, maxBytes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(access)
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
	if status, err := authorizeManagedPath(h.Store, h.Engine, h.Auth, r, serverName, path, managedFileRead); err != nil {
		http.Error(w, err.Error(), status)
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

func (h *AgentHandler) AgentFileRoots(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]
	server, _, status, err := authorizeManagedCapability(h.Store, h.Engine, h.Auth, r, serverName, managedFileRead)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	management, err := h.Store.GetServerManagement(server.ID)
	if err != nil || management == nil {
		http.Error(w, "load server management policy", http.StatusInternalServerError)
		return
	}

	roots := make([]string, 0, len(management.WritablePaths))
	for _, allowedPath := range management.WritablePaths {
		relative, relErr := filepath.Rel(filepath.Clean(management.DataPath), filepath.Clean(allowedPath))
		if relErr != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		roots = append(roots, filepath.ToSlash(relative))
	}
	sort.Strings(roots)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"data":    map[string]interface{}{"roots": roots},
	})
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
	if status, err := authorizeManagedPath(h.Store, h.Engine, h.Auth, r, serverName, path, managedFileRead); err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	forwardHeaders := make(http.Header)
	if value := r.Header.Get("Range"); value != "" {
		forwardHeaders.Set("Range", value)
	}
	result, err := h.Service.FileStreamHeaders(r.Context(), serverName, http.MethodGet, path, nil, forwardHeaders)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer result.Body.Close()
	for _, header := range []string{"Content-Type", "Content-Length", "Content-Range", "Content-Disposition", "Accept-Ranges", "ETag", "Last-Modified"} {
		if value := result.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	w.WriteHeader(result.StatusCode)
	_, _ = io.Copy(w, result.Body)
}

func (h *AgentHandler) AgentFileWrite(w http.ResponseWriter, r *http.Request) {
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
	if status, err := authorizeManagedPath(h.Store, h.Engine, h.Auth, r, serverName, path, managedFileWrite); err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	headers := make(http.Header)
	if match := r.Header.Get("If-Match"); match != "" {
		headers.Set("If-Match", match)
	}
	if contentType := r.Header.Get("Content-Type"); contentType != "" {
		headers.Set("Content-Type", contentType)
	}
	result, err := h.Service.FileStreamHeaders(r.Context(), serverName, http.MethodPut, path, r.Body, headers)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer result.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(result.StatusCode)
	_, _ = io.Copy(w, result.Body)
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
	if status, err := authorizeManagedPath(h.Store, h.Engine, h.Auth, r, serverName, path, managedFileWrite); err != nil {
		http.Error(w, err.Error(), status)
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
	if status, err := authorizeManagedPath(h.Store, h.Engine, h.Auth, r, serverName, path, managedFileWrite); err != nil {
		http.Error(w, err.Error(), status)
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

func (h *AgentHandler) AgentFileOperation(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var request struct {
		Operation string `json:"operation"`
		Path      string `json:"path"`
		Target    string `json:"target"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid file operation", http.StatusBadRequest)
		return
	}
	if request.Operation != "rename" && request.Operation != "copy" && request.Operation != "move" {
		http.Error(w, "unsupported file operation", http.StatusBadRequest)
		return
	}
	if !isValidAgentPath(request.Path) || !isValidAgentPath(request.Target) {
		http.Error(w, "valid source and target paths are required", http.StatusBadRequest)
		return
	}
	for _, path := range []string{request.Path, request.Target} {
		if status, err := authorizeManagedPath(h.Store, h.Engine, h.Auth, r, serverName, path, managedFileWrite); err != nil {
			http.Error(w, err.Error(), status)
			return
		}
	}
	result, err := h.Service.FileOperation(serverName, request.Operation, request.Path, request.Target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (h *AgentHandler) AgentBackupCreate(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]
	if _, _, status, err := authorizeManagedCapability(h.Store, h.Engine, h.Auth, r, serverName, managedBackupCreate); err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB limit

	var req struct {
		Paths []string `json:"paths"`
		Tags  []string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	result, err := h.Service.BackupCreate(serverName, req.Paths, req.Tags)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *AgentHandler) AgentBackupRestore(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]
	server, user, status, err := authorizeManagedCapability(h.Store, h.Engine, h.Auth, r, serverName, managedBackupRestore)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB limit

	var req struct {
		Snapshot        string `json:"snapshot"`
		ConfirmServerID string `json:"confirmServerId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Snapshot == "" {
		http.Error(w, "snapshot is required", http.StatusBadRequest)
		return
	}
	if req.ConfirmServerID != server.ManagementID {
		http.Error(w, "confirmation must match the immutable server ID", http.StatusBadRequest)
		return
	}

	result, err := h.Service.BackupRestore(serverName, req.Snapshot, req.ConfirmServerID)
	if err != nil {
		if h.Engine != nil {
			h.Engine.LogAction(server.Name, string(managedBackupRestore), user.Email, "failed", err.Error(), "web", map[string]string{"snapshot": req.Snapshot})
		}
		if result != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(result)
		} else {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		}
		return
	}
	if h.Engine != nil {
		h.Engine.LogAction(server.Name, string(managedBackupRestore), user.Email, "success", "transactional restore completed", "web", map[string]string{"snapshot": req.Snapshot})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *AgentHandler) AgentBackupList(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]
	if _, _, status, err := authorizeManagedCapability(h.Store, h.Engine, h.Auth, r, serverName, managedBackupList); err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB limit

	result, err := h.Service.BackupList(serverName)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *AgentHandler) AgentBackupInit(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "backup repositories are provisioned on the managed node", http.StatusGone)
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
