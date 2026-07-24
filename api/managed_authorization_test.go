package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
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
	if len(operators) == 1 {
		if err := store.SetServerAccessGrant(&database.ServerAccessGrant{
			ServerID: server.ID, SubjectType: "group", Subject: operators[0], Effect: "allow",
			Capabilities: []string{"console.read", "backup.list", "status"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	authenticator := auth.NewTestAuthenticator(store, "managed-authorization-test-secret")
	eng := engine.NewEngine(store, &config.Config{}, query.NewServerStatusCache())
	return store, authenticator, eng
}

func TestAPIKeyPrincipalPreservesMachineIdentityAndRole(t *testing.T) {
	_, store := testInventoryHandler(t)
	plain := "hogs_1111111111111111111111111111111111111111111111111111111111111111"
	key := &database.APIKey{
		Name: "acceptance-moderator", KeyHash: database.HashAPIKey(plain), KeyPrefix: plain[:8], Role: "user",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := store.CreateAPIKey(key); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	recorder := httptest.NewRecorder()
	var principal *engine.UserEnv
	auth.APIKeyMiddleware(store, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		principal = userEnvFromRequest(store, nil, r)
	})).ServeHTTP(recorder, req)
	if principal == nil || principal.Email != "api-key:acceptance-moderator" || principal.Role != "user" {
		t.Fatalf("wrong API-key principal: %#v", principal)
	}
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

	_, user, status, err := authorizeManagedCapability(store, eng, authenticator, req, "managed-test", managedConsoleRead)
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

	_, _, status, err := authorizeManagedCapability(store, eng, authenticator, req, "managed-test", managedConsoleRead)
	if err == nil || status != http.StatusForbidden {
		t.Fatalf("unlisted user status=%d err=%v, want forbidden", status, err)
	}
}

func TestManagedConsoleExplicitDenyOverridesAllow(t *testing.T) {
	store, authenticator, eng := managedAuthorizationFixture(t, []string{"game-moderators"}, `user.Role == "admin"`)
	server, _ := store.GetServerByName("managed-test")
	if err := store.SetServerAccessGrant(&database.ServerAccessGrant{
		ServerID: server.ID, SubjectType: "group", Subject: "game-moderators", Effect: "deny",
		Capabilities: []string{"console.read"},
	}); err != nil {
		t.Fatal(err)
	}
	req := managedTestRequest(t, store, authenticator, "moderator@example.test", "user", "game-moderators")

	_, _, status, err := authorizeManagedCapability(store, eng, authenticator, req, "managed-test", managedConsoleRead)
	if err == nil || status != http.StatusForbidden {
		t.Fatalf("explicitly denied operator status=%d err=%v, want forbidden", status, err)
	}
}

func TestManagedFileAccessUsesReconciledWritablePaths(t *testing.T) {
	store, authenticator, eng := managedAuthorizationFixture(t, []string{"game-moderators"}, `true`)
	server, _ := store.GetServerByName("managed-test")
	if _, err := store.DB.Exec(`UPDATE server_management SET writable_paths='["/srv/managed-test/config"]' WHERE server_id=?`, server.ID); err != nil {
		t.Fatal(err)
	}
	req := managedTestRequest(t, store, authenticator, "admin@example.test", "admin")

	if status, err := authorizeManagedPath(store, eng, authenticator, req, "managed-test", "config/settings.json", managedFileRead); err != nil || status != http.StatusOK {
		t.Fatalf("allowlisted path status=%d err=%v", status, err)
	}
	if status, err := authorizeManagedPath(store, eng, authenticator, req, "managed-test", "world/level.dat", managedFileRead); err == nil || status != http.StatusForbidden {
		t.Fatalf("unlisted path status=%d err=%v, want forbidden", status, err)
	}
	if status, err := authorizeManagedPath(store, eng, authenticator, req, "managed-test", "/srv/managed-test/config/settings.json", managedFileRead); err == nil || status != http.StatusBadRequest {
		t.Fatalf("absolute path status=%d err=%v, want bad request", status, err)
	}
}

func TestManagedFileAccessRejectsNonAdminOperator(t *testing.T) {
	store, authenticator, eng := managedAuthorizationFixture(t, []string{"game-moderators"}, `true`)
	server, _ := store.GetServerByName("managed-test")
	if _, err := store.DB.Exec(`UPDATE server_management SET writable_paths='["/srv/managed-test/config"]' WHERE server_id=?`, server.ID); err != nil {
		t.Fatal(err)
	}
	req := managedTestRequest(t, store, authenticator, "moderator@example.test", "user", "game-moderators")
	if status, err := authorizeManagedPath(store, eng, authenticator, req, "managed-test", "config/settings.json", managedFileRead); err == nil || status != http.StatusForbidden {
		t.Fatalf("non-admin operator status=%d err=%v, want forbidden", status, err)
	}
}

func TestManagedBackupAllowsOperatorWithoutGrantingRestore(t *testing.T) {
	store, authenticator, eng := managedAuthorizationFixture(t, []string{"game-moderators"}, `true`)
	server, _ := store.GetServerByName("managed-test")
	if _, err := store.DB.Exec(`UPDATE server_management SET backup_enabled=1,restore_enabled=0 WHERE server_id=?`, server.ID); err != nil {
		t.Fatal(err)
	}
	req := managedTestRequest(t, store, authenticator, "moderator@example.test", "user", "game-moderators")

	if _, _, status, err := authorizeManagedCapability(store, eng, authenticator, req, "managed-test", managedBackupList); err != nil || status != http.StatusOK {
		t.Fatalf("backup authorization status=%d err=%v", status, err)
	}
	if _, _, status, err := authorizeManagedCapability(store, eng, authenticator, req, "managed-test", managedBackupRestore); err == nil || status != http.StatusForbidden {
		t.Fatalf("restore authorization status=%d err=%v, want forbidden", status, err)
	}
}

func TestManagedStatusAllowsConfiguredOperator(t *testing.T) {
	store, authenticator, eng := managedAuthorizationFixture(t, []string{"game-moderators"}, `true`)
	req := managedTestRequest(t, store, authenticator, "moderator@example.test", "user", "game-moderators")

	if _, _, status, err := authorizeManagedCapability(store, eng, authenticator, req, "managed-test", managedStatus); err != nil || status != http.StatusOK {
		t.Fatalf("status authorization status=%d err=%v", status, err)
	}
}

func TestWhitelistStatusIgnoresCallerSuppliedIdentity(t *testing.T) {
	store, authenticator, eng := managedAuthorizationFixture(t, nil, `true`)
	server, _ := store.GetServerByName("managed-test")
	if err := store.SetUserWhitelist("player@example.test", server.ID, "RightfulPlayer"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetUserWhitelist("victim@example.test", server.ID, "VictimPlayer"); err != nil {
		t.Fatal(err)
	}
	req := managedTestRequest(t, store, authenticator, "player@example.test", "user")
	req.URL.RawQuery = "user_email=victim%40example.test"
	req = mux.SetURLVars(req, map[string]string{"serverName": "managed-test"})
	recorder := httptest.NewRecorder()
	handler := NewPterodactylHandler(store, &config.Config{}, eng, nil, authenticator)
	handler.WhitelistStatus(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["username"] != "RightfulPlayer" {
		t.Fatalf("caller selected another identity: %#v", response)
	}
}
