package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tionis/hogs/backend"
	"github.com/tionis/hogs/gametypes"
	"github.com/tionis/hogs/internal/capability"
)

func agentAPI() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", handleHealth)
	mux.HandleFunc("GET /v1/servers/{serverID}/status", withServer(handleStatus))
	mux.HandleFunc("POST /v1/servers/{serverID}/actions/{action}", withServer(handleAction))
	mux.HandleFunc("POST /v1/servers/{serverID}/command", withServer(handleCommand))
	mux.HandleFunc("GET /v1/servers/{serverID}/whitelist", withServer(handleWhitelist))
	mux.HandleFunc("POST /v1/servers/{serverID}/whitelist", withServer(handleWhitelist))
	mux.HandleFunc("GET /v1/servers/{serverID}/console", withServer(handleConsole))
	mux.HandleFunc("GET /v1/servers/{serverID}/files", withServer(handleFileList))
	mux.HandleFunc("GET /v1/servers/{serverID}/file", withServer(handleFileRead))
	mux.HandleFunc("PUT /v1/servers/{serverID}/file", withServer(handleFileWrite))
	mux.HandleFunc("DELETE /v1/servers/{serverID}/file", withServer(handleFileDelete))
	mux.HandleFunc("POST /v1/servers/{serverID}/file-operations", withServer(handleFileOperation))
	mux.HandleFunc("POST /v1/servers/{serverID}/directories", withServer(handleMkdir))
	mux.HandleFunc("GET /v1/servers/{serverID}/backups", withServer(handleBackupList))
	mux.HandleFunc("POST /v1/servers/{serverID}/backups", withServer(handleBackupCreate))
	mux.HandleFunc("POST /v1/servers/{serverID}/restore", withServer(handleBackupRestore))
	authenticated := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if !allowedOrigin(origin) {
				http.Error(w, "origin is not allowed", http.StatusForbidden)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, If-Match, Range")
			w.Header().Set("Access-Control-Expose-Headers", "Accept-Ranges, Content-Disposition, Content-Length, Content-Range, ETag, Last-Modified")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" {
			token = r.URL.Query().Get("access_token")
		}
		claims, err := capability.Verify(agentSecret, token, time.Now())
		if err != nil {
			http.Error(w, "invalid or expired capability", http.StatusUnauthorized)
			return
		}
		if err := capability.AuthorizePaths(
			claims,
			agentConfig.Node,
			r.Method,
			r.URL.Path,
			r.URL.Query().Get("path"),
			r.URL.Query().Get("target"),
		); err != nil {
			http.Error(w, "capability does not authorize this request", http.StatusForbidden)
			return
		}
		if claims.MaxBytes > 0 && r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, claims.MaxBytes)
		}
		mux.ServeHTTP(w, r)
	})
	return authenticated
}

func allowedOrigin(origin string) bool {
	for _, allowed := range agentConfig.API.AllowedOrigins {
		if origin == allowed {
			return true
		}
	}
	return false
}

type serverHTTPHandler func(http.ResponseWriter, *http.Request, *ServerConfig)

func withServer(next serverHTTPHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		server, err := serverConfig(r.PathValue("serverID"))
		if err != nil {
			writeAPIError(w, http.StatusNotFound, err)
			return
		}
		next(w, r, server)
	}
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSONResponse(w, http.StatusOK, map[string]interface{}{
		"node":         agentConfig.Node,
		"status":       "healthy",
		"serverIds":    sortedServerIDs(),
		"capabilities": agentCapabilities(),
	})
}

func handleStatus(w http.ResponseWriter, r *http.Request, server *ServerConfig) {
	active, substate, resources := getServiceStatusWithResources(server.Unit, time.Now())
	players, maxPlayers, known := 0, 0, false
	version := ""
	driver := gametypes.Generic(server.GameType)
	if requested := r.URL.Query().Get("driver"); requested != "generic" {
		if requested == "" {
			requested = server.GameType
		}
		if embedded, ok := gametypes.Embedded(requested); ok && embedded.Slug == server.GameType {
			driver = embedded
		}
	}
	if active {
		players, maxPlayers, known = playerStatus(server, driver)
		version = serverVersion(server, driver)
	}
	writeJSONResponse(w, http.StatusOK, StatusReportData{
		ServerID: r.PathValue("serverID"), Online: active, Players: players,
		MaxPlayers: maxPlayers, PlayersKnown: known, Version: version,
		Substate: substate, Resources: resources,
	})
}

func handleAction(w http.ResponseWriter, r *http.Request, server *ServerConfig) {
	lock := serverOperationLock(server)
	lock.Lock()
	defer lock.Unlock()
	result := executeAction(server, r.PathValue("action"))
	writeOperationResult(w, result)
}

func handleWhitelist(w http.ResponseWriter, r *http.Request, server *ServerConfig) {
	request := backend.WhitelistRequest{Operation: "list"}
	if r.Method == http.MethodPost {
		if !decodeJSON(w, r, &request) {
			return
		}
	}
	lock := serverOperationLock(server)
	lock.Lock()
	defer lock.Unlock()
	result, operationErr := whitelistOperation(server, request)
	if operationErr != nil {
		status := http.StatusBadGateway
		switch operationErr.Code {
		case "invalid_operation", "invalid_identity":
			status = http.StatusBadRequest
		case "identity_required":
			status = http.StatusUnprocessableEntity
		case "server_started", "server_stopped":
			status = http.StatusConflict
		case "status_unknown":
			status = http.StatusServiceUnavailable
		case "unsupported", "online_unsupported", "offline_unsupported":
			status = http.StatusNotImplemented
		}
		writeJSONResponse(w, status, map[string]interface{}{
			"success": false, "error": operationErr.Message, "code": operationErr.Code,
		})
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]interface{}{"success": true, "data": result})
}

func handleCommand(w http.ResponseWriter, r *http.Request, server *ServerConfig) {
	var request struct {
		Command string `json:"command"`
	}
	if !decodeJSON(w, r, &request) || strings.TrimSpace(request.Command) == "" {
		return
	}
	lock := serverOperationLock(server)
	lock.Lock()
	defer lock.Unlock()
	output, err := executeCommand(server, request.Command)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err)
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"data":    map[string]string{"output": output},
	})
}

func handleConsole(w http.ResponseWriter, r *http.Request, server *ServerConfig) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, fmt.Errorf("streaming is unavailable"))
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	// HOGS persists the bounded console transcript. Start at the live cursor so
	// reconnects do not duplicate the last journal lines.
	cmd := exec.CommandContext(r.Context(), "journalctl", "-u", server.Unit, "-f", "-n", "0", "--no-hostname", "-o", "cat")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	if err := cmd.Start(); err != nil {
		writeAPIError(w, http.StatusBadGateway, err)
		return
	}
	defer cmd.Wait()
	commitConsoleStream(w, flusher)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	encoder := json.NewEncoder(w)
	for scanner.Scan() {
		line := scanner.Text()
		if isRoutineConsoleLine(server.GameType, line) {
			continue
		}
		if err := encoder.Encode(map[string]string{
			"serverId":  r.PathValue("serverID"),
			"line":      line,
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			return
		}
		flusher.Flush()
	}
}

func commitConsoleStream(w http.ResponseWriter, flusher http.Flusher) {
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
}

func handleFileList(w http.ResponseWriter, r *http.Request, server *ServerConfig) {
	writeOperationResult(w, filelist(server, r.URL.Query().Get("path")))
}

func handleFileRead(w http.ResponseWriter, r *http.Request, server *ServerConfig) {
	path, err := resolvePath(server, r.URL.Query().Get("path"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		writeAPIError(w, statusForFileError(err), err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeAPIError(w, http.StatusBadRequest, fmt.Errorf("path is not a regular file"))
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(path)))
	w.Header().Set("ETag", fileETag(info))
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

func handleFileWrite(w http.ResponseWriter, r *http.Request, server *ServerConfig) {
	lock := serverOperationLock(server)
	lock.Lock()
	defer lock.Unlock()
	path, err := resolvePath(server, r.URL.Query().Get("path"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	if match := r.Header.Get("If-Match"); match != "" {
		info, err := os.Stat(path)
		if err != nil || match != fileETag(info) {
			writeAPIError(w, http.StatusPreconditionFailed, fmt.Errorf("file changed since it was opened"))
			return
		}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".hogs-*")
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	size, copyErr := io.Copy(temp, r.Body)
	syncErr := temp.Sync()
	closeErr := temp.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		writeAPIError(w, http.StatusBadRequest, firstError(copyErr, syncErr, closeErr))
		return
	}
	if err := os.Chmod(tempName, 0644); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	if err := os.Rename(tempName, path); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"data":    map[string]interface{}{"path": path, "size": size},
	})
}

func handleFileDelete(w http.ResponseWriter, r *http.Request, server *ServerConfig) {
	lock := serverOperationLock(server)
	lock.Lock()
	defer lock.Unlock()
	writeOperationResult(w, fileDelete(server, r.URL.Query().Get("path")))
}

func handleFileOperation(w http.ResponseWriter, r *http.Request, server *ServerConfig) {
	lock := serverOperationLock(server)
	lock.Lock()
	defer lock.Unlock()
	writeOperationResult(w, fileOperation(
		server,
		r.URL.Query().Get("operation"),
		r.URL.Query().Get("path"),
		r.URL.Query().Get("target"),
	))
}

func handleMkdir(w http.ResponseWriter, r *http.Request, server *ServerConfig) {
	var request struct {
		Path string `json:"path"`
	}
	request.Path = r.URL.Query().Get("path")
	if request.Path == "" {
		if !decodeJSON(w, r, &request) {
			return
		}
	}
	lock := serverOperationLock(server)
	lock.Lock()
	defer lock.Unlock()
	writeOperationResult(w, mkdir(server, request.Path))
}

func handleBackupList(w http.ResponseWriter, _ *http.Request, server *ServerConfig) {
	writeOperationResult(w, backupList(server))
}

func handleBackupCreate(w http.ResponseWriter, r *http.Request, server *ServerConfig) {
	var request struct {
		Paths []string `json:"paths"`
		Tags  []string `json:"tags"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	lock := serverOperationLock(server)
	lock.Lock()
	defer lock.Unlock()
	writeOperationResult(w, backupCreate(server, request.Paths, request.Tags))
}

func handleBackupRestore(w http.ResponseWriter, r *http.Request, server *ServerConfig) {
	var request struct {
		Snapshot        string `json:"snapshot"`
		ConfirmServerID string `json:"confirmServerId"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	writeOperationResult(w, restoreSnapshot(r.PathValue("serverID"), server, request.Snapshot, request.ConfirmServerID))
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination interface{}) bool {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1024*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeAPIError(w, http.StatusBadRequest, err)
		return false
	}
	return true
}

func writeOperationResult(w http.ResponseWriter, result map[string]interface{}) {
	success, _ := result["success"].(bool)
	delete(result, "success")
	if !success {
		message, _ := result["error"].(string)
		if message == "" {
			message, _ = result["message"].(string)
		}
		if message == "" {
			message = "operation failed"
		}
		delete(result, "error")
		delete(result, "message")
		writeJSONResponse(w, http.StatusBadGateway, map[string]interface{}{
			"success": false, "error": message, "data": result,
		})
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]interface{}{"success": true, "data": result})
}

func writeAPIError(w http.ResponseWriter, status int, err error) {
	writeJSONResponse(w, status, map[string]interface{}{"success": false, "error": err.Error()})
}

func writeJSONResponse(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func statusForFileError(err error) int {
	if os.IsNotExist(err) {
		return http.StatusNotFound
	}
	if os.IsPermission(err) {
		return http.StatusForbidden
	}
	return http.StatusInternalServerError
}

func fileETag(info os.FileInfo) string {
	sum := sha256.Sum256([]byte(info.Name() + "\x00" + strconv.FormatInt(info.Size(), 10) + "\x00" + strconv.FormatInt(info.ModTime().UnixNano(), 10)))
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

func firstError(errors ...error) error {
	for _, err := range errors {
		if err != nil {
			return err
		}
	}
	return nil
}
