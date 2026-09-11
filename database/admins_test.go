package database

import (
	"testing"
)

func TestUserAdminOwnershipTracksIdentityMoves(t *testing.T) {
	store, err := NewStore(t.TempDir() + "/hogs.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DB.Close() })
	server := &Server{
		ManagementID: "managed", Name: "Managed", GameType: "valheim",
		Address: "game.example.test", State: "offline",
	}
	if err := store.CreateServer(server); err != nil {
		t.Fatal(err)
	}
	if err := store.SetUserAdminForIdentity("viking", server.ID, "V_111", true); err != nil {
		t.Fatal(err)
	}
	owned, err := store.GetUserAdmin("viking", server.ID)
	if err != nil || owned == nil || owned.Username != "V_111" {
		t.Fatalf("admin ownership=%#v err=%v", owned, err)
	}
	listed, err := store.ListUserAdmins(server.ID)
	if err != nil || len(listed) != 1 {
		t.Fatalf("admin list=%#v err=%v", listed, err)
	}
	// The same platform identity claimed by another user moves ownership.
	if err := store.SetUserAdminForIdentity("other", server.ID, "V_111", true); err != nil {
		t.Fatal(err)
	}
	if moved, _ := store.GetUserAdmin("other", server.ID); moved == nil {
		t.Fatal("admin ownership did not move to the claiming user")
	}
	if stale, _ := store.GetUserAdmin("viking", server.ID); stale != nil {
		t.Fatalf("stale admin ownership survived: %#v", stale)
	}
	if err := store.DeleteUserAdminsByIdentity(server.ID, "V_111", true); err != nil {
		t.Fatal(err)
	}
	if remaining, _ := store.ListUserAdmins(server.ID); len(remaining) != 0 {
		t.Fatalf("admin entries survived identity deletion: %#v", remaining)
	}
}
