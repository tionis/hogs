package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"time"

	"github.com/gorilla/websocket"
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

type ActionRequestData struct {
	Action string `json:"action"`
}

type CommandRequestData struct {
	Command string `json:"command"`
}

type StatusReportData struct {
	Online     bool   `json:"online"`
	Players    int    `json:"players"`
	MaxPlayers int    `json:"maxPlayers"`
	Version    string `json:"version"`
}

var (
	serverURL  string
	agentToken string
	nodeName   string
	serverName string
	gameType   string
)

func main() {
	serverURL = envOr("HOGS_SERVER_URL", "ws://localhost:8080/agent/ws")
	agentToken = envOr("HOGS_AGENT_TOKEN", "")
	nodeName = envOr("HOGS_AGENT_NODE", "default")
	serverName = envOr("HOGS_AGENT_SERVER_NAME", "")
	gameType = envOr("HOGS_AGENT_GAME_TYPE", "minecraft")

	if agentToken == "" {
		log.Fatal("HOGS_AGENT_TOKEN is required")
	}

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

func connectAndServe(interrupt chan os.Signal) error {
	u, err := url.Parse(serverURL)
	if err != nil {
		return fmt.Errorf("invalid server URL: %w", err)
	}
	q := u.Query()
	q.Set("token", agentToken)
	u.RawQuery = q.Encode()

	log.Printf("Connecting to %s...", u.String())
	c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return fmt.Errorf("dial failed: %w", err)
	}
	defer c.Close()

	register := Envelope{
		Type: "register",
		Data: mustMarshal(RegisterData{
			NodeName:     nodeName,
			Capabilities: []string{"start", "stop", "restart", "command", "status"},
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
		resp := Envelope{
			Type: "action_result",
			Data: mustMarshal(result),
		}
		c.WriteJSON(resp)

	case "command":
		var data CommandRequestData
		json.Unmarshal(env.Data, &data)
		log.Printf("Received command: %s", data.Command)
		output, err := executeCommand(data.Command)
		resp := Envelope{
			Type: "command_result",
			Data: mustMarshal(map[string]interface{}{
				"success": err == nil,
				"output":  output,
			}),
		}
		c.WriteJSON(resp)

	default:
		log.Printf("Unknown message type: %s", env.Type)
	}
}

func executeAction(action string) map[string]interface{} {
	log.Printf("Executing action: %s (stub - implement process management)", action)
	return map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Action %s executed (stub)", action),
	}
}

func executeCommand(command string) (string, error) {
	log.Printf("Executing command: %s (stub - implement process management)", command)
	return fmt.Sprintf("Command %s executed (stub)", command), nil
}

func reportStatus(c *websocket.Conn) {
	status := StatusReportData{
		Online:     true,
		Players:    0,
		MaxPlayers: 0,
		Version:    "0.0.0",
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
