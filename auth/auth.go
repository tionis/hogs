package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gorilla/sessions"
	"golang.org/x/oauth2"
)

const sessionCookieName = "hogs-session"

// Provider discovery is retried with exponential backoff between these bounds
// when the identity provider is unreachable at startup.
var (
	discoveryRetryMin = 2 * time.Second
	discoveryRetryMax = time.Minute
)

const discoveryTimeout = 10 * time.Second

// oidcClient holds everything derived from the provider's discovery document.
type oidcClient struct {
	config         oauth2.Config
	verifier       *oidc.IDTokenVerifier
	logoutVerifier *oidc.IDTokenVerifier
}

type Authenticator struct {
	// client stays nil until provider discovery succeeds; the login,
	// callback and back-channel logout handlers answer 503 until then.
	client      atomic.Pointer[oidcClient]
	Store       *database.Store
	Cfg         *config.Config
	cookieStore *sessions.CookieStore
	forbidden   http.Handler
}

func NewAuthenticator(cfg *config.Config, store *database.Store) (*Authenticator, error) {
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

	cookieStore := sessions.NewCookieStore([]byte(cfg.SessionSecret))
	cookieStore.Options = &sessions.Options{
		Path:   "/",
		MaxAge: 86400 * 30,
		// Lax (not Strict): the session must survive cross-site top-level
		// navigation from the portal and back from the identity provider,
		// otherwise every such navigation silently drops the session and
		// forces a fresh login. CSRF-sensitive mutations stay protected by
		// the separate CSRF token.
		SameSite: http.SameSiteLaxMode,
		HttpOnly: true,
		Secure:   cfg.TLSCert != "",
	}

	a := &Authenticator{
		Store:       store,
		Cfg:         cfg,
		cookieStore: cookieStore,
	}

	// The identity provider often starts alongside HOGS (for example after a
	// host reboot), so a failed discovery must not disable login for the
	// lifetime of the process.
	if err := a.discover(); err != nil {
		log.Printf("Warning: OIDC provider discovery failed, retrying in the background: %v", err)
		go a.retryDiscovery()
	}
	return a, nil
}

func (a *Authenticator) discover() error {
	ctx, cancel := context.WithTimeout(context.Background(), discoveryTimeout)
	defer cancel()

	provider, err := oidc.NewProvider(ctx, a.Cfg.OIDCProviderURL)
	if err != nil {
		return err
	}

	verifierConfig := &oidc.Config{ClientID: a.Cfg.OIDCClientID}
	a.client.Store(&oidcClient{
		config: oauth2.Config{
			ClientID:     a.Cfg.OIDCClientID,
			ClientSecret: a.Cfg.OIDCClientSecret,
			RedirectURL:  a.Cfg.OIDCRedirectURL,
			Endpoint:     provider.Endpoint(),
			Scopes:       []string{oidc.ScopeOpenID, "profile"},
		},
		verifier:       provider.Verifier(verifierConfig),
		logoutVerifier: provider.Verifier(verifierConfig),
	})
	return nil
}

func (a *Authenticator) retryDiscovery() {
	delay := discoveryRetryMin
	for {
		time.Sleep(delay)
		err := a.discover()
		if err == nil {
			log.Println("OIDC provider discovery succeeded; login is available.")
			return
		}
		delay = min(delay*2, discoveryRetryMax)
		log.Printf("Warning: OIDC provider discovery failed, retrying in %s: %v", delay, err)
	}
}

// oidcClientOrUnavailable returns the discovered OIDC client, or writes a 503
// and returns nil while discovery is still pending.
func (a *Authenticator) oidcClientOrUnavailable(w http.ResponseWriter) *oidcClient {
	client := a.client.Load()
	if client == nil {
		http.Error(w, "Authentication provider is unavailable, try again shortly", http.StatusServiceUnavailable)
	}
	return client
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
	client := a.oidcClientOrUnavailable(w)
	if client == nil {
		return
	}

	state, err := generateRandomState()
	if err != nil {
		http.Error(w, "Failed to generate state", http.StatusInternalServerError)
		return
	}

	session, _ := a.cookieStore.Get(r, sessionCookieName)
	session.Values["state"] = state
	a.saveSession(session, r, w)

	http.Redirect(w, r, client.config.AuthCodeURL(state), http.StatusFound)
}

func (a *Authenticator) HandleCallback(w http.ResponseWriter, r *http.Request) {
	client := a.oidcClientOrUnavailable(w)
	if client == nil {
		return
	}

	session, _ := a.cookieStore.Get(r, sessionCookieName)

	state := r.URL.Query().Get("state")
	if session.Values["state"] != state {
		http.Error(w, "Invalid state parameter", http.StatusBadRequest)
		return
	}

	oauth2Token, err := client.config.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		http.Error(w, "Failed to exchange token", http.StatusInternalServerError)
		return
	}

	rawIDToken, ok := oauth2Token.Extra("id_token").(string)
	if !ok {
		http.Error(w, "Authentication failed", http.StatusInternalServerError)
		return
	}

	idToken, err := client.verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		http.Error(w, "Authentication failed", http.StatusInternalServerError)
		return
	}

	var claims struct {
		Sub               string `json:"sub"`
		PreferredUsername string `json:"preferred_username"`
	}
	if err := idToken.Claims(&claims); err != nil {
		http.Error(w, "Authentication failed", http.StatusInternalServerError)
		return
	}

	groups := extractGroups(idToken, a.Cfg.OIDCGroupsClaim)
	role := a.resolveRole(groups)

	user, err := a.provisionUser(role, idToken.Issuer, claims.Sub, claims.PreferredUsername, groups)
	if err != nil {
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
		SessionID:    sessionID,
		UserSub:      claims.Sub,
		UserUsername: user.Username,
		UserRole:     role,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
		ExpiresAt:    expiresAt,
	}
	if err := a.Store.CreateSession(dbSession); err != nil {
		http.Error(w, "Failed to create session", http.StatusInternalServerError)
		return
	}

	session.Values["session_id"] = sessionID
	session.Values["state"] = ""
	a.saveSession(session, r, w)

	http.Redirect(w, r, loginDestination(role), http.StatusFound)
}

func loginDestination(role string) string {
	if role == "admin" {
		return "/admin"
	}
	return "/my-servers"
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
	client := a.oidcClientOrUnavailable(w)
	if client == nil {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	logoutToken := r.FormValue("logout_token")
	if logoutToken == "" {
		http.Error(w, "Missing logout_token", http.StatusBadRequest)
		return
	}

	idToken, err := client.logoutVerifier.Verify(r.Context(), logoutToken)
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
	session, cookieErr := a.cookieStore.Get(r, sessionCookieName)
	sessionID, ok := session.Values["session_id"].(string)
	if !ok || sessionID == "" {
		// No session cookie (anonymous) or a pre-login state cookie.
		// Only log undecryptable cookies: those indicate a secret change
		// or corruption rather than a signed-out visitor.
		if _, cookiePresent := r.Cookie(sessionCookieName); cookiePresent == nil && cookieErr != nil {
			log.Printf("session cookie present but invalid; forcing fresh login")
		}
		return nil
	}

	dbSession, err := a.Store.GetSession(sessionID)
	if err != nil {
		// A transient database error (for example SQLITE_BUSY before the
		// busy timeout was configured) must not be treated as a signed-out
		// visitor, and must not delete a valid session.
		log.Printf("session lookup failed; leaving session intact: %v", err)
		return nil
	}
	if dbSession == nil {
		log.Printf("unknown session id; forcing fresh login")
		return nil
	}

	expiresAt, err := time.Parse(time.RFC3339, dbSession.ExpiresAt)
	if err != nil || time.Now().UTC().After(expiresAt) {
		a.Store.DeleteSession(sessionID)
		log.Printf("expired session for user %q; forcing fresh login", dbSession.UserUsername)
		return nil
	}

	// Verify the user is still active
	user, err := a.Store.GetUserByUsername(dbSession.UserUsername)
	if err != nil {
		log.Printf("user lookup failed during session validation; leaving session intact: %v", err)
		return nil
	}
	if user == nil || !user.Active {
		a.Store.DeleteSession(sessionID)
		log.Printf("session for inactive or deleted user; forcing fresh login")
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

func (a *Authenticator) resolveRole(groups []string) string {
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

	if userGroup == "" {
		return "user"
	}

	return ""
}

func (a *Authenticator) provisionUser(role, issuer, subject, preferredUsername string, groups []string) (*database.User, error) {
	username := strings.TrimSpace(preferredUsername)
	if username == "" || strings.TrimSpace(issuer) == "" || strings.TrimSpace(subject) == "" {
		return nil, fmt.Errorf("OIDC identity requires issuer, subject, and preferred_username claims")
	}
	if role == "" {
		role = "user"
	}
	user, err := a.Store.GetUserByOIDCIdentity(issuer, subject)
	if err != nil {
		return nil, fmt.Errorf("GetUserByOIDCIdentity failed: %w", err)
	}
	if user == nil {
		user, err = a.Store.GetUserByExternalID(subject)
		if err != nil {
			return nil, fmt.Errorf("GetUserByExternalID failed: %w", err)
		}
	}
	if user == nil {
		user, err = a.Store.CreateUser(username, role)
		if err != nil {
			return nil, fmt.Errorf("CreateUser failed: %w", err)
		}
	}
	if user.Username != username {
		if err := a.Store.UpdateUserUsername(user.ID, username); err != nil {
			return nil, fmt.Errorf("UpdateUserUsername failed: %w", err)
		}
		user.Username = username
	}
	if user.Role != role {
		if err := a.Store.UpdateUserRole(user.ID, role); err != nil {
			return nil, fmt.Errorf("UpdateUserRole failed: %w", err)
		}
		user.Role = role
	}
	if err := a.Store.TouchUserLastLogin(user.ID); err != nil {
		return nil, fmt.Errorf("TouchUserLastLogin failed: %w", err)
	}
	if err := a.Store.UpdateUserOIDCIdentity(user.ID, issuer, subject, username); err != nil {
		return nil, fmt.Errorf("UpdateUserOIDCIdentity failed: %w", err)
	}
	if err := a.Store.SyncUserOIDCGroups(user.ID, groups); err != nil {
		return nil, fmt.Errorf("SyncUserOIDCGroups failed: %w", err)
	}
	user.OIDCIssuer = issuer
	user.OIDCSubject = subject
	user.PreferredUsername = username
	return user, nil
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
				a.writeForbidden(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// SetForbiddenHandler configures the branded HTML response used when an
// authenticated user does not have the required role. API responses remain
// plain HTTP errors for machine clients.
func (a *Authenticator) SetForbiddenHandler(handler http.Handler) {
	a.forbidden = handler
}

func (a *Authenticator) writeForbidden(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/api/") && a.forbidden != nil {
		a.forbidden.ServeHTTP(w, r)
		return
	}
	http.Error(w, "Forbidden", http.StatusForbidden)
}

func (a *Authenticator) IsAuthenticated(r *http.Request) bool {
	if a == nil {
		return false
	}
	return a.getSession(r) != nil
}

func (a *Authenticator) GetUsername(r *http.Request) string {
	if a == nil {
		return ""
	}
	dbSession := a.getSession(r)
	if dbSession == nil {
		return ""
	}
	return dbSession.UserUsername
}

func (a *Authenticator) GetUserRole(r *http.Request) string {
	if a == nil {
		return ""
	}
	dbSession := a.getSession(r)
	if dbSession == nil {
		return ""
	}
	return dbSession.UserRole
}

func (a *Authenticator) GetSessionID(r *http.Request) string {
	if a == nil {
		return ""
	}
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
