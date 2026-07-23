package api

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	"github.com/tionis/hogs/database"
)

type AccessHandler struct {
	Store *database.Store
}

func NewAccessHandler(store *database.Store) *AccessHandler {
	return &AccessHandler{Store: store}
}

var accessCapabilities = map[string]bool{
	"status": true, "start": true, "stop": true, "restart": true,
	"command": true, "console": true, "whitelist": true, "backup": true,
}

func (h *AccessHandler) ListGrants(w http.ResponseWriter, r *http.Request) {
	server, ok := h.server(w, r)
	if !ok {
		return
	}
	grants, err := h.Store.ListServerAccessGrants(server.ID)
	if err != nil {
		http.Error(w, "Failed to load access grants", http.StatusInternalServerError)
		return
	}
	if grants == nil {
		grants = []database.ServerAccessGrant{}
	}
	writeJSON(w, http.StatusOK, grants)
}

func (h *AccessHandler) SetGrant(w http.ResponseWriter, r *http.Request) {
	server, ok := h.server(w, r)
	if !ok {
		return
	}
	var request struct {
		SubjectType  string   `json:"subjectType"`
		Subject      string   `json:"subject"`
		Capabilities []string `json:"capabilities"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "Invalid access grant", http.StatusBadRequest)
		return
	}
	if request.SubjectType != "user" && request.SubjectType != "group" && request.SubjectType != "authenticated" {
		http.Error(w, "Invalid subject type", http.StatusBadRequest)
		return
	}
	request.Subject = strings.TrimSpace(request.Subject)
	if request.SubjectType == "authenticated" {
		request.Subject = "*"
	}
	if request.Subject == "" || len(request.Capabilities) == 0 {
		http.Error(w, "Subject and capabilities are required", http.StatusBadRequest)
		return
	}
	for _, capability := range request.Capabilities {
		if !accessCapabilities[capability] {
			http.Error(w, "Unknown capability: "+capability, http.StatusBadRequest)
			return
		}
	}
	grant := &database.ServerAccessGrant{
		ServerID: server.ID, SubjectType: request.SubjectType,
		Subject: request.Subject, Capabilities: request.Capabilities,
	}
	if err := h.Store.SetServerAccessGrant(grant); err != nil {
		http.Error(w, "Failed to save access grant", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, grant)
}

func (h *AccessHandler) DeleteGrant(w http.ResponseWriter, r *http.Request) {
	server, ok := h.server(w, r)
	if !ok {
		return
	}
	grantID, err := strconv.Atoi(mux.Vars(r)["grantID"])
	if err != nil {
		http.Error(w, "Invalid grant ID", http.StatusBadRequest)
		return
	}
	if err := h.Store.DeleteServerAccessGrant(grantID, server.ID); err != nil {
		http.Error(w, "Failed to delete access grant", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AccessHandler) server(w http.ResponseWriter, r *http.Request) (*database.Server, bool) {
	server, err := h.Store.GetServerByName(mux.Vars(r)["serverName"])
	if err != nil {
		http.Error(w, "Failed to load server", http.StatusInternalServerError)
		return nil, false
	}
	if server == nil {
		http.Error(w, "Server not found", http.StatusNotFound)
		return nil, false
	}
	return server, true
}

func (h *AccessHandler) ListGameIdentities(w http.ResponseWriter, r *http.Request) {
	identities, err := h.Store.ListGameIdentities(r.URL.Query().Get("userEmail"))
	if err != nil {
		http.Error(w, "Failed to load game identities", http.StatusInternalServerError)
		return
	}
	if identities == nil {
		identities = []database.GameIdentity{}
	}
	writeJSON(w, http.StatusOK, identities)
}

var apiGameTypePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
var apiGameUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

func (h *AccessHandler) SetGameIdentity(w http.ResponseWriter, r *http.Request) {
	var identity database.GameIdentity
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&identity); err != nil {
		http.Error(w, "Invalid game identity", http.StatusBadRequest)
		return
	}
	identity.UserEmail = strings.TrimSpace(identity.UserEmail)
	identity.GameType = strings.ToLower(strings.TrimSpace(identity.GameType))
	identity.Username = strings.TrimSpace(identity.Username)
	identity.Source = "admin"
	if identity.UserEmail == "" || !apiGameTypePattern.MatchString(identity.GameType) ||
		!apiGameUsernamePattern.MatchString(identity.Username) {
		http.Error(w, "Invalid game identity", http.StatusBadRequest)
		return
	}
	if identity.GameType == "minecraft" && !minecraftUsernameRegex.MatchString(identity.Username) {
		http.Error(w, "Invalid Minecraft username", http.StatusBadRequest)
		return
	}
	if err := h.Store.SetGameIdentity(&identity); err != nil {
		http.Error(w, "Failed to save game identity", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, identity)
}

func (h *AccessHandler) DeleteGameIdentity(w http.ResponseWriter, r *http.Request) {
	email := r.URL.Query().Get("userEmail")
	gameType := r.URL.Query().Get("gameType")
	if email == "" || gameType == "" {
		http.Error(w, "userEmail and gameType are required", http.StatusBadRequest)
		return
	}
	if err := h.Store.DeleteGameIdentity(email, gameType); err != nil {
		http.Error(w, "Failed to delete game identity", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
