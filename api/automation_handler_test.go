package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/tionis/hogs/access"
	"github.com/tionis/hogs/auth"
	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/engine"
	"github.com/tionis/hogs/query"
)

func testAutomationHandler(t *testing.T) (*AutomationHandler, *database.Store) {
	t.Helper()
	dbPath := t.TempDir() + "/test.db"
	store, err := database.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() {
		store.DB.Close()
		os.Remove(dbPath)
	})
	cfg := &config.Config{AuditLogRetentionDays: 90}
	cache := query.NewServerStatusCache()
	eng := engine.NewEngine(store, cfg, cache)
	return NewAutomationHandler(store, cfg, eng), store
}

func TestServerAutomationMutationIsCapabilityCheckedAndServerScoped(t *testing.T) {
	handler, store := testAutomationHandler(t)
	for _, name := range []string{"Alpha", "Beta"} {
		if err := store.CreateServer(&database.Server{Name: name, GameType: "generic", State: "online"}); err != nil {
			t.Fatal(err)
		}
	}
	alpha, _ := store.GetServerByName("Alpha")
	beta, _ := store.GetServerByName("Beta")
	if err := store.SetServerAccessGrant(&database.ServerAccessGrant{
		ServerID: alpha.ID, SubjectType: "user", Subject: "operator", Effect: "allow",
		Capabilities: []string{access.AutomationManage},
	}); err != nil {
		t.Fatal(err)
	}
	authenticator := auth.NewTestAuthenticator(store, "automation-scope-test-secret")
	handler.SetAuthenticator(authenticator)
	sessionRequest := managedTestRequest(t, store, authenticator, "operator", "user")

	form := url.Values{
		"name": {"alpha_idle"}, "schedule": {"0 * * * * *"}, "action": {"stop"},
		"condition": {"true"}, "stability_seconds": {"0"}, "cooldown_seconds": {"0"},
		"params": {"{}"}, "enabled": {"on"}, "server_id": {strconv.Itoa(beta.ID)},
	}
	req := httptest.NewRequest(http.MethodPost, "/servers/Alpha/automation/add", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = mux.SetURLVars(req, map[string]string{"serverName": "Alpha"})
	for _, cookie := range sessionRequest.Cookies() {
		req.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	handler.AddCronJob(recorder, req)
	if recorder.Code != http.StatusFound {
		t.Fatalf("add status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	alphaJobs, _ := store.ListCronJobsForServer(alpha.ID)
	betaJobs, _ := store.ListCronJobsForServer(beta.ID)
	if len(alphaJobs) != 1 || len(betaJobs) != 0 {
		t.Fatalf("forged server_id escaped route scope: alpha=%d beta=%d", len(alphaJobs), len(betaJobs))
	}

	betaRule := &database.CronJob{Name: "beta_rule", Schedule: "0 * * * * *", ServerID: beta.ID, Action: "stop", Enabled: true}
	if err := store.CreateCronJob(betaRule); err != nil {
		t.Fatal(err)
	}
	form.Set("id", strconv.Itoa(betaRule.ID))
	form.Set("name", "hijacked")
	req = httptest.NewRequest(http.MethodPost, "/servers/Alpha/automation/update", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = mux.SetURLVars(req, map[string]string{"serverName": "Alpha"})
	for _, cookie := range sessionRequest.Cookies() {
		req.AddCookie(cookie)
	}
	recorder = httptest.NewRecorder()
	handler.UpdateCronJob(recorder, req)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("cross-server update status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	unchanged, _ := store.GetCronJob(betaRule.ID)
	if unchanged.Name != "beta_rule" {
		t.Fatal("cross-server update changed another server's rule")
	}
}

func TestBulkTags(t *testing.T) {
	handler, store := testAutomationHandler(t)

	store.CreateServer(&database.Server{Name: "Alpha", GameType: "minecraft", State: "online"})
	store.CreateServer(&database.Server{Name: "Beta", GameType: "valheim", State: "offline"})

	payload, _ := json.Marshal(map[string]interface{}{
		"servers": []string{"Alpha", "Beta"},
		"tags":    []string{"production", "eu-west"},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/admin/bulk-tags", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.BulkTags(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if resp["updated"] != float64(2) {
		t.Errorf("expected updated=2, got %v", resp["updated"])
	}

	// Verify tags were actually set
	alpha, _ := store.GetServerByName("Alpha")
	beta, _ := store.GetServerByName("Beta")
	alphaTags, _ := store.GetServerTags(alpha.ID)
	betaTags, _ := store.GetServerTags(beta.ID)
	if len(alphaTags) != 2 || !containsTag(alphaTags, "production") || !containsTag(alphaTags, "eu-west") {
		t.Errorf("Alpha tags = %v, want [production eu-west]", alphaTags)
	}
	if len(betaTags) != 2 || !containsTag(betaTags, "production") || !containsTag(betaTags, "eu-west") {
		t.Errorf("Beta tags = %v, want [production eu-west]", betaTags)
	}
}

func containsTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

func TestBulkACL(t *testing.T) {
	handler, store := testAutomationHandler(t)

	store.CreateServer(&database.Server{Name: "Gamma", GameType: "minecraft", State: "online"})
	store.CreateServer(&database.Server{Name: "Delta", GameType: "valheim", State: "offline"})

	// Create pterodactyl links
	gamma, _ := store.GetServerByName("Gamma")
	delta, _ := store.GetServerByName("Delta")
	store.DB.Exec("INSERT INTO pterodactyl_servers (server_id, ptero_server_id, ptero_identifier, allowed_actions, acl_rule) VALUES (?, ?, ?, ?, ?)",
		gamma.ID, "uuid1", "id1", "[\"start\"]", "")
	store.DB.Exec("INSERT INTO pterodactyl_servers (server_id, ptero_server_id, ptero_identifier, allowed_actions, acl_rule) VALUES (?, ?, ?, ?, ?)",
		delta.ID, "uuid2", "id2", "[\"start\"]", "")

	payload, _ := json.Marshal(map[string]interface{}{
		"servers":  []string{"Gamma", "Delta"},
		"acl_rule": "user.Role == \"admin\"",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/admin/bulk-acl", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.BulkACL(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if resp["updated"] != float64(2) {
		t.Errorf("expected updated=2, got %v", resp["updated"])
	}

	// Verify ACL rules were actually set
	link1, _ := store.GetPterodactylLink(gamma.ID)
	link2, _ := store.GetPterodactylLink(delta.ID)
	if link1.ACLRule != "user.Role == \"admin\"" {
		t.Errorf("Gamma ACL = %q, want admin rule", link1.ACLRule)
	}
	if link2.ACLRule != "user.Role == \"admin\"" {
		t.Errorf("Delta ACL = %q, want admin rule", link2.ACLRule)
	}
}
