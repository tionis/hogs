package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/tionis/hogs/database"
)

func testInventoryHandler(t *testing.T) (*InventoryHandler, *database.Store) {
	t.Helper()
	database.APIKeyPepper = "inventory-test-pepper"
	dbPath := t.TempDir() + "/test.db"
	store, err := database.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { store.DB.Close(); os.Remove(dbPath) })
	return NewInventoryHandler(store), store
}

func testManifest() InventoryManifest {
	return InventoryManifest{
		APIVersion: InventoryAPIVersion,
		Generation: "git:abc123",
		Nodes: []InventoryNode{{
			Name: "destiny", NodeName: "destiny", Labels: map[string]string{"site": "cloud"},
			DesiredCapabilities: []string{"action", "backup", "console", "files"}, TokenState: "active",
			Token: "hogs_0000000000000000000000000000000000000000000000000000000000000000",
		}},
		Servers: []InventoryServer{{
			Name: "cog", Address: "cog.internal:25565", Description: "Managed Minecraft",
			State: "online", GameType: "minecraft", ShowMOTD: true,
			Metadata: map[string]string{"edition": "java", "rcon_password": "do-not-return"}, Tags: []string{"game", "minecraft"},
			Unit: "cog.service", DataPath: "/srv/cog", Backend: InventoryBackend{Type: "agent", Node: "destiny"},
			Policy: InventoryServerPolicy{ACLRule: `user.Role == "admin"`, AllowedActions: []string{"restart", "start", "stop"},
				Operators: []string{"games-admins"}, Console: true, Start: true, Stop: true, Backup: true, Restore: true,
				WritablePaths: []string{"/srv/cog/config", "/srv/cog/world"}},
			Commands: []InventoryCommand{{Name: "say", DisplayName: "Say", Template: "say {message}", Params: json.RawMessage(`{"message":{"type":"string","required":true}}`), Enabled: true}},
		}},
		Constraints:   []InventoryConstraint{{Name: "one_game", Condition: "true", Strategy: "deny", Priority: 10, Enabled: true}},
		Schedules:     []InventorySchedule{{Name: "nightly_restart", Schedule: "0 0 4 * * *", ServerName: "cog", Action: "restart", Params: json.RawMessage(`{}`), Enabled: true}},
		Templates:     []InventoryTemplate{{Name: "minecraft", GameType: "minecraft", DefaultSettings: json.RawMessage(`{}`), DefaultCommands: json.RawMessage(`[]`), DefaultTags: json.RawMessage(`["minecraft"]`)}},
		Webhooks:      []InventoryWebhook{{Name: "audit", URL: "https://hooks.example.test/hogs", Secret: "webhook-secret", Events: json.RawMessage(`["*"]`), Enabled: true}},
		Notifications: []InventoryNotification{{Name: "ops", Type: "ntfy", URL: "ntfy://token@example.test/topic", Events: json.RawMessage(`["server_down"]`), Enabled: true}},
		Settings:      map[string]string{"site_name": "Managed HOGS", "integration_token": "setting-secret"},
	}
}

func requestInventory(t *testing.T, method, path string, manifest InventoryManifest) *http.Request {
	t.Helper()
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func decodeResponse(t *testing.T, recorder *httptest.ResponseRecorder, target interface{}) {
	t.Helper()
	if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
		t.Fatalf("invalid response JSON: %v: %s", err, recorder.Body.String())
	}
}

func TestInventoryApplyIsIdempotentAndNeverReturnsAgentToken(t *testing.T) {
	handler, store := testInventoryHandler(t)
	manifest := testManifest()

	planRecorder := httptest.NewRecorder()
	handler.Plan(planRecorder, requestInventory(t, http.MethodPost, "/api/v1/inventory/plan", manifest))
	if planRecorder.Code != http.StatusOK {
		t.Fatalf("plan status=%d body=%s", planRecorder.Code, planRecorder.Body.String())
	}
	var plan struct {
		Changes []InventoryChange `json:"changes"`
	}
	decodeResponse(t, planRecorder, &plan)
	if len(plan.Changes) == 0 {
		t.Fatal("initial plan should create resources")
	}

	applyRecorder := httptest.NewRecorder()
	handler.Apply(applyRecorder, requestInventory(t, http.MethodPut, "/api/v1/inventory", manifest))
	if applyRecorder.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", applyRecorder.Code, applyRecorder.Body.String())
	}
	if bytes.Contains(applyRecorder.Body.Bytes(), []byte(manifest.Nodes[0].Token)) || bytes.Contains(applyRecorder.Body.Bytes(), []byte("credentials")) {
		t.Fatal("apply response exposed agent credential material")
	}

	agent, err := store.GetAgentByNodeName("destiny")
	if err != nil || agent == nil {
		t.Fatalf("agent not persisted: %v", err)
	}
	server, err := store.GetServerByName("cog")
	if err != nil || server == nil {
		t.Fatalf("server not persisted: %v", err)
	}
	link, err := store.GetPterodactylLink(server.ID)
	if err != nil || link == nil || link.Node != "destiny" {
		t.Fatalf("agent backend not persisted: %#v err=%v", link, err)
	}

	secondPlan := httptest.NewRecorder()
	handler.Plan(secondPlan, requestInventory(t, http.MethodPost, "/api/v1/inventory/plan", manifest))
	var stable struct {
		Changes []InventoryChange `json:"changes"`
	}
	decodeResponse(t, secondPlan, &stable)
	if len(stable.Changes) != 0 {
		t.Fatalf("second plan is not idempotent: %#v", stable.Changes)
	}

	secondApply := httptest.NewRecorder()
	handler.Apply(secondApply, requestInventory(t, http.MethodPut, "/api/v1/inventory", manifest))
	if secondApply.Code != http.StatusOK {
		t.Fatalf("stable apply failed: %s", secondApply.Body.String())
	}
}

func TestInventoryStateRedactsSecrets(t *testing.T) {
	handler, _ := testInventoryHandler(t)
	manifest := testManifest()
	recorder := httptest.NewRecorder()
	handler.Apply(recorder, requestInventory(t, http.MethodPut, "/api/v1/inventory", manifest))
	if recorder.Code != http.StatusOK {
		t.Fatalf("apply failed: %s", recorder.Body.String())
	}

	stateRecorder := httptest.NewRecorder()
	handler.GetState(stateRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/inventory", nil))
	if stateRecorder.Code != http.StatusOK {
		t.Fatalf("state failed: %s", stateRecorder.Body.String())
	}
	body := stateRecorder.Body.String()
	for _, secret := range []string{"webhook-secret", "ntfy://token@", "setting-secret", "do-not-return"} {
		if bytes.Contains([]byte(body), []byte(secret)) {
			t.Fatalf("state leaked %q", secret)
		}
	}
	if !bytes.Contains([]byte(body), []byte(`"secret":"***"`)) {
		t.Fatal("webhook secret redaction marker missing")
	}
	var storedManifest string
	if err := handler.Store.DB.QueryRow("SELECT manifest FROM inventory_state WHERE singleton=1").Scan(&storedManifest); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains([]byte(storedManifest), []byte(manifest.Nodes[0].Token)) {
		t.Fatal("inventory state persisted a plaintext agent token")
	}
}

func TestInventoryPruneRequiresConfirmation(t *testing.T) {
	handler, store := testInventoryHandler(t)
	manifest := testManifest()
	first := httptest.NewRecorder()
	handler.Apply(first, requestInventory(t, http.MethodPut, "/api/v1/inventory", manifest))
	if first.Code != http.StatusOK {
		t.Fatalf("initial apply failed: %s", first.Body.String())
	}

	manifest.Servers = []InventoryServer{}
	manifest.Schedules = []InventorySchedule{}
	manifest.Generation = "git:def456"
	blocked := httptest.NewRecorder()
	handler.Apply(blocked, requestInventory(t, http.MethodPut, "/api/v1/inventory", manifest))
	if blocked.Code != http.StatusConflict {
		t.Fatalf("prune without confirmation status=%d", blocked.Code)
	}
	server, _ := store.GetServerByName("cog")
	if server == nil {
		t.Fatal("blocked prune removed the server")
	}

	req := requestInventory(t, http.MethodPut, "/api/v1/inventory", manifest)
	req.Header.Set("X-HOGS-Confirm-Prune", "true")
	confirmed := httptest.NewRecorder()
	handler.Apply(confirmed, req)
	if confirmed.Code != http.StatusOK {
		t.Fatalf("confirmed prune failed: %s", confirmed.Body.String())
	}
	server, _ = store.GetServerByName("cog")
	if server != nil {
		t.Fatal("confirmed prune did not remove the server")
	}
}

func TestInventoryTokenRotationAndEvents(t *testing.T) {
	handler, _ := testInventoryHandler(t)
	manifest := testManifest()
	first := httptest.NewRecorder()
	handler.Apply(first, requestInventory(t, http.MethodPut, "/api/v1/inventory", manifest))

	manifest.Generation = "git:rotate"
	manifest.Nodes[0].RotateToken = true
	manifest.Nodes[0].Token = "hogs_1111111111111111111111111111111111111111111111111111111111111111"
	rotated := httptest.NewRecorder()
	handler.Apply(rotated, requestInventory(t, http.MethodPut, "/api/v1/inventory", manifest))
	if rotated.Code != http.StatusOK {
		t.Fatalf("rotate failed: %s", rotated.Body.String())
	}
	if bytes.Contains(rotated.Body.Bytes(), []byte(manifest.Nodes[0].Token)) {
		t.Fatal("rotated credential leaked in response")
	}

	events := httptest.NewRecorder()
	handler.Events(events, httptest.NewRequest(http.MethodGet, "/api/v1/inventory/events?after=0", nil))
	if events.Code != http.StatusOK {
		t.Fatalf("events failed: %s", events.Body.String())
	}
	if !bytes.Contains(events.Body.Bytes(), []byte(`"action":"rotate"`)) {
		t.Fatal("rotation event missing")
	}
	retry := httptest.NewRecorder()
	handler.Apply(retry, requestInventory(t, http.MethodPut, "/api/v1/inventory", manifest))
	if retry.Code != http.StatusOK || bytes.Contains(retry.Body.Bytes(), []byte(`"action":"rotate"`)) {
		t.Fatalf("rotation retry was not idempotent: %s", retry.Body.String())
	}
}

func TestInventoryTokenRevocationIsExplicitAndAudited(t *testing.T) {
	handler, store := testInventoryHandler(t)
	manifest := testManifest()
	first := httptest.NewRecorder()
	handler.Apply(first, requestInventory(t, http.MethodPut, "/api/v1/inventory", manifest))
	if first.Code != http.StatusOK {
		t.Fatalf("initial apply failed: %s", first.Body.String())
	}

	manifest.Generation = "git:revoke"
	manifest.Nodes[0].TokenState = "revoked"
	manifest.Nodes[0].Token = ""
	revoked := httptest.NewRecorder()
	handler.Apply(revoked, requestInventory(t, http.MethodPut, "/api/v1/inventory", manifest))
	if revoked.Code != http.StatusOK {
		t.Fatalf("revoke failed: %s", revoked.Body.String())
	}
	var tokenHash, tokenPrefix string
	if err := store.DB.QueryRow("SELECT token_hash, token_prefix FROM agents WHERE name=?", "destiny").Scan(&tokenHash, &tokenPrefix); err != nil {
		t.Fatal(err)
	}
	if tokenHash != "" || tokenPrefix != "" {
		t.Fatal("revoked agent still has usable token material")
	}

	events := httptest.NewRecorder()
	handler.Events(events, httptest.NewRequest(http.MethodGet, "/api/v1/inventory/events?after=0", nil))
	if !bytes.Contains(events.Body.Bytes(), []byte(`"generation":"git:revoke"`)) || !bytes.Contains(events.Body.Bytes(), []byte(`"resourceKey":"destiny"`)) {
		t.Fatalf("revocation was not audited: %s", events.Body.String())
	}
}

func TestInventoryApplyReloadsRuntimeState(t *testing.T) {
	handler, _ := testInventoryHandler(t)
	calls := 0
	handler.SetAfterApply(func(_ []InventoryChange) error {
		calls++
		return nil
	})
	recorder := httptest.NewRecorder()
	handler.Apply(recorder, requestInventory(t, http.MethodPut, "/api/v1/inventory", testManifest()))
	if recorder.Code != http.StatusOK || calls != 1 {
		t.Fatalf("runtime reload status=%d calls=%d body=%s", recorder.Code, calls, recorder.Body.String())
	}
}

func TestInventoryValidationRejectsUnknownNode(t *testing.T) {
	handler, _ := testInventoryHandler(t)
	manifest := testManifest()
	manifest.Servers[0].Backend.Node = "missing"
	recorder := httptest.NewRecorder()
	handler.Plan(recorder, requestInventory(t, http.MethodPost, "/api/v1/inventory/plan", manifest))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
