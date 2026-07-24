package database

import (
	"encoding/json"
	"os"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dbPath := t.TempDir() + "/test.db"
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() {
		store.DB.Close()
		os.Remove(dbPath)
	})
	return store
}

func TestCreateAndGetServer(t *testing.T) {
	store := testStore(t)

	srv := &Server{
		Name:        "Creative",
		Address:     "creative.example.com:25565",
		Description: "A creative server",
		MapURL:      "http://localhost:8100",
		ModURL:      "/files/creative/mods/pack.zip",
		GameType:    "minecraft",
		State:       "online",
		ShowMOTD:    true,
		Metadata:    map[string]string{"foo": "bar"},
	}

	if err := store.CreateServer(srv); err != nil {
		t.Fatalf("CreateServer failed: %v", err)
	}

	got, err := store.GetServerByName("Creative")
	if err != nil {
		t.Fatalf("GetServerByName failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected server, got nil")
	}
	if got.Name != "Creative" {
		t.Errorf("Name = %q, want %q", got.Name, "Creative")
	}
	if got.GameType != "minecraft" {
		t.Errorf("GameType = %q, want %q", got.GameType, "minecraft")
	}
	if got.MapURL != "http://localhost:8100" {
		t.Errorf("MapURL = %q, want %q", got.MapURL, "http://localhost:8100")
	}
	if got.Metadata["foo"] != "bar" {
		t.Errorf("Metadata[foo] = %q, want %q", got.Metadata["foo"], "bar")
	}
}

func TestListServers(t *testing.T) {
	store := testStore(t)

	servers := []*Server{
		{Name: "Alpha", Address: "a:25565", GameType: "minecraft", State: "online"},
		{Name: "Beta", Address: "b:15777", GameType: "satisfactory", State: "auto"},
	}
	for _, s := range servers {
		if err := store.CreateServer(s); err != nil {
			t.Fatalf("CreateServer failed: %v", err)
		}
	}

	got, err := store.ListServers()
	if err != nil {
		t.Fatalf("ListServers failed: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("len(ListServers) = %d, want 2", len(got))
	}
}

func TestUpdateServer(t *testing.T) {
	store := testStore(t)

	srv := &Server{Name: "Test", Address: "t:25565", GameType: "minecraft", State: "online"}
	if err := store.CreateServer(srv); err != nil {
		t.Fatalf("CreateServer failed: %v", err)
	}

	got, _ := store.GetServerByName("Test")
	got.State = "maintenance"
	got.GameType = "factorio"
	if err := store.UpdateServer(got); err != nil {
		t.Fatalf("UpdateServer failed: %v", err)
	}

	updated, _ := store.GetServerByName("Test")
	if updated.State != "maintenance" {
		t.Errorf("State = %q, want %q", updated.State, "maintenance")
	}
	if updated.GameType != "factorio" {
		t.Errorf("GameType = %q, want %q", updated.GameType, "factorio")
	}
}

func TestDeleteServer(t *testing.T) {
	store := testStore(t)

	srv := &Server{Name: "ToDelete", Address: "d:25565", GameType: "minecraft", State: "online"}
	if err := store.CreateServer(srv); err != nil {
		t.Fatalf("CreateServer failed: %v", err)
	}

	got, _ := store.GetServerByName("ToDelete")
	if err := store.DeleteServer(got.ID); err != nil {
		t.Fatalf("DeleteServer failed: %v", err)
	}

	deleted, _ := store.GetServerByName("ToDelete")
	if deleted != nil {
		t.Error("expected nil after delete")
	}
}

func TestGetServerByNameNotFound(t *testing.T) {
	store := testStore(t)

	got, err := store.GetServerByName("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent server")
	}
}

func TestDefaultGameType(t *testing.T) {
	store := testStore(t)

	srv := &Server{Name: "NoGame", Address: "n:25565", State: "online"}
	if err := store.CreateServer(srv); err != nil {
		t.Fatalf("CreateServer failed: %v", err)
	}

	got, _ := store.GetServerByName("NoGame")
	if got.GameType != "minecraft" {
		t.Errorf("GameType = %q, want default %q", got.GameType, "minecraft")
	}
}

func TestPublicMetadataStripsSecrets(t *testing.T) {
	srv := &Server{
		Name:     "Test",
		Metadata: map[string]string{"api_token": "secret123", "rcon_password": "pass", "version": "1.0"},
	}

	public := srv.PublicMetadata()
	if _, ok := public["api_token"]; ok {
		t.Error("api_token should be stripped from public metadata")
	}
	if _, ok := public["rcon_password"]; ok {
		t.Error("rcon_password should be stripped from public metadata")
	}
	if public["version"] != "1.0" {
		t.Error("version should be preserved in public metadata")
	}
}

func TestToPublic(t *testing.T) {
	srv := &Server{
		Name:     "Test",
		Metadata: map[string]string{"api_token": "secret", "visible": "yes"},
	}

	public := srv.ToPublic()
	if _, ok := public.Metadata["api_token"]; ok {
		t.Error("api_token should be stripped in ToPublic")
	}
	if public.Metadata["visible"] != "yes" {
		t.Error("visible key should be preserved in ToPublic")
	}
}

func TestCreateAndGetUser(t *testing.T) {
	store := testStore(t)

	user, err := store.CreateUser("alice@example.com", "admin")
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}
	if user.Email != "alice@example.com" {
		t.Errorf("Email = %q, want %q", user.Email, "alice@example.com")
	}
	if user.Role != "admin" {
		t.Errorf("Role = %q, want %q", user.Role, "admin")
	}

	got, err := store.GetUserByEmail("alice@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected user, got nil")
	}
	if got.Email != "alice@example.com" {
		t.Errorf("Email = %q, want %q", got.Email, "alice@example.com")
	}
	if got.Role != "admin" {
		t.Errorf("Role = %q, want %q", got.Role, "admin")
	}
}

func TestCreateUserDefaultRole(t *testing.T) {
	store := testStore(t)

	user, err := store.CreateUser("bob@example.com", "")
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}
	if user.Role != "user" {
		t.Errorf("Role = %q, want default %q", user.Role, "user")
	}
}

func TestGetUserByEmailNotFound(t *testing.T) {
	store := testStore(t)

	got, err := store.GetUserByEmail("nonexistent@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent user")
	}
}

func TestUpdateUserRole(t *testing.T) {
	store := testStore(t)

	user, _ := store.CreateUser("charlie@example.com", "user")
	if err := store.UpdateUserRole(user.ID, "admin"); err != nil {
		t.Fatalf("UpdateUserRole failed: %v", err)
	}

	got, _ := store.GetUserByEmail("charlie@example.com")
	if got.Role != "admin" {
		t.Errorf("Role = %q, want %q after update", got.Role, "admin")
	}
}

func TestListUsers(t *testing.T) {
	store := testStore(t)

	store.CreateUser("user1@example.com", "user")
	store.CreateUser("user2@example.com", "admin")

	users, err := store.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers failed: %v", err)
	}
	if len(users) != 2 {
		t.Errorf("len(ListUsers) = %d, want 2", len(users))
	}
}

func TestTouchUserLastLogin(t *testing.T) {
	store := testStore(t)

	user, _ := store.CreateUser("login@example.com", "user")
	if err := store.TouchUserLastLogin(user.ID); err != nil {
		t.Fatalf("TouchUserLastLogin failed: %v", err)
	}

	got, _ := store.GetUserByEmail("login@example.com")
	if got == nil {
		t.Fatal("expected user, got nil")
	}
}

func TestPterodactylLinkCRUD(t *testing.T) {
	store := testStore(t)

	srv := &Server{Name: "test", Address: "t:25565", GameType: "minecraft", State: "online"}
	if err := store.CreateServer(srv); err != nil {
		t.Fatalf("CreateServer failed: %v", err)
	}

	link := &PterodactylLink{
		ServerID:       srv.ID,
		PteroServerID:  "abc-123-def",
		AllowedActions: `["start","stop"]`,
	}
	if err := store.CreatePterodactylLink(link); err != nil {
		t.Fatalf("CreatePterodactylLink failed: %v", err)
	}

	got, err := store.GetPterodactylLink(srv.ID)
	if err != nil {
		t.Fatalf("GetPterodactylLink failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected link, got nil")
	}
	if got.PteroServerID != "abc-123-def" {
		t.Errorf("PteroServerID = %q, want %q", got.PteroServerID, "abc-123-def")
	}

	got.AllowedActions = `["start","stop","restart"]`
	if err := store.UpdatePterodactylLink(got); err != nil {
		t.Fatalf("UpdatePterodactylLink failed: %v", err)
	}

	updated, _ := store.GetPterodactylLink(srv.ID)
	if updated.AllowedActions != `["start","stop","restart"]` {
		t.Errorf("AllowedActions = %q, want updated value", updated.AllowedActions)
	}

	if err := store.DeletePterodactylLink(srv.ID); err != nil {
		t.Fatalf("DeletePterodactylLink failed: %v", err)
	}

	deleted, _ := store.GetPterodactylLink(srv.ID)
	if deleted != nil {
		t.Error("expected nil after delete")
	}
}

func TestPterodactylCommandCRUD(t *testing.T) {
	store := testStore(t)

	srv := &Server{Name: "cmdtest", Address: "c:25565", GameType: "minecraft", State: "online"}
	if err := store.CreateServer(srv); err != nil {
		t.Fatalf("CreateServer failed: %v", err)
	}

	cmd1 := &PterodactylCommand{ServerID: srv.ID, Command: "seed", DisplayName: "Random Seed"}
	if err := store.CreatePterodactylCommand(cmd1); err != nil {
		t.Fatalf("CreatePterodactylCommand failed: %v", err)
	}

	cmd2 := &PterodactylCommand{ServerID: srv.ID, Command: "time set day", DisplayName: "Set Day"}
	if err := store.CreatePterodactylCommand(cmd2); err != nil {
		t.Fatalf("CreatePterodactylCommand failed: %v", err)
	}

	commands, err := store.ListPterodactylCommands(srv.ID)
	if err != nil {
		t.Fatalf("ListPterodactylCommands failed: %v", err)
	}
	if len(commands) != 2 {
		t.Errorf("len(commands) = %d, want 2", len(commands))
	}

	if err := store.DeletePterodactylCommand(cmd1.ID); err != nil {
		t.Fatalf("DeletePterodactylCommand failed: %v", err)
	}

	commands, _ = store.ListPterodactylCommands(srv.ID)
	if len(commands) != 1 {
		t.Errorf("len(commands) after delete = %d, want 1", len(commands))
	}
}

func TestStructuredAccessGrants(t *testing.T) {
	store := testStore(t)
	server := &Server{Name: "grant-test", GameType: "minecraft", State: "online"}
	if err := store.CreateServer(server); err != nil {
		t.Fatal(err)
	}
	grant := &ServerAccessGrant{
		ServerID: server.ID, SubjectType: "group", Subject: "Game Operators",
		Capabilities: []string{"start", "command"},
	}
	if err := store.SetServerAccessGrant(grant); err != nil {
		t.Fatal(err)
	}
	if allowed, governed, err := store.ServerAccessAllowed(server.ID, "player@example.test", []string{"Game Operators"}, "start"); err != nil || !governed || !allowed {
		t.Fatalf("start allowed=%v governed=%v err=%v", allowed, governed, err)
	}
	if allowed, governed, err := store.ServerAccessAllowed(server.ID, "player@example.test", []string{"Game Operators"}, "command:seed"); err != nil || !governed || !allowed {
		t.Fatalf("command allowed=%v governed=%v err=%v", allowed, governed, err)
	}
	if allowed, governed, err := store.ServerAccessAllowed(server.ID, "player@example.test", []string{"Game Operators"}, "stop"); err != nil || !governed || allowed {
		t.Fatalf("stop allowed=%v governed=%v err=%v", allowed, governed, err)
	}
}

func TestGameIdentityAndServerCascade(t *testing.T) {
	store := testStore(t)
	server := &Server{Name: "identity-test", GameType: "minecraft", State: "online"}
	if err := store.CreateServer(server); err != nil {
		t.Fatal(err)
	}
	if server.ID == 0 {
		t.Fatal("CreateServer did not populate ID")
	}
	identity := &GameIdentity{
		UserEmail: "player@example.test", GameType: "minecraft", Username: "PlayerOne",
		ExternalID: "123456781234123412341234567890ab", Source: "self",
	}
	if err := store.SetGameIdentity(identity); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetGameIdentity(identity.UserEmail, identity.GameType)
	if err != nil || got == nil || got.Username != identity.Username || got.ExternalID != identity.ExternalID {
		t.Fatalf("identity=%#v err=%v", got, err)
	}
	if err := store.SetServerTags(server.ID, []string{"temporary"}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteServer(server.ID); err != nil {
		t.Fatal(err)
	}
	tags, err := store.GetServerTags(server.ID)
	if err != nil || len(tags) != 0 {
		t.Fatalf("orphan tags=%v err=%v", tags, err)
	}
}

func TestGameTypesAndConsoleHistory(t *testing.T) {
	store := testStore(t)
	custom := &GameType{
		Slug: "custom_game", DisplayName: "Custom Game", PlayerNoun: "Pilots",
		Icon: "✦", AccentColor: "#123456", Kind: "generic", Enabled: true,
	}
	if err := store.SetGameType(custom); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetGameType(custom.Slug)
	if err != nil || got == nil || got.DisplayName != custom.DisplayName || got.PlayerNoun != "Pilots" ||
		got.Kind != "generic" || !got.Enabled {
		t.Fatalf("game type=%#v err=%v", got, err)
	}
	minecraft, err := store.GetGameType("minecraft")
	if err != nil || minecraft == nil || minecraft.Kind != "embedded" || !minecraft.Enabled {
		t.Fatalf("embedded game type=%#v err=%v", minecraft, err)
	}
	minecraft.Enabled = false
	if err := store.SetGameType(minecraft); err != nil {
		t.Fatal(err)
	}
	if driver := store.ResolveGameDriver("minecraft"); driver.Kind != "generic" || driver.SupportsWhitelist() {
		t.Fatalf("disabled type retained specialized behavior: %#v", driver)
	}
	server := &Server{Name: "history-test", GameType: custom.Slug, State: "online"}
	if err := store.CreateServer(server); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"server line", "> command", "response"} {
		if err := store.AppendConsoleHistory(server.ID, "server", line, 2); err != nil {
			t.Fatal(err)
		}
	}
	history, err := store.ListConsoleHistory(server.ID, 10)
	if err != nil || len(history) != 2 || history[0].Line != "> command" || history[1].Line != "response" {
		t.Fatalf("bounded console history=%#v err=%v", history, err)
	}
	if err := store.DeleteGameType(custom.Slug); err == nil {
		t.Fatal("removed a game type still used by a server")
	}
}

func TestPruneUnusedBackgroundGameTags(t *testing.T) {
	store := testStore(t)
	server := &Server{Name: "tag-server", GameType: "minecraft", State: "online"}
	if err := store.CreateServer(server); err != nil {
		t.Fatal(err)
	}
	background := &Background{
		Filename: "background.webp", ContentHash: "abc123", Enabled: true,
		Tags: []string{"dark", "minecraft", "valheim"},
	}
	if err := store.CreateBackground(background); err != nil {
		t.Fatal(err)
	}
	if err := store.PruneUnusedBackgroundGameTags(); err != nil {
		t.Fatal(err)
	}
	tags, err := store.GetBackgroundTags(background.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 || tags[0] != "dark" || tags[1] != "minecraft" {
		t.Fatalf("tags after prune = %#v", tags)
	}
}

func TestAgentPendingOpCRUD(t *testing.T) {
	store := testStore(t)

	op := &AgentPendingOp{
		RequestID: "req-123",
		AgentID:   1,
		OpType:    "action",
		Payload:   `{"action":"start"}`,
		CreatedAt: "2024-01-01T00:00:00Z",
		ExpiresAt: "2024-01-01T00:05:00Z",
		Resolved:  false,
	}

	if err := store.CreateAgentPendingOp(op); err != nil {
		t.Fatalf("CreateAgentPendingOp failed: %v", err)
	}
	if op.ID == 0 {
		t.Error("expected ID to be set")
	}

	got, err := store.GetAgentPendingOp("req-123")
	if err != nil {
		t.Fatalf("GetAgentPendingOp failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected op, got nil")
	}
	if got.OpType != "action" {
		t.Errorf("OpType = %q, want action", got.OpType)
	}

	ops, err := store.ListPendingOpsByAgent(1)
	if err != nil {
		t.Fatalf("ListPendingOpsByAgent failed: %v", err)
	}
	if len(ops) != 1 {
		t.Errorf("len(ops) = %d, want 1", len(ops))
	}

	allOps, err := store.ListAllPendingOps()
	if err != nil {
		t.Fatalf("ListAllPendingOps failed: %v", err)
	}
	if len(allOps) != 1 {
		t.Errorf("len(allOps) = %d, want 1", len(allOps))
	}

	if err := store.ResolveAgentPendingOp("req-123"); err != nil {
		t.Fatalf("ResolveAgentPendingOp failed: %v", err)
	}

	resolved, _ := store.GetAgentPendingOp("req-123")
	if !resolved.Resolved {
		t.Error("expected op to be resolved")
	}

	// After resolve, should not appear in pending lists
	ops, _ = store.ListPendingOpsByAgent(1)
	if len(ops) != 0 {
		t.Errorf("len(ops) after resolve = %d, want 0", len(ops))
	}
}

func TestAgentPendingOpCleanup(t *testing.T) {
	store := testStore(t)

	// Create an expired resolved op
	op := &AgentPendingOp{
		RequestID: "req-old",
		AgentID:   1,
		OpType:    "action",
		Payload:   "{}",
		CreatedAt: "2020-01-01T00:00:00Z",
		ExpiresAt: "2020-01-01T00:05:00Z",
		Resolved:  true,
	}
	if err := store.CreateAgentPendingOp(op); err != nil {
		t.Fatalf("CreateAgentPendingOp failed: %v", err)
	}

	if err := store.CleanupExpiredPendingOps(); err != nil {
		t.Fatalf("CleanupExpiredPendingOps failed: %v", err)
	}

	got, _ := store.GetAgentPendingOp("req-old")
	if got != nil {
		t.Error("expected expired op to be cleaned up")
	}
}

func TestListAuditLogScansJSONText(t *testing.T) {
	store := testStore(t)
	entry := &AuditLogEntry{
		Timestamp:   "2026-01-02T03:04:05Z",
		UserEmail:   "operator@example.test",
		ServerName:  "synthetic-server",
		Action:      "start",
		Params:      json.RawMessage(`{"reason":"test"}`),
		Result:      "allowed",
		Reason:      "test entry",
		Source:      "test",
		ClientIP:    "198.51.100.42",
		CountryCode: "DE",
	}
	if err := store.CreateAuditLog(entry); err != nil {
		t.Fatalf("CreateAuditLog failed: %v", err)
	}

	entries, err := store.ListAuditLog(10, 0)
	if err != nil {
		t.Fatalf("ListAuditLog failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	if string(entries[0].Params) != `{"reason":"test"}` {
		t.Fatalf("params = %s", entries[0].Params)
	}
	if entries[0].ClientIP != "198.51.100.42" || entries[0].CountryCode != "DE" {
		t.Fatalf("request context = %q/%q", entries[0].ClientIP, entries[0].CountryCode)
	}
}

func TestUserWhitelistLinksAreUniquePerServerUsername(t *testing.T) {
	store := testStore(t)
	if err := store.CreateServer(&Server{Name: "Roster", GameType: "minecraft", State: "online"}); err != nil {
		t.Fatal(err)
	}
	server, _ := store.GetServerByName("Roster")
	if err := store.SetUserWhitelist("first@example.test", server.ID, "TestPlayer"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetUserWhitelist("second@example.test", server.ID, "testplayer"); err != nil {
		t.Fatal(err)
	}
	entries, err := store.ListUserWhitelists(server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].UserEmail != "second@example.test" {
		t.Fatalf("links=%#v, want the replacement panel user", entries)
	}
	if err := store.DeleteUserWhitelistsByUsername(server.ID, "TESTPLAYER"); err != nil {
		t.Fatal(err)
	}
	entries, err = store.ListUserWhitelists(server.ID)
	if err != nil || len(entries) != 0 {
		t.Fatalf("links after case-insensitive removal=%#v err=%v", entries, err)
	}
}

func TestCaseSensitiveUserWhitelistLinksRemainDistinct(t *testing.T) {
	store := testStore(t)
	if err := store.CreateServer(&Server{Name: "Viking Hall", GameType: "valheim", State: "online"}); err != nil {
		t.Fatal(err)
	}
	server, _ := store.GetServerByName("Viking Hall")
	if err := store.SetUserWhitelistForIdentity(
		"first@example.test", server.ID, "Steam_ABC", true,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.SetUserWhitelistForIdentity(
		"second@example.test", server.ID, "steam_ABC", true,
	); err != nil {
		t.Fatal(err)
	}
	entries, err := store.ListUserWhitelists(server.ID)
	if err != nil || len(entries) != 2 {
		t.Fatalf("case-sensitive links=%#v err=%v", entries, err)
	}
	if err := store.DeleteUserWhitelistsByIdentity(server.ID, "Steam_ABC", true); err != nil {
		t.Fatal(err)
	}
	entries, err = store.ListUserWhitelists(server.ID)
	if err != nil || len(entries) != 1 || entries[0].Username != "steam_ABC" {
		t.Fatalf("links after exact removal=%#v err=%v", entries, err)
	}
}
