package agent

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tionis/hogs/internal/capability"
)

func TestServerResourcesRejectsStaleObservations(t *testing.T) {
	manager := &Manager{resources: map[string]ResourceStatus{
		"fresh": {SampledAt: time.Now().Add(-5 * time.Second)},
		"stale": {SampledAt: time.Now().Add(-time.Minute)},
	}}

	if _, found := manager.ServerResources("fresh"); !found {
		t.Fatal("fresh resource observation was rejected")
	}
	if _, found := manager.ServerResources("stale"); found {
		t.Fatal("stale resource observation was accepted")
	}
	if _, found := manager.ServerResources("missing"); found {
		t.Fatal("missing resource observation was accepted")
	}
}

func TestDirectAccessUsesPublicURLAndExactScope(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	manager := &Manager{nodes: map[string]ManagedNode{
		"worker-a": {
			Node: "worker-a", Mode: "direct",
			ControlURL: "https://control.example.test",
			PublicURL:  "https://worker.example.test",
			secret:     secret,
		},
	}}
	access, err := manager.DirectAccess("worker-a", "admin@example.test", http.MethodGet,
		"/v1/servers/alpha/file?path=world%2Fdata.bin", "world/data.bin", 0)
	if err != nil {
		t.Fatal(err)
	}
	if access.Mode != "direct" || access.URL != "https://worker.example.test/v1/servers/alpha/file?path=world%2Fdata.bin" {
		t.Fatalf("unexpected direct access: %#v", access)
	}
	claims, err := capability.Verify(secret, access.Token, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := capability.Authorize(claims, "worker-a", http.MethodGet,
		"/v1/servers/alpha/file", "world/data.bin"); err != nil {
		t.Fatal(err)
	}
}

func TestManagedNodeModesAreValidated(t *testing.T) {
	direct := ManagedNode{
		Node: "direct", Mode: "direct", ControlURL: "https://worker.example.test",
		PublicURL: "https://worker.example.test", SecretFile: "/run/secret",
	}
	if err := validateManagedNode(direct); err != nil {
		t.Fatal(err)
	}
	direct.ControlURL = "http://worker.example.test"
	if err := validateManagedNode(direct); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("insecure direct URL error=%v", err)
	}
	tunneled := ManagedNode{
		Node: "tunnel", Mode: "tunneled", ControlURL: "http://[fd00::2]:9081",
		SecretFile: "/run/secret",
	}
	if err := validateManagedNode(tunneled); err != nil {
		t.Fatal(err)
	}
}
