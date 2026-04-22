package api

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/tionis/hogs/agent"
	"github.com/tionis/hogs/auth"
)

// ConsoleHandler handles browser WebSocket connections for console streaming.
type ConsoleHandler struct {
	AgentHub *agent.Hub
	Auth     *auth.Authenticator
}

func NewConsoleHandler(hub *agent.Hub, auth *auth.Authenticator) *ConsoleHandler {
	return &ConsoleHandler{AgentHub: hub, Auth: auth}
}

var consoleUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func (h *ConsoleHandler) ServeWS(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	serverName := vars["serverName"]

	// Require authentication
	if h.Auth != nil && !h.Auth.IsAuthenticated(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := consoleUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Console WS upgrade failed: %v", err)
		return
	}
	defer h.AgentHub.RemoveConsoleClient(serverName, conn)

	// Subscribe to agent console for this server
	if err := h.AgentHub.SendConsoleSubscribe(serverName); err != nil {
		log.Printf("Console subscribe failed for %s: %v", serverName, err)
		// Continue anyway — agent may come online later
	}

	// Add client and send replay buffer
	h.AgentHub.AddConsoleClient(serverName, conn)

	// Read loop for browser input
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("Console WS read error for %s: %v", serverName, err)
			}
			break
		}

		var req struct {
			Input string `json:"input"`
		}
		if err := json.Unmarshal(message, &req); err != nil {
			continue
		}
		if req.Input != "" {
			if err := h.AgentHub.SendConsoleInput(serverName, req.Input); err != nil {
				log.Printf("Console input failed for %s: %v", serverName, err)
			}
		}
	}
}
