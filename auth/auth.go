package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"github.com/tionis/hogs/config"
	"net/http"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gorilla/sessions"
	"golang.org/x/oauth2"
)

// Authenticator handles OIDC authentication.
type Authenticator struct {
	Provider     *oidc.Provider
	Config       oauth2.Config
	Verifier     *oidc.IDTokenVerifier
	SessionStore *sessions.CookieStore
}

// NewAuthenticator creates a new Authenticator.
func NewAuthenticator(cfg *config.Config) (*Authenticator, error) {
	ctx := context.Background()

	// If OIDC is not configured, return nil (or handle graceful fallback)
	if cfg.OIDCProviderURL == "" {
		return nil, nil // OIDC disabled
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
	}, nil
}

// HandleLogin redirects the user to the OIDC provider.
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

// HandleCallback handles the OIDC callback.
func (a *Authenticator) HandleCallback(w http.ResponseWriter, r *http.Request) {
	session, _ := a.SessionStore.Get(r, "hogs-session")

	// Validate state
	state := r.URL.Query().Get("state")
	if session.Values["state"] != state {
		http.Error(w, "Invalid state parameter", http.StatusBadRequest)
		return
	}

	// Exchange code for token
	oauth2Token, err := a.Config.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		http.Error(w, "Failed to exchange token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Extract ID Token
	rawIDToken, ok := oauth2Token.Extra("id_token").(string)
	if !ok {
		http.Error(w, "No id_token field in oauth2 token", http.StatusInternalServerError)
		return
	}

	// Verify ID Token
	idToken, err := a.Verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		http.Error(w, "Failed to verify ID Token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Get claims
	var claims struct {
		Email string `json:"email"`
		Sub   string `json:"sub"`
	}
	if err := idToken.Claims(&claims); err != nil {
		http.Error(w, "Failed to parse claims: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Save session
	session.Values["user_email"] = claims.Email
	session.Values["authenticated"] = true
	session.Save(r, w)

	http.Redirect(w, r, "/admin", http.StatusFound)
}

// Middleware protects routes that require authentication.
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

// HandleLogout logs the user out.
func (a *Authenticator) HandleLogout(w http.ResponseWriter, r *http.Request) {
	session, _ := a.SessionStore.Get(r, "hogs-session")
	session.Values["authenticated"] = false
	session.Values["user_email"] = ""
	session.Options.MaxAge = -1 // delete cookie
	session.Save(r, w)
	http.Redirect(w, r, "/", http.StatusFound)
}

// IsAuthenticated checks if the user is currently authenticated.
func (a *Authenticator) IsAuthenticated(r *http.Request) bool {
	session, _ := a.SessionStore.Get(r, "hogs-session")
	auth, ok := session.Values["authenticated"].(bool)
	return ok && auth
}

// GetUserEmail returns the email of the authenticated user.
func (a *Authenticator) GetUserEmail(r *http.Request) string {
	session, _ := a.SessionStore.Get(r, "hogs-session")
	email, ok := session.Values["user_email"].(string)
	if !ok {
		return ""
	}
	return email
}

func generateRandomState() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}
