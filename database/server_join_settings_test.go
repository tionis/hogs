package database

import "testing"

func TestServerJoinEnforcementDefaultsAndPersists(t *testing.T) {
	store := testStore(t)
	server := &Server{Name: "Join mode", GameType: "factorio", State: "online"}
	if err := store.CreateServer(server); err != nil {
		t.Fatal(err)
	}
	mode, err := store.GetServerJoinEnforcementMode(server.ID)
	if err != nil || mode != JoinEnforcementAuto {
		t.Fatalf("default mode=%q err=%v", mode, err)
	}
	if !JoinWhitelistEnabled(mode, true) || JoinWhitelistEnabled(mode, false) {
		t.Fatal("automatic enforcement did not follow driver whitelist support")
	}
	if err := store.SetServerJoinEnforcementMode(server.ID, JoinEnforcementPassword); err != nil {
		t.Fatal(err)
	}
	mode, err = store.GetServerJoinEnforcementMode(server.ID)
	if err != nil || mode != JoinEnforcementPassword || JoinWhitelistEnabled(mode, true) {
		t.Fatalf("password mode=%q enabled=%t err=%v", mode, JoinWhitelistEnabled(mode, true), err)
	}
	if err := store.SetServerJoinEnforcementMode(server.ID, "invalid"); err == nil {
		t.Fatal("invalid enforcement mode was accepted")
	}
}
