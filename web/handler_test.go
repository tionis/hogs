package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/tionis/hogs/access"
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
	if err := store.ConfigureServerFieldEncryption("test-server-field-key-material-000000000000"); err != nil {
		t.Fatalf("failed to configure field encryption: %v", err)
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

func grantPublicView(t *testing.T, store *database.Store, serverName string) {
	t.Helper()
	server, err := store.GetServerByName(serverName)
	if err != nil || server == nil {
		t.Fatalf("load server for public grant: %v", err)
	}
	if err := store.SetServerAccessGrant(&database.ServerAccessGrant{
		ServerID: server.ID, SubjectType: "everyone", Subject: "*", Effect: "allow",
		Capabilities: []string{"status", "view"},
	}); err != nil {
		t.Fatal(err)
	}
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

func TestServerAccessManagerCanGrantOnlyAuthorizedServer(t *testing.T) {
	handler, store, authenticator := testWebHandler(t)
	for _, name := range []string{"managed-one", "managed-two"} {
		if err := store.CreateServer(&database.Server{Name: name, GameType: "example", State: "online"}); err != nil {
			t.Fatal(err)
		}
	}
	first, _ := store.GetServerByName("managed-one")
	second, _ := store.GetServerByName("managed-two")
	if err := store.SetServerAccessGrant(&database.ServerAccessGrant{
		ServerID: first.ID, SubjectType: "user", Subject: "manager@example.test", Effect: "allow",
		Capabilities: []string{"access.manage"},
	}); err != nil {
		t.Fatal(err)
	}
	cookie := createTestSession(t, store, authenticator, "manager@example.test", "user")

	submit := func(serverID int) *httptest.ResponseRecorder {
		form := url.Values{
			"server_id": {strconv.Itoa(serverID)}, "effect": {"allow"}, "subject_type": {"user"},
			"subject": {"player@example.test"}, "capability": {"view"},
		}
		req := httptest.NewRequest(http.MethodPost, "/admin/access-grants/set", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		recorder := httptest.NewRecorder()
		handler.HandleAccessGrantSet(recorder, req)
		return recorder
	}

	if recorder := submit(first.ID); recorder.Code != http.StatusFound {
		t.Fatalf("authorized grant status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if decision, err := store.EvaluateServerAccess(first.ID, "player@example.test", nil, "view"); err != nil || !decision.Allowed {
		t.Fatalf("authorized grant was not applied: %#v err=%v", decision, err)
	}
	if recorder := submit(second.ID); recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-server grant status=%d body=%s", recorder.Code, recorder.Body.String())
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
		"Browse Game Servers",
	} {
		if !contains(body, want) {
			t.Errorf("expected forbidden page to contain %q", want)
		}
	}
	if contains(body, "My Servers") {
		t.Error("forbidden page still links to the retired My Servers view")
	}
}

func TestAuditLogUsesJSONFieldNamesAndRequestContext(t *testing.T) {
	handler, store, authenticator := testWebHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/audit", nil)
	req.AddCookie(createTestSession(t, store, authenticator, "admin@example.test", "admin"))
	recorder := httptest.NewRecorder()
	handler.AuditLog(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("audit status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	for _, expected := range []string{
		"e.userEmail", "e.serverName", "e.clientIp", "e.countryCode",
		"JSON.stringify(e.params", "<th>Client</th>", "<th>Context</th>",
	} {
		if !contains(recorder.Body.String(), expected) {
			t.Errorf("audit page missing %q", expected)
		}
	}
	if contains(recorder.Body.String(), "e.user_email") || contains(recorder.Body.String(), "e.server_name") {
		t.Error("audit page still reads obsolete snake_case JSON fields")
	}
}

func TestUserSettingsContainsLinkedGameAccounts(t *testing.T) {
	handler, store, authenticator := testWebHandler(t)
	store.CreateServer(&database.Server{Name: "IdentitySrv", GameType: "minecraft", State: "online"})
	if err := store.SetGameIdentity(&database.GameIdentity{
		UserEmail: "player@example.test", GameType: "minecraft", Username: "TestPlayer", Source: "self",
	}); err != nil {
		t.Fatal(err)
	}
	cookie := createTestSession(t, store, authenticator, "player@example.test", "user")
	req := httptest.NewRequest(http.MethodGet, "/account/settings", nil)
	req.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	handler.UserSettings(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("settings status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	for _, expected := range []string{
		"User Settings", "Panel Account", "Linked Game Accounts",
		"player@example.test", "TestPlayer", `href="/account/settings"`,
	} {
		if !contains(recorder.Body.String(), expected) {
			t.Errorf("user settings missing %q", expected)
		}
	}
	if contains(recorder.Body.String(), "My Servers") {
		t.Error("user settings still renders the retired My Servers navigation")
	}

	redirectReq := httptest.NewRequest(http.MethodGet, "/my-servers", nil)
	redirectRecorder := httptest.NewRecorder()
	handler.MyServers(redirectRecorder, redirectReq)
	if redirectRecorder.Code != http.StatusFound || redirectRecorder.Header().Get("Location") != "/account/settings" {
		t.Fatalf("My Servers redirect=%d %q", redirectRecorder.Code, redirectRecorder.Header().Get("Location"))
	}
}

func TestHomeRenders(t *testing.T) {
	handler, store, _ := testWebHandler(t)
	store.CreateServer(&database.Server{
		Name: "PublicSrv", GameType: "minecraft", State: "online",
		Metadata: map[string]string{
			"directAddress": "node.example.test:25565",
			"region":        "test-region",
		},
	})
	grantPublicView(t, store, "PublicSrv")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	handler.Home(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	if !contains(w.Body.String(), "PublicSrv") {
		t.Error("expected home page to contain server name")
	}
	if contains(w.Body.String(), "node.example.test:25565") {
		t.Error("home page renders the direct fallback address")
	}
	if !contains(w.Body.String(), "test-region") {
		t.Error("home page omitted ordinary server metadata")
	}
}

func TestAdminGameTypesIncludeCustomServerType(t *testing.T) {
	types := adminGameTypes([]database.Server{{GameType: "custom_game"}})
	if !containsString(types, "custom_game") || !containsString(types, "minecraft") {
		t.Fatalf("admin game types = %#v", types)
	}
}

func TestSelectableGameTypesExcludeDisabledUnlessAssigned(t *testing.T) {
	handler, store, _ := testWebHandler(t)
	minecraft, err := store.GetGameType("minecraft")
	if err != nil || minecraft == nil {
		t.Fatalf("load Minecraft game type: %#v, %v", minecraft, err)
	}
	minecraft.Enabled = false
	if err := store.SetGameType(minecraft); err != nil {
		t.Fatal(err)
	}
	if types := handler.adminGameTypes(nil); containsString(types, "minecraft") {
		t.Fatalf("disabled unassigned type is selectable: %#v", types)
	}
	types := handler.adminGameTypes([]database.Server{{GameType: "minecraft"}})
	if !containsString(types, "minecraft") {
		t.Fatalf("current disabled assignment was hidden: %#v", types)
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
		Metadata: map[string]string{
			"directAddress": "node.example.test:25565",
			"map_lifecycle": "independent",
			"region":        "test-region",
		},
	})
	grantPublicView(t, store, "DetailSrv")

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
		"metadata-list-item", "connection-address", "copy-address-button",
		`aria-label="Copy connect address"`, "test-region",
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
	if contains(body, "map_lifecycle") || contains(body, "independent") {
		t.Error("internal map lifecycle metadata rendered in Server Info")
	}
}

func TestStructuredServerFieldsRenderByPlacementAndRevealOnDemand(t *testing.T) {
	handler, store, authenticator := testWebHandler(t)
	server := &database.Server{Name: "Secret Server", GameType: "generic", State: "online"}
	if err := store.CreateServer(server); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceServerFields(server.ID, []database.ServerField{
		{Key: "region", Label: "Region", Value: "Europe", Placement: database.FieldPlacementSummary, Disclosure: database.FieldDisclosurePlain},
		{Key: "rules", Label: "Rules", Value: "Be kind", Placement: database.FieldPlacementDetails, Disclosure: database.FieldDisclosurePlain},
		{Key: "join_password", Label: "Join password", Value: "extremely-secret-value", Placement: database.FieldPlacementDetails, Disclosure: database.FieldDisclosureReveal},
		{Key: "api_token", Label: "API token", Value: "never-for-users", Placement: database.FieldPlacementInternal, Disclosure: database.FieldDisclosureWriteOnly},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetServerAccessGrant(&database.ServerAccessGrant{
		ServerID: server.ID, SubjectType: "user", Subject: "player@example.test", Effect: "allow",
		Capabilities: []string{access.View, access.SecretRead},
	}); err != nil {
		t.Fatal(err)
	}
	cookie := createTestSession(t, store, authenticator, "player@example.test", "user")

	homeReq := httptest.NewRequest(http.MethodGet, "/", nil)
	homeReq.AddCookie(cookie)
	home := httptest.NewRecorder()
	handler.Home(home, homeReq)
	if home.Code != http.StatusOK {
		t.Fatalf("home status=%d body=%s", home.Code, home.Body.String())
	}
	homeBody := home.Body.String()
	if !contains(homeBody, "Region: Europe") || contains(homeBody, "Be kind") ||
		contains(homeBody, "extremely-secret-value") || contains(homeBody, "never-for-users") {
		t.Fatalf("home field disclosure was incorrect: %s", homeBody)
	}

	detailReq := httptest.NewRequest(http.MethodGet, "/servers/Secret%20Server", nil)
	detailReq = mux.SetURLVars(detailReq, map[string]string{"serverName": "Secret Server"})
	detailReq.AddCookie(cookie)
	detail := httptest.NewRecorder()
	handler.ServerDetail(detail, detailReq)
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	detailBody := detail.Body.String()
	for _, expected := range []string{"Region", "Europe", "Rules", "Be kind", "Join password", "toggleServerSecret"} {
		if !contains(detailBody, expected) {
			t.Errorf("detail page missing %q", expected)
		}
	}
	if contains(detailBody, "extremely-secret-value") || contains(detailBody, "never-for-users") || contains(detailBody, "API token") {
		t.Fatal("secret value or internal field leaked into the detail HTML")
	}

	fields, err := store.ListServerFields(server.ID)
	if err != nil {
		t.Fatal(err)
	}
	var revealID int
	for _, field := range fields {
		if field.Key == "join_password" {
			revealID = field.ID
		}
	}
	revealReq := httptest.NewRequest(http.MethodPost, "/servers/Secret%20Server/fields/"+strconv.Itoa(revealID)+"/reveal", nil)
	revealReq = mux.SetURLVars(revealReq, map[string]string{"serverName": "Secret Server", "fieldID": strconv.Itoa(revealID)})
	revealReq.AddCookie(cookie)
	reveal := httptest.NewRecorder()
	handler.RevealServerField(reveal, revealReq)
	if reveal.Code != http.StatusOK || !contains(reveal.Body.String(), "extremely-secret-value") {
		t.Fatalf("reveal status=%d body=%s", reveal.Code, reveal.Body.String())
	}
	if !strings.Contains(reveal.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("reveal cache policy = %q", reveal.Header().Get("Cache-Control"))
	}
	entries, err := store.ListAuditLog(10, 0)
	if err != nil || len(entries) == 0 || entries[0].Action != access.SecretRead ||
		contains(string(entries[0].Params), "extremely-secret-value") {
		t.Fatalf("reveal audit=%#v err=%v", entries, err)
	}

	adminCookie := createTestSession(t, store, authenticator, "admin@example.test", "admin")
	settingsReq := httptest.NewRequest(http.MethodGet, "/servers/Secret%20Server/settings", nil)
	settingsReq = mux.SetURLVars(settingsReq, map[string]string{"serverName": "Secret Server"})
	settingsReq.AddCookie(adminCookie)
	settings := httptest.NewRecorder()
	handler.ServerSettings(settings, settingsReq)
	if settings.Code != http.StatusOK {
		t.Fatalf("settings status=%d body=%s", settings.Code, settings.Body.String())
	}
	if contains(settings.Body.String(), "extremely-secret-value") || contains(settings.Body.String(), "never-for-users") {
		t.Fatal("server settings preloaded a reveal or write-only value")
	}
}

func TestServerSecretRevealRequiresExplicitCapability(t *testing.T) {
	handler, store, authenticator := testWebHandler(t)
	server := &database.Server{Name: "Restricted Secret", GameType: "generic", State: "online"}
	if err := store.CreateServer(server); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceServerFields(server.ID, []database.ServerField{{
		Key: "join_password", Label: "Join password", Value: "not-authorized",
		Placement: database.FieldPlacementDetails, Disclosure: database.FieldDisclosureReveal,
	}}); err != nil {
		t.Fatal(err)
	}
	fields, _ := store.ListServerFields(server.ID)
	cookie := createTestSession(t, store, authenticator, "viewer@example.test", "user")
	req := httptest.NewRequest(http.MethodPost, "/servers/Restricted%20Secret/fields/"+strconv.Itoa(fields[0].ID)+"/reveal", nil)
	req = mux.SetURLVars(req, map[string]string{"serverName": server.Name, "fieldID": strconv.Itoa(fields[0].ID)})
	req.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	handler.RevealServerField(recorder, req)
	if recorder.Code != http.StatusForbidden || contains(recorder.Body.String(), "not-authorized") {
		t.Fatalf("unauthorized reveal status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestServerDetailOmitsDirectFallbackWhenNotConfigured(t *testing.T) {
	handler, store, _ := testWebHandler(t)
	if err := store.CreateServer(&database.Server{
		Name: "NoFallbackSrv", GameType: "factorio", State: "online", Address: "factorio.example.test",
	}); err != nil {
		t.Fatal(err)
	}
	grantPublicView(t, store, "NoFallbackSrv")
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
		AllowedActions: `[]`, Node: "managed-node",
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
		"Resource history", "resource-cpu-chart", "resource-memory-chart",
		"/resources/history?", "Not running", "Stopped periods are shaded",
		"windowStart = windowEnd - rangeHours * 60 * 60 * 1000",
		"window.setInterval(loadResourceHistory, 15000)",
		"formatPercentTick", "axisFormat: formatPercentTick",
		`<option value="1" selected>Last hour</option>`,
	} {
		if !contains(w.Body.String(), expected) {
			t.Errorf("expected resource UI to contain %q", expected)
		}
	}
}

func TestServerSettingsArePartOfServerView(t *testing.T) {
	handler, store, authenticator := testWebHandler(t)
	if err := store.CreateServer(&database.Server{
		Name: "Managed Settings", GameType: "minecraft", State: "online",
		Metadata: map[string]string{"map_lifecycle": "independent"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAgent(&database.Agent{Name: "Worker A", NodeName: "node-a"}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/servers/Managed%20Settings/settings", nil)
	req = mux.SetURLVars(req, map[string]string{"serverName": "Managed Settings"})
	req.AddCookie(createTestSession(t, store, authenticator, "admin@test.com", "admin"))
	recorder := httptest.NewRecorder()
	handler.ServerSettings(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("settings status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`href="/servers/Managed%20Settings/settings"`, ">Settings</a>",
		"Server Details", "Presentation state", "Automatic — show live worker state",
		"This does not start or stop the service",
		"Management backend", "Assign to worker", "Worker A",
		"Also add <code>srv-", "below <code>servers:</code>",
	} {
		if !contains(body, expected) {
			t.Errorf("settings page missing %q", expected)
		}
	}
	if contains(body, "Your Server Access") || contains(body, "Manage Server Access") {
		t.Error("settings page duplicates the dedicated access section")
	}
}

func TestServerSettingsRequireInstanceAdmin(t *testing.T) {
	handler, store, authenticator := testWebHandler(t)
	if err := store.CreateServer(&database.Server{
		Name: "RestrictedSettings", GameType: "minecraft", State: "online",
	}); err != nil {
		t.Fatal(err)
	}
	server, _ := store.GetServerByName("RestrictedSettings")
	if err := store.SetServerAccessGrant(&database.ServerAccessGrant{
		ServerID: server.ID, SubjectType: "user", Subject: "user@test.com", Effect: "allow",
		Capabilities: []string{"status", "view"},
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/servers/RestrictedSettings/settings", nil)
	req = mux.SetURLVars(req, map[string]string{"serverName": "RestrictedSettings"})
	req.AddCookie(createTestSession(t, store, authenticator, "user@test.com", "user"))
	recorder := httptest.NewRecorder()
	handler.ServerSettings(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("settings status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestLegacyServerEditRedirectsToSettingsTab(t *testing.T) {
	handler, store, _ := testWebHandler(t)
	if err := store.CreateServer(&database.Server{
		Name: "Renamed Server", GameType: "factorio", State: "online",
	}); err != nil {
		t.Fatal(err)
	}
	server, _ := store.GetServerByName("Renamed Server")
	req := httptest.NewRequest(http.MethodGet, "/admin/servers/"+strconv.Itoa(server.ID), nil)
	req = mux.SetURLVars(req, map[string]string{"id": strconv.Itoa(server.ID)})
	recorder := httptest.NewRecorder()
	handler.ServerEdit(recorder, req)

	if recorder.Code != http.StatusMovedPermanently {
		t.Fatalf("legacy edit status=%d", recorder.Code)
	}
	if location := recorder.Header().Get("Location"); location != "/servers/Renamed%20Server/settings" {
		t.Fatalf("legacy edit location=%q", location)
	}
}

func TestNormalizePresentationState(t *testing.T) {
	for input, expected := range map[string]string{
		"": "online", "online": "online", "auto": "online",
		"offline": "offline", "planned": "planned", "maintenance": "maintenance",
	} {
		got, ok := normalizePresentationState(input)
		if !ok || got != expected {
			t.Errorf("normalizePresentationState(%q) = %q, %t; want %q, true", input, got, ok, expected)
		}
	}
	if got, ok := normalizePresentationState("running"); ok || got != "" {
		t.Fatalf("invalid presentation state = %q, %t", got, ok)
	}
}

func TestServerDetailAndFilesHonorCapabilities(t *testing.T) {
	handler, store, authenticator := testWebHandler(t)
	if err := store.CreateServer(&database.Server{
		Name: "CapabilitySrv", GameType: "minecraft", State: "online",
	}); err != nil {
		t.Fatal(err)
	}
	server, err := store.GetServerByName("CapabilitySrv")
	if err != nil || server == nil {
		t.Fatalf("load server: %v", err)
	}
	if err := store.CreatePterodactylLink(&database.PterodactylLink{
		ServerID: server.ID, PteroServerID: "agent:CapabilitySrv",
		AllowedActions: `[]`, Node: "capability-node",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAgent(&database.Agent{Name: "capability-node", NodeName: "capability-node"}); err != nil {
		t.Fatal(err)
	}
	const email = "player@example.test"
	if err := store.SetServerAccessGrant(&database.ServerAccessGrant{
		ServerID: server.ID, SubjectType: "user", Subject: email, Effect: "allow",
		Capabilities: []string{access.View, access.Status},
	}); err != nil {
		t.Fatal(err)
	}
	cookie := createTestSession(t, store, authenticator, email, "user")

	renderDetail := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/CapabilitySrv", nil)
		req = mux.SetURLVars(req, map[string]string{"serverName": "CapabilitySrv"})
		req.AddCookie(cookie)
		recorder := httptest.NewRecorder()
		handler.ServerDetail(recorder, req)
		return recorder
	}
	renderFiles := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/servers/CapabilitySrv/files", nil)
		req = mux.SetURLVars(req, map[string]string{"serverName": "CapabilitySrv"})
		req.AddCookie(cookie)
		recorder := httptest.NewRecorder()
		handler.ServerFiles(recorder, req)
		return recorder
	}
	renderConsole := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/servers/CapabilitySrv/console", nil)
		req = mux.SetURLVars(req, map[string]string{"serverName": "CapabilitySrv"})
		req.AddCookie(cookie)
		recorder := httptest.NewRecorder()
		handler.ServerConsole(recorder, req)
		return recorder
	}

	detail := renderDetail()
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	if contains(detail.Body.String(), `id="console-output"`) || contains(detail.Body.String(), "Manage Files") {
		t.Fatal("console or file navigation rendered without its capability")
	}
	if !contains(detail.Body.String(), `id="resource-usage-card"`) {
		t.Fatal("resource usage missing for a user with status capability")
	}
	if files := renderFiles(); files.Code != http.StatusForbidden {
		t.Fatalf("files status without file.read=%d body=%s", files.Code, files.Body.String())
	}
	if console := renderConsole(); console.Code != http.StatusForbidden {
		t.Fatalf("console status without console.read=%d body=%s", console.Code, console.Body.String())
	}

	if err := store.SetServerAccessGrant(&database.ServerAccessGrant{
		ServerID: server.ID, SubjectType: "user", Subject: email, Effect: "allow",
		Capabilities: []string{access.View, access.Status, access.ConsoleRead, access.FileRead, access.FileWrite},
	}); err != nil {
		t.Fatal(err)
	}
	detail = renderDetail()
	if !contains(detail.Body.String(), `/servers/CapabilitySrv/console`) || !contains(detail.Body.String(), `/servers/CapabilitySrv/files`) {
		t.Fatal("console or file tab missing with read capabilities")
	}
	if contains(detail.Body.String(), `id="console-output"`) || contains(detail.Body.String(), `id="file-browser-card"`) || contains(detail.Body.String(), "runFileOperation") {
		t.Fatal("console and file implementations must not render on the dashboard")
	}
	console := renderConsole()
	if console.Code != http.StatusOK || !contains(console.Body.String(), `id="console-output"`) ||
		!contains(console.Body.String(), `/console/ws`) {
		t.Fatalf("dedicated console page missing console UI: status=%d body=%s", console.Code, console.Body.String())
	}
	files := renderFiles()
	if files.Code != http.StatusOK {
		t.Fatalf("files status=%d body=%s", files.Code, files.Body.String())
	}
	for _, expected := range []string{
		`id="file-browser-card"`, "runFileOperation", "/access?",
		"descriptor.mode !== 'direct'", "Authorization", "access_token", "method: 'PUT'",
	} {
		if !contains(files.Body.String(), expected) {
			t.Fatalf("dedicated file page missing %q", expected)
		}
	}
	if !contains(files.Body.String(), `id="file-browser-card"`) {
		t.Fatal("dedicated file page did not render its browser")
	}
	if !contains(files.Body.String(), `/servers/CapabilitySrv/console`) {
		t.Fatal("console tab disappeared from the file page")
	}
	if contains(files.Body.String(), `id="console-output"`) || contains(files.Body.String(), "Server Info") {
		t.Fatal("dedicated file page rendered server console or sidebar")
	}
}

func TestServerTabsAndAccessPage(t *testing.T) {
	handler, store, authenticator := testWebHandler(t)
	if err := store.CreateServer(&database.Server{
		Name: "OrderedSrv", GameType: "minecraft", State: "online",
	}); err != nil {
		t.Fatal(err)
	}
	server, _ := store.GetServerByName("OrderedSrv")
	if err := store.CreatePterodactylLink(&database.PterodactylLink{
		ServerID: server.ID, PteroServerID: "agent:OrderedSrv",
		AllowedActions: `["status"]`, Node: "ordered-node",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAgent(&database.Agent{Name: "ordered-node", NodeName: "ordered-node"}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/OrderedSrv", nil)
	req = mux.SetURLVars(req, map[string]string{"serverName": "OrderedSrv"})
	adminCookie := createTestSession(t, store, authenticator, "admin@example.test", "admin")
	req.AddCookie(adminCookie)
	recorder := httptest.NewRecorder()
	handler.ServerDetail(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !contains(body, "server-section-bar") {
		t.Fatal("server tabs are not rendered in the title/status bar")
	}
	if count := strings.Count(body, `class="server-status-poller`); count != 1 {
		t.Fatalf("server status rendered %d times, want once in the section bar", count)
	}
	if contains(body, "overflow-x-auto") || contains(body, "flex-nowrap") {
		t.Fatal("server tab bar forces horizontal scrolling")
	}
	for _, tab := range []string{"Dashboard", "Console", "Files", "Whitelist", "Access", "Backups"} {
		if !contains(body, ">"+tab+"</a>") {
			t.Fatalf("missing server tab %q", tab)
		}
	}
	if contains(body, "Your Server Access") || contains(body, "Manage Server Access") {
		t.Fatal("access management must not render on the dashboard")
	}

	whitelistReq := httptest.NewRequest(http.MethodGet, "/servers/OrderedSrv/whitelist", nil)
	whitelistReq = mux.SetURLVars(whitelistReq, map[string]string{"serverName": "OrderedSrv"})
	whitelistReq.AddCookie(adminCookie)
	whitelistRecorder := httptest.NewRecorder()
	handler.ServerWhitelist(whitelistRecorder, whitelistReq)
	if whitelistRecorder.Code != http.StatusOK {
		t.Fatalf("whitelist status=%d body=%s", whitelistRecorder.Code, whitelistRecorder.Body.String())
	}
	whitelistBody := whitelistRecorder.Body.String()
	for _, expected := range []string{
		"Your whitelist entry", "Edit linked game account", "Manage server whitelist",
		"Minecraft username", "Panel user (optional)", "Save link", "Add entry",
	} {
		if !contains(whitelistBody, expected) {
			t.Fatalf("whitelist page missing %q", expected)
		}
	}
	if contains(whitelistBody, `id="whitelist-username"`) {
		t.Fatal("whitelist page contains a second self-service identity editor")
	}

	accessReq := httptest.NewRequest(http.MethodGet, "/servers/OrderedSrv/access", nil)
	accessReq = mux.SetURLVars(accessReq, map[string]string{"serverName": "OrderedSrv"})
	accessReq.AddCookie(adminCookie)
	accessRecorder := httptest.NewRecorder()
	handler.ServerAccess(accessRecorder, accessReq)
	if accessRecorder.Code != http.StatusOK {
		t.Fatalf("access status=%d body=%s", accessRecorder.Code, accessRecorder.Body.String())
	}
	accessBody := accessRecorder.Body.String()
	headings := []string{"Your Server Access", "Manage Server Access"}
	previous := -1
	for _, heading := range headings {
		position := strings.Index(accessBody, heading)
		if position < 0 {
			t.Fatalf("missing access-page heading %q", heading)
		}
		if position <= previous {
			t.Fatalf("access-page heading %q is out of order", heading)
		}
		previous = position
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
	for _, expected := range []string{
		"Constraint Tester", "user.ClientIP", "user.CountryCode",
		"ipInCIDR", "ipInAnyCIDR", "TRUST_PROXY_HEADERS",
		`id="test-user-client-ip"`, `id="test-user-country"`,
	} {
		if !contains(w.Body.String(), expected) {
			t.Errorf("constraint manager missing %q", expected)
		}
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

func TestServerBackupRestoreRequiresPolicyAndTypedConfirmation(t *testing.T) {
	handler, store, authenticator := testWebHandler(t)
	if err := store.CreateServer(&database.Server{Name: "RestoreSrv", GameType: "minecraft", State: "online"}); err != nil {
		t.Fatal(err)
	}
	server, _ := store.GetServerByName("RestoreSrv")
	if _, err := store.DB.Exec(`INSERT INTO server_management(
		server_id,unit_name,data_path,backup_enabled,restore_enabled
	) VALUES(?,?,?,?,?)`, server.ID, "restore.service", "/srv/restore", 1, 1); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/servers/RestoreSrv/backups", nil)
	req = mux.SetURLVars(req, map[string]string{"serverName": "RestoreSrv"})
	req.AddCookie(createTestSession(t, store, authenticator, "admin@test.com", "admin"))
	recorder := httptest.NewRecorder()
	handler.ServerBackups(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("restore page status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		"Restore server snapshot", "create a safety snapshot", "atomically swap",
		"confirmServerId", server.ManagementID, "Restore…", "roll back",
	} {
		if !contains(body, expected) {
			t.Errorf("restore UI missing %q", expected)
		}
	}

	if _, err := store.DB.Exec("UPDATE server_management SET restore_enabled=0 WHERE server_id=?", server.ID); err != nil {
		t.Fatal(err)
	}
	disabledRecorder := httptest.NewRecorder()
	handler.ServerBackups(disabledRecorder, req)
	if contains(disabledRecorder.Body.String(), "Restore server snapshot") {
		t.Fatal("restore controls rendered while deployment policy disabled restores")
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
	for _, expected := range []string{"Add worker", "Initial credentials are deliberately host-provisioned", "local <code>servers</code> allowlist", "Add or choose a server"} {
		if !contains(body, expected) {
			t.Errorf("expected worker enrollment and assignment guidance to contain %q", expected)
		}
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
