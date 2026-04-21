package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/webhook"
)

type WebhookHandler struct {
	Store      *database.Store
	Dispatcher *webhook.Dispatcher
}

func NewWebhookHandler(store *database.Store, dispatcher *webhook.Dispatcher) *WebhookHandler {
	return &WebhookHandler{Store: store, Dispatcher: dispatcher}
}

func (h *WebhookHandler) ListWebhooks(w http.ResponseWriter, r *http.Request) {
	webhooks, err := h.Store.ListWebhooks()
	if err != nil {
		http.Error(w, "Failed to list webhooks", http.StatusInternalServerError)
		return
	}
	if webhooks == nil {
		webhooks = []database.Webhook{}
	}

	type webhookPublic struct {
		ID        int             `json:"id"`
		Name      string          `json:"name"`
		URL       string          `json:"url"`
		Events    json.RawMessage `json:"events"`
		Enabled   bool            `json:"enabled"`
		CreatedAt string          `json:"createdAt"`
	}

	var result []webhookPublic
	for _, wh := range webhooks {
		result = append(result, webhookPublic{
			ID:        wh.ID,
			Name:      wh.Name,
			URL:       wh.URL,
			Events:    wh.Events,
			Enabled:   wh.Enabled,
			CreatedAt: wh.CreatedAt,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *WebhookHandler) CreateWebhook(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	url := r.FormValue("url")
	if name == "" || url == "" {
		http.Error(w, "name and url are required", http.StatusBadRequest)
		return
	}

	secret := r.FormValue("secret")
	events := json.RawMessage(r.FormValue("events"))
	if len(events) == 0 {
		events = json.RawMessage("[]")
	}

	wh := &database.Webhook{
		Name:    name,
		URL:     url,
		Secret:  secret,
		Events:  events,
		Enabled: true,
	}

	if err := h.Store.CreateWebhook(wh); err != nil {
		http.Error(w, "Failed to create webhook: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":        wh.ID,
		"name":      wh.Name,
		"url":       wh.URL,
		"events":    wh.Events,
		"enabled":   wh.Enabled,
		"createdAt": wh.CreatedAt,
	})
}

func (h *WebhookHandler) DeleteWebhook(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid webhook ID", http.StatusBadRequest)
		return
	}

	if err := h.Store.DeleteWebhook(id); err != nil {
		http.Error(w, "Failed to delete webhook", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (h *WebhookHandler) TestWebhook(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.URL.Query().Get("id"))
	if err != nil {
		http.Error(w, "Invalid webhook ID", http.StatusBadRequest)
		return
	}

	wh, err := h.Store.GetWebhook(id)
	if err != nil {
		http.Error(w, "Failed to get webhook", http.StatusInternalServerError)
		return
	}
	if wh == nil {
		http.Error(w, "Webhook not found", http.StatusNotFound)
		return
	}

	event := &webhook.Event{
		Type:      "webhook.test",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	h.Dispatcher.Send(event)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "sent"})
}
