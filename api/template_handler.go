package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/tionis/hogs/database"
)

type TemplateHandler struct {
	Store *database.Store
}

func NewTemplateHandler(store *database.Store) *TemplateHandler {
	return &TemplateHandler{Store: store}
}

func (h *TemplateHandler) ListTemplates(w http.ResponseWriter, r *http.Request) {
	templates, err := h.Store.ListServerTemplates()
	if err != nil {
		http.Error(w, "Failed to list templates", http.StatusInternalServerError)
		return
	}
	if templates == nil {
		templates = []database.ServerTemplate{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(templates)
}

func (h *TemplateHandler) GetTemplate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.URL.Query().Get("id"))
	if err != nil {
		name := r.URL.Query().Get("name")
		if name == "" {
			http.Error(w, "id or name required", http.StatusBadRequest)
			return
		}
		t, err := h.Store.GetServerTemplateByName(name)
		if err != nil {
			http.Error(w, "Failed to get template", http.StatusInternalServerError)
			return
		}
		if t == nil {
			http.Error(w, "Template not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(t)
		return
	}

	t, err := h.Store.GetServerTemplate(id)
	if err != nil {
		http.Error(w, "Failed to get template", http.StatusInternalServerError)
		return
	}
	if t == nil {
		http.Error(w, "Template not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(t)
}

func (h *TemplateHandler) CreateTemplate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	gameType := r.FormValue("game_type")
	if gameType == "" {
		http.Error(w, "game_type is required", http.StatusBadRequest)
		return
	}

	t := &database.ServerTemplate{
		Name:            name,
		GameType:        gameType,
		DefaultSettings: json.RawMessage(r.FormValue("default_settings")),
		DefaultCommands: json.RawMessage(r.FormValue("default_commands")),
		DefaultACL:      r.FormValue("default_acl"),
		DefaultTags:     json.RawMessage(r.FormValue("default_tags")),
		Description:     r.FormValue("description"),
	}

	if t.DefaultSettings == nil {
		t.DefaultSettings = json.RawMessage("{}")
	}
	if t.DefaultCommands == nil {
		t.DefaultCommands = json.RawMessage("[]")
	}
	if t.DefaultTags == nil {
		t.DefaultTags = json.RawMessage("[]")
	}

	if err := h.Store.CreateServerTemplate(t); err != nil {
		http.Error(w, "Failed to create template: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(t)
}

func (h *TemplateHandler) DeleteTemplate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid template ID", http.StatusBadRequest)
		return
	}

	if err := h.Store.DeleteServerTemplate(id); err != nil {
		http.Error(w, "Failed to delete template", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
