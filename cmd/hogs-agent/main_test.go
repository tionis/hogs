package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testNodeConfig(root string) AgentConfig {
	return AgentConfig{
		Node: "node-a", ServerURL: "wss://hogs.example.test/agent/ws", ResticBin: "restic",
		Servers: map[string]ServerConfig{
			"alpha": {Unit: "game-alpha.service", GameType: "minecraft", DataDir: filepath.Join(root, "alpha")},
			"beta":  {Unit: "game-beta.service", GameType: "factorio", DataDir: filepath.Join(root, "beta")},
		},
	}
}

func TestAgentCapabilitiesOnlyAdvertiseConfiguredBackup(t *testing.T) {
	agentConfig = testNodeConfig(t.TempDir())
	if strings.Contains(strings.Join(agentCapabilities(), ","), "backup") {
		t.Fatal("backup capability advertised without a local backup profile")
	}
	server := agentConfig.Servers["alpha"]
	server.Backup.EnvironmentFile = "/etc/restic/restic.env"
	agentConfig.Servers["alpha"] = server
	if !strings.Contains(strings.Join(agentCapabilities(), ","), "backup") {
		t.Fatal("backup capability missing with a local backup profile")
	}
}

func TestResticEnvLoadsNodeLocalProfile(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "restic.env")
	if err := os.WriteFile(profile, []byte("export RESTIC_REPOSITORY=local:/backup\nexport RESTIC_PASSWORD_FILE=\"/run/secret\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	server := &ServerConfig{Backup: BackupConfig{EnvironmentFile: profile}}
	env, err := resticEnv(server)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "RESTIC_REPOSITORY=local:/backup") || !strings.Contains(joined, "RESTIC_PASSWORD_FILE=/run/secret") {
		t.Fatalf("local backup profile was not loaded: %v", env)
	}
}

func TestResticEnvRejectsIncompleteOrMalformedProfiles(t *testing.T) {
	for name, content := range map[string]string{
		"missing password": "RESTIC_REPOSITORY=local:/backup\n",
		"malformed key":    "RESTIC-REPOSITORY=local:/backup\nRESTIC_PASSWORD=x\n",
	} {
		t.Run(name, func(t *testing.T) {
			profile := filepath.Join(t.TempDir(), "restic.env")
			if err := os.WriteFile(profile, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := resticEnv(&ServerConfig{Backup: BackupConfig{EnvironmentFile: profile}})
			if err == nil {
				t.Fatal("unsafe or incomplete backup profile was accepted")
			}
		})
	}
}

func TestRCONPacketRoundTrip(t *testing.T) {
	var packet bytes.Buffer
	if err := writeRCONPacket(&packet, 7, 2, "list"); err != nil {
		t.Fatal(err)
	}
	id, packetType, body, err := readRCONPacket(&packet)
	if err != nil || id != 7 || packetType != 2 || body != "list" {
		t.Fatalf("id=%d type=%d body=%q err=%v", id, packetType, body, err)
	}
}

func TestParsePlayerStatus(t *testing.T) {
	players, maxPlayers, known := parsePlayerStatus("minecraft", "There are 2 of a max of 20 players online: Alex, Steve")
	if !known || players != 2 || maxPlayers != 20 {
		t.Fatalf("minecraft status=%d/%d known=%v", players, maxPlayers, known)
	}
	players, maxPlayers, known = parsePlayerStatus("factorio", "Online players:\nAlice (online)\nBob (online)\nOffline players:\nCarol")
	if !known || players != 2 || maxPlayers != 0 {
		t.Fatalf("factorio status=%d/%d known=%v", players, maxPlayers, known)
	}
	if _, _, known = parsePlayerStatus("minecraft", "unexpected response"); known {
		t.Fatal("malformed status was marked known")
	}
}

func TestRoutineRCONConnectionLineFilter(t *testing.T) {
	for _, line := range []string{
		"[RCON Listener #2/INFO] Thread RCON Client /10.0.0.1 started",
		"[RconClient] Thread RCON Client /10.0.0.1 shutting down",
	} {
		if !isRoutineRCONConnectionLine(line) {
			t.Fatalf("routine RCON line was not filtered: %q", line)
		}
	}
	if isRoutineRCONConnectionLine("[Server thread/INFO] Player joined the game") {
		t.Fatal("normal server output was filtered")
	}
}

func TestValidateConfigAcceptsNodeScopedServerAllowlist(t *testing.T) {
	cfg := testNodeConfig(t.TempDir())
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestServerConfigRejectsUnknownServer(t *testing.T) {
	agentConfig = testNodeConfig(t.TempDir())
	if _, err := serverConfig("not-allowed"); err == nil {
		t.Fatal("unknown server was accepted")
	}
}

func TestResolvePathStaysInsideSelectedServer(t *testing.T) {
	root := t.TempDir()
	server := ServerConfig{DataDir: filepath.Join(root, "alpha")}
	if err := os.MkdirAll(server.DataDir, 0755); err != nil {
		t.Fatal(err)
	}
	path, err := resolvePath(&server, "world/level.dat")
	if err != nil || path != filepath.Join(root, "alpha", "world", "level.dat") {
		t.Fatalf("valid path=%q err=%v", path, err)
	}
	if _, err := resolvePath(&server, "../beta/secret"); err == nil {
		t.Fatal("path traversal escaped the selected server")
	}
	if _, err := resolvePath(&server, filepath.Join(root, "beta", "secret")); err == nil {
		t.Fatal("absolute path escaped the selected server")
	}
	if err := os.Symlink(filepath.Join(root, "beta"), filepath.Join(server.DataDir, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := resolvePath(&server, "escape/secret"); err == nil {
		t.Fatal("symlink escaped the selected server")
	}
}

func TestValidateConfigRejectsUnsafeUnitAndRelativeDataDir(t *testing.T) {
	cfg := testNodeConfig(t.TempDir())
	server := cfg.Servers["alpha"]
	server.Unit = "../unsafe.service"
	cfg.Servers["alpha"] = server
	if err := validateConfig(cfg); err == nil {
		t.Fatal("unsafe unit was accepted")
	}
	server.Unit = "safe.service"
	server.DataDir = "relative/path"
	cfg.Servers["alpha"] = server
	if err := validateConfig(cfg); err == nil {
		t.Fatal("relative data directory was accepted")
	}
}
