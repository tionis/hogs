package auth

import (
	"net/http/httptest"
	"testing"
)

func TestNilAuthenticatorAccessorsStaySafe(t *testing.T) {
	var authenticator *Authenticator
	request := httptest.NewRequest("GET", "/servers/demo/console", nil)
	if authenticator.IsAuthenticated(request) {
		t.Fatal("nil authenticator reported an authenticated request")
	}
	if username := authenticator.GetUsername(request); username != "" {
		t.Fatalf("nil authenticator username=%q", username)
	}
	if role := authenticator.GetUserRole(request); role != "" {
		t.Fatalf("nil authenticator role=%q", role)
	}
	if sessionID := authenticator.GetSessionID(request); sessionID != "" {
		t.Fatalf("nil authenticator session=%q", sessionID)
	}
}
