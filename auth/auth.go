package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gorilla/sessions"
	"golang.org/x/oauth2"
)

const sessionCookieName = "hogs-session"

type Authenticator struct {
	Provider       *oidc.Provider
	Config         oauth2.Config
	Verifier       *oidc.IDTokenVerifier
	LogoutVerifier *oidc.IDTokenVerifier
	Store          *database.Store
	Cfg            *config.Config
	cookieStore    *sessions.CookieStore
}

func NewAuthenticator(cfg *config.Config, store *database.Store) (*Authenticator, error) {
	ctx := context.Background()

	if cfg.SessionSecret == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		cfg.SessionSecret = base64.StdEncoding.EncodeToString(b)
		log.Println("WARNING: SESSION_SECRET not set. A random secret has been generated. Sessions will not persist across restarts. Set SESSION_SECRET to avoid this.")
	}

	if cfg.OIDCProviderURL == "" {
		return nil, nil
	}

	provider, err := oidc.NewProvider(ctx, cfg.OIDCProviderURL)
	if err != nil {
		return nil, err
	}

	oidcConfig := &oidc.Config{
		ClientID: cfg.OIDCClientID,
	}

	logoutOidcConfig := &oidc.Config{
		ClientID: cfg.OIDCClientID,
	}

	cookieStore := sessions.NewCookieStore([]byte(cfg.SessionSecret))
	cookieStore.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400 * 30,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   cfg.TLSCert != "",
	}

	return &Authenticator{
		Provider: provider,
		Config: oauth2.Config{
			ClientID:     cfg.OIDCClientID,
			ClientSecret: cfg.OIDCClientSecret,
			RedirectURL:  cfg.OIDCRedirectURL,
			Endpoint:     provider.Endpoint(),
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
		Verifier:       provider.Verifier(oidcConfig),
		LogoutVerifier: provider.Verifier(logoutOidcConfig),
		Store:          store,
		Cfg:            cfg,
		cookieStore:    cookieStore,
	}, nil
}

func (a *Authenticator) IsSecureRequest(r *http.Request) bool {
	if a.Cfg.TLSCert != "" {
		return true
	}
	if a.Cfg.TrustProxyHeaders {
		if r.Header.Get("X-Forwarded-Proto") == "https" {
			return true
		}
	}
	return false
}

func (a *Authenticator) saveSession(session *sessions.Session, r *http.Request, w http.ResponseWriter) error {
	session.Options.Secure = a.IsSecureRequest(r)
	return session.Save(r, w)
}

func (a *Authenticator) HandleLogin(w http.ResponseWriter, r *http.Request) {
	state, err := generateRandomState()
	if err != nil {
		http.Error(w, "Failed to generate state", http.StatusInternalServerError)
		return
	}

	session, _ := a.cookieStore.Get(r, sessionCookieName)
	session.Values["state"] = state
	a.saveSession(session, r, w)

	http.Redirect(w, r, a.Config.AuthCodeURL(state), http.StatusFound)
}

func (a *Authenticator) HandleCallback(w http.ResponseWriter, r *http.Request) {
	session, _ := a.cookieStore.Get(r, sessionCookieName)

	state := r.URL.Query().Get("state")
	if session.Values["state"] != state {
		http.Error(w, "Invalid state parameter", http.StatusBadRequest)
		return
	}

	oauth2Token, err := a.Config.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		http.Error(w, "Failed to exchange token", http.StatusInternalServerError)
		return
	}

	rawIDToken, ok := oauth2Token.Extra("id_token").(string)
	if !ok {
		http.Error(w, "Authentication failed", http.StatusInternalServerError)
		return
	}

	idToken, err := a.Verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		http.Error(w, "Authentication failed", http.StatusInternalServerError)
		return
	}

	var claims struct {
		Email             string      `json:"email"`
		Sub               string      `json:"sub"`
		Name              string      `json:"name"`
		PreferredUsername string      `json:"preferred_username"`
		Groups            interface{} `json:"-"`
	}
	if err := idToken.Claims(&claims); err != nil {
		http.Error(w, "Authentication failed", http.StatusInternalServerError)
		return
	}

	groups := extractGroups(idToken, a.Cfg.OIDCGroupsClaim)
	role := a.resolveRole(claims.Email, groups)

	displayName := claims.Name
	if displayName == "" {
		displayName = claims.PreferredUsername
	}

	log.Printf("OIDC login: email=%s sub=%s name=%s displayName=%s groups=%v groupsClaim=%s", claims.Email, claims.Sub, claims.Name, displayName, groups, a.Cfg.OIDCGroupsClaim)

	if err := a.provisionUser(claims.Email, role, claims.Sub, displayName, groups); err != nil {
		log.Printf("OIDC provisionUser failed: %v", err)
		http.Error(w, "Authentication failed", http.StatusInternalServerError)
		return
	}

	sessionID, err := generateRandomState()
	if err != nil {
		http.Error(w, "Failed to create session", http.StatusInternalServerError)
		return
	}

	expiresAt := time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339)
	dbSession := &database.Session{
		SessionID: sessionID,
		UserSub:   claims.Sub,
		UserEmail: claims.Email,
		UserRole:  role,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		ExpiresAt: expiresAt,
	}
	if err := a.Store.CreateSession(dbSession); err != nil {
		http.Error(w, "Failed to create session", http.StatusInternalServerError)
		return
	}

	session.Values["session_id"] = sessionID
	session.Values["state"] = ""
	a.saveSession(session, r, w)

	http.Redirect(w, r, "/admin", http.StatusFound)
}

func (a *Authenticator) HandleLogout(w http.ResponseWriter, r *http.Request) {
	session, _ := a.cookieStore.Get(r, sessionCookieName)

	if sessionID, ok := session.Values["session_id"].(string); ok && sessionID != "" {
		a.Store.DeleteSession(sessionID)
	}

	session.Values["session_id"] = ""
	session.Options.MaxAge = -1
	a.saveSession(session, r, w)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (a *Authenticator) HandleBackChannelLogout(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	logoutToken := r.FormValue("logout_token")
	if logoutToken == "" {
		http.Error(w, "Missing logout_token", http.StatusBadRequest)
		return
	}

	idToken, err := a.LogoutVerifier.Verify(r.Context(), logoutToken)
	if err != nil {
		log.Printf("Back-channel logout: token verification failed: %v", err)
		http.Error(w, "Invalid logout token", http.StatusBadRequest)
		return
	}

	var claims struct {
		Sub    string                 `json:"sub"`
		Sid    string                 `json:"sid"`
		Events map[string]interface{} `json:"events"`
	}
	if err := idToken.Claims(&claims); err != nil {
		log.Printf("Back-channel logout: failed to parse claims: %v", err)
		http.Error(w, "Invalid token claims", http.StatusBadRequest)
		return
	}

	if len(claims.Events) == 0 {
		http.Error(w, "Missing events claim", http.StatusBadRequest)
		return
	}

	if claims.Sub != "" {
		if err := a.Store.DeleteSessionsBySub(claims.Sub); err != nil {
			log.Printf("Back-channel logout: failed to delete sessions for sub %s: %v", claims.Sub, err)
			http.Error(w, "Internal error", http.StatusInternalServerError)
			return
		}
		log.Printf("Back-channel logout: invalidated sessions for sub=%s", claims.Sub)
	}

	w.WriteHeader(http.StatusOK)
}

func (a *Authenticator) getSession(r *http.Request) *database.Session {
	session, _ := a.cookieStore.Get(r, sessionCookieName)
	sessionID, ok := session.Values["session_id"].(string)
	if !ok || sessionID == "" {
		return nil
	}

	dbSession, err := a.Store.GetSession(sessionID)
	if err != nil || dbSession == nil {
		return nil
	}

	expiresAt, err := time.Parse(time.RFC3339, dbSession.ExpiresAt)
	if err != nil || time.Now().UTC().After(expiresAt) {
		a.Store.DeleteSession(sessionID)
		return nil
	}

	// Verify the user is still active
	user, err := a.Store.GetUserByEmail(dbSession.UserEmail)
	if err != nil || user == nil || !user.Active {
		a.Store.DeleteSession(sessionID)
		return nil
	}

	return dbSession
}

func extractGroups(idToken *oidc.IDToken, claimName string) []string {
	var rawClaims map[string]interface{}
	if err := idToken.Claims(&rawClaims); err != nil {
		return nil
	}

	groupsRaw, ok := rawClaims[claimName]
	if !ok {
		return nil
	}

	switch v := groupsRaw.(type) {
	case []string:
		return v
	case []interface{}:
		var result []string
		for _, item := range v {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	default:
		return nil
	}
}

func (a *Authenticator) resolveRole(email string, groups []string) string {
	adminGroup := a.Cfg.OIDCAdminGroup
	userGroup := a.Cfg.OIDCUserGroup

	for _, g := range groups {
		if adminGroup != "" && g == adminGroup {
			return "admin"
		}
	}

	for _, g := range groups {
		if userGroup != "" && g == userGroup {
			return "user"
		}
	}

	user, err := a.Store.GetUserByEmail(email)
	if err == nil && user != nil {
		return user.Role
	}

	if userGroup == "" {
		return "user"
	}

	return ""
}

func (a *Authenticator) provisionUser(email, role, externalID, displayName string, groups []string) error {
	if role == "" {
		role = "user"
	}
	user, err := a.Store.GetUserByEmail(email)
	if err != nil {
		return fmt.Errorf("GetUserByEmail failed: %w", err)
	}
	if user == nil {
		user, err = a.Store.CreateUser(email, role)
		if err != nil {
			return fmt.Errorf("CreateUser failed: %w", err)
		}
		log.Printf("Created new user: id=%d email=%s", user.ID, email)
	} else {
		log.Printf("Existing user: id=%d email=%s", user.ID, email)
	}
	if role == "admin" && user.Role != "admin" {
		if err := a.Store.UpdateUserRole(user.ID, "admin"); err != nil {
			return fmt.Errorf("UpdateUserRole failed: %w", err)
		}
	}
	if err := a.Store.TouchUserLastLogin(user.ID); err != nil {
		return fmt.Errorf("TouchUserLastLogin failed: %w", err)
	}
	if err := a.Store.UpdateUserSCIM(user.ID, externalID, displayName, true); err != nil {
		return fmt.Errorf("UpdateUserSCIM failed: %w", err)
	}
	log.Printf("Updated user SCIM: id=%d external_id=%s display_name=%s", user.ID, externalID, displayName)
	if err := a.Store.SyncUserOIDCGroups(user.ID, groups); err != nil {
		return fmt.Errorf("SyncUserOIDCGroups failed: %w", err)
	}
	log.Printf("Synced user groups: id=%d groups=%v", user.ID, groups)
	return nil
}

func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dbSession := a.getSession(r)
		if dbSession == nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *Authenticator) RequireRole(roles ...string) func(http.Handler) http.Handler {
	roleSet := make(map[string]bool, len(roles))
	for _, r := range roles {
		roleSet[r] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				key, _ := r.Context().Value(apiKeyContextKey).(*database.APIKey)
				if key != nil {
					if !roleSet[key.Role] {
						http.Error(w, "Forbidden", http.StatusForbidden)
						return
					}
					next.ServeHTTP(w, r)
					return
				}
				// No API key: fall through to session auth
			}

			dbSession := a.getSession(r)
			if dbSession == nil {
				if strings.HasPrefix(r.URL.Path, "/api/") {
					http.Error(w, "Unauthorized", http.StatusUnauthorized)
					return
				}
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			if !roleSet[dbSession.UserRole] {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (a *Authenticator) IsAuthenticated(r *http.Request) bool {
	return a.getSession(r) != nil
}

func (a *Authenticator) GetUserEmail(r *http.Request) string {
	dbSession := a.getSession(r)
	if dbSession == nil {
		return ""
	}
	return dbSession.UserEmail
}

func (a *Authenticator) GetUserRole(r *http.Request) string {
	dbSession := a.getSession(r)
	if dbSession == nil {
		return ""
	}
	return dbSession.UserRole
}

func (a *Authenticator) GetSessionID(r *http.Request) string {
	dbSession := a.getSession(r)
	if dbSession == nil {
		return ""
	}
	return dbSession.SessionID
}

func (a *Authenticator) CleanupSessions() {
	if err := a.Store.CleanupExpiredSessions(); err != nil {
		log.Printf("Warning: session cleanup failed: %v", err)
	}
}

func generateRandomState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// CookieStore returns the underlying cookie store (for test helpers).
func (a *Authenticator) CookieStore() *sessions.CookieStore {
	return a.cookieStore
}

// NewTestAuthenticator creates an Authenticator for testing without OIDC.
func NewTestAuthenticator(store *database.Store, secret string) *Authenticator {
	if secret == "" {
		secret = "test-secret"
	}
	cookieStore := sessions.NewCookieStore([]byte(secret))
	cookieStore.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400 * 30,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   false,
	}
	return &Authenticator{
		Store:       store,
		Cfg:         &config.Config{SessionSecret: secret},
		cookieStore: cookieStore,
	}
}
