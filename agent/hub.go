package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/query"
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

type CommandRequestData struct {
	ServerName string `json:"serverName"`
	Command    string `json:"command"`
}

type ActionRequestData struct {
	ServerName string `json:"serverName"`
	Action     string `json:"action"`
}

type ActionResultData struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

type StatusReportData struct {
	ServerName   string `json:"serverName"`
	Online       bool   `json:"online"`
	Players      int    `json:"players"`
	MaxPlayers   int    `json:"maxPlayers"`
	PlayersKnown bool   `json:"playersKnown"`
	Version      string `json:"version"`
}

type CommandResultData struct {
	Success bool   `json:"success"`
	Output  string `json:"output"`
}

type ConsoleLineData struct {
	ServerName string `json:"serverName"`
	Line       string `json:"line"`
	Timestamp  string `json:"timestamp"`
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

type BackupCreateRequestData struct {
	ServerName string   `json:"serverName"`
	Paths      []string `json:"paths"`
	Tags       []string `json:"tags"`
}

type BackupRestoreRequestData struct {
	ServerName string `json:"serverName"`
	Snapshot   string `json:"snapshot"`
	Target     string `json:"target"`
}

type BackupListRequestData struct {
	ServerName string `json:"serverName"`
}

type GenericResultData struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

type pendingRequest struct {
	ch      chan *GenericResultData
	agentID int
}

const consoleBufferSize = 500

type consoleLine struct {
	Line      string
	Timestamp string
}

type Notifier interface {
	Send(eventType, message string)
}

type Hub struct {
	Store    *database.Store
	Config   *config.Config
	Conns    map[int]*AgentConn
	mu       sync.RWMutex
	upgrader websocket.Upgrader

	pending   map[string]*pendingRequest
	pendingMu sync.Mutex
	nextReqID uint64

	consoleClients   map[string]map[*websocket.Conn]bool // serverName -> browser conns
	consoleClientsMu sync.RWMutex
	consoleBuffers   map[string][]consoleLine // serverName -> ring buffer
	consoleBuffersMu sync.RWMutex

	Notifier    Notifier
	StatusCache *query.ServerStatusCache
}

func (h *Hub) SetStatusCache(cache *query.ServerStatusCache) {
	h.StatusCache = cache
}

type AgentConn struct {
	AgentID      int
	NodeName     string
	ServerNames  []string
	Hub          *Hub
	Conn         *websocket.Conn
	Send         chan []byte
	Capabilities []string
}

func NewHub(store *database.Store, cfg *config.Config) *Hub {
	return &Hub{
		Store:          store,
		Config:         cfg,
		Conns:          make(map[int]*AgentConn),
		pending:        make(map[string]*pendingRequest),
		consoleClients: make(map[string]map[*websocket.Conn]bool),
		consoleBuffers: make(map[string][]consoleLine),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				if origin == "" {
					return true
				}
				// Allow same-origin connections only (HTTP/HTTPS only)
				return origin == "http://"+r.Host || origin == "https://"+r.Host
			},
		},
	}
}

func (h *Hub) SetNotifier(n Notifier) {
	h.Notifier = n
}

func (h *Hub) allocRequestID() string {
	h.pendingMu.Lock()
	defer h.pendingMu.Unlock()
	h.nextReqID++
	return strconv.FormatUint(h.nextReqID, 10)
}

func (h *Hub) registerPending(reqID string, agentID int, opType string, payload []byte) *pendingRequest {
	pr := &pendingRequest{ch: make(chan *GenericResultData, 1), agentID: agentID}
	h.pendingMu.Lock()
	h.pending[reqID] = pr
	h.pendingMu.Unlock()

	// Persist to DB for recovery
	if h.Store != nil {
		op := &database.AgentPendingOp{
			RequestID: reqID,
			AgentID:   agentID,
			OpType:    opType,
			// Pending operations are only recovered as failed after restart; their
			// arguments are never replayed. Persist no commands, file contents, repo
			// credentials, or other potentially sensitive request data.
			Payload:   "{}",
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
			ExpiresAt: time.Now().UTC().Add(5 * time.Minute).Format(time.RFC3339),
			Resolved:  false,
		}
		if err := h.Store.CreateAgentPendingOp(op); err != nil {
			log.Printf("Warning: failed to persist pending op %s: %v", reqID, err)
		}
	}
	return pr
}

func (h *Hub) resolvePending(reqID string, result *GenericResultData) {
	h.pendingMu.Lock()
	pr, ok := h.pending[reqID]
	if ok {
		delete(h.pending, reqID)
	}
	h.pendingMu.Unlock()
	if ok {
		pr.ch <- result
	}

	// Mark resolved in DB
	if h.Store != nil {
		if err := h.Store.ResolveAgentPendingOp(reqID); err != nil {
			log.Printf("Warning: failed to resolve pending op %s: %v", reqID, err)
		}
	}
}

func (h *Hub) LoadAndRecoverPendingOps() {
	if h.Store == nil {
		return
	}

	ops, err := h.Store.ListAllPendingOps()
	if err != nil {
		log.Printf("Warning: failed to load pending ops: %v", err)
		return
	}

	if len(ops) == 0 {
		return
	}

	log.Printf("Found %d unresolved pending ops from previous session", len(ops))
	for _, op := range ops {
		// These ops were interrupted by a restart; we cannot resume them
		// because the original caller/channel is gone. Mark them as resolved
		// with a failure status so they don't linger forever.
		if err := h.Store.ResolveAgentPendingOp(op.RequestID); err != nil {
			log.Printf("Warning: failed to resolve stale pending op %s: %v", op.RequestID, err)
		}
		if h.Notifier != nil {
			h.Notifier.Send("agent_disconnect", fmt.Sprintf("Pending operation %s for agent %d was lost due to server restart", op.OpType, op.AgentID))
		}
	}

	// Clean up old resolved ops
	if err := h.Store.CleanupExpiredPendingOps(); err != nil {
		log.Printf("Warning: failed to cleanup expired pending ops: %v", err)
	}
}

func (h *Hub) StartPendingOpsCleanup() {
	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			if h.Store != nil {
				if err := h.Store.CleanupExpiredPendingOps(); err != nil {
					log.Printf("Warning: pending ops cleanup failed: %v", err)
				}
			}
		}
	}()
}

func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("Authorization")
	if token == "" {
		http.Error(w, "missing authorization header", http.StatusUnauthorized)
		return
	}
	const bearerPrefix = "Bearer "
	if !strings.HasPrefix(token, bearerPrefix) {
		http.Error(w, "invalid authorization format", http.StatusUnauthorized)
		return
	}
	token = strings.TrimPrefix(token, bearerPrefix)

	agent, err := h.Store.GetAgentByToken(token)
	if err != nil || agent == nil {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Agent WS upgrade failed: %v", err)
		return
	}

	ac := &AgentConn{
		AgentID:  agent.ID,
		NodeName: agent.NodeName,
		Hub:      h,
		Conn:     conn,
		Send:     make(chan []byte, 256),
	}

	h.mu.Lock()
	h.Conns[agent.ID] = ac
	h.mu.Unlock()

	h.Store.UpdateAgentOnline(agent.ID, true)
	log.Printf("Agent %q (id=%d, node=%s) connected", agent.Name, agent.ID, agent.NodeName)
	if h.Notifier != nil {
		h.Notifier.Send("agent_connect", fmt.Sprintf("Agent %s (node=%s) connected", agent.Name, agent.NodeName))
	}

	go ac.writePump()
	ac.readPump()
}

func (h *Hub) GetConn(agentID int) *AgentConn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.Conns[agentID]
}

func (h *Hub) GetConnByNode(nodeName string) *AgentConn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, ac := range h.Conns {
		if ac.NodeName == nodeName {
			return ac
		}
	}
	return nil
}

func (h *Hub) GetConnByServerName(serverName string) *AgentConn {
	server, err := h.Store.GetServerByName(serverName)
	if err != nil || server == nil {
		return nil
	}
	link, err := h.Store.GetPterodactylLink(server.ID)
	if err != nil || link == nil || link.Node == "" {
		return nil
	}
	agent, err := h.Store.GetAgentByNodeName(link.Node)
	if err != nil || agent == nil {
		return nil
	}
	return h.GetConn(agent.ID)
}

func (h *Hub) AddConsoleClient(serverName string, conn *websocket.Conn) {
	if conn == nil {
		return
	}
	h.consoleClientsMu.Lock()
	if h.consoleClients[serverName] == nil {
		h.consoleClients[serverName] = make(map[*websocket.Conn]bool)
	}
	h.consoleClients[serverName][conn] = true
	h.consoleClientsMu.Unlock()

	// Send buffered lines as replay
	h.consoleBuffersMu.RLock()
	lines := make([]consoleLine, len(h.consoleBuffers[serverName]))
	copy(lines, h.consoleBuffers[serverName])
	h.consoleBuffersMu.RUnlock()

	for _, line := range lines {
		msg, _ := json.Marshal(map[string]string{"type": "console", "line": line.Line, "timestamp": line.Timestamp})
		conn.WriteMessage(websocket.TextMessage, msg)
	}
}

func (h *Hub) RemoveConsoleClient(serverName string, conn *websocket.Conn) {
	h.consoleClientsMu.Lock()
	if clients := h.consoleClients[serverName]; clients != nil {
		delete(clients, conn)
		if len(clients) == 0 {
			delete(h.consoleClients, serverName)
		}
	}
	h.consoleClientsMu.Unlock()
	if conn != nil {
		conn.Close()
	}
}

func (h *Hub) broadcastConsole(serverName string, line consoleLine) {
	// Add to buffer
	h.consoleBuffersMu.Lock()
	buf := h.consoleBuffers[serverName]
	buf = append(buf, line)
	if len(buf) > consoleBufferSize {
		buf = buf[len(buf)-consoleBufferSize:]
	}
	h.consoleBuffers[serverName] = buf
	h.consoleBuffersMu.Unlock()

	// Broadcast to clients
	msg, _ := json.Marshal(map[string]string{"type": "console", "line": line.Line, "timestamp": line.Timestamp})
	h.consoleClientsMu.RLock()
	clients := h.consoleClients[serverName]
	for conn := range clients {
		conn.WriteMessage(websocket.TextMessage, msg)
	}
	h.consoleClientsMu.RUnlock()
}

func (h *Hub) SendConsoleInput(serverName, input string) error {
	ac := h.GetConnByServerName(serverName)
	if ac == nil {
		return fmt.Errorf("agent for server %s is offline", serverName)
	}
	payload, _ := json.Marshal(map[string]string{"serverName": serverName, "input": input})
	env := Envelope{Type: "console_input", Data: payload}
	msg, _ := json.Marshal(env)
	select {
	case ac.Send <- msg:
		return nil
	default:
		return fmt.Errorf("agent send buffer full")
	}
}

func (h *Hub) SendConsoleSubscribe(serverName string) error {
	ac := h.GetConnByServerName(serverName)
	if ac == nil {
		return fmt.Errorf("agent for server %s is offline", serverName)
	}
	payload, _ := json.Marshal(map[string]string{"serverName": serverName})
	env := Envelope{Type: "console_subscribe", Data: payload}
	msg, _ := json.Marshal(env)
	select {
	case ac.Send <- msg:
		return nil
	default:
		return fmt.Errorf("agent send buffer full")
	}
}

func (h *Hub) RemoveConn(agentID int) {
	h.mu.Lock()
	if ac, ok := h.Conns[agentID]; ok {
		close(ac.Send)
		delete(h.Conns, agentID)
	}
	h.mu.Unlock()

	h.pendingMu.Lock()
	for reqID, pr := range h.pending {
		if pr.agentID == agentID {
			pr.ch <- &GenericResultData{Success: false, Error: "agent disconnected"}
			delete(h.pending, reqID)
		}
	}
	h.pendingMu.Unlock()

	agent, _ := h.Store.GetAgent(agentID)
	if agent != nil && h.Notifier != nil {
		h.Notifier.Send("agent_disconnect", fmt.Sprintf("Agent %s (node=%s) disconnected", agent.Name, agent.NodeName))
	}
	h.Store.UpdateAgentOnline(agentID, false)
}

// ReconcileCredentials immediately closes connections authenticated by a
// rotated or revoked token. A revoked token has an empty stored hash.
func (h *Hub) ReconcileCredentials(changes []string) error {
	rotated := make(map[string]bool, len(changes))
	for _, name := range changes {
		rotated[name] = true
	}
	h.mu.RLock()
	ids := make([]int, 0, len(h.Conns))
	for id := range h.Conns {
		ids = append(ids, id)
	}
	h.mu.RUnlock()
	for _, id := range ids {
		var name, tokenHash string
		if err := h.Store.DB.QueryRow("SELECT name, token_hash FROM agents WHERE id=?", id).Scan(&name, &tokenHash); err != nil {
			if err == sql.ErrNoRows {
				h.RemoveConn(id)
				continue
			}
			return err
		}
		if tokenHash == "" || rotated[name] {
			h.RemoveConn(id)
		}
	}
	return nil
}

func (h *Hub) sendEnvelopeWithResult(ctx context.Context, agentID int, msgType string, data interface{}) (*GenericResultData, error) {
	ac := h.GetConn(agentID)
	if ac == nil {
		return nil, fmt.Errorf("agent offline")
	}

	reqID := h.allocRequestID()
	payload, _ := json.Marshal(data)
	env := Envelope{Type: msgType, RequestID: reqID, Data: payload}
	msg, _ := json.Marshal(env)

	pr := h.registerPending(reqID, agentID, msgType, payload)

	select {
	case ac.Send <- msg:
	default:
		h.pendingMu.Lock()
		delete(h.pending, reqID)
		h.pendingMu.Unlock()
		return nil, fmt.Errorf("agent send buffer full")
	}

	select {
	case result := <-pr.ch:
		if !result.Success {
			return result, fmt.Errorf("%s", result.Error)
		}
		return result, nil
	case <-ctx.Done():
		h.pendingMu.Lock()
		delete(h.pending, reqID)
		h.pendingMu.Unlock()
		return nil, ctx.Err()
	}
}

func (h *Hub) SendCommand(ctx context.Context, agentID int, serverName, command string) (*GenericResultData, error) {
	return h.sendEnvelopeWithResult(ctx, agentID, "command", CommandRequestData{ServerName: serverName, Command: command})
}

func (h *Hub) SendAction(ctx context.Context, agentID int, serverName, action string) (*GenericResultData, error) {
	return h.sendEnvelopeWithResult(ctx, agentID, "action", ActionRequestData{ServerName: serverName, Action: action})
}

func (h *Hub) SendFileList(ctx context.Context, agentID int, serverName, path string) (*GenericResultData, error) {
	return h.sendEnvelopeWithResult(ctx, agentID, "file_list", FileListRequestData{ServerName: serverName, Path: path})
}

func (h *Hub) SendFileRead(ctx context.Context, agentID int, serverName, path string) (*GenericResultData, error) {
	return h.sendEnvelopeWithResult(ctx, agentID, "file_read", FileReadRequestData{ServerName: serverName, Path: path})
}

func (h *Hub) SendFileWrite(ctx context.Context, agentID int, serverName, path, content string) (*GenericResultData, error) {
	return h.sendEnvelopeWithResult(ctx, agentID, "file_write", FileWriteRequestData{ServerName: serverName, Path: path, Content: content})
}

func (h *Hub) SendFileDelete(ctx context.Context, agentID int, serverName, path string) (*GenericResultData, error) {
	return h.sendEnvelopeWithResult(ctx, agentID, "file_delete", FileDeleteRequestData{ServerName: serverName, Path: path})
}

func (h *Hub) SendMkdir(ctx context.Context, agentID int, serverName, path string) (*GenericResultData, error) {
	return h.sendEnvelopeWithResult(ctx, agentID, "mkdir", MkdirRequestData{ServerName: serverName, Path: path})
}

func (h *Hub) SendBackupCreate(ctx context.Context, agentID int, serverName string, paths, tags []string) (*GenericResultData, error) {
	return h.sendEnvelopeWithResult(ctx, agentID, "backup_create", BackupCreateRequestData{ServerName: serverName, Paths: paths, Tags: tags})
}

func (h *Hub) SendBackupRestore(ctx context.Context, agentID int, serverName, snapshot, target string) (*GenericResultData, error) {
	return h.sendEnvelopeWithResult(ctx, agentID, "backup_restore", BackupRestoreRequestData{ServerName: serverName, Snapshot: snapshot, Target: target})
}

func (h *Hub) SendBackupList(ctx context.Context, agentID int, serverName string) (*GenericResultData, error) {
	return h.sendEnvelopeWithResult(ctx, agentID, "backup_list", BackupListRequestData{ServerName: serverName})
}

var resultTypes = map[string]string{
	"action_result":         "action_result",
	"command_result":        "command_result",
	"file_list_result":      "file_list_result",
	"file_read_result":      "file_read_result",
	"file_write_result":     "file_write_result",
	"file_delete_result":    "file_delete_result",
	"mkdir_result":          "mkdir_result",
	"backup_create_result":  "backup_create_result",
	"backup_restore_result": "backup_restore_result",
	"backup_list_result":    "backup_list_result",
	"backup_init_result":    "backup_init_result",
}

func isResultType(t string) bool {
	_, ok := resultTypes[t]
	return ok
}

func (ac *AgentConn) readPump() {
	defer func() {
		ac.Conn.Close()
		ac.Hub.RemoveConn(ac.AgentID)
	}()

	ac.Conn.SetReadLimit(65536)
	ac.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	ac.Conn.SetPongHandler(func(string) error {
		ac.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, message, err := ac.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("Agent %d read error: %v", ac.AgentID, err)
			}
			break
		}

		var env Envelope
		if err := json.Unmarshal(message, &env); err != nil {
			log.Printf("Agent %d invalid message: %v", ac.AgentID, err)
			continue
		}

		if env.RequestID != "" && isResultType(env.Type) {
			var result GenericResultData
			if err := json.Unmarshal(env.Data, &result); err != nil {
				result = GenericResultData{Success: false, Error: err.Error()}
			}
			ac.Hub.resolvePending(env.RequestID, &result)
			continue
		}

		switch env.Type {
		case "register":
			var reg RegisterData
			if err := json.Unmarshal(env.Data, &reg); err != nil {
				log.Printf("Agent %d invalid register data: %v", ac.AgentID, err)
				continue
			}
			record, err := ac.Hub.Store.GetAgent(ac.AgentID)
			if err != nil || record == nil || reg.NodeName != record.NodeName {
				log.Printf("Agent %d registration node mismatch", ac.AgentID)
				return
			}
			authorized := make([]string, 0, len(reg.Servers))
			for _, name := range reg.Servers {
				if ac.Hub.agentAssignedServer(ac.AgentID, name) {
					authorized = append(authorized, name)
				}
			}
			ac.NodeName = reg.NodeName
			ac.ServerNames = authorized
			ac.Capabilities = reg.Capabilities
			caps, _ := json.Marshal(reg.Capabilities)
			ac.Hub.Store.UpdateAgentCapabilities(ac.AgentID, caps)
			log.Printf("Agent %d registered: node=%s servers=%v caps=%v", ac.AgentID, reg.NodeName, authorized, reg.Capabilities)

		case "status":
			var status StatusReportData
			if err := json.Unmarshal(env.Data, &status); err != nil {
				continue
			}
			log.Printf("Agent %d status: online=%v players=%d/%d", ac.AgentID, status.Online, status.Players, status.MaxPlayers)
			// Agent reachability and game-process status are separate observations.
			// A status report proves the agent is reachable even when the game is off.
			ac.Hub.Store.UpdateAgentOnline(ac.AgentID, true)
			metricName := status.ServerName
			if !ac.Hub.agentAssignedServer(ac.AgentID, metricName) {
				log.Printf("Agent %d reported unauthorized server %q", ac.AgentID, metricName)
				continue
			}
			if metricName != "" {
				if ac.Hub.StatusCache != nil {
					ac.Hub.StatusCache.Set(metricName, &query.ServerStatus{
						Online: status.Online, Players: status.Players, MaxPlayers: status.MaxPlayers,
						PlayersKnown: status.PlayersKnown, Version: status.Version,
					})
				}
				metric := &database.ServerMetric{
					ServerName: metricName,
					AgentID:    ac.AgentID,
					Timestamp:  time.Now().UTC().Format(time.RFC3339),
					Online:     status.Online,
					Players:    status.Players,
					MaxPlayers: status.MaxPlayers,
					Version:    status.Version,
				}
				if err := ac.Hub.Store.CreateServerMetric(metric); err != nil {
					log.Printf("Warning: failed to store metric for agent %d: %v", ac.AgentID, err)
				}
			}

		case "action_result":
			var result ActionResultData
			if err := json.Unmarshal(env.Data, &result); err != nil {
				continue
			}
			log.Printf("Agent %d action result: success=%v msg=%s", ac.AgentID, result.Success, result.Message)

		case "command_result":
			var result CommandResultData
			if err := json.Unmarshal(env.Data, &result); err != nil {
				continue
			}
			log.Printf("Agent %d command result: success=%v", ac.AgentID, result.Success)

		case "console":
			var line ConsoleLineData
			if err := json.Unmarshal(env.Data, &line); err != nil {
				continue
			}
			log.Printf("Agent %d console: %s", ac.AgentID, line.Line)
			if line.ServerName != "" {
				ac.Hub.broadcastConsole(line.ServerName, consoleLine{Line: line.Line, Timestamp: line.Timestamp})
			}

		case "file_list_result":
			var result GenericResultData
			if err := json.Unmarshal(env.Data, &result); err != nil {
				continue
			}
			log.Printf("Agent %d file list: success=%v", ac.AgentID, result.Success)

		case "file_read_result":
			var result GenericResultData
			if err := json.Unmarshal(env.Data, &result); err != nil {
				continue
			}
			log.Printf("Agent %d file read: success=%v", ac.AgentID, result.Success)

		case "file_write_result":
			var result GenericResultData
			if err := json.Unmarshal(env.Data, &result); err != nil {
				continue
			}
			log.Printf("Agent %d file write: success=%v", ac.AgentID, result.Success)

		case "file_delete_result":
			var result GenericResultData
			if err := json.Unmarshal(env.Data, &result); err != nil {
				continue
			}
			log.Printf("Agent %d file delete: success=%v", ac.AgentID, result.Success)

		case "mkdir_result":
			var result GenericResultData
			if err := json.Unmarshal(env.Data, &result); err != nil {
				continue
			}
			log.Printf("Agent %d mkdir: success=%v", ac.AgentID, result.Success)

		case "backup_create_result":
			var result GenericResultData
			if err := json.Unmarshal(env.Data, &result); err != nil {
				continue
			}
			log.Printf("Agent %d backup create: success=%v", ac.AgentID, result.Success)

		case "backup_restore_result":
			var result GenericResultData
			if err := json.Unmarshal(env.Data, &result); err != nil {
				continue
			}
			log.Printf("Agent %d backup restore: success=%v", ac.AgentID, result.Success)

		case "backup_list_result":
			var result GenericResultData
			if err := json.Unmarshal(env.Data, &result); err != nil {
				continue
			}
			log.Printf("Agent %d backup list: success=%v", ac.AgentID, result.Success)
		}
	}
}

func (h *Hub) agentAssignedServer(agentID int, serverName string) bool {
	var count int
	err := h.Store.DB.QueryRow(`SELECT COUNT(*) FROM servers s
		JOIN pterodactyl_servers p ON p.server_id=s.id
		JOIN agents a ON a.node_name=p.node
		WHERE a.id=? AND s.name=?`, agentID, serverName).Scan(&count)
	return err == nil && count == 1
}

func (ac *AgentConn) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		ac.Conn.Close()
	}()

	for {
		select {
		case message, ok := <-ac.Send:
			ac.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				ac.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := ac.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}

		case <-ticker.C:
			ac.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := ac.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
