package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
)

func secretFieldValue(t *testing.T, handler *InventoryHandler, serverID int, key string) string {
	t.Helper()
	fields, err := handler.Store.ListServerFields(serverID)
	if err != nil {
		t.Fatalf("list server fields: %v", err)
	}
	for _, field := range fields {
		if field.Key != key {
			continue
		}
		if field.Disclosure != "write_only" || field.Placement != "internal" {
			t.Fatalf("secret field %q has wrong shape: %+v", key, field)
		}
		if field.Value != "" {
			t.Fatalf("secret field %q leaked plaintext in list view", key)
		}
		value, err := handler.Store.GetServerFieldValue(serverID, field.ID)
		if err != nil {
			t.Fatalf("open secret field %q: %v", key, err)
		}
		return value
	}
	t.Fatalf("secret field %q not found", key)
	return ""
}

func planChanges(t *testing.T, handler *InventoryHandler, manifest InventoryManifest) []InventoryChange {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.Plan(recorder, requestInventory(t, http.MethodPost, "/api/v1/inventory/plan", manifest))
	if recorder.Code != http.StatusOK {
		t.Fatalf("plan status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var plan struct {
		Changes []InventoryChange `json:"changes"`
	}
	decodeResponse(t, recorder, &plan)
	return plan.Changes
}

func applyManifestRequest(t *testing.T, handler *InventoryHandler, manifest InventoryManifest) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.Apply(recorder, requestInventory(t, http.MethodPut, "/api/v1/inventory", manifest))
	if recorder.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestInventorySecretFieldsRoundTrip(t *testing.T) {
	handler, store := testInventoryHandler(t)
	if err := store.ConfigureServerFieldEncryption("test-server-field-key-material-000000000000"); err != nil {
		t.Fatalf("configure field encryption: %v", err)
	}
	manifest := testManifest()
	manifest.Servers[0].SecretFields = map[string]string{"api_token": "sat-token-1"}

	if changes := planChanges(t, handler, manifest); len(changes) == 0 {
		t.Fatal("initial plan should create resources")
	}
	applyManifestRequest(t, handler, manifest)

	server, err := store.GetServerByName("cog")
	if err != nil || server == nil {
		t.Fatalf("server not persisted: %v", err)
	}
	if got := secretFieldValue(t, handler, server.ID, "api_token"); got != "sat-token-1" {
		t.Fatalf("sealed secret round trip mismatch: %q", got)
	}
	if token, ok := server.Metadata["api_token"]; !ok || token != "sat-token-1" {
		t.Fatal("sealed secret missing from runtime metadata view")
	}

	stateRecorder := httptest.NewRecorder()
	handler.GetState(stateRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/inventory", nil))
	if stateRecorder.Code != http.StatusOK {
		t.Fatalf("state status=%d", stateRecorder.Code)
	}
	stateBody := stateRecorder.Body.String()
	if strings.Contains(stateBody, "sat-token-1") {
		t.Fatal("state leaked secret plaintext")
	}
	if !strings.Contains(stateBody, `"secretFields":{"api_token":"***"}`) {
		t.Fatalf("state did not mask secret fields: %s", stateBody)
	}
	var storedManifest string
	if err := store.DB.QueryRow("SELECT manifest FROM inventory_state WHERE singleton=1").Scan(&storedManifest); err != nil {
		t.Fatalf("read stored manifest: %v", err)
	}
	if strings.Contains(storedManifest, "sat-token-1") {
		t.Fatal("stored manifest holds secret plaintext")
	}
	if !strings.Contains(storedManifest, "hmac-sha256:") {
		t.Fatal("stored manifest should carry fingerprints, not plaintext")
	}

	if changes := planChanges(t, handler, manifest); len(changes) != 0 {
		t.Fatalf("second plan is not idempotent: %#v", changes)
	}

	manifest.Servers[0].SecretFields = map[string]string{"api_token": "sat-token-2"}
	changes := planChanges(t, handler, manifest)
	found := false
	for _, change := range changes {
		if change.Resource == "servers/cog" && change.Action == "update" {
			found = true
		}
	}
	if !found {
		t.Fatalf("rotation did not register as update: %#v", changes)
	}
	applyManifestRequest(t, handler, manifest)
	if got := secretFieldValue(t, handler, server.ID, "api_token"); got != "sat-token-2" {
		t.Fatalf("rotated secret mismatch: %q", got)
	}

	manifest.Servers[0].SecretFields = map[string]string{"api_token": ""}
	applyManifestRequest(t, handler, manifest)
	fields, err := store.ListServerFields(server.ID)
	if err != nil {
		t.Fatalf("list fields after removal: %v", err)
	}
	for _, field := range fields {
		if field.Key == "api_token" {
			t.Fatal("removed secret field still present")
		}
	}
}

func TestInventorySecretFieldsRejectUnknownKey(t *testing.T) {
	handler, _ := testInventoryHandler(t)
	manifest := testManifest()
	manifest.Servers[0].SecretFields = map[string]string{"game_password": "nope"}
	recorder := httptest.NewRecorder()
	handler.Plan(recorder, requestInventory(t, http.MethodPost, "/api/v1/inventory/plan", manifest))
	if recorder.Code != http.StatusBadRequest ||
		!strings.Contains(recorder.Body.String(), "not automation-managed") {
		t.Fatalf("unknown secret key status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestServerSecretFieldsEndpoint(t *testing.T) {
	handler, store := testInventoryHandler(t)
	if err := store.ConfigureServerFieldEncryption("test-server-field-key-material-000000000000"); err != nil {
		t.Fatalf("configure field encryption: %v", err)
	}
	applyManifestRequest(t, handler, testManifest())

	setFields := func(serverRef string, fields map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]interface{}{"fields": fields})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPut, "/api/v1/servers/"+serverRef+"/secret-fields", bytes.NewReader(body))
		req = mux.SetURLVars(req, map[string]string{"serverName": serverRef})
		recorder := httptest.NewRecorder()
		handler.SetServerSecretFields(recorder, req)
		return recorder
	}

	recorder := setFields("cog", map[string]string{"api_token": "endpoint-token"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("endpoint status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var result struct {
		Server  string   `json:"server"`
		Updated []string `json:"updated"`
		Removed []string `json:"removed"`
	}
	decodeResponse(t, recorder, &result)
	if result.Server != "cog" || len(result.Updated) != 1 || result.Updated[0] != "api_token" {
		t.Fatalf("unexpected endpoint result: %s", recorder.Body.String())
	}
	server, err := store.GetServerByName("cog")
	if err != nil || server == nil {
		t.Fatalf("server lookup: %v", err)
	}
	if got := secretFieldValue(t, handler, server.ID, "api_token"); got != "endpoint-token" {
		t.Fatalf("endpoint secret mismatch: %q", got)
	}

	recorder = setFields("cog", map[string]string{"nope": "x"})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown key status=%d", recorder.Code)
	}
	recorder = setFields("missing", map[string]string{"api_token": "x"})
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing server status=%d", recorder.Code)
	}
}
