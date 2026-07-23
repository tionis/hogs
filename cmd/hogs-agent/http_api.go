package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func agentAPI() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", handleHealth)
	mux.HandleFunc("GET /v1/servers/{server}/status", withServer(handleStatus))
	mux.HandleFunc("POST /v1/servers/{server}/actions/{action}", withServer(handleAction))
	mux.HandleFunc("POST /v1/servers/{server}/command", withServer(handleCommand))
	mux.HandleFunc("GET /v1/servers/{server}/console", withServer(handleConsole))
	mux.HandleFunc("GET /v1/servers/{server}/files", withServer(handleFileList))
	mux.HandleFunc("GET /v1/servers/{server}/file", withServer(handleFileRead))
	mux.HandleFunc("PUT /v1/servers/{server}/file", withServer(handleFileWrite))
	mux.HandleFunc("DELETE /v1/servers/{server}/file", withServer(handleFileDelete))
	mux.HandleFunc("POST /v1/servers/{server}/directories", withServer(handleMkdir))
	mux.HandleFunc("GET /v1/servers/{server}/backups", withServer(handleBackupList))
	mux.HandleFunc("POST /v1/servers/{server}/backups", withServer(handleBackupCreate))
	mux.HandleFunc("POST /v1/servers/{server}/restore", withServer(handleBackupRestore))
	authenticated := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, err := netipFromRemote(r.RemoteAddr)
		if err != nil {
			http.Error(w, "invalid peer address", http.StatusForbidden)
			return
		}
		_, allowed, err := net.ParseCIDR(agentConfig.WireGuard.Peer.AllowedIP)
		if err != nil || !allowed.Contains(peer) {
			http.Error(w, "peer is not authorized", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
	return h2c.NewHandler(authenticated, &http2.Server{})
}

func netipFromRemote(remote string) (net.IP, error) {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil, fmt.Errorf("not an IP address")
	}
	return ip, nil
}

type serverHTTPHandler func(http.ResponseWriter, *http.Request, *ServerConfig)

func withServer(next serverHTTPHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		server, err := serverConfig(r.PathValue("server"))
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
		"servers":      sortedServerNames(),
		"capabilities": agentCapabilities(),
	})
}

func handleStatus(w http.ResponseWriter, r *http.Request, server *ServerConfig) {
	active, substate := getServiceStatus(server.Unit)
	players, maxPlayers, known := 0, 0, false
	version := ""
	if active {
		players, maxPlayers, known = playerStatus(server)
		version = serverVersion(server)
	}
	writeJSONResponse(w, http.StatusOK, StatusReportData{
		ServerName: r.PathValue("server"), Online: active, Players: players,
		MaxPlayers: maxPlayers, PlayersKnown: known, Version: version,
		Substate: substate,
	})
}

func handleAction(w http.ResponseWriter, r *http.Request, server *ServerConfig) {
	result := executeAction(server, r.PathValue("action"))
	writeOperationResult(w, result)
}

func handleCommand(w http.ResponseWriter, r *http.Request, server *ServerConfig) {
	var request struct {
		Command string `json:"command"`
	}
	if !decodeJSON(w, r, &request) || strings.TrimSpace(request.Command) == "" {
		return
	}
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
	cmd := exec.CommandContext(r.Context(), "journalctl", "-u", server.Unit, "-f", "-n", "100", "--no-hostname", "-o", "cat")
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
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	encoder := json.NewEncoder(w)
	for scanner.Scan() {
		line := scanner.Text()
		if isRoutineRCONConnectionLine(line) {
			continue
		}
		if err := encoder.Encode(map[string]string{
			"serverName": r.PathValue("server"),
			"line":       line,
			"timestamp":  time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			return
		}
		flusher.Flush()
	}
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
	writeOperationResult(w, fileDelete(server, r.URL.Query().Get("path")))
}

func handleMkdir(w http.ResponseWriter, r *http.Request, server *ServerConfig) {
	var request struct {
		Path string `json:"path"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
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
	writeOperationResult(w, backupCreate(server, request.Paths, request.Tags))
}

func handleBackupRestore(w http.ResponseWriter, r *http.Request, server *ServerConfig) {
	var request struct {
		Snapshot string `json:"snapshot"`
		Target   string `json:"target"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	writeOperationResult(w, backupRestore(server, request.Snapshot, request.Target))
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
		writeAPIError(w, http.StatusBadGateway, fmt.Errorf("%s", message))
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
