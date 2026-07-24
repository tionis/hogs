package agent

import (
	"strings"
	"testing"
)

func TestAgentBackendStatusReadsLiveWorkerState(t *testing.T) {
	status, err := decodeBackendStatus(strings.NewReader(
		`{"serverName":"cog","online":false,"substate":"dead","players":0,"maxPlayers":20,"version":"1.21"}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if status.Online || status.MaxPlayers != 20 || status.Version != "1.21" {
		t.Fatalf("unexpected worker status: %#v", status)
	}
}
