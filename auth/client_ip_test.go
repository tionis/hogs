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

func TestClientIPPrefersStandardProxyAddress(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "192.0.2.10:4321"
	request.Header.Set("X-Forwarded-For", "198.51.100.20")
	request.Header.Set("CF-Connecting-IP", "203.0.113.99")

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

func TestClientCountryRequiresTrustedProxy(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("CDN-Country-Code", "de")
	if got := ClientCountry(request, false); got != "" {
		t.Fatalf("untrusted country = %q", got)
	}
	if got := ClientCountry(request, true); got != "DE" {
		t.Fatalf("trusted country = %q", got)
	}
}
