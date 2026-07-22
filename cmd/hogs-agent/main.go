package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"gopkg.in/yaml.v3"
)

type Envelope struct {
	Type      string          `json:"type"`
	RequestID string          `json:"requestId,omitempty"`
	Data      json.RawMessage `json:"data"`
}

type RegisterData struct {
	NodeName     string   `json:"nodeName"`
	Capabilities []string `json:"capabilities"`
	Servers      []string `json:"servers"`
}

type ActionRequestData struct {
	ServerName string `json:"serverName"`
	Action     string `json:"action"`
}

type CommandRequestData struct {
	ServerName string `json:"serverName"`
	Command    string `json:"command"`
}

type FileListRequestData struct {
	ServerName string `json:"serverName"`
	Path       string `json:"path"`
}

type FileReadRequestData struct {
	ServerName string `json:"serverName"`
	Path       string `json:"path"`
}

type FileWriteRequestData struct {
	ServerName string `json:"serverName"`
	Path       string `json:"path"`
	Content    string `json:"content"`
}

type FileDeleteRequestData struct {
	ServerName string `json:"serverName"`
	Path       string `json:"path"`
}

type MkdirRequestData struct {
	ServerName string `json:"serverName"`
	Path       string `json:"path"`
}

type BackupRequestData struct {
	ServerName string   `json:"serverName"`
	Repo       string   `json:"repo"`
	Password   string   `json:"password"`
	Paths      []string `json:"paths"`
	Tags       []string `json:"tags"`
}

type BackupRestoreRequestData struct {
	ServerName string `json:"serverName"`
	Repo       string `json:"repo"`
	Password   string `json:"password"`
	Snapshot   string `json:"snapshot"`
	Target     string `json:"target"`
}

type BackupListRequestData struct {
	ServerName string `json:"serverName"`
	Repo       string `json:"repo"`
	Password   string `json:"password"`
}

type StatusReportData struct {
	ServerName string `json:"serverName"`
	Online     bool   `json:"online"`
	Players    int    `json:"players"`
	MaxPlayers int    `json:"maxPlayers"`
	Version    string `json:"version"`
}

type AgentConfig struct {
	Node       string                  `yaml:"node"`
	ServerURL  string                  `yaml:"server_url"`
	ResticBin  string                  `yaml:"restic_bin"`
	HealthAddr string                  `yaml:"health_addr"`
	Servers    map[string]ServerConfig `yaml:"servers"`
}

type ServerConfig struct {
	Unit           string        `yaml:"unit"`
	GameType       string        `yaml:"game_type"`
	DataDir        string        `yaml:"data_dir"`
	Address        string        `yaml:"address"`
	ExclusiveGroup string        `yaml:"exclusive_group"`
	Console        ConsoleConfig `yaml:"console"`
}

type ConsoleConfig struct {
	Type         string `yaml:"type"`
	Host         string `yaml:"host"`
	Port         int    `yaml:"port"`
	PasswordFile string `yaml:"password_file"`
}

var agentConfig AgentConfig
var agentToken string
var websocketWriteMu sync.Mutex

func main() {
	agentToken = envOr("HOGS_AGENT_TOKEN", "")
	if agentToken == "" {
		log.Fatal("HOGS_AGENT_TOKEN is required")
	}
	configPath := envOr("HOGS_AGENT_CONFIG", "/etc/hogs-agent/config.yaml")
	raw, err := os.ReadFile(configPath)
	if err != nil {
		log.Fatalf("Read config: %v", err)
	}
	if err := yaml.Unmarshal(raw, &agentConfig); err != nil {
		log.Fatalf("Parse config: %v", err)
	}
	if err := validateConfig(agentConfig); err != nil {
		log.Fatalf("Invalid config: %v", err)
	}
	if agentConfig.ResticBin == "" {
		agentConfig.ResticBin = "restic"
	}

	go func() {
		if agentConfig.HealthAddr == "" {
			return
		}
		http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
		})
		log.Printf("Agent health endpoint listening on %s", agentConfig.HealthAddr)
		if err := http.ListenAndServe(agentConfig.HealthAddr, nil); err != nil {
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

func serverConfig(name string) (*ServerConfig, error) {
	server, ok := agentConfig.Servers[name]
	if !ok {
		return nil, fmt.Errorf("server %q is not in the local allowlist", name)
	}
	return &server, nil
}

func validateConfig(cfg AgentConfig) error {
	if cfg.Node == "" || cfg.ServerURL == "" || len(cfg.Servers) == 0 {
		return fmt.Errorf("node, server_url, and at least one server are required")
	}
	for name, server := range cfg.Servers {
		if name == "" || server.Unit == "" || !filepath.IsAbs(server.DataDir) || strings.Contains(server.Unit, "/") {
			return fmt.Errorf("server %q requires a unit and absolute data_dir", name)
		}
		if server.Console.Type == "rcon" && (server.Console.Host == "" || server.Console.Port <= 0 || !filepath.IsAbs(server.Console.PasswordFile)) {
			return fmt.Errorf("server %q has incomplete RCON configuration", name)
		}
	}
	return nil
}

func sortedServerNames() []string {
	names := make([]string, 0, len(agentConfig.Servers))
	for name := range agentConfig.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func connectAndServe(interrupt chan os.Signal) error {
	u, err := url.Parse(agentConfig.ServerURL)
	if err != nil {
		return fmt.Errorf("invalid server URL: %w", err)
	}
	log.Printf("Connecting to %s://%s%s...", u.Scheme, u.Host, u.Path)

	dialer := websocket.DefaultDialer
	tlsCert, tlsKey := envOr("HOGS_AGENT_TLS_CERT", ""), envOr("HOGS_AGENT_TLS_KEY", "")
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

	header := http.Header{}
	header.Set("Authorization", "Bearer "+agentToken)
	c, _, err := dialer.Dial(u.String(), header)
	if err != nil {
		return fmt.Errorf("dial failed: %w", err)
	}
	defer c.Close()

	register := Envelope{
		Type: "register",
		Data: mustMarshal(RegisterData{
			NodeName:     agentConfig.Node,
			Capabilities: []string{"start", "stop", "restart", "command", "console", "status", "file", "backup"},
			Servers:      sortedServerNames(),
		}),
	}
	if err := writeJSON(c, register); err != nil {
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
		server, err := serverConfig(data.ServerName)
		if err != nil {
			sendResult(c, "action_result", env.RequestID, map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		result := executeAction(server, data.Action)
		sendResult(c, "action_result", env.RequestID, result)

	case "command":
		var data CommandRequestData
		json.Unmarshal(env.Data, &data)
		log.Printf("Received command: %s", data.Command)
		server, err := serverConfig(data.ServerName)
		if err != nil {
			sendResult(c, "command_result", env.RequestID, map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		output, err := executeCommand(server, data.Command)
		sendResult(c, "command_result", env.RequestID, map[string]interface{}{
			"success": err == nil,
			"output":  output,
			"error":   errStr(err),
		})

	case "file_list":
		var data FileListRequestData
		json.Unmarshal(env.Data, &data)
		server, err := serverConfig(data.ServerName)
		if err != nil {
			sendResult(c, "file_list_result", env.RequestID, map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		result := filelist(server, data.Path)
		sendResult(c, "file_list_result", env.RequestID, result)

	case "file_read":
		var data FileReadRequestData
		json.Unmarshal(env.Data, &data)
		server, err := serverConfig(data.ServerName)
		if err != nil {
			sendResult(c, "file_read_result", env.RequestID, map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		result := fileRead(server, data.Path)
		sendResult(c, "file_read_result", env.RequestID, result)

	case "file_write":
		var data FileWriteRequestData
		json.Unmarshal(env.Data, &data)
		server, err := serverConfig(data.ServerName)
		if err != nil {
			sendResult(c, "file_write_result", env.RequestID, map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		result := fileWrite(server, data.Path, data.Content)
		sendResult(c, "file_write_result", env.RequestID, result)

	case "file_delete":
		var data FileDeleteRequestData
		json.Unmarshal(env.Data, &data)
		server, err := serverConfig(data.ServerName)
		if err != nil {
			sendResult(c, "file_delete_result", env.RequestID, map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		result := fileDelete(server, data.Path)
		sendResult(c, "file_delete_result", env.RequestID, result)

	case "mkdir":
		var data MkdirRequestData
		json.Unmarshal(env.Data, &data)
		server, err := serverConfig(data.ServerName)
		if err != nil {
			sendResult(c, "mkdir_result", env.RequestID, map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		result := mkdir(server, data.Path)
		sendResult(c, "mkdir_result", env.RequestID, result)

	case "backup_create":
		var data BackupRequestData
		json.Unmarshal(env.Data, &data)
		server, err := serverConfig(data.ServerName)
		if err != nil {
			sendResult(c, "backup_create_result", env.RequestID, map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		result := backupCreate(server, data.Repo, data.Password, data.Paths, data.Tags)
		sendResult(c, "backup_create_result", env.RequestID, result)

	case "backup_restore":
		var data BackupRestoreRequestData
		json.Unmarshal(env.Data, &data)
		server, err := serverConfig(data.ServerName)
		if err != nil {
			sendResult(c, "backup_restore_result", env.RequestID, map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		result := backupRestore(server, data.Repo, data.Password, data.Snapshot, data.Target)
		sendResult(c, "backup_restore_result", env.RequestID, result)

	case "backup_list":
		var data BackupListRequestData
		json.Unmarshal(env.Data, &data)
		server, err := serverConfig(data.ServerName)
		if err != nil {
			sendResult(c, "backup_list_result", env.RequestID, map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		result := backupList(server, data.Repo, data.Password)
		sendResult(c, "backup_list_result", env.RequestID, result)

	case "backup_init":
		var data BackupRequestData
		json.Unmarshal(env.Data, &data)
		server, err := serverConfig(data.ServerName)
		if err != nil {
			sendResult(c, "backup_init_result", env.RequestID, map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		result := backupInit(server, data.Repo, data.Password)
		sendResult(c, "backup_init_result", env.RequestID, result)

	case "console_subscribe":
		var data struct {
			ServerName string `json:"serverName"`
		}
		json.Unmarshal(env.Data, &data)
		server, err := serverConfig(data.ServerName)
		if err != nil {
			return
		}
		startConsoleStreaming(c, data.ServerName, server)

	case "console_input":
		var data struct {
			ServerName string `json:"serverName"`
			Input      string `json:"input"`
		}
		json.Unmarshal(env.Data, &data)
		server, err := serverConfig(data.ServerName)
		if err != nil {
			return
		}
		executeConsoleInput(server, data.Input)

	default:
		log.Printf("Unknown message type: %s", env.Type)
	}
}

var (
	consoleCancels  = make(map[string]chan struct{})
	consoleCancelMu sync.Mutex
)

func startConsoleStreaming(c *websocket.Conn, serverName string, server *ServerConfig) {
	unit := server.Unit
	if unit == "" {
		log.Println("Console subscribe: no service name configured")
		return
	}

	consoleCancelMu.Lock()
	if previous := consoleCancels[serverName]; previous != nil {
		close(previous)
	}
	cancel := make(chan struct{})
	consoleCancels[serverName] = cancel
	consoleCancelMu.Unlock()

	go func() {
		// Tail journalctl for this unit
		cmd := exec.Command("journalctl", "-u", unit, "-f", "-n", "100", "--no-hostname", "-o", "cat")
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			log.Printf("journalctl stdout pipe failed: %v", err)
			return
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			log.Printf("journalctl stderr pipe failed: %v", err)
			return
		}
		if err := cmd.Start(); err != nil {
			log.Printf("journalctl start failed: %v", err)
			return
		}

		reader := io.MultiReader(stdout, stderr)
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			select {
			case <-cancel:
				cmd.Process.Kill()
				return
			default:
				line := scanner.Text()
				env := Envelope{
					Type: "console",
					Data: mustMarshal(map[string]string{
						"serverName": serverName,
						"line":       line,
						"timestamp":  time.Now().UTC().Format(time.RFC3339),
					}),
				}
				if err := writeJSON(c, env); err != nil {
					log.Printf("console write failed: %v", err)
					cmd.Process.Kill()
					return
				}
			}
		}
		if err := cmd.Wait(); err != nil {
			log.Printf("journalctl exited: %v", err)
		}
	}()
}

func executeConsoleInput(server *ServerConfig, input string) {
	out, err := executeCommand(server, input)
	if err != nil {
		log.Printf("console input error: %v", err)
	} else if out != "" {
		log.Printf("console input completed: %s", out)
	}
}

func sendResult(c *websocket.Conn, resultType string, requestID string, data interface{}) {
	resp := Envelope{Type: resultType, RequestID: requestID, Data: mustMarshal(data)}
	_ = writeJSON(c, resp)
}

func writeJSON(c *websocket.Conn, value interface{}) error {
	websocketWriteMu.Lock()
	defer websocketWriteMu.Unlock()
	return c.WriteJSON(value)
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ── Systemd / Podman Quadlet Process Management ──

func executeAction(server *ServerConfig, action string) map[string]interface{} {
	unit := server.Unit
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

func executeCommand(server *ServerConfig, command string) (string, error) {
	if server.Console.Type == "rcon" {
		return executeRCON(server.Console, command)
	}
	unit := server.Unit
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
		// Split command into args to avoid shell injection
		args := []string{"exec", containerName}
		args = append(args, strings.Fields(command)...)
		out, err := exec.Command("podman", args...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}

	return "", fmt.Errorf("container %s is not running", containerName)
}

func executeRCON(console ConsoleConfig, command string) (string, error) {
	if console.Host == "" || console.Port == 0 || console.PasswordFile == "" {
		return "", fmt.Errorf("incomplete RCON configuration")
	}
	password, err := os.ReadFile(console.PasswordFile)
	if err != nil {
		return "", fmt.Errorf("read RCON credential: %w", err)
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", console.Host, console.Port), 5*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := writeRCONPacket(conn, 1, 3, strings.TrimSpace(string(password))); err != nil {
		return "", err
	}
	id, _, _, err := readRCONPacket(conn)
	if err != nil {
		return "", err
	}
	if id == -1 {
		return "", fmt.Errorf("RCON authentication failed")
	}
	if err := writeRCONPacket(conn, 2, 2, command); err != nil {
		return "", err
	}
	_, _, body, err := readRCONPacket(conn)
	return body, err
}

func writeRCONPacket(w io.Writer, id, packetType int32, body string) error {
	length := int32(10 + len(body))
	if err := binary.Write(w, binary.LittleEndian, length); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, id); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, packetType); err != nil {
		return err
	}
	_, err := w.Write(append([]byte(body), 0, 0))
	return err
}

func readRCONPacket(r io.Reader) (int32, int32, string, error) {
	var length, id, packetType int32
	if err := binary.Read(r, binary.LittleEndian, &length); err != nil {
		return 0, 0, "", err
	}
	if length < 10 || length > 4<<20 {
		return 0, 0, "", fmt.Errorf("invalid RCON packet length %d", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, 0, "", err
	}
	id = int32(binary.LittleEndian.Uint32(payload[0:4]))
	packetType = int32(binary.LittleEndian.Uint32(payload[4:8]))
	body := strings.TrimRight(string(payload[8:]), "\x00")
	return id, packetType, body, nil
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

func resolvePath(server *ServerConfig, p string) (string, error) {
	var path string
	if filepath.IsAbs(p) {
		path = filepath.Clean(p)
	} else {
		path = filepath.Clean(filepath.Join(server.DataDir, p))
	}
	cleanDataDir := filepath.Clean(server.DataDir)
	if !strings.HasPrefix(path, cleanDataDir+string(filepath.Separator)) && path != cleanDataDir {
		return "", fmt.Errorf("path traversal detected")
	}
	current := cleanDataDir
	relative, _ := filepath.Rel(cleanDataDir, path)
	components := []string{}
	if relative != "." {
		components = strings.Split(relative, string(filepath.Separator))
	}
	for _, component := range append([]string{""}, components...) {
		if component != "" {
			current = filepath.Join(current, component)
		}
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			break
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink path component is not allowed")
		}
	}
	return path, nil
}

func filelist(server *ServerConfig, p string) map[string]interface{} {
	path, err := resolvePath(server, p)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
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

func fileRead(server *ServerConfig, p string) map[string]interface{} {
	path, err := resolvePath(server, p)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
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

func fileWrite(server *ServerConfig, p string, contentBase64 string) map[string]interface{} {
	path, err := resolvePath(server, p)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
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

func fileDelete(server *ServerConfig, p string) map[string]interface{} {
	path, err := resolvePath(server, p)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	info, err := os.Stat(path)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	if info.IsDir() {
		return map[string]interface{}{"success": false, "error": "cannot delete directories"}
	}
	if err := os.Remove(path); err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	return map[string]interface{}{"success": true, "path": path}
}

func mkdir(server *ServerConfig, p string) map[string]interface{} {
	path, err := resolvePath(server, p)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	if err := os.MkdirAll(path, 0755); err != nil {
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

func backupCreate(server *ServerConfig, repo, password string, paths []string, tags []string) map[string]interface{} {
	if len(paths) == 0 {
		resolved, err := resolvePath(server, ".")
		if err != nil {
			return map[string]interface{}{"success": false, "error": err.Error()}
		}
		paths = []string{resolved}
	}

	args := []string{"backup"}
	for _, p := range paths {
		resolved, err := resolvePath(server, p)
		if err != nil {
			return map[string]interface{}{"success": false, "error": err.Error()}
		}
		args = append(args, resolved)
	}
	for _, t := range tags {
		args = append(args, "--tag", t)
	}

	cmd := exec.Command(agentConfig.ResticBin, args...)
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

func backupRestore(server *ServerConfig, repo, password, snapshot, target string) map[string]interface{} {
	var err error
	if target == "" {
		target, err = resolvePath(server, ".")
		if err != nil {
			return map[string]interface{}{"success": false, "error": err.Error()}
		}
	} else {
		target, err = resolvePath(server, target)
		if err != nil {
			return map[string]interface{}{"success": false, "error": err.Error()}
		}
	}

	args := []string{"restore", snapshot, "--target", target}
	cmd := exec.Command(agentConfig.ResticBin, args...)
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

func backupList(_ *ServerConfig, repo, password string) map[string]interface{} {
	cmd := exec.Command(agentConfig.ResticBin, "snapshots", "--json")
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

func backupInit(_ *ServerConfig, repo, password string) map[string]interface{} {
	cmd := exec.Command(agentConfig.ResticBin, "init", "--json")
	cmd.Env = append(os.Environ(), resticEnv(repo, password)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return map[string]interface{}{"success": false, "error": string(output)}
	}
	return map[string]interface{}{"success": true, "message": "restic repository initialized"}
}

// ── Status Reporting ──

func reportStatus(c *websocket.Conn) {
	for _, name := range sortedServerNames() {
		server := agentConfig.Servers[name]
		online, subState := getServiceStatus(server.Unit)
		status := StatusReportData{
			ServerName: name,
			Online:     online,
			Players:    0,
			MaxPlayers: 0,
			Version:    subState,
		}
		env := Envelope{Type: "status", Data: mustMarshal(status)}
		_ = writeJSON(c, env)
	}
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
