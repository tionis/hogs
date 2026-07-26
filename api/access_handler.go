package api

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	"github.com/tionis/hogs/access"
	"github.com/tionis/hogs/auth"
	"github.com/tionis/hogs/database"
)

type AccessHandler struct {
	Store             *database.Store
	Auth              *auth.Authenticator
	AfterAccessChange func(int)
}

func NewAccessHandler(store *database.Store, authenticator ...*auth.Authenticator) *AccessHandler {
	handler := &AccessHandler{Store: store}
	if len(authenticator) > 0 {
		handler.Auth = authenticator[0]
	}
	return handler
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
		Effect       string   `json:"effect"`
		Capabilities []string `json:"capabilities"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "Invalid access grant", http.StatusBadRequest)
		return
	}
	if request.SubjectType != "user" && request.SubjectType != "group" && request.SubjectType != "authenticated" && request.SubjectType != "everyone" {
		http.Error(w, "Invalid subject type", http.StatusBadRequest)
		return
	}
	request.Subject = strings.TrimSpace(request.Subject)
	if request.SubjectType == "authenticated" || request.SubjectType == "everyone" {
		request.Subject = "*"
	}
	if request.Subject == "" || len(request.Capabilities) == 0 {
		http.Error(w, "Subject and capabilities are required", http.StatusBadRequest)
		return
	}
	if request.Effect == "" {
		request.Effect = "allow"
	}
	if request.Effect != "allow" && request.Effect != "deny" {
		http.Error(w, "Effect must be allow or deny", http.StatusBadRequest)
		return
	}
	for _, capability := range request.Capabilities {
		if !access.Known(capability) {
			http.Error(w, "Unknown capability: "+capability, http.StatusBadRequest)
			return
		}
	}
	grant := &database.ServerAccessGrant{
		ServerID: server.ID, SubjectType: request.SubjectType,
		Subject: request.Subject, Effect: request.Effect, Capabilities: request.Capabilities,
	}
	if err := h.Store.SetServerAccessGrant(grant); err != nil {
		http.Error(w, "Failed to save access grant", http.StatusInternalServerError)
		return
	}
	if h.AfterAccessChange != nil {
		h.AfterAccessChange(server.ID)
	}
	writeJSON(w, http.StatusOK, grant)
}

func (h *AccessHandler) EffectiveAccess(w http.ResponseWriter, r *http.Request) {
	server, ok := h.server(w, r)
	if !ok {
		return
	}
	user := userEnvFromRequest(h.Store, h.Auth, r)
	decisions := make(map[string]database.ServerAccessDecision, len(access.Capabilities))
	for _, capability := range access.Capabilities {
		if user.Role == "admin" || user.Role == "system" {
			decisions[capability.Name] = database.ServerAccessDecision{
				Allowed: true, Governed: true, Reason: "instance administrator",
			}
			continue
		}
		decision, err := h.Store.EvaluateServerAccess(server.ID, user.Username, user.Groups, capability.Name)
		if err != nil {
			http.Error(w, "Failed to evaluate access", http.StatusInternalServerError)
			return
		}
		decisions[capability.Name] = decision
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"server": server.Name, "user": user.Username, "role": user.Role,
		"groups": user.Groups, "capabilities": decisions,
	})
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
	if h.AfterAccessChange != nil {
		h.AfterAccessChange(server.ID)
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
	identities, err := h.Store.ListGameIdentities(r.URL.Query().Get("userUsername"))
	if err != nil {
		http.Error(w, "Failed to load game identities", http.StatusInternalServerError)
		return
	}
	if identities == nil {
		identities = []database.GameIdentity{}
	}
	scimIdentities := make([]database.GameIdentity, 0, len(identities))
	for _, identity := range identities {
		if identity.Source == "scim" {
			scimIdentities = append(scimIdentities, identity)
		}
	}
	writeJSON(w, http.StatusOK, scimIdentities)
}

var apiGameTypePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
var apiGameUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9_. -]{1,192}$`)

func (h *AccessHandler) SetGameIdentity(w http.ResponseWriter, r *http.Request) {
	var identity database.GameIdentity
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&identity); err != nil {
		http.Error(w, "Invalid game identity", http.StatusBadRequest)
		return
	}
	identity.UserUsername = strings.TrimSpace(identity.UserUsername)
	identity.GameType = strings.ToLower(strings.TrimSpace(identity.GameType))
	identity.Username = strings.TrimSpace(identity.Username)
	identity.Source = "admin"
	if identity.UserUsername == "" || !apiGameTypePattern.MatchString(identity.GameType) ||
		!apiGameUsernamePattern.MatchString(identity.Username) {
		http.Error(w, "Invalid game identity", http.StatusBadRequest)
		return
	}
	driver := h.Store.ResolveGameDriver(identity.GameType)
	if !driver.IdentityValid(identity.Username) {
		http.Error(w, "Invalid username for game type", http.StatusBadRequest)
		return
	}
	identity.ExternalID = ""
	if existing, _ := h.Store.GetGameIdentity(identity.UserUsername, identity.GameType); existing != nil &&
		driver.IdentitiesEqual(existing.Username, identity.Username) {
		identity.ExternalID = existing.ExternalID
	}
	if err := h.Store.SetGameIdentity(&identity); err != nil {
		http.Error(w, "Failed to save game identity", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, identity)
}

func (h *AccessHandler) DeleteGameIdentity(w http.ResponseWriter, r *http.Request) {
	username := r.URL.Query().Get("userUsername")
	gameType := r.URL.Query().Get("gameType")
	if username == "" || gameType == "" {
		http.Error(w, "userUsername and gameType are required", http.StatusBadRequest)
		return
	}
	if err := h.Store.DeleteGameIdentity(username, gameType); err != nil {
		http.Error(w, "Failed to delete game identity", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
