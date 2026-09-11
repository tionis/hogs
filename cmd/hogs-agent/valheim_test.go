package main

import "testing"

func TestParseValheimConnections(t *testing.T) {
	for _, test := range []struct {
		name    string
		output  string
		players int
		known   bool
	}{
		{
			name:    "empty journal",
			output:  "",
			players: 0,
			known:   false,
		},
		{
			name:    "unrelated lines",
			output:  "09/11/2026 18:23:23: Game server connected\nDungeonDB Awake 16557\n",
			players: 0,
			known:   false,
		},
		{
			name:    "latest line wins",
			output:  "09/11/2026 18:06:58:  Connections 1 ZDOS:11633  sent:0 recv:0\n09/11/2026 18:08:25:  Connections 0 ZDOS:11633  sent:0 recv:0\n",
			players: 0,
			known:   true,
		},
		{
			name:    "connected players",
			output:  "09/11/2026 18:40:35: Got handshake from client 76561198135472318\n09/11/2026 18:41:02:  Connections 2 ZDOS:12001  sent:10 recv:12\n",
			players: 2,
			known:   true,
		},
		{
			name:    "ignores connection state lines",
			output:  "Got status changed msg k_ESteamNetworkingConnectionState_Connecting\n",
			players: 0,
			known:   false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			players, known := parseValheimConnections(test.output)
			if players != test.players || known != test.known {
				t.Fatalf("players=%d known=%v", players, known)
			}
		})
	}
}

func TestNativeUnitCommandReportsUnsupported(t *testing.T) {
	if podmanContainerExists("hogs-test-nonexistent-container") {
		t.Fatal("unexpected container matched the test name")
	}
	server := &ServerConfig{Unit: "hogs-test-nonexistent.service", GameType: "valheim"}
	_, err := executeCommand(server, "help")
	unsupported, ok := err.(*commandError)
	if !ok || unsupported.code != "unsupported" {
		t.Fatalf("native unit command error=%v", err)
	}
}
