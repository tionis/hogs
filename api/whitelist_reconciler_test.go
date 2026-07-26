package api

import (
	"context"
	"testing"

	"github.com/tionis/hogs/backend"
	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"
)

type memoryWhitelistBackend struct {
	entries []backend.WhitelistEntry
}

func (b *memoryWhitelistBackend) Start(context.Context) error   { return nil }
func (b *memoryWhitelistBackend) Stop(context.Context) error    { return nil }
func (b *memoryWhitelistBackend) Restart(context.Context) error { return nil }
func (b *memoryWhitelistBackend) SendCommand(context.Context, string) error {
	return nil
}
func (b *memoryWhitelistBackend) Status(context.Context) (*backend.ServerStatus, error) {
	return &backend.ServerStatus{Online: true}, nil
}
func (b *memoryWhitelistBackend) Name() string { return "memory" }
func (b *memoryWhitelistBackend) Whitelist(_ context.Context, request backend.WhitelistRequest) (*backend.WhitelistResult, error) {
	switch request.Operation {
	case "add":
		for _, entry := range b.entries {
			if entry.Name == request.Username {
				return &backend.WhitelistResult{Mode: "online", Entries: b.entries}, nil
			}
		}
		b.entries = append(b.entries, backend.WhitelistEntry{Name: request.Username, UUID: request.ExternalID})
	case "remove":
		filtered := b.entries[:0]
		for _, entry := range b.entries {
			if entry.Name != request.Username {
				filtered = append(filtered, entry)
			}
		}
		b.entries = filtered
	}
	return &backend.WhitelistResult{Mode: "online", Entries: append([]backend.WhitelistEntry(nil), b.entries...)}, nil
}

func TestWhitelistReconciliationPreservesExternalEntriesAndRemovesOwnedRevocations(t *testing.T) {
	store, err := database.NewStore(t.TempDir() + "/hogs.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DB.Close() })
	server := &database.Server{
		ManagementID: "managed", Name: "Managed", GameType: "minecraft",
		Address: "game.example.test", State: "offline",
	}
	if err := store.CreateServer(server); err != nil {
		t.Fatal(err)
	}
	if err := store.CreatePterodactylLink(&database.PterodactylLink{
		ServerID: server.ID, PteroServerID: "managed", AllowedActions: `["whitelist"]`,
	}); err != nil {
		t.Fatal(err)
	}
	user, err := store.CreateUser("player", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetUserActive(user.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceSCIMGameIdentities("player", []database.GameIdentity{{
		GameType: "minecraft", Username: "ManagedPlayer",
		ExternalID: "00000000-0000-0000-0000-000000000042",
	}}); err != nil {
		t.Fatal(err)
	}
	grant := &database.ServerAccessGrant{
		ServerID: server.ID, SubjectType: "user", Subject: "player",
		Effect: "allow", Capabilities: []string{"server.join"},
	}
	if err := store.SetServerAccessGrant(grant); err != nil {
		t.Fatal(err)
	}
	memory := &memoryWhitelistBackend{entries: []backend.WhitelistEntry{{Name: "ExternalPlayer"}}}
	handler := NewPterodactylHandler(store, &config.Config{}, nil, nil, nil)
	handler.BackendResolver = func(*database.Server, *database.PterodactylLink) (backend.Backend, error) {
		return memory, nil
	}
	reconciler := NewWhitelistReconciler(handler)

	first, err := reconciler.ReconcileServer(context.Background(), server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Added != 1 || first.Manual != 1 {
		t.Fatalf("first result=%#v", first)
	}
	owned, _ := store.GetUserWhitelist("player", server.ID)
	if owned == nil || owned.Username != "ManagedPlayer" {
		t.Fatalf("managed ownership=%#v", owned)
	}

	if err := store.DeleteServerAccessGrant(grant.ID, server.ID); err != nil {
		t.Fatal(err)
	}
	second, err := reconciler.ReconcileServer(context.Background(), server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Removed != 1 || len(memory.entries) != 1 || memory.entries[0].Name != "ExternalPlayer" {
		t.Fatalf("second=%#v entries=%#v", second, memory.entries)
	}
}
