package capability

import (
	"net/http"
	"testing"
	"time"
)

func TestCapabilityRoundTripAndScope(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	claims := NewClaims("worker-a", "admin@example.test", http.MethodPut,
		"/v1/servers/alpha/file", "world/data.bin", 4096, time.Minute)
	token, err := Sign(secret, claims)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := Verify(secret, token, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := Authorize(verified, "worker-a", http.MethodPut,
		"/v1/servers/alpha/file", "world/data.bin"); err != nil {
		t.Fatal(err)
	}
	if err := Authorize(verified, "worker-a", http.MethodDelete,
		"/v1/servers/alpha/file", "world/data.bin"); err == nil {
		t.Fatal("capability authorized a different method")
	}
	if err := Authorize(verified, "worker-a", http.MethodPut,
		"/v1/servers/alpha/file", "world/other.bin"); err == nil {
		t.Fatal("capability authorized a different path")
	}
}

func TestCapabilityRejectsTamperingAndExpiry(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	claims := NewClaims("worker-a", "hogs-control", http.MethodGet,
		"/v1/health", "", 0, time.Minute)
	token, err := Sign(secret, claims)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(secret, token+"x", time.Now()); err == nil {
		t.Fatal("tampered capability was accepted")
	}
	if _, err := Verify(secret, token, time.Unix(claims.Expires+1, 0)); err == nil {
		t.Fatal("expired capability was accepted")
	}
}

func TestCapabilityBindsFileOperationDestination(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	claims := NewClaims("worker-a", "admin@example.test", http.MethodPost,
		"/v1/servers/alpha/file-operations", "config/source.txt", 0, time.Minute)
	claims.TargetPath = "config/target.txt"
	token, err := Sign(secret, claims)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := Verify(secret, token, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := AuthorizePaths(verified, "worker-a", http.MethodPost,
		"/v1/servers/alpha/file-operations", "config/source.txt", "config/target.txt"); err != nil {
		t.Fatal(err)
	}
	if err := AuthorizePaths(verified, "worker-a", http.MethodPost,
		"/v1/servers/alpha/file-operations", "config/source.txt", "world/escaped.txt"); err == nil {
		t.Fatal("capability authorized a different destination")
	}
}
