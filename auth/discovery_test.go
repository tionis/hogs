package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tionis/hogs/config"
)

func TestLoginRecoversWhenProviderStartsAfterHOGS(t *testing.T) {
	previousMin, previousMax := discoveryRetryMin, discoveryRetryMax
	discoveryRetryMin, discoveryRetryMax = 10*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { discoveryRetryMin, discoveryRetryMax = previousMin, previousMax })

	var providerUp atomic.Bool
	var issuer string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !providerUp.Load() {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 issuer,
			"authorization_endpoint": issuer + "/authorize",
			"token_endpoint":         issuer + "/token",
			"jwks_uri":               issuer + "/jwks",
		})
	}))
	defer provider.Close()
	issuer = provider.URL

	authenticator, err := NewAuthenticator(&config.Config{
		SessionSecret:   "discovery-test-secret",
		OIDCProviderURL: issuer,
		OIDCClientID:    "hogs",
		OIDCRedirectURL: "https://games.example.test/auth/callback",
	}, nil)
	if err != nil {
		t.Fatalf("NewAuthenticator failed while the provider was down: %v", err)
	}
	if authenticator == nil {
		t.Fatal("NewAuthenticator disabled authentication while the provider was down")
	}

	login := func() int {
		recorder := httptest.NewRecorder()
		authenticator.HandleLogin(recorder, httptest.NewRequest("GET", "/login", nil))
		return recorder.Code
	}
	if code := login(); code != http.StatusServiceUnavailable {
		t.Fatalf("login before discovery: status=%d, want 503", code)
	}

	providerUp.Store(true)
	deadline := time.Now().Add(2 * time.Second)
	for login() != http.StatusFound {
		if time.Now().After(deadline) {
			t.Fatal("login did not recover after the provider came up")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
