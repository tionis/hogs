package agent

import "testing"

func TestAgentBackendStatusReadsLiveWorkerState(t *testing.T) {
	status, err := decodeBackendStatus(map[string]interface{}{
		"online": false, "players": float64(0), "maxPlayers": float64(20), "version": "1.21",
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.Online || status.MaxPlayers != 20 || status.Version != "1.21" {
		t.Fatalf("unexpected worker status: %#v", status)
	}
}
