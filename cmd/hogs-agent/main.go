package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type Envelope struct {
	Type      string          `json:"type"`
	RequestID string          `json:"requestId,omitempty"`
	Data      json.RawMessage `json:"data"`
}

type RegisterData struct {
	NodeName     string   `json:"nodeName"`
	Capabilities []string `json:"capabilities"`
	ServerName   string   `json:"serverName"`
	GameType     string   `json:"gameType"`
}

type ActionRequestData struct {
	Action string `json:"action"`
}

type CommandRequestData struct {
	Command string `json:"command"`
}

type FileListRequestData struct {
	Path string `json:"path"`
}

type FileReadRequestData struct {
	Path string `json:"path"`
}

type FileWriteRequestData struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type FileDeleteRequestData struct {
	Path string `json:"path"`
}

type MkdirRequestData struct {
	Path string `json:"path"`
}

type BackupRequestData struct {
	Repo     string   `json:"repo"`
	Password string   `json:"password"`
	Paths    []string `json:"paths"`
	Tags     []string `json:"tags"`
}

type BackupRestoreRequestData struct {
	Repo     string `json:"repo"`
	Password string `json:"password"`
	Snapshot string `json:"snapshot"`
	Target   string `json:"target"`
}

type BackupListRequestData struct {
	Repo     string `json:"repo"`
	Password string `json:"password"`
}

type StatusReportData struct {
	Online     bool   `json:"online"`
	Players    int    `json:"players"`
	MaxPlayers int    `json:"maxPlayers"`
	Version    string `json:"version"`
}

var (
	serverURL   string
	agentToken  string
	nodeName    string
	serverName  string
	gameType    string
	serviceName string
	dataDir     string
	resticBin   string
	unitPrefix  string
	tlsCert     string
	tlsKey      string
	healthAddr  string
)

func main() {
	serverURL = envOr("HOGS_SERVER_URL", "ws://localhost:8080/agent/ws")
	agentToken = envOr("HOGS_AGENT_TOKEN", "")
	nodeName = envOr("HOGS_AGENT_NODE", "default")
	serverName = envOr("HOGS_AGENT_SERVER_NAME", "")
	gameType = envOr("HOGS_AGENT_GAME_TYPE", "minecraft")
	serviceName = envOr("HOGS_AGENT_SERVICE_NAME", "")
	dataDir = envOr("HOGS_AGENT_DATA_DIR", "/opt/game-servers")
	resticBin = envOr("HOGS_AGENT_RESTIC_BIN", "restic")
	unitPrefix = envOr("HOGS_AGENT_UNIT_PREFIX", "")
	tlsCert = envOr("HOGS_AGENT_TLS_CERT", "")
	tlsKey = envOr("HOGS_AGENT_TLS_KEY", "")
	healthAddr = envOr("HOGS_AGENT_HEALTH_ADDR", "")

	if agentToken == "" {
		log.Fatal("HOGS_AGENT_TOKEN is required")
	}
	if serviceName == "" && serverName != "" {
		serviceName = serverName
	}

	go func() {
		if healthAddr == "" {
			return
		}
		http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
		})
		log.Printf("Agent health endpoint listening on %s", healthAddr)
		if err := http.ListenAndServe(healthAddr, nil); err != nil {
			log.Printf("Health endpoint error: %v", err)
		}
	}()

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	for {
		err := connectAndServe(interrupt)
		if err != nil {
			log.Printf("Connection error: %v, reconnecting in 5s...", err)
		} else {
			log.Println("Disconnected, reconnecting in 5s...")
		}
		select {
		case <-interrupt:
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func unitName() string {
	if unitPrefix != "" {
		return unitPrefix + serviceName
	}
	return serviceName
}

func connectAndServe(interrupt chan os.Signal) error {
	u, err := url.Parse(serverURL)
	if err != nil {
		return fmt.Errorf("invalid server URL: %w", err)
	}
	q := u.Query()
	q.Set("token", agentToken)
	u.RawQuery = q.Encode()

	log.Printf("Connecting to %s...", u.String())

	dialer := websocket.DefaultDialer
	if tlsCert != "" && tlsKey != "" {
		cert, err := tls.LoadX509KeyPair(tlsCert, tlsKey)
		if err != nil {
			return fmt.Errorf("failed to load TLS cert/key: %w", err)
		}
		dialer = &websocket.Dialer{
			TLSClientConfig: &tls.Config{
				Certificates: []tls.Certificate{cert},
			},
		}
	}

	c, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		return fmt.Errorf("dial failed: %w", err)
	}
	defer c.Close()

	register := Envelope{
		Type: "register",
		Data: mustMarshal(RegisterData{
			NodeName:     nodeName,
			Capabilities: []string{"start", "stop", "restart", "command", "status", "file", "backup"},
			ServerName:   serverName,
			GameType:     gameType,
		}),
	}
	if err := c.WriteJSON(register); err != nil {
		return fmt.Errorf("register failed: %w", err)
	}
	log.Println("Registered with server")

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, message, err := c.ReadMessage()
			if err != nil {
				log.Printf("Read error: %v", err)
				return
			}
			handleMessage(message, c)
		}
	}()

	ticker := time.NewTicker(15 * time.Second)
	statusTicker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	defer statusTicker.Stop()

	for {
		select {
		case <-done:
			return nil
		case <-ticker.C:
			if err := c.WriteMessage(websocket.PingMessage, nil); err != nil {
				return err
			}
		case <-statusTicker.C:
			reportStatus(c)
		case <-interrupt:
			c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			select {
			case <-done:
			case <-time.After(time.Second):
			}
			return nil
		}
	}
}

func handleMessage(message []byte, c *websocket.Conn) {
	var env Envelope
	if err := json.Unmarshal(message, &env); err != nil {
		log.Printf("Invalid message: %v", err)
		return
	}

	switch env.Type {
	case "action":
		var data ActionRequestData
		json.Unmarshal(env.Data, &data)
		log.Printf("Received action: %s", data.Action)
		result := executeAction(data.Action)
		sendResult(c, "action_result", env.RequestID, result)

	case "command":
		var data CommandRequestData
		json.Unmarshal(env.Data, &data)
		log.Printf("Received command: %s", data.Command)
		output, err := executeCommand(data.Command)
		sendResult(c, "command_result", env.RequestID, map[string]interface{}{
			"success": err == nil,
			"output":  output,
			"error":   errStr(err),
		})

	case "file_list":
		var data FileListRequestData
		json.Unmarshal(env.Data, &data)
		result := filelist(data.Path)
		sendResult(c, "file_list_result", env.RequestID, result)

	case "file_read":
		var data FileReadRequestData
		json.Unmarshal(env.Data, &data)
		result := fileRead(data.Path)
		sendResult(c, "file_read_result", env.RequestID, result)

	case "file_write":
		var data FileWriteRequestData
		json.Unmarshal(env.Data, &data)
		result := fileWrite(data.Path, data.Content)
		sendResult(c, "file_write_result", env.RequestID, result)

	case "file_delete":
		var data FileDeleteRequestData
		json.Unmarshal(env.Data, &data)
		result := fileDelete(data.Path)
		sendResult(c, "file_delete_result", env.RequestID, result)

	case "mkdir":
		var data MkdirRequestData
		json.Unmarshal(env.Data, &data)
		result := mkdir(data.Path)
		sendResult(c, "mkdir_result", env.RequestID, result)

	case "backup_create":
		var data BackupRequestData
		json.Unmarshal(env.Data, &data)
		result := backupCreate(data.Repo, data.Password, data.Paths, data.Tags)
		sendResult(c, "backup_create_result", env.RequestID, result)

	case "backup_restore":
		var data BackupRestoreRequestData
		json.Unmarshal(env.Data, &data)
		result := backupRestore(data.Repo, data.Password, data.Snapshot, data.Target)
		sendResult(c, "backup_restore_result", env.RequestID, result)

	case "backup_list":
		var data BackupListRequestData
		json.Unmarshal(env.Data, &data)
		result := backupList(data.Repo, data.Password)
		sendResult(c, "backup_list_result", env.RequestID, result)

	default:
		log.Printf("Unknown message type: %s", env.Type)
	}
}

func sendResult(c *websocket.Conn, resultType string, requestID string, data interface{}) {
	resp := Envelope{Type: resultType, RequestID: requestID, Data: mustMarshal(data)}
	c.WriteJSON(resp)
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ── Systemd / Podman Quadlet Process Management ──

func executeAction(action string) map[string]interface{} {
	unit := unitName()
	if unit == "" {
		return map[string]interface{}{"success": false, "message": "no service name configured"}
	}

	var cmd *exec.Cmd
	switch action {
	case "start":
		cmd = exec.Command("systemctl", "start", unit)
	case "stop":
		cmd = exec.Command("systemctl", "stop", unit)
	case "restart":
		cmd = exec.Command("systemctl", "restart", unit)
	default:
		return map[string]interface{}{"success": false, "message": fmt.Sprintf("unknown action: %s", action)}
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"message": fmt.Sprintf("systemctl %s %s failed: %s: %s", action, unit, err, strings.TrimSpace(string(output))),
		}
	}

	return map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("systemctl %s %s succeeded", action, unit),
	}
}

func executeCommand(command string) (string, error) {
	unit := unitName()
	if unit == "" {
		return "", fmt.Errorf("no service name configured")
	}

	// Try podman exec first (for quadlet containers)
	containerName := unit
	if strings.HasSuffix(unit, ".service") {
		containerName = strings.TrimSuffix(unit, ".service")
	}

	// Check if container is running
	psOut, _ := exec.Command("podman", "ps", "--filter", "name="+containerName, "--format", "{{.Names}}").Output()
	running := strings.TrimSpace(string(psOut)) != ""

	if running {
		out, err := exec.Command("podman", "exec", containerName, "sh", "-c", command).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}

	return "", fmt.Errorf("container %s is not running", containerName)
}

func getServiceStatus(unit string) (active bool, substate string) {
	out, err := exec.Command("systemctl", "show", unit, "--property=ActiveState,SubState", "--value").Output()
	if err != nil {
		return false, "unknown"
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	active = len(lines) > 0 && lines[0] == "active"
	if len(lines) > 1 {
		substate = lines[1]
	}
	return
}

// ── File Management ──

func resolvePath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(dataDir, p)
}

func filelist(p string) map[string]interface{} {
	path := resolvePath(p)
	entries, err := os.ReadDir(path)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}

	type entry struct {
		Name  string `json:"name"`
		IsDir bool   `json:"isDir"`
		Size  int64  `json:"size"`
	}

	var files []entry
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, entry{
			Name:  e.Name(),
			IsDir: e.IsDir(),
			Size:  info.Size(),
		})
	}

	return map[string]interface{}{
		"success": true,
		"path":    path,
		"entries": files,
	}
}

func fileRead(p string) map[string]interface{} {
	path := resolvePath(p)
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}

	encoded := base64.StdEncoding.EncodeToString(data)

	return map[string]interface{}{
		"success":       true,
		"path":          path,
		"contentBase64": encoded,
		"size":          len(data),
	}
}

func fileWrite(p string, contentBase64 string) map[string]interface{} {
	path := resolvePath(p)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}

	data, err := base64.StdEncoding.DecodeString(contentBase64)
	if err != nil {
		return map[string]interface{}{"success": false, "error": "invalid base64: " + err.Error()}
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}

	return map[string]interface{}{
		"success": true,
		"path":    path,
		"size":    len(data),
	}
}

func fileDelete(p string) map[string]interface{} {
	path := resolvePath(p)
	err := os.RemoveAll(path)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	return map[string]interface{}{"success": true, "path": path}
}

func mkdir(p string) map[string]interface{} {
	path := resolvePath(p)
	err := os.MkdirAll(path, 0755)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	return map[string]interface{}{"success": true, "path": path}
}

// ── Restic Backup Management ──

func resticEnv(repo, password string) []string {
	return []string{
		"RESTIC_REPOSITORY=" + repo,
		"RESTIC_PASSWORD=" + password,
	}
}

func backupCreate(repo, password string, paths []string, tags []string) map[string]interface{} {
	if len(paths) == 0 {
		resolved := resolvePath(".")
		paths = []string{resolved}
	}

	args := []string{"backup"}
	for _, p := range paths {
		args = append(args, resolvePath(p))
	}
	for _, t := range tags {
		args = append(args, "--tag", t)
	}

	cmd := exec.Command(resticBin, args...)
	cmd.Env = append(os.Environ(), resticEnv(repo, password)...)
	output, err := cmd.CombinedOutput()

	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("%s: %s", err, strings.TrimSpace(string(output))),
		}
	}

	snapshotID := ""
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "snapshot ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				snapshotID = parts[1]
			}
		}
	}

	return map[string]interface{}{
		"success":    true,
		"snapshotId": snapshotID,
		"output":     strings.TrimSpace(string(output)),
	}
}

func backupRestore(repo, password, snapshot, target string) map[string]interface{} {
	if target == "" {
		target = resolvePath(".")
	} else {
		target = resolvePath(target)
	}

	args := []string{"restore", snapshot, "--target", target}
	cmd := exec.Command(resticBin, args...)
	cmd.Env = append(os.Environ(), resticEnv(repo, password)...)
	output, err := cmd.CombinedOutput()

	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("%s: %s", err, strings.TrimSpace(string(output))),
		}
	}

	return map[string]interface{}{
		"success": true,
		"target":  target,
		"output":  strings.TrimSpace(string(output)),
	}
}

func backupList(repo, password string) map[string]interface{} {
	cmd := exec.Command(resticBin, "snapshots", "--json")
	cmd.Env = append(os.Environ(), resticEnv(repo, password)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}

	var snapshots []map[string]interface{}
	dec := json.NewDecoder(bufio.NewReader(stdout))
	if err := dec.Decode(&snapshots); err != nil && err != io.EOF {
		cmd.Wait()
		return map[string]interface{}{"success": false, "error": "failed to parse restic output: " + err.Error()}
	}

	cmd.Wait()

	type snapInfo struct {
		ID    string   `json:"id"`
		Time  string   `json:"time"`
		Tags  []string `json:"tags"`
		Paths []string `json:"paths"`
	}

	var result []snapInfo
	for _, s := range snapshots {
		si := snapInfo{}
		if v, ok := s["id"].(string); ok {
			si.ID = v
		}
		if v, ok := s["time"].(string); ok {
			si.Time = v
		}
		if v, ok := s["tags"].([]interface{}); ok {
			for _, t := range v {
				if ts, ok := t.(string); ok {
					si.Tags = append(si.Tags, ts)
				}
			}
		}
		if v, ok := s["paths"].([]interface{}); ok {
			for _, p := range v {
				if ps, ok := p.(string); ok {
					si.Paths = append(si.Paths, ps)
				}
			}
		}
		result = append(result, si)
	}

	return map[string]interface{}{
		"success":   true,
		"snapshots": result,
	}
}

// ── Status Reporting ──

func reportStatus(c *websocket.Conn) {
	unit := unitName()
	online := false
	var subState string

	if unit != "" {
		online, subState = getServiceStatus(unit)
	}

	status := StatusReportData{
		Online:     online,
		Players:    0,
		MaxPlayers: 0,
		Version:    subState,
	}
	env := Envelope{Type: "status", Data: mustMarshal(status)}
	c.WriteJSON(env)
}

func mustMarshal(v interface{}) json.RawMessage {
	b, _ := json.Marshal(v)
	return json.RawMessage(b)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
