package api

import (
	"context"
	"testing"

	"github.com/tionis/hogs/agent"
	"github.com/tionis/hogs/backend"
	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"
)

type memoryFileBackend struct {
	files map[string][]byte
}

func (b *memoryFileBackend) Start(context.Context) error   { return nil }
func (b *memoryFileBackend) Stop(context.Context) error    { return nil }
func (b *memoryFileBackend) Restart(context.Context) error { return nil }
func (b *memoryFileBackend) SendCommand(context.Context, string) error {
	return nil
}
func (b *memoryFileBackend) Status(context.Context) (*backend.ServerStatus, error) {
	return &backend.ServerStatus{Online: true}, nil
}
func (b *memoryFileBackend) Name() string { return "memory" }
func (b *memoryFileBackend) FileRead(_ context.Context, serverPath string) ([]byte, error) {
	content, ok := b.files[serverPath]
	if !ok {
		return nil, agent.ErrFileNotFound
	}
	return append([]byte(nil), content...), nil
}
func (b *memoryFileBackend) FileWrite(_ context.Context, serverPath string, content []byte) error {
	if b.files == nil {
		b.files = map[string][]byte{}
	}
	b.files[serverPath] = append([]byte(nil), content...)
	return nil
}

func adminReconcileFixture(t *testing.T) (*database.Store, *database.Server, *memoryFileBackend, *WhitelistReconciler) {
	t.Helper()
	store, err := database.NewStore(t.TempDir() + "/hogs.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DB.Close() })
	server := &database.Server{
		ManagementID: "managed", Name: "Managed", GameType: "valheim",
		Address: "game.example.test", State: "offline",
	}
	if err := store.CreateServer(server); err != nil {
		t.Fatal(err)
	}
	if err := store.CreatePterodactylLink(&database.PterodactylLink{
		ServerID: server.ID, PteroServerID: "managed", Node: "test-node",
	}); err != nil {
		t.Fatal(err)
	}
	// Password admission disables the whitelist pass so the test isolates
	// the admin pass, which is orthogonal to join enforcement.
	if err := store.SetServerJoinEnforcementMode(server.ID, database.JoinEnforcementPassword); err != nil {
		t.Fatal(err)
	}
	user, err := store.CreateUser("viking", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetUserActive(user.ID, true); err != nil {
		t.Fatal(err)
	}
	// Valheim resolves linked identities through the steam provider key,
	// mirroring how SCIM syncs Authentik's game_identities attribute.
	if err := store.ReplaceSCIMGameIdentities("viking", []database.GameIdentity{{
		GameType: "steam", Username: "linked-steam",
		ExternalID: "76561198000000001",
	}}); err != nil {
		t.Fatal(err)
	}
	memory := &memoryFileBackend{files: map[string][]byte{}}
	handler := NewPterodactylHandler(store, &config.Config{}, nil, nil, nil)
	handler.BackendResolver = func(*database.Server, *database.PterodactylLink) (backend.Backend, error) {
		return memory, nil
	}
	return store, server, memory, NewWhitelistReconciler(handler)
}

func TestAdminReconciliationManagesValheimAdminList(t *testing.T) {
	store, server, memory, reconciler := adminReconcileFixture(t)
	grant := &database.ServerAccessGrant{
		ServerID: server.ID, SubjectType: "user", Subject: "viking",
		Effect: "allow", Capabilities: []string{"server.admin"},
	}
	if err := store.SetServerAccessGrant(grant); err != nil {
		t.Fatal(err)
	}
	memory.files["adminlist.txt"] = []byte("// List admin players ID  ONE per line\nV_76561198000000002\nV_76561198000000003\n")
	if err := store.SetUserAdminForIdentity("viking", server.ID, "V_76561198000000003", true); err != nil {
		t.Fatal(err)
	}
	first, err := reconciler.ReconcileServer(context.Background(), server.ID)
	if err != nil {
		t.Fatal(err)
	}
	_ = first
	admins, err := store.ListUserAdmins(server.ID)
	if err != nil || len(admins) != 1 || admins[0].Username != "V_76561198000000001" {
		t.Fatalf("admin ownership=%#v err=%v", admins, err)
	}
	want := "// List admin players ID  ONE per line\nV_76561198000000002\nV_76561198000000001\n"
	if string(memory.files["adminlist.txt"]) != want {
		t.Fatalf("admin list=%q", memory.files["adminlist.txt"])
	}
	second, err := reconciler.ReconcileServer(context.Background(), server.ID)
	if err != nil {
		t.Fatal(err)
	}
	_ = second
	if string(memory.files["adminlist.txt"]) != want {
		t.Fatalf("idempotent admin list=%q", memory.files["adminlist.txt"])
	}
	if err := store.DeleteServerAccessGrant(grant.ID, server.ID); err != nil {
		t.Fatal(err)
	}
	revoked, err := reconciler.ReconcileServer(context.Background(), server.ID)
	if err != nil {
		t.Fatal(err)
	}
	_ = revoked
	remaining := "// List admin players ID  ONE per line\nV_76561198000000002\n"
	if string(memory.files["adminlist.txt"]) != remaining {
		t.Fatalf("revoked admin list=%q", memory.files["adminlist.txt"])
	}
	if rows, _ := store.ListUserAdmins(server.ID); len(rows) != 0 {
		t.Fatalf("revoked ownership survived: %#v", rows)
	}
}
