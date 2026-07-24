package agent

import (
	"strings"
	"testing"

	"github.com/tionis/hogs/database"
)

func TestAgentBackendStatusReadsLiveWorkerState(t *testing.T) {
	status, err := decodeBackendStatus(strings.NewReader(
		`{"serverId":"cog","online":false,"substate":"dead","players":0,"maxPlayers":20,"version":"1.21"}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if status.Online || status.MaxPlayers != 20 || status.Version != "1.21" {
		t.Fatalf("unexpected worker status: %#v", status)
	}
}

func TestAgentBackendUsesImmutableIDAfterDisplayNameChange(t *testing.T) {
	store, err := database.NewStore(t.TempDir() + "/hogs.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.DB.Close() })
	server := &database.Server{
		ManagementID: "factorio", Name: "Factorio", GameType: "factorio", State: "online",
	}
	if err := store.CreateServer(server); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAgent(&database.Agent{Name: "vigil", NodeName: "vigil"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreatePterodactylLink(&database.PterodactylLink{
		ServerID: server.ID, PteroServerID: "agent:factorio", Node: "vigil",
	}); err != nil {
		t.Fatal(err)
	}
	service := NewAgentService(store, &Manager{})
	resolved, err := service.backend("Factorio")
	if err != nil || resolved.ServerID != "factorio" {
		t.Fatalf("initial backend=%#v err=%v", resolved, err)
	}
	server.Name = "Old Factorio Server"
	if err := store.UpdateServer(server); err != nil {
		t.Fatal(err)
	}
	resolved, err = service.backend("Old Factorio Server")
	if err != nil || resolved.ServerID != "factorio" {
		t.Fatalf("renamed backend=%#v err=%v", resolved, err)
	}
}
