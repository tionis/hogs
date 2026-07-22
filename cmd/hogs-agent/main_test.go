package main

import (
	"bytes"
	"os"
	"path/filepath"
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
