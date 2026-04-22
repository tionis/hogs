package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/tionis/hogs/auth"
	"github.com/tionis/hogs/database"
)

type APIKeyHandler struct {
	Store *database.Store
}

func NewAPIKeyHandler(store *database.Store) *APIKeyHandler {
	return &APIKeyHandler{Store: store}
}

func (h *APIKeyHandler) ListAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.Store.ListAPIKeys()
	if err != nil {
		http.Error(w, "Failed to list API keys", http.StatusInternalServerError)
		return
	}
	if keys == nil {
		keys = []database.APIKey{}
	}

	type apiKeyPublic struct {
		ID        int     `json:"id"`
		Name      string  `json:"name"`
		KeyPrefix string  `json:"keyPrefix"`
		Role      string  `json:"role"`
		CreatedAt string  `json:"createdAt"`
		LastUsed  *string `json:"lastUsed"`
		ExpiresAt *string `json:"expiresAt"`
	}

	var result []apiKeyPublic
	for _, k := range keys {
		result = append(result, apiKeyPublic{
			ID:        k.ID,
			Name:      k.Name,
			KeyPrefix: k.KeyPrefix,
			Role:      k.Role,
			CreatedAt: k.CreatedAt,
			LastUsed:  k.LastUsed,
			ExpiresAt: k.ExpiresAt,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *APIKeyHandler) CreateAPIKey(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	role := r.FormValue("role")
	if role == "" {
		role = "user"
	}
	if role != "admin" && role != "user" {
		http.Error(w, "role must be 'admin' or 'user'", http.StatusBadRequest)
		return
	}

	plain, hash, prefix := auth.GenerateAPIKey()
	if plain == "" {
		http.Error(w, "Failed to generate API key", http.StatusInternalServerError)
		return
	}

	key := &database.APIKey{
		Name:      name,
		KeyHash:   hash,
		KeyPrefix: prefix,
		Role:      role,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	if err := h.Store.CreateAPIKey(key); err != nil {
		http.Error(w, "Failed to create API key", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":        key.ID,
		"name":      key.Name,
		"key":       plain,
		"keyPrefix": prefix,
		"role":      key.Role,
		"createdAt": key.CreatedAt,
	})
}

func (h *APIKeyHandler) DeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid API key ID", http.StatusBadRequest)
		return
	}

	if err := h.Store.DeleteAPIKey(id); err != nil {
		http.Error(w, "Failed to delete API key", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
