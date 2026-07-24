package auth

import (
	"net/http/httptest"
	"testing"
)

func TestClientIPTrustsForwardingOnlyWhenConfigured(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "192.0.2.10:4321"
	request.Header.Set("X-Forwarded-For", "198.51.100.20, 192.0.2.1")

	if got := ClientIP(request, false); got != "192.0.2.10" {
		t.Fatalf("untrusted proxy address = %q", got)
	}
	if got := ClientIP(request, true); got != "198.51.100.20" {
		t.Fatalf("trusted proxy address = %q", got)
	}
}

func TestClientIPRejectsInvalidForwardedAddress(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "[2001:db8::10]:4321"
	request.Header.Set("CF-Connecting-IP", "not-an-address")

	if got := ClientIP(request, true); got != "2001:db8::10" {
		t.Fatalf("fallback address = %q", got)
	}
}
