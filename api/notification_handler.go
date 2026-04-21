package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/notify"
)

type NotificationHandler struct {
	Store   *database.Store
	Service *notify.Service
}

func NewNotificationHandler(store *database.Store, service *notify.Service) *NotificationHandler {
	return &NotificationHandler{Store: store, Service: service}
}

func (h *NotificationHandler) ListChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := h.Store.ListNotificationChannels()
	if err != nil {
		http.Error(w, "Failed to list notification channels", http.StatusInternalServerError)
		return
	}
	if channels == nil {
		channels = []database.NotificationChannel{}
	}

	type channelPublic struct {
		ID        int             `json:"id"`
		Name      string          `json:"name"`
		Type      string          `json:"type"`
		URL       string          `json:"url"`
		Events    json.RawMessage `json:"events"`
		Enabled   bool            `json:"enabled"`
		CreatedAt string          `json:"createdAt"`
	}

	var result []channelPublic
	for _, ch := range channels {
		result = append(result, channelPublic{
			ID:        ch.ID,
			Name:      ch.Name,
			Type:      ch.Type,
			URL:       ch.URL,
			Events:    ch.Events,
			Enabled:   ch.Enabled,
			CreatedAt: ch.CreatedAt,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *NotificationHandler) CreateChannel(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	chType := r.FormValue("type")
	url := r.FormValue("url")
	if name == "" || url == "" {
		http.Error(w, "name and url are required", http.StatusBadRequest)
		return
	}
	if chType == "" {
		chType = "shoutrrr"
	}

	events := json.RawMessage(r.FormValue("events"))
	if len(events) == 0 {
		events = json.RawMessage("[]")
	}

	ch := &database.NotificationChannel{
		Name:    name,
		Type:    chType,
		URL:     url,
		Events:  events,
		Enabled: true,
	}

	if err := h.Store.CreateNotificationChannel(ch); err != nil {
		http.Error(w, "Failed to create channel: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":        ch.ID,
		"name":      ch.Name,
		"type":      ch.Type,
		"url":       ch.URL,
		"events":    ch.Events,
		"enabled":   ch.Enabled,
		"createdAt": ch.CreatedAt,
	})
}

func (h *NotificationHandler) DeleteChannel(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid channel ID", http.StatusBadRequest)
		return
	}

	if err := h.Store.DeleteNotificationChannel(id); err != nil {
		http.Error(w, "Failed to delete channel", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (h *NotificationHandler) TestChannel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.URL.Query().Get("id"))
	if err != nil {
		http.Error(w, "Invalid channel ID", http.StatusBadRequest)
		return
	}

	channels, err := h.Store.ListNotificationChannels()
	if err != nil {
		http.Error(w, "Failed to list channels", http.StatusInternalServerError)
		return
	}

	var found *database.NotificationChannel
	for _, ch := range channels {
		if ch.ID == id {
			found = &ch
			break
		}
	}
	if found == nil {
		http.Error(w, "Channel not found", http.StatusNotFound)
		return
	}

	h.Service.Send("test", "HOGS notification test")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "sent"})
}
