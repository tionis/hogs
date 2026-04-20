package agent

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"
)

type Envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type RegisterData struct {
	NodeName     string   `json:"nodeName"`
	Capabilities []string `json:"capabilities"`
	ServerName   string   `json:"serverName"`
	GameType     string   `json:"gameType"`
}

type CommandRequestData struct {
	Command string `json:"command"`
}

type ActionRequestData struct {
	Action string `json:"action"`
}

type ActionResultData struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

type StatusReportData struct {
	Online     bool   `json:"online"`
	Players    int    `json:"players"`
	MaxPlayers int    `json:"maxPlayers"`
	Version    string `json:"version"`
}

type CommandResultData struct {
	Success bool   `json:"success"`
	Output  string `json:"output"`
}

type ConsoleLineData struct {
	Line      string `json:"line"`
	Timestamp string `json:"timestamp"`
}

type Hub struct {
	Store    *database.Store
	Config   *config.Config
	Conns    map[int]*AgentConn
	mu       sync.RWMutex
	upgrader websocket.Upgrader
}

type AgentConn struct {
	AgentID      int
	NodeName     string
	Hub          *Hub
	Conn         *websocket.Conn
	Send         chan []byte
	Capabilities []string
}

func NewHub(store *database.Store, cfg *config.Config) *Hub {
	return &Hub{
		Store:  store,
		Config: cfg,
		Conns:  make(map[int]*AgentConn),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "missing token", http.StatusUnauthorized)
		return
	}

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

func (h *Hub) RemoveConn(agentID int) {
	h.mu.Lock()
	if ac, ok := h.Conns[agentID]; ok {
		close(ac.Send)
		delete(h.Conns, agentID)
	}
	h.mu.Unlock()
	h.Store.UpdateAgentOnline(agentID, false)
}

func (h *Hub) SendCommand(agentID int, command string) (bool, string) {
	ac := h.GetConn(agentID)
	if ac == nil {
		return false, "agent offline"
	}

	data, _ := json.Marshal(CommandRequestData{Command: command})
	env := Envelope{Type: "command", Data: data}
	msg, _ := json.Marshal(env)

	select {
	case ac.Send <- msg:
	default:
		return false, "agent send buffer full"
	}

	return true, "command sent"
}

func (h *Hub) SendAction(agentID int, action string) (bool, string) {
	ac := h.GetConn(agentID)
	if ac == nil {
		return false, "agent offline"
	}

	data, _ := json.Marshal(ActionRequestData{Action: action})
	env := Envelope{Type: "action", Data: data}
	msg, _ := json.Marshal(env)

	select {
	case ac.Send <- msg:
	default:
		return false, "agent send buffer full"
	}

	return true, "action sent"
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

		switch env.Type {
		case "register":
			var reg RegisterData
			if err := json.Unmarshal(env.Data, &reg); err != nil {
				log.Printf("Agent %d invalid register data: %v", ac.AgentID, err)
				continue
			}
			ac.NodeName = reg.NodeName
			ac.Capabilities = reg.Capabilities
			caps, _ := json.Marshal(reg.Capabilities)
			ac.Hub.Store.UpdateAgentCapabilities(ac.AgentID, caps)
			log.Printf("Agent %d registered: node=%s caps=%v", ac.AgentID, reg.NodeName, reg.Capabilities)

		case "status":
			var status StatusReportData
			if err := json.Unmarshal(env.Data, &status); err != nil {
				continue
			}
			log.Printf("Agent %d status: online=%v players=%d/%d", ac.AgentID, status.Online, status.Players, status.MaxPlayers)

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
		}
	}
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
