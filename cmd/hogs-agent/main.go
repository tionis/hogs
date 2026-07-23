package main

import (
	"bufio"
	"bytes"
	"context"
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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

type StatusReportData struct {
	ServerName   string          `json:"serverName"`
	Online       bool            `json:"online"`
	Substate     string          `json:"substate"`
	Players      int             `json:"players"`
	MaxPlayers   int             `json:"maxPlayers"`
	PlayersKnown bool            `json:"playersKnown"`
	Version      string          `json:"version"`
	Resources    *ResourceStatus `json:"resources,omitempty"`
}

type ResourceStatus struct {
	CPUPercent      *float64  `json:"cpuPercent,omitempty"`
	CPULimitPercent *float64  `json:"cpuLimitPercent,omitempty"`
	MemoryCurrent   *uint64   `json:"memoryCurrentBytes,omitempty"`
	MemoryPeak      *uint64   `json:"memoryPeakBytes,omitempty"`
	MemoryHigh      *uint64   `json:"memoryHighBytes,omitempty"`
	MemoryLimit     *uint64   `json:"memoryLimitBytes,omitempty"`
	SampledAt       time.Time `json:"sampledAt"`
}

type resourceCPUSample struct {
	usage uint64
	at    time.Time
}

var (
	resourceSampleMu sync.Mutex
	resourceSamples  = map[string]resourceCPUSample{}
)

var minecraftPlayerCount = regexp.MustCompile(`(?i)there are\s+(\d+)\s+of a max of\s+(\d+)\s+players online`)

func playerStatus(server *ServerConfig) (players, maxPlayers int, known bool) {
	if server.Console.Type != "rcon" {
		return 0, 0, false
	}
	command := "list"
	if server.GameType == "factorio" {
		command = "/players"
	}
	output, err := executeCommand(server, command)
	if err != nil {
		return 0, 0, false
	}
	return parsePlayerStatus(server.GameType, output)
}

func serverVersion(server *ServerConfig) string {
	if server.GameType != "factorio" || server.Console.Type != "rcon" {
		return ""
	}
	output, err := executeCommand(server, "/version")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(output)
}

func parsePlayerStatus(gameType, output string) (players, maxPlayers int, known bool) {
	if gameType == "minecraft" {
		matches := minecraftPlayerCount.FindStringSubmatch(output)
		if len(matches) != 3 {
			return 0, 0, false
		}
		players, err := strconv.Atoi(matches[1])
		if err != nil {
			return 0, 0, false
		}
		maxPlayers, err = strconv.Atoi(matches[2])
		return players, maxPlayers, err == nil
	}
	if gameType == "factorio" {
		for _, line := range strings.Split(output, "\n") {
			if strings.HasSuffix(strings.TrimSpace(line), "(online)") {
				players++
			}
		}
		return players, 0, true
	}
	return 0, 0, false
}

type AgentConfig struct {
	Node       string                  `yaml:"node"`
	ResticBin  string                  `yaml:"restic_bin"`
	HealthAddr string                  `yaml:"health_addr"`
	API        AgentAPIConfig          `yaml:"api"`
	Servers    map[string]ServerConfig `yaml:"servers"`
}

type AgentAPIConfig struct {
	Listen         string   `yaml:"listen"`
	SecretFile     string   `yaml:"secret_file"`
	TLSCertFile    string   `yaml:"tls_cert_file"`
	TLSKeyFile     string   `yaml:"tls_key_file"`
	AllowedOrigins []string `yaml:"allowed_origins"`
}

type ServerConfig struct {
	Unit           string        `yaml:"unit"`
	GameType       string        `yaml:"game_type"`
	DataDir        string        `yaml:"data_dir"`
	Address        string        `yaml:"address"`
	ExclusiveGroup string        `yaml:"exclusive_group"`
	Console        ConsoleConfig `yaml:"console"`
	Backup         BackupConfig  `yaml:"backup"`
}

type ConsoleConfig struct {
	Type         string `yaml:"type"`
	Host         string `yaml:"host"`
	Port         int    `yaml:"port"`
	PasswordFile string `yaml:"password_file"`
}

type BackupConfig struct {
	EnvironmentFile string `yaml:"environment_file"`
}

var agentConfig AgentConfig
var agentSecret []byte

func main() {
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

	agentSecret, err = os.ReadFile(agentConfig.API.SecretFile)
	if err != nil {
		log.Fatalf("Read agent API secret: %v", err)
	}
	agentSecret = bytes.TrimSpace(agentSecret)
	listener, err := net.Listen("tcp", agentConfig.API.Listen)
	if err != nil {
		log.Fatalf("Listen on agent API: %v", err)
	}
	apiServer := &http.Server{
		Handler:           agentAPI(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		log.Printf("Agent API listening on %s", agentConfig.API.Listen)
		var serveErr error
		if agentConfig.API.TLSCertFile != "" {
			serveErr = apiServer.ServeTLS(listener, agentConfig.API.TLSCertFile, agentConfig.API.TLSKeyFile)
		} else {
			serveErr = apiServer.Serve(listener)
		}
		if serveErr != nil && serveErr != http.ErrServerClosed {
			log.Fatalf("Agent API: %v", serveErr)
		}
	}()

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	<-interrupt
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = apiServer.Shutdown(ctx)
}

func serverConfig(name string) (*ServerConfig, error) {
	server, ok := agentConfig.Servers[name]
	if !ok {
		return nil, fmt.Errorf("server %q is not in the local allowlist", name)
	}
	return &server, nil
}

func validateConfig(cfg AgentConfig) error {
	if cfg.Node == "" || len(cfg.Servers) == 0 {
		return fmt.Errorf("node and at least one server are required")
	}
	if cfg.API.Listen == "" {
		return fmt.Errorf("api.listen is required")
	}
	if cfg.API.SecretFile == "" || !filepath.IsAbs(cfg.API.SecretFile) {
		return fmt.Errorf("api.secret_file must be absolute")
	}
	if (cfg.API.TLSCertFile == "") != (cfg.API.TLSKeyFile == "") {
		return fmt.Errorf("api.tls_cert_file and api.tls_key_file must be configured together")
	}
	if cfg.API.TLSCertFile != "" && (!filepath.IsAbs(cfg.API.TLSCertFile) || !filepath.IsAbs(cfg.API.TLSKeyFile)) {
		return fmt.Errorf("agent API TLS paths must be absolute")
	}
	for _, origin := range cfg.API.AllowedOrigins {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Path != "" {
			return fmt.Errorf("api.allowed_origins must contain HTTPS origins")
		}
	}
	for name, server := range cfg.Servers {
		if name == "" || server.Unit == "" || !filepath.IsAbs(server.DataDir) || strings.Contains(server.Unit, "/") {
			return fmt.Errorf("server %q requires a unit and absolute data_dir", name)
		}
		if server.Console.Type == "rcon" && (server.Console.Host == "" || server.Console.Port <= 0 || !filepath.IsAbs(server.Console.PasswordFile)) {
			return fmt.Errorf("server %q has incomplete RCON configuration", name)
		}
		if server.Backup.EnvironmentFile != "" && !filepath.IsAbs(server.Backup.EnvironmentFile) {
			return fmt.Errorf("server %q backup environment_file must be absolute", name)
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

func agentCapabilities() []string {
	capabilities := []string{"start", "stop", "restart", "command", "console", "status", "file"}
	for _, server := range agentConfig.Servers {
		if server.Backup.EnvironmentFile != "" {
			return append(capabilities, "backup")
		}
	}
	return capabilities
}

func isRoutineRCONConnectionLine(line string) bool {
	minecraftConnection := strings.Contains(line, "Thread RCON Client /") &&
		(strings.Contains(line, " started") || strings.Contains(line, " shutting down"))
	factorioConnection := strings.Contains(line, "RemoteCommandProcessor.cpp:") &&
		strings.Contains(line, "New RCON connection from IP ADDR:")
	return minecraftConnection || factorioConnection
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
	return readRCONResponse(conn, 2)
}

func readRCONResponse(conn net.Conn, requestID int32) (string, error) {
	var response strings.Builder
	gotResponse := false
	for {
		idleTimeout := 100 * time.Millisecond
		if !gotResponse {
			idleTimeout = 10 * time.Second
		}
		_ = conn.SetReadDeadline(time.Now().Add(idleTimeout))
		id, _, body, err := readRCONPacket(conn)
		if err != nil {
			if gotResponse {
				return response.String(), nil
			}
			// Several Minecraft commands legitimately return an empty response
			// and close the request without a response packet. Authentication
			// has already succeeded, so EOF here means the command was accepted.
			if err == io.EOF {
				return "", nil
			}
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				return "", nil
			}
			return "", err
		}
		if id == requestID {
			gotResponse = true
			response.WriteString(body)
		}
	}
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
	active, substate, _ = getServiceStatusWithResources(unit, time.Now())
	return
}

func getServiceStatusWithResources(unit string, sampledAt time.Time) (active bool, substate string, resources *ResourceStatus) {
	properties := "ActiveState,SubState,CPUUsageNSec,CPUQuotaPerSecUSec,MemoryCurrent,MemoryPeak,MemoryHigh,MemoryMax"
	out, err := exec.Command("systemctl", "show", unit, "--property="+properties).Output()
	if err != nil {
		return false, "unknown", nil
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		key, value, found := strings.Cut(line, "=")
		if found {
			values[key] = value
		}
	}
	active = values["ActiveState"] == "active"
	substate = values["SubState"]
	resources = &ResourceStatus{
		MemoryCurrent: parseSystemdBytes(values["MemoryCurrent"]),
		MemoryPeak:    parseSystemdBytes(values["MemoryPeak"]),
		MemoryHigh:    parseSystemdBytes(values["MemoryHigh"]),
		MemoryLimit:   parseSystemdBytes(values["MemoryMax"]),
		SampledAt:     sampledAt,
	}
	if quota := parseSystemdDuration(values["CPUQuotaPerSecUSec"]); quota != nil {
		percent := float64(*quota) / float64(time.Second) * 100
		resources.CPULimitPercent = &percent
	}
	if usage, parseErr := strconv.ParseUint(values["CPUUsageNSec"], 10, 64); parseErr == nil {
		resources.CPUPercent = sampleCPUPercent(unit, usage, sampledAt, active)
	}
	return active, substate, resources
}

func parseSystemdBytes(value string) *uint64 {
	if value == "" || value == "infinity" || value == "[not set]" {
		return nil
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return nil
	}
	return &parsed
}

func parseSystemdDuration(value string) *time.Duration {
	if value == "" || value == "infinity" || value == "[not set]" {
		return nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return nil
	}
	return &parsed
}

func sampleCPUPercent(unit string, usage uint64, sampledAt time.Time, active bool) *float64 {
	resourceSampleMu.Lock()
	previous, exists := resourceSamples[unit]
	resourceSamples[unit] = resourceCPUSample{usage: usage, at: sampledAt}
	resourceSampleMu.Unlock()
	if !active || !exists || usage < previous.usage || !sampledAt.After(previous.at) {
		return nil
	}
	percent := float64(usage-previous.usage) / float64(sampledAt.Sub(previous.at)) * 100
	return &percent
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

func fileOperation(server *ServerConfig, operation, sourcePath, targetPath string) map[string]interface{} {
	if operation != "rename" && operation != "copy" && operation != "move" {
		return map[string]interface{}{"success": false, "error": "unsupported file operation"}
	}
	source, err := resolvePath(server, sourcePath)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	target, err := resolvePath(server, targetPath)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	dataDir := filepath.Clean(server.DataDir)
	if source == dataDir || target == dataDir || source == target {
		return map[string]interface{}{"success": false, "error": "source and destination must identify different entries"}
	}
	sourceInfo, err := os.Lstat(source)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	if sourceInfo.Mode()&os.ModeSymlink != 0 {
		return map[string]interface{}{"success": false, "error": "symbolic links are not supported"}
	}
	if _, err := os.Lstat(target); err == nil {
		return map[string]interface{}{"success": false, "error": "destination already exists"}
	} else if !os.IsNotExist(err) {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	parentInfo, err := os.Stat(filepath.Dir(target))
	if err != nil || !parentInfo.IsDir() {
		return map[string]interface{}{"success": false, "error": "destination directory does not exist"}
	}

	switch operation {
	case "rename":
		if filepath.Dir(source) != filepath.Dir(target) {
			return map[string]interface{}{"success": false, "error": "rename must stay in the same directory"}
		}
		err = os.Rename(source, target)
	case "move":
		if sourceInfo.IsDir() {
			relative, relErr := filepath.Rel(source, target)
			if relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return map[string]interface{}{"success": false, "error": "cannot move a directory into itself"}
			}
		}
		err = os.Rename(source, target)
	case "copy":
		if !sourceInfo.Mode().IsRegular() {
			return map[string]interface{}{"success": false, "error": "only regular files can be copied"}
		}
		err = copyFileExclusive(source, target, sourceInfo.Mode().Perm())
	}
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	return map[string]interface{}{
		"success": true, "operation": operation, "path": sourcePath, "target": targetPath,
	}
}

func copyFileExclusive(source, target string, mode os.FileMode) (resultErr error) {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := output.Close(); resultErr == nil {
			resultErr = closeErr
		}
		if resultErr != nil {
			_ = os.Remove(target)
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	return output.Sync()
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

func resticEnv(server *ServerConfig) ([]string, error) {
	path := server.Backup.EnvironmentFile
	if path == "" {
		return nil, fmt.Errorf("backup profile is not configured")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read backup profile: %w", err)
	}
	defer file.Close()

	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 || !validEnvironmentKey(parts[0]) {
			return nil, fmt.Errorf("invalid backup environment entry")
		}
		value := strings.TrimSpace(parts[1])
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			if value[0] == '"' {
				decoded, decodeErr := strconv.Unquote(value)
				if decodeErr != nil {
					return nil, fmt.Errorf("invalid quoted backup environment value")
				}
				value = decoded
			} else {
				value = value[1 : len(value)-1]
			}
		}
		values[parts[0]] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read backup profile: %w", err)
	}
	if values["RESTIC_REPOSITORY"] == "" || (values["RESTIC_PASSWORD"] == "" && values["RESTIC_PASSWORD_FILE"] == "") {
		return nil, fmt.Errorf("backup profile requires RESTIC_REPOSITORY and a password source")
	}
	env := os.Environ()
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env, nil
}

func validEnvironmentKey(key string) bool {
	if key == "" {
		return false
	}
	for i, char := range key {
		if (char >= 'A' && char <= 'Z') || char == '_' || (i > 0 && char >= '0' && char <= '9') {
			continue
		}
		return false
	}
	return true
}

func backupCreate(server *ServerConfig, paths []string, tags []string) map[string]interface{} {
	env, err := resticEnv(server)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
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
	cmd.Env = env
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

func backupRestore(server *ServerConfig, snapshot, target string) map[string]interface{} {
	env, err := resticEnv(server)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
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
	cmd.Env = env
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

func backupList(server *ServerConfig) map[string]interface{} {
	env, err := resticEnv(server)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	cmd := exec.Command(agentConfig.ResticBin, "snapshots", "--json")
	cmd.Env = env
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

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
