package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/tionis/hogs/auth"
	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/engine"
	"github.com/tionis/hogs/query"
)

func testStore(t *testing.T) *database.Store {
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
	return store
}

func testWebHandler(t *testing.T) (*WebHandler, *database.Store, *auth.Authenticator) {
	t.Helper()
	store := testStore(t)
	cfg := &config.Config{
		GameDataPath:          t.TempDir(),
		AuditLogRetentionDays: 90,
	}
	cache := query.NewServerStatusCache()
	eng := engine.NewEngine(store, cfg, cache)

	authenticator := auth.NewTestAuthenticator(store, "test-session-secret-for-tests-only")

	return NewWebHandler(store, cfg, authenticator, eng), store, authenticator
}

func createTestSession(t *testing.T, store *database.Store, authenticator *auth.Authenticator, email, role string) *http.Cookie {
	t.Helper()
	// Create user in DB
	_, err := store.DB.Exec("INSERT INTO users (email, role, active) VALUES (?, ?, 1) ON CONFLICT(email) DO UPDATE SET role = ?", email, role, role)
	if err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}

	// Create DB session
	sessionID := "test-session-" + email
	_, err = store.DB.Exec(
		"INSERT INTO sessions (session_id, user_email, user_role, expires_at) VALUES (?, ?, ?, ?)",
		sessionID, email, role, time.Now().UTC().Add(24*time.Hour).Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("failed to create test session: %v", err)
	}

	// Create cookie with session_id using the same cookie store as the authenticator
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	session, _ := authenticator.CookieStore().Get(req, "hogs-session")
	session.Values["session_id"] = sessionID
	session.Save(req, w)

	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "hogs-session" {
			cookie = c
			break
		}
	}
	if cookie == nil {
		t.Fatal("failed to create session cookie")
	}
	return cookie
}

func TestDashboardRenders(t *testing.T) {
	handler, store, auth := testWebHandler(t)

	// Create test servers
	store.CreateServer(&database.Server{Name: "Alpha", GameType: "minecraft", State: "online"})
	store.CreateServer(&database.Server{Name: "Beta", GameType: "valheim", State: "offline"})

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard", nil)
	w := httptest.NewRecorder()

	// Without auth, the RequireRole middleware would block, but we're testing the handler directly
	// In real usage, the handler is wrapped by RequireRole("admin")
	// For this test, we call the handler directly since we trust the middleware
	cookie := createTestSession(t, store, auth, "admin@test.com", "admin")
	req.AddCookie(cookie)

	handler.Dashboard(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !contains(body, "Total Servers") {
		t.Error("expected dashboard to contain 'Total Servers'")
	}
	if !contains(body, "2") {
		t.Error("expected dashboard to contain server count")
	}
	if !contains(body, "Minecraft") {
		t.Error("expected dashboard to contain game type")
	}
}

func TestAdminRenders(t *testing.T) {
	handler, store, auth := testWebHandler(t)
	store.CreateServer(&database.Server{Name: "TestSrv", GameType: "minecraft", State: "online"})

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	w := httptest.NewRecorder()
	cookie := createTestSession(t, store, auth, "admin@test.com", "admin")
	req.AddCookie(cookie)

	handler.Admin(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	if !contains(w.Body.String(), "TestSrv") {
		t.Error("expected admin page to contain server name")
	}
}

func TestAdminRoleFailureRendersForbiddenPage(t *testing.T) {
	handler, store, authenticator := testWebHandler(t)
	authenticator.SetForbiddenHandler(http.HandlerFunc(handler.Forbidden))

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(createTestSession(t, store, authenticator, "player@test.com", "user"))
	w := httptest.NewRecorder()

	authenticator.RequireRole("admin")(http.HandlerFunc(handler.Admin)).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		"You don&rsquo;t have access to this page",
		"player@test.com",
		"Go to My Servers",
		"Browse Game Servers",
	} {
		if !contains(body, want) {
			t.Errorf("expected forbidden page to contain %q", want)
		}
	}
}

func TestHomeRenders(t *testing.T) {
	handler, store, _ := testWebHandler(t)
	store.CreateServer(&database.Server{Name: "PublicSrv", GameType: "minecraft", State: "online"})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	handler.Home(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	if !contains(w.Body.String(), "PublicSrv") {
		t.Error("expected home page to contain server name")
	}
}

func TestAdminGameTypesIncludeCustomServerType(t *testing.T) {
	types := adminGameTypes([]database.Server{{GameType: "custom_game"}})
	if !containsString(types, "custom_game") || !containsString(types, "minecraft") {
		t.Fatalf("admin game types = %#v", types)
	}
}

func TestBackgroundGameTagsOnlyUseConfiguredServers(t *testing.T) {
	options := AvailableBackgroundTags([]string{"factorio"})
	var values []string
	for _, option := range options {
		values = append(values, option.Value)
	}
	if !containsString(values, "factorio") || containsString(values, "valheim") {
		t.Fatalf("background tag values = %#v", values)
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func TestServerDetailRenders(t *testing.T) {
	handler, store, _ := testWebHandler(t)
	store.CreateServer(&database.Server{
		Name: "DetailSrv", GameType: "minecraft", State: "online", Address: "play.example.test",
		Metadata: map[string]string{"directAddress": "node.example.test:25565"},
	})

	req := httptest.NewRequest(http.MethodGet, "/DetailSrv", nil)
	req = mux.SetURLVars(req, map[string]string{"serverName": "DetailSrv"})
	w := httptest.NewRecorder()

	handler.ServerDetail(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	if !contains(w.Body.String(), "DetailSrv") {
		t.Error("expected server detail to contain server name")
	}
	if !contains(w.Body.String(), "play.example.test") || !contains(w.Body.String(), "node.example.test:25565") {
		t.Error("expected server detail to contain discovery and fallback addresses")
	}
	body := w.Body.String()
	for _, expected := range []string{
		"Connect address", "Direct fallback", `data-copy-target="connect-address"`,
		`data-copy-target="direct-address"`, "copyServerAddress",
		"metadata-list-item", "runFileOperation", "Rename", "Copy", "Move",
	} {
		if !contains(body, expected) {
			t.Errorf("expected address UI to contain %q", expected)
		}
	}
	if count := strings.Count(body, "play.example.test"); count != 1 {
		t.Errorf("connect address rendered %d times, want once", count)
	}
	if count := strings.Count(body, "node.example.test:25565"); count != 1 {
		t.Errorf("direct address rendered %d times, want once", count)
	}
	if contains(body, "<strong>directAddress</strong>") || contains(body, "<span>Connect:</span>") {
		t.Error("address metadata was also rendered through the generic or top-level UI")
	}
}

func TestServerDetailOmitsDirectFallbackWhenNotConfigured(t *testing.T) {
	handler, store, _ := testWebHandler(t)
	if err := store.CreateServer(&database.Server{
		Name: "NoFallbackSrv", GameType: "factorio", State: "online", Address: "factorio.example.test",
	}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/NoFallbackSrv", nil)
	req = mux.SetURLVars(req, map[string]string{"serverName": "NoFallbackSrv"})
	w := httptest.NewRecorder()
	handler.ServerDetail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	if contains(w.Body.String(), "Direct fallback") || contains(w.Body.String(), `id="direct-address"`) {
		t.Error("direct fallback controls rendered without a configured address")
	}
}

func TestServerDetailRendersAuthenticatedResourceUsage(t *testing.T) {
	handler, store, authenticator := testWebHandler(t)
	if err := store.CreateServer(&database.Server{
		Name: "ManagedSrv", GameType: "minecraft", State: "online",
	}); err != nil {
		t.Fatal(err)
	}
	server, err := store.GetServerByName("ManagedSrv")
	if err != nil || server == nil {
		t.Fatalf("load server: %v", err)
	}
	if err := store.CreatePterodactylLink(&database.PterodactylLink{
		ServerID: server.ID, PteroServerID: "agent:ManagedSrv",
		AllowedActions: `["status"]`, Node: "managed-node",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAgent(&database.Agent{Name: "managed-node", NodeName: "managed-node"}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/ManagedSrv", nil)
	req = mux.SetURLVars(req, map[string]string{"serverName": "ManagedSrv"})
	req.AddCookie(createTestSession(t, store, authenticator, "admin@test.com", "admin"))
	w := httptest.NewRecorder()
	handler.ServerDetail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	for _, expected := range []string{
		"Resource Usage", "/resources", "No systemd limit",
		"CPU usage uses 100% per processor core",
		"/access?", "descriptor.mode !== 'direct'",
		"Authorization", "access_token", "method: 'PUT'",
	} {
		if !contains(w.Body.String(), expected) {
			t.Errorf("expected resource UI to contain %q", expected)
		}
	}
}

func TestServerDetailOfflineNotFoundForPublic(t *testing.T) {
	handler, store, _ := testWebHandler(t)
	store.CreateServer(&database.Server{Name: "HiddenSrv", GameType: "minecraft", State: "offline"})

	req := httptest.NewRequest(http.MethodGet, "/HiddenSrv", nil)
	w := httptest.NewRecorder()

	handler.ServerDetail(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404 for offline server without auth, got %d", w.Code)
	}
}

func TestConstraintManagerRenders(t *testing.T) {
	handler, store, auth := testWebHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/constraints", nil)
	w := httptest.NewRecorder()
	cookie := createTestSession(t, store, auth, "admin@test.com", "admin")
	req.AddCookie(cookie)

	handler.ConstraintManager(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	if !contains(w.Body.String(), "Constraint Tester") {
		t.Error("expected constraint manager to contain tester section")
	}
}

func TestCronManagerRenders(t *testing.T) {
	handler, store, auth := testWebHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/cron", nil)
	w := httptest.NewRecorder()
	cookie := createTestSession(t, store, auth, "admin@test.com", "admin")
	req.AddCookie(cookie)

	handler.CronManager(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestBackupsRenders(t *testing.T) {
	handler, store, auth := testWebHandler(t)
	store.CreateServer(&database.Server{Name: "BackupSrv", GameType: "minecraft", State: "online"})

	req := httptest.NewRequest(http.MethodGet, "/admin/backups", nil)
	w := httptest.NewRecorder()
	cookie := createTestSession(t, store, auth, "admin@test.com", "admin")
	req.AddCookie(cookie)

	handler.Backups(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	if !contains(w.Body.String(), "BackupSrv") {
		t.Error("expected backups page to contain server name")
	}
	if !contains(w.Body.String(), "content-type") || !contains(w.Body.String(), "await resp.text()") {
		t.Error("expected backups page to handle JSON and plain-text failures")
	}
	body := w.Body.String()
	for _, expected := range []string{"aria-live=\"polite\"", "renderSnapshots", "Created", "Snapshot", "Tags", "Paths", "Copy full snapshot ID"} {
		if !contains(body, expected) {
			t.Errorf("expected human-readable snapshot UI to contain %q", expected)
		}
	}
	if contains(body, "JSON.stringify(data") {
		t.Error("snapshot responses must not be rendered as raw JSON")
	}
}

func TestAgentsRendersConnectionState(t *testing.T) {
	handler, store, _ := testWebHandler(t)
	agent := &database.Agent{
		Name:         "node-agent",
		NodeName:     "node-a",
		Capabilities: json.RawMessage(`["stop","backup","start"]`),
	}
	if err := store.CreateAgent(agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	handler.AgentConnected = func(id int) bool { return id == agent.ID }

	req := httptest.NewRequest(http.MethodGet, "/admin/agents", nil)
	w := httptest.NewRecorder()
	handler.Agents(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	if !contains(w.Body.String(), "node-agent") || !contains(w.Body.String(), "Reachable") {
		t.Error("expected agent page to render the live connection state")
	}
	body := w.Body.String()
	for _, capability := range []string{"backup", "start", "stop"} {
		if !contains(body, ">"+capability+"</span>") {
			t.Errorf("expected capability %q to be rendered as a label", capability)
		}
	}
	if contains(body, "[91 34") {
		t.Error("capabilities must not be rendered as raw JSON bytes")
	}
	if !contains(body, "A system administrator prepares a game server") {
		t.Error("expected agents page to explain the generic management workflow")
	}
	if contains(body, "Gandalf") || contains(body, "No host tunnel interface") {
		t.Error("agents page must not expose deployment-specific transport guidance")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr))
}

func containsAt(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
