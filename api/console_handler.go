package api

import (
	"bufio"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
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
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		parsed, err := url.Parse(origin)
		return err == nil && strings.EqualFold(parsed.Host, r.Host)
	},
}

func (h *ConsoleHandler) ServeWS(w http.ResponseWriter, r *http.Request) {
	serverName := mux.Vars(r)["serverName"]
	if h.Auth != nil && !h.Auth.IsAuthenticated(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	server, _, status, err := authorizeManagedCapability(h.Store, h.Engine, h.Auth, r, serverName, managedConsoleRead)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	_, _, _, writeErr := authorizeManagedCapability(h.Store, h.Engine, h.Auth, r, serverName, managedConsoleWrite)
	history, _ := h.Store.ListConsoleHistory(server.ID, 500)
	agentStream, streamErr := h.Service.Console(r.Context(), serverName)
	conn, err := consoleUpgrader.Upgrade(w, r, nil)
	if err != nil {
		if agentStream != nil {
			agentStream.Body.Close()
		}
		return
	}
	defer conn.Close()
	if agentStream != nil {
		defer agentStream.Body.Close()
	}

	var writeMu sync.Mutex
	writeLine := func(stream, line string, persist bool) error {
		if persist {
			_ = h.Store.AppendConsoleHistory(server.ID, stream, line, 500)
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(map[string]string{"type": "console", "stream": stream, "line": line})
	}
	for _, entry := range history {
		if writeLine(entry.Stream, entry.Line, false) != nil {
			return
		}
	}
	if streamErr != nil {
		_ = writeLine("error", "Live console unavailable: "+streamErr.Error(), false)
		return
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
				if writeLine("server", line.Line, true) != nil {
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
		if writeErr != nil {
			_ = writeLine("error", "You have read-only console access.", false)
			continue
		}
		_ = writeLine("command", "> "+request.Input, true)
		result, err := h.Service.SendCommandResult(serverName, request.Input)
		if err != nil {
			_ = writeLine("error", "Error: "+err.Error(), true)
			continue
		}
		if data, ok := result.Data.(map[string]interface{}); ok {
			if output, ok := data["output"].(string); ok {
				for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
					if line != "" {
						_ = writeLine("response", line, true)
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
