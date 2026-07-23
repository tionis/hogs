package api

import (
	"bufio"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/tionis/hogs/agent"
	"github.com/tionis/hogs/auth"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/engine"
)

type ConsoleHandler struct {
	Service *agent.AgentService
	Auth    *auth.Authenticator
	Store   *database.Store
	Engine  *engine.Engine
}

func NewConsoleHandler(service *agent.AgentService, authenticator *auth.Authenticator, store *database.Store, eng *engine.Engine) *ConsoleHandler {
	return &ConsoleHandler{Service: service, Auth: authenticator, Store: store, Engine: eng}
}

var consoleUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func (h *ConsoleHandler) ServeWS(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]
	if h.Auth != nil && !h.Auth.IsAuthenticated(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if _, _, status, err := authorizeManagedCapability(h.Store, h.Engine, h.Auth, r, serverName, managedConsole); err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	agentStream, err := h.Service.Console(r.Context(), serverName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer agentStream.Body.Close()
	conn, err := consoleUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	var writeMu sync.Mutex
	writeLine := func(line string) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(map[string]string{"type": "console", "line": line})
	}
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		scanner := bufio.NewScanner(agentStream.Body)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			var line struct {
				Line string `json:"line"`
			}
			if json.Unmarshal(scanner.Bytes(), &line) == nil && line.Line != "" {
				if writeLine(line.Line) != nil {
					return
				}
			}
		}
	}()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			break
		}
		var request struct {
			Input string `json:"input"`
		}
		if json.Unmarshal(message, &request) != nil || strings.TrimSpace(request.Input) == "" {
			continue
		}
		_ = writeLine("> " + request.Input)
		result, err := h.Service.SendCommandResult(serverName, request.Input)
		if err != nil {
			_ = writeLine("Error: " + err.Error())
			continue
		}
		if data, ok := result.Data.(map[string]interface{}); ok {
			if output, ok := data["output"].(string); ok {
				for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
					if line != "" {
						_ = writeLine(line)
					}
				}
			}
		}
		select {
		case <-streamDone:
			log.Printf("Agent console stream ended for %s", serverName)
			return
		default:
		}
	}
}
