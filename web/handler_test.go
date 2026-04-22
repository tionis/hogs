package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

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
	_, err = store.DB.Exec("INSERT INTO sessions (session_id, user_email, user_role, expires_at) VALUES (?, ?, ?, datetime('now', '+1 day'))", sessionID, email, role)
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

func TestServerDetailRenders(t *testing.T) {
	handler, store, _ := testWebHandler(t)
	store.CreateServer(&database.Server{Name: "DetailSrv", GameType: "minecraft", State: "online"})

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
