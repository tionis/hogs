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

func TestSecurityHeadersScopeUnsafeEvalToMapProxy(t *testing.T) {
	handler := securityHeadersMiddleware(&config.Config{})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, test := range []struct {
		path           string
		wantUnsafeEval bool
	}{
		{path: "/cog/map/", wantUnsafeEval: true},
		{path: "/cog/map/assets/main.js", wantUnsafeEval: true},
		{path: "/cog", wantUnsafeEval: false},
		{path: "/map/", wantUnsafeEval: false},
		{path: "/admin/map/settings", wantUnsafeEval: false},
	} {
		t.Run(test.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
			got := strings.Contains(recorder.Header().Get("Content-Security-Policy"), "'unsafe-eval'")
			if got != test.wantUnsafeEval {
				t.Fatalf("unsafe-eval present = %v, want %v", got, test.wantUnsafeEval)
			}
		})
	}
}
