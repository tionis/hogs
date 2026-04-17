package database

import (
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
