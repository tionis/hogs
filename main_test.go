package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tionis/hogs/config"
)

func TestSecurityHeadersAllowSameOriginFrames(t *testing.T) {
	handler := securityHeadersMiddleware(&config.Config{})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/server/map/", nil))

	if got := recorder.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Fatalf("X-Frame-Options = %q, want SAMEORIGIN", got)
	}
	if got := recorder.Header().Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'self'") {
		t.Fatalf("Content-Security-Policy = %q, want same-origin frame policy", got)
	}
}
