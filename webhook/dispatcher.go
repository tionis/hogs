package webhook

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/tionis/hogs/database"
)

type Event struct {
	Type      string                 `json:"type"`
	Server    string                 `json:"server,omitempty"`
	Action    string                 `json:"action,omitempty"`
	Data      map[string]interface{} `json:"data,omitempty"`
	Timestamp string                 `json:"timestamp"`
}

type Dispatcher struct {
	Store  *database.Store
	Client *http.Client
}

func NewDispatcher(store *database.Store) *Dispatcher {
	return &Dispatcher{
		Store:  store,
		Client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (d *Dispatcher) Send(event *Event) {
	webhooks, err := d.Store.ListWebhooks()
	if err != nil {
		log.Printf("webhook: failed to list webhooks: %v", err)
		return
	}

	payload, err := json.Marshal(event)
	if err != nil {
		log.Printf("webhook: failed to marshal event: %v", err)
		return
	}

	for _, w := range webhooks {
		if !w.Enabled {
			continue
		}
		if !matchesEvent(w.Events, event.Type) {
			continue
		}
		go d.sendOne(w, payload)
	}
}

func matchesEvent(events json.RawMessage, eventType string) bool {
	if len(events) == 0 || string(events) == "[]" {
		return true
	}
	var eventList []string
	if err := json.Unmarshal(events, &eventList); err != nil {
		return true
	}
	for _, e := range eventList {
		if e == eventType || e == "*" {
			return true
		}
	}
	return false
}

func (d *Dispatcher) sendOne(w database.Webhook, payload []byte) {
	signature := computeSignature(w.Secret, payload)

	req, err := http.NewRequest(http.MethodPost, w.URL, bytes.NewReader(payload))
	if err != nil {
		log.Printf("webhook: failed to create request for %q: %v", w.Name, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Signature", signature)
	req.Header.Set("X-Webhook-Event", "")

	resp, err := d.Client.Do(req)
	if err != nil {
		log.Printf("webhook: failed to send to %q (%s): %v", w.Name, w.URL, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("webhook: %q returned status %d: %s", w.Name, resp.StatusCode, string(body))
		return
	}

	log.Printf("webhook: sent %q to %q (status %d)", w.Name, w.URL, resp.StatusCode)
}

func computeSignature(secret string, payload []byte) string {
	if secret == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func ServerEvent(serverName, action string, data map[string]interface{}) *Event {
	return &Event{
		Type:      fmt.Sprintf("server.%s", action),
		Server:    serverName,
		Action:    action,
		Data:      data,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
}

func CronEvent(jobName, serverName, result string) *Event {
	return &Event{
		Type:      "cron.complete",
		Server:    serverName,
		Action:    "cron",
		Data:      map[string]interface{}{"job": jobName, "result": result},
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
}

func AgentEvent(agentName, event string) *Event {
	return &Event{
		Type:      fmt.Sprintf("agent.%s", event),
		Action:    event,
		Data:      map[string]interface{}{"agent": agentName},
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
}
