package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tionis/hogs/auth"
	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/engine"
	"github.com/tionis/hogs/query"
)

func managedAuthorizationFixture(t *testing.T, operators []string, acl string) (*database.Store, *auth.Authenticator, *engine.Engine) {
	t.Helper()
	_, store := testInventoryHandler(t)
	server := &database.Server{Name: "managed-test", GameType: "minecraft", State: "online"}
	if err := store.CreateServer(server); err != nil {
		t.Fatal(err)
	}
	server, err := store.GetServerByName("managed-test")
	if err != nil || server == nil {
		t.Fatalf("load created server: %v", err)
	}
	if err := store.CreatePterodactylLink(&database.PterodactylLink{
		ServerID: server.ID, PteroServerID: "agent:managed-test", AllowedActions: `[]`, ACLRule: acl, Node: "test-node",
	}); err != nil {
		t.Fatal(err)
	}
	operatorsJSON := `[]`
	if len(operators) == 1 {
		operatorsJSON = `[` + `"` + operators[0] + `"` + `]`
	}
	if _, err := store.DB.Exec(`INSERT INTO server_management(server_id,unit_name,data_path,operators,console_enabled,writable_paths) VALUES(?,?,?,?,1,'[]')`,
		server.ID, "managed-test.service", "/srv/managed-test", operatorsJSON); err != nil {
		t.Fatal(err)
	}
	authenticator := auth.NewTestAuthenticator(store, "managed-authorization-test-secret")
	eng := engine.NewEngine(store, &config.Config{}, query.NewServerStatusCache())
	return store, authenticator, eng
}

func managedTestRequest(t *testing.T, store *database.Store, authenticator *auth.Authenticator, email, role string, groups ...string) *http.Request {
	t.Helper()
	user, err := store.CreateUser(email, role)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range groups {
		group := &database.SCIMGroup{DisplayName: name}
		if err := store.CreateSCIMGroup(group); err != nil {
			t.Fatal(err)
		}
		if err := store.AddSCIMGroupMember(group.ID, user.ID); err != nil {
			t.Fatal(err)
		}
	}
	sessionID := "session-" + email
	if err := store.CreateSession(&database.Session{
		SessionID: sessionID, UserEmail: email, UserRole: role,
		CreatedAt: time.Now().UTC().Format(time.RFC3339), ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/servers/managed-test/console", nil)
	recorder := httptest.NewRecorder()
	session, _ := authenticator.CookieStore().Get(req, "hogs-session")
	session.Values["session_id"] = sessionID
	if err := session.Save(req, recorder); err != nil {
		t.Fatal(err)
	}
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == "hogs-session" {
			req.AddCookie(cookie)
			return req
		}
	}
	t.Fatal("session cookie was not created")
	return nil
}

func TestManagedConsoleAllowsConfiguredNonAdminOperator(t *testing.T) {
	store, authenticator, eng := managedAuthorizationFixture(t, []string{"game-moderators"}, `inList("game-moderators", user.Groups)`)
	req := managedTestRequest(t, store, authenticator, "moderator@example.test", "user", "game-moderators")

	_, user, status, err := authorizeManagedCapability(store, eng, authenticator, req, "managed-test", managedConsole)
	if err != nil || status != http.StatusOK {
		t.Fatalf("operator authorization failed: status=%d err=%v", status, err)
	}
	if user.Email != "moderator@example.test" || user.Role != "user" {
		t.Fatalf("wrong authenticated identity: %#v", user)
	}
}

func TestManagedConsoleRejectsUnlistedNonAdmin(t *testing.T) {
	store, authenticator, eng := managedAuthorizationFixture(t, []string{"game-moderators"}, `true`)
	req := managedTestRequest(t, store, authenticator, "player@example.test", "user")

	_, _, status, err := authorizeManagedCapability(store, eng, authenticator, req, "managed-test", managedConsole)
	if err == nil || status != http.StatusForbidden {
		t.Fatalf("unlisted user status=%d err=%v, want forbidden", status, err)
	}
}

func TestManagedConsoleStillAppliesServerACL(t *testing.T) {
	store, authenticator, eng := managedAuthorizationFixture(t, []string{"game-moderators"}, `user.Role == "admin"`)
	req := managedTestRequest(t, store, authenticator, "moderator@example.test", "user", "game-moderators")

	_, _, status, err := authorizeManagedCapability(store, eng, authenticator, req, "managed-test", managedConsole)
	if err == nil || status != http.StatusForbidden {
		t.Fatalf("ACL-denied operator status=%d err=%v, want forbidden", status, err)
	}
}
