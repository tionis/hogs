package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxMinecraftPacket = 2 << 20
	maxBootSamples     = 20
)

type GatewayConfig struct {
	Type                string `yaml:"type"`
	Listen              string `yaml:"listen"`
	Backend             string `yaml:"backend"`
	ReadinessTimeout    string `yaml:"readiness_timeout"`
	ConnectTimeout      string `yaml:"connect_timeout"`
	WakeCooldown        string `yaml:"wake_cooldown"`
	LoginRateLimit      string `yaml:"login_rate_limit"`
	DefaultBootEstimate string `yaml:"default_boot_estimate"`
	StartingMessage     string `yaml:"starting_message"`
}

type GatewayStatus struct {
	Type                    string     `json:"type"`
	State                   string     `json:"state"`
	EstimatedBootSeconds    int        `json:"estimatedBootSeconds"`
	LastBootDurationSeconds int        `json:"lastBootDurationSeconds,omitempty"`
	WakeAttempts            uint64     `json:"wakeAttempts"`
	WakeTriggered           uint64     `json:"wakeTriggered"`
	ProxiedConnections      uint64     `json:"proxiedConnections"`
	RejectedConnections     uint64     `json:"rejectedConnections"`
	ActiveConnections       int64      `json:"activeConnections"`
	LastWakeAt              *time.Time `json:"lastWakeAt,omitempty"`
	LastReadyAt             *time.Time `json:"lastReadyAt,omitempty"`
	LastError               string     `json:"lastError,omitempty"`
	Backend                 string     `json:"backend"`
	Listen                  string     `json:"listen"`
}

type gatewayPersistentState struct {
	BootDurationsSeconds []float64  `json:"bootDurationsSeconds"`
	LastReadyAt          *time.Time `json:"lastReadyAt,omitempty"`
}

type minecraftHandshake struct {
	Protocol  int
	Host      string
	Port      uint16
	NextState int
}

type minecraftGateway struct {
	serverID string
	server   *ServerConfig
	config   GatewayConfig
	listener net.Listener
	stateDir string

	mu             sync.Mutex
	state          string
	bootStartedAt  time.Time
	lastWakeAt     *time.Time
	lastReadyAt    *time.Time
	lastError      string
	bootDurations  []float64
	wakeAttempts   atomic.Uint64
	wakeTriggered  atomic.Uint64
	proxied        atomic.Uint64
	rejected       atomic.Uint64
	active         atomic.Int64
	startInFlight  bool
	readinessTimer time.Duration
	connectTimeout time.Duration
	wakeCooldown   time.Duration
	defaultBoot    time.Duration
	loginRateLimit time.Duration
	loginAttempts  map[string]time.Time
	serviceStatus  func(string) (bool, string)
	runAction      func(*ServerConfig, string) map[string]interface{}
	dialTimeout    func(string, string, time.Duration) (net.Conn, error)
}

type gameGatewayManager struct {
	mu       sync.RWMutex
	gateways map[string]*minecraftGateway
}

var runningGameGateways *gameGatewayManager

func validateGatewayConfig(serverID string, server ServerConfig) error {
	cfg := server.Gateway
	if cfg.Type == "" {
		return nil
	}
	if cfg.Type != "minecraft" || server.GameType != "minecraft" {
		return fmt.Errorf("server %q gateway type %q requires game_type minecraft", serverID, cfg.Type)
	}
	if cfg.Listen == "" || cfg.Backend == "" || cfg.Listen == cfg.Backend {
		return fmt.Errorf("server %q gateway requires distinct listen and backend addresses", serverID)
	}
	for name, value := range map[string]string{
		"readiness_timeout": cfg.ReadinessTimeout, "connect_timeout": cfg.ConnectTimeout,
		"wake_cooldown": cfg.WakeCooldown, "login_rate_limit": cfg.LoginRateLimit,
		"default_boot_estimate": cfg.DefaultBootEstimate,
	} {
		if value == "" {
			continue
		}
		duration, err := time.ParseDuration(value)
		if err != nil || duration <= 0 {
			return fmt.Errorf("server %q gateway %s must be a positive duration", serverID, name)
		}
	}
	return nil
}

func startGameGateways(cfg *AgentConfig) (*gameGatewayManager, error) {
	manager := &gameGatewayManager{gateways: map[string]*minecraftGateway{}}
	for serverID, configured := range cfg.Servers {
		if configured.Gateway.Type == "" {
			continue
		}
		server := configured
		gateway, err := newMinecraftGateway(serverID, &server, cfg.StateDir)
		if err != nil {
			manager.Close()
			return nil, fmt.Errorf("gateway %s: %w", serverID, err)
		}
		manager.gateways[serverID] = gateway
		gateway.start()
	}
	runningGameGateways = manager
	return manager, nil
}

func newMinecraftGateway(serverID string, server *ServerConfig, stateDir string) (*minecraftGateway, error) {
	listener, err := net.Listen("tcp", server.Gateway.Listen)
	if err != nil {
		return nil, err
	}
	gateway := &minecraftGateway{
		serverID: serverID, server: server, config: server.Gateway, listener: listener,
		stateDir: stateDir, state: "stopped",
		readinessTimer: durationOr(server.Gateway.ReadinessTimeout, 10*time.Minute),
		connectTimeout: durationOr(server.Gateway.ConnectTimeout, 2*time.Second),
		wakeCooldown:   durationOr(server.Gateway.WakeCooldown, 30*time.Second),
		defaultBoot:    durationOr(server.Gateway.DefaultBootEstimate, 2*time.Minute),
		loginRateLimit: durationOr(server.Gateway.LoginRateLimit, 5*time.Second),
		loginAttempts:  map[string]time.Time{},
		serviceStatus:  getServiceStatus,
		runAction:      executeAction,
		dialTimeout:    net.DialTimeout,
	}
	gateway.loadState()
	if gateway.backendReady() {
		gateway.state = "ready"
	} else if active, _ := gateway.serviceStatus(server.Unit); active {
		gateway.state = "starting"
		gateway.startInFlight = true
		gateway.bootStartedAt = time.Now()
		go gateway.awaitReadiness()
	}
	return gateway, nil
}

func durationOr(value string, fallback time.Duration) time.Duration {
	if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
		return parsed
	}
	return fallback
}

func (m *gameGatewayManager) Close() {
	if m == nil {
		return
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, gateway := range m.gateways {
		_ = gateway.listener.Close()
	}
}

func gatewayStatusFor(serverID string) *GatewayStatus {
	manager := runningGameGateways
	if manager == nil {
		return nil
	}
	manager.mu.RLock()
	gateway := manager.gateways[serverID]
	manager.mu.RUnlock()
	if gateway == nil {
		return nil
	}
	gateway.refreshState()
	status := gateway.status()
	return &status
}

func (g *minecraftGateway) refreshState() {
	if g.backendReady() {
		g.markReadyWithoutBoot()
		return
	}
	active, _ := g.serviceStatus(g.server.Unit)
	g.mu.Lock()
	if g.startInFlight {
		g.mu.Unlock()
		return
	}
	if !active {
		g.state = "stopped"
		g.mu.Unlock()
		return
	}
	g.state = "starting"
	g.startInFlight = true
	g.bootStartedAt = time.Now()
	g.mu.Unlock()
	go g.awaitReadiness()
}

func (g *minecraftGateway) start() {
	log.Printf("Minecraft gateway %s listening on %s -> %s", g.serverID, g.config.Listen, g.config.Backend)
	go func() {
		for {
			conn, err := g.listener.Accept()
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					log.Printf("Minecraft gateway %s accept: %v", g.serverID, err)
				}
				return
			}
			go g.handleConnection(conn)
		}
	}()
}

func (g *minecraftGateway) handleConnection(client net.Conn) {
	defer client.Close()
	// A live backend owns the complete port, not only the Minecraft protocol.
	// Mods such as AutoModpack multiplex an AMMH preface and TLS downloads on
	// the game listener. Dial before reading so those and future side protocols
	// remain byte-for-byte transparent.
	if backend, err := g.dialTimeout("tcp", g.config.Backend, g.connectTimeout); err == nil {
		g.markReadyWithoutBoot()
		g.proxy(client, backend, nil)
		return
	}
	g.markBackendUnavailable()

	_ = client.SetDeadline(time.Now().Add(8 * time.Second))
	handshakeBody, handshakeRaw, err := readMinecraftPacket(client)
	if err != nil {
		g.rejected.Add(1)
		return
	}
	handshake, err := parseMinecraftHandshake(handshakeBody)
	if err != nil {
		g.rejected.Add(1)
		return
	}

	if handshake.NextState == 1 {
		_ = writeMinecraftStatus(client, handshake.Protocol, g.statusDescription())
		return
	}
	if handshake.NextState != 2 {
		g.rejected.Add(1)
		return
	}

	loginBody, loginRaw, err := readMinecraftPacket(client)
	if err != nil {
		g.rejected.Add(1)
		return
	}
	username, err := parseMinecraftLoginUsername(loginBody)
	if err != nil {
		g.rejected.Add(1)
		return
	}
	// Close the small race where the backend became ready while the client sent
	// its login packet.
	if backend, err := g.dialTimeout("tcp", g.config.Backend, g.connectTimeout); err == nil {
		g.markReadyWithoutBoot()
		g.proxy(client, backend, append(handshakeRaw, loginRaw...))
		return
	}
	if !g.allowOfflineLogin(client.RemoteAddr()) {
		g.rejected.Add(1)
		_ = writeMinecraftDisconnect(client, "Please wait a few seconds before trying to wake the server again.")
		return
	}

	g.wakeAttempts.Add(1)
	estimate, wakeErr := g.requestWake(username)
	message := g.startingMessage(username, estimate)
	if wakeErr != nil {
		message = "The server could not be started automatically. Please try again later."
	}
	g.rejected.Add(1)
	_ = writeMinecraftDisconnect(client, message)
}

func (g *minecraftGateway) allowOfflineLogin(remote net.Addr) bool {
	host := remote.String()
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if previous, found := g.loginAttempts[host]; found && now.Sub(previous) < g.loginRateLimit {
		return false
	}
	g.loginAttempts[host] = now
	if len(g.loginAttempts) > 1024 {
		cutoff := now.Add(-2 * g.loginRateLimit)
		for candidate, seen := range g.loginAttempts {
			if seen.Before(cutoff) {
				delete(g.loginAttempts, candidate)
			}
		}
	}
	return true
}

func (g *minecraftGateway) proxy(client, backend net.Conn, initial []byte) {
	defer backend.Close()
	_ = client.SetDeadline(time.Time{})
	_ = backend.SetDeadline(time.Time{})
	if _, err := backend.Write(initial); err != nil {
		g.rejected.Add(1)
		return
	}
	g.proxied.Add(1)
	g.active.Add(1)
	defer g.active.Add(-1)
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(backend, client)
		if tcp, ok := backend.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, backend)
		if tcp, ok := client.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
}

func (g *minecraftGateway) requestWake(_ string) (time.Duration, error) {
	g.mu.Lock()
	estimate := g.estimatedBootLocked()
	if g.startInFlight {
		g.mu.Unlock()
		return estimate, nil
	}
	now := time.Now()
	if g.lastWakeAt != nil && now.Sub(*g.lastWakeAt) < g.wakeCooldown {
		g.mu.Unlock()
		return estimate, nil
	}
	g.startInFlight = true
	g.state = "starting"
	g.bootStartedAt = now
	g.lastWakeAt = &now
	g.lastError = ""
	g.wakeTriggered.Add(1)
	g.mu.Unlock()

	go func() {
		lock := serverOperationLock(g.server)
		lock.Lock()
		active, _ := g.serviceStatus(g.server.Unit)
		var result map[string]interface{}
		if active {
			result = map[string]interface{}{"success": true}
		} else {
			result = g.runAction(g.server, "start")
		}
		lock.Unlock()
		if success, _ := result["success"].(bool); !success {
			g.mu.Lock()
			g.state = "failed"
			g.startInFlight = false
			g.lastError, _ = result["message"].(string)
			g.mu.Unlock()
			return
		}
		g.awaitReadiness()
	}()
	return estimate, nil
}

func (g *minecraftGateway) awaitReadiness() {
	deadline := time.Now().Add(g.readinessTimer)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if g.backendReady() {
			now := time.Now()
			g.mu.Lock()
			duration := now.Sub(g.bootStartedAt)
			if !g.bootStartedAt.IsZero() && duration > 0 {
				g.bootDurations = append(g.bootDurations, duration.Seconds())
				if len(g.bootDurations) > maxBootSamples {
					g.bootDurations = g.bootDurations[len(g.bootDurations)-maxBootSamples:]
				}
			}
			g.state = "ready"
			g.startInFlight = false
			g.lastReadyAt = &now
			g.lastError = ""
			g.mu.Unlock()
			g.saveState()
			return
		}
		if time.Now().After(deadline) {
			g.mu.Lock()
			g.state = "failed"
			g.startInFlight = false
			g.lastError = "backend readiness timeout"
			g.mu.Unlock()
			return
		}
		<-ticker.C
	}
}

func (g *minecraftGateway) backendReady() bool {
	conn, err := g.dialTimeout("tcp", g.config.Backend, g.connectTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (g *minecraftGateway) markReadyWithoutBoot() {
	g.mu.Lock()
	g.state = "ready"
	g.lastError = ""
	g.mu.Unlock()
}

func (g *minecraftGateway) markBackendUnavailable() {
	active, _ := g.serviceStatus(g.server.Unit)
	g.mu.Lock()
	if !g.startInFlight {
		if active {
			g.state = "starting"
		} else {
			g.state = "stopped"
		}
	}
	g.mu.Unlock()
}

func (g *minecraftGateway) estimatedBootLocked() time.Duration {
	if len(g.bootDurations) == 0 {
		return g.defaultBoot
	}
	values := append([]float64(nil), g.bootDurations...)
	sort.Float64s(values)
	index := int(math.Ceil(float64(len(values))*0.75)) - 1
	if index < 0 {
		index = 0
	}
	return time.Duration(values[index] * float64(time.Second))
}

func (g *minecraftGateway) status() GatewayStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	status := GatewayStatus{
		Type: g.config.Type, State: g.state, Backend: g.config.Backend, Listen: g.config.Listen,
		EstimatedBootSeconds: int(math.Ceil(g.estimatedBootLocked().Seconds())),
		WakeAttempts:         g.wakeAttempts.Load(), WakeTriggered: g.wakeTriggered.Load(),
		ProxiedConnections: g.proxied.Load(), RejectedConnections: g.rejected.Load(),
		ActiveConnections: g.active.Load(), LastWakeAt: g.lastWakeAt,
		LastReadyAt: g.lastReadyAt, LastError: g.lastError,
	}
	if len(g.bootDurations) > 0 {
		status.LastBootDurationSeconds = int(math.Round(g.bootDurations[len(g.bootDurations)-1]))
	}
	return status
}

func (g *minecraftGateway) statusDescription() string {
	status := g.status()
	switch status.State {
	case "starting":
		return fmt.Sprintf("Server is starting · about %d minute(s)", max(1, int(math.Ceil(float64(status.EstimatedBootSeconds)/60))))
	case "failed":
		return "Server startup failed · try again later"
	default:
		return "Server sleeps when empty · connect to wake it"
	}
}

func (g *minecraftGateway) startingMessage(username string, estimate time.Duration) string {
	message := g.config.StartingMessage
	if message == "" {
		message = "The server is starting. Please reconnect in about {minutes} minute(s)."
	}
	minutes := max(1, int(math.Ceil(estimate.Minutes())))
	message = strings.ReplaceAll(message, "{minutes}", fmt.Sprint(minutes))
	message = strings.ReplaceAll(message, "{seconds}", fmt.Sprint(int(math.Ceil(estimate.Seconds()))))
	return strings.ReplaceAll(message, "{player}", username)
}

func (g *minecraftGateway) statePath() string {
	filename := "gateway-" + base64.RawURLEncoding.EncodeToString([]byte(g.serverID)) + ".json"
	return filepath.Join(g.stateDir, filename)
}

func (g *minecraftGateway) loadState() {
	raw, err := os.ReadFile(g.statePath())
	if err != nil {
		return
	}
	var state gatewayPersistentState
	if json.Unmarshal(raw, &state) == nil {
		g.bootDurations = state.BootDurationsSeconds
		if len(g.bootDurations) > maxBootSamples {
			g.bootDurations = g.bootDurations[len(g.bootDurations)-maxBootSamples:]
		}
		g.lastReadyAt = state.LastReadyAt
	}
}

func (g *minecraftGateway) saveState() {
	g.mu.Lock()
	state := gatewayPersistentState{
		BootDurationsSeconds: append([]float64(nil), g.bootDurations...),
		LastReadyAt:          g.lastReadyAt,
	}
	g.mu.Unlock()
	raw, err := json.Marshal(state)
	if err != nil {
		return
	}
	path := g.statePath()
	temp := path + ".tmp"
	if err := os.WriteFile(temp, append(raw, '\n'), 0600); err == nil {
		_ = os.Rename(temp, path)
	}
}

func readMinecraftPacket(reader io.Reader) ([]byte, []byte, error) {
	length, prefix, err := readMinecraftVarIntFromReader(reader)
	if err != nil {
		return nil, nil, err
	}
	if length <= 0 || length > maxMinecraftPacket {
		return nil, nil, fmt.Errorf("invalid Minecraft packet length %d", length)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(reader, body); err != nil {
		return nil, nil, err
	}
	return body, append(prefix, body...), nil
}

func readMinecraftVarIntFromReader(reader io.Reader) (int, []byte, error) {
	value := 0
	prefix := make([]byte, 0, 5)
	var one [1]byte
	for position := 0; position < 5; position++ {
		if _, err := io.ReadFull(reader, one[:]); err != nil {
			return 0, prefix, err
		}
		current := one[0]
		prefix = append(prefix, current)
		value |= int(current&0x7f) << (7 * position)
		if current&0x80 == 0 {
			return value, prefix, nil
		}
	}
	return 0, prefix, fmt.Errorf("Minecraft VarInt is too long")
}

func readMinecraftVarInt(reader io.ByteReader) (int, []byte, error) {
	value := 0
	prefix := make([]byte, 0, 5)
	for position := 0; position < 5; position++ {
		current, err := reader.ReadByte()
		if err != nil {
			return 0, prefix, err
		}
		prefix = append(prefix, current)
		value |= int(current&0x7f) << (7 * position)
		if current&0x80 == 0 {
			return value, prefix, nil
		}
	}
	return 0, prefix, fmt.Errorf("Minecraft VarInt is too long")
}

func appendMinecraftVarInt(target []byte, value int) []byte {
	for {
		current := byte(value & 0x7f)
		value >>= 7
		if value != 0 {
			current |= 0x80
		}
		target = append(target, current)
		if value == 0 {
			return target
		}
	}
}

func parseMinecraftHandshake(body []byte) (minecraftHandshake, error) {
	reader := bytes.NewReader(body)
	packetID, _, err := readMinecraftVarInt(reader)
	if err != nil || packetID != 0 {
		return minecraftHandshake{}, fmt.Errorf("not a Minecraft handshake")
	}
	protocol, _, err := readMinecraftVarInt(reader)
	if err != nil {
		return minecraftHandshake{}, err
	}
	host, err := readMinecraftString(reader, 255)
	if err != nil {
		return minecraftHandshake{}, err
	}
	var port uint16
	if err := binary.Read(reader, binary.BigEndian, &port); err != nil {
		return minecraftHandshake{}, err
	}
	nextState, _, err := readMinecraftVarInt(reader)
	if err != nil || (nextState != 1 && nextState != 2) {
		return minecraftHandshake{}, fmt.Errorf("invalid Minecraft next state")
	}
	return minecraftHandshake{Protocol: protocol, Host: host, Port: port, NextState: nextState}, nil
}

func parseMinecraftLoginUsername(body []byte) (string, error) {
	reader := bytes.NewReader(body)
	packetID, _, err := readMinecraftVarInt(reader)
	if err != nil || packetID != 0 {
		return "", fmt.Errorf("not a Minecraft login start packet")
	}
	username, err := readMinecraftString(reader, 16)
	if err != nil || len(username) < 3 {
		return "", fmt.Errorf("invalid Minecraft username")
	}
	for _, character := range username {
		if !(character == '_' || character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' || character >= '0' && character <= '9') {
			return "", fmt.Errorf("invalid Minecraft username")
		}
	}
	return username, nil
}

func readMinecraftString(reader *bytes.Reader, maximum int) (string, error) {
	length, _, err := readMinecraftVarInt(reader)
	if err != nil || length < 0 || length > maximum*4 || length > reader.Len() {
		return "", fmt.Errorf("invalid Minecraft string length")
	}
	value := make([]byte, length)
	if _, err := io.ReadFull(reader, value); err != nil {
		return "", err
	}
	return string(value), nil
}

func minecraftPacket(body []byte) []byte {
	return append(appendMinecraftVarInt(nil, len(body)), body...)
}

func minecraftString(value string) []byte {
	return append(appendMinecraftVarInt(nil, len(value)), value...)
}

func writeMinecraftDisconnect(writer io.Writer, message string) error {
	chat, _ := json.Marshal(map[string]string{"text": message})
	body := appendMinecraftVarInt(nil, 0)
	body = append(body, minecraftString(string(chat))...)
	_, err := writer.Write(minecraftPacket(body))
	return err
}

func writeMinecraftStatus(conn net.Conn, protocol int, description string) error {
	payload, _ := json.Marshal(map[string]interface{}{
		"version":     map[string]interface{}{"name": "HOGS on-demand", "protocol": protocol},
		"players":     map[string]int{"max": 0, "online": 0},
		"description": map[string]string{"text": description},
	})
	body := appendMinecraftVarInt(nil, 0)
	body = append(body, minecraftString(string(payload))...)
	if _, err := conn.Write(minecraftPacket(body)); err != nil {
		return err
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	ping, _, err := readMinecraftPacket(conn)
	if err != nil {
		return nil
	}
	reader := bytes.NewReader(ping)
	packetID, _, err := readMinecraftVarInt(reader)
	if err == nil && packetID == 1 && reader.Len() == 8 {
		response := appendMinecraftVarInt(nil, 1)
		response = append(response, ping[len(ping)-8:]...)
		_, _ = conn.Write(minecraftPacket(response))
	}
	return nil
}
