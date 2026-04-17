package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"

	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gorilla/sessions"
	"golang.org/x/oauth2"
)

type Authenticator struct {
	Provider     *oidc.Provider
	Config       oauth2.Config
	Verifier     *oidc.IDTokenVerifier
	SessionStore *sessions.CookieStore
	Store        *database.Store
	Cfg          *config.Config
}

func NewAuthenticator(cfg *config.Config, store *database.Store) (*Authenticator, error) {
	ctx := context.Background()

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

	return &Authenticator{
		Provider: provider,
		Config: oauth2.Config{
			ClientID:     cfg.OIDCClientID,
			ClientSecret: cfg.OIDCClientSecret,
			RedirectURL:  cfg.OIDCRedirectURL,
			Endpoint:     provider.Endpoint(),
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
		Verifier:     provider.Verifier(oidcConfig),
		SessionStore: sessions.NewCookieStore([]byte(cfg.SessionSecret)),
		Store:        store,
		Cfg:          cfg,
	}, nil
}

func (a *Authenticator) HandleLogin(w http.ResponseWriter, r *http.Request) {
	state, err := generateRandomState()
	if err != nil {
		http.Error(w, "Failed to generate state", http.StatusInternalServerError)
		return
	}

	session, _ := a.SessionStore.Get(r, "hogs-session")
	session.Values["state"] = state
	session.Save(r, w)

	http.Redirect(w, r, a.Config.AuthCodeURL(state), http.StatusFound)
}

func (a *Authenticator) HandleCallback(w http.ResponseWriter, r *http.Request) {
	session, _ := a.SessionStore.Get(r, "hogs-session")

	state := r.URL.Query().Get("state")
	if session.Values["state"] != state {
		http.Error(w, "Invalid state parameter", http.StatusBadRequest)
		return
	}

	oauth2Token, err := a.Config.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		http.Error(w, "Failed to exchange token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	rawIDToken, ok := oauth2Token.Extra("id_token").(string)
	if !ok {
		http.Error(w, "No id_token field in oauth2 token", http.StatusInternalServerError)
		return
	}

	idToken, err := a.Verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		http.Error(w, "Failed to verify ID Token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var claims struct {
		Email  string      `json:"email"`
		Sub    string      `json:"sub"`
		Groups interface{} `json:"-"`
	}
	if err := idToken.Claims(&claims); err != nil {
		http.Error(w, "Failed to parse claims: "+err.Error(), http.StatusInternalServerError)
		return
	}

	groups := extractGroups(idToken, a.Cfg.OIDCGroupsClaim)
	role := a.resolveRole(claims.Email, groups)

	if err := a.provisionUser(claims.Email, role); err != nil {
		http.Error(w, "Failed to provision user: "+err.Error(), http.StatusInternalServerError)
		return
	}

	session.Values["user_email"] = claims.Email
	session.Values["authenticated"] = true
	session.Values["user_role"] = role
	session.Save(r, w)

	http.Redirect(w, r, "/admin", http.StatusFound)
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

func (a *Authenticator) provisionUser(email, role string) error {
	if role == "" {
		role = "user"
	}
	user, err := a.Store.GetUserByEmail(email)
	if err != nil {
		return err
	}
	if user == nil {
		_, err = a.Store.CreateUser(email, role)
		return err
	}
	if role == "admin" && user.Role != "admin" {
		return a.Store.UpdateUserRole(user.ID, "admin")
	}
	return a.Store.TouchUserLastLogin(user.ID)
}

func (a *Authenticator) HandleLogout(w http.ResponseWriter, r *http.Request) {
	session, _ := a.SessionStore.Get(r, "hogs-session")
	session.Values["authenticated"] = false
	session.Values["user_email"] = ""
	session.Values["user_role"] = ""
	session.Options.MaxAge = -1
	session.Save(r, w)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, _ := a.SessionStore.Get(r, "hogs-session")
		if auth, ok := session.Values["authenticated"].(bool); !ok || !auth {
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
			session, _ := a.SessionStore.Get(r, "hogs-session")
			if auth, ok := session.Values["authenticated"].(bool); !ok || !auth {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			role, _ := session.Values["user_role"].(string)
			if !roleSet[role] {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (a *Authenticator) IsAuthenticated(r *http.Request) bool {
	session, _ := a.SessionStore.Get(r, "hogs-session")
	auth, ok := session.Values["authenticated"].(bool)
	return ok && auth
}

func (a *Authenticator) GetUserEmail(r *http.Request) string {
	session, _ := a.SessionStore.Get(r, "hogs-session")
	email, ok := session.Values["user_email"].(string)
	if !ok {
		return ""
	}
	return email
}

func (a *Authenticator) GetUserRole(r *http.Request) string {
	session, _ := a.SessionStore.Get(r, "hogs-session")
	role, ok := session.Values["user_role"].(string)
	if !ok {
		return ""
	}
	return role
}

func generateRandomState() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}
