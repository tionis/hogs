package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tionis/hogs/internal/capability"
)

func testNodeConfig(root string) AgentConfig {
	return AgentConfig{
		Node: "node-a", ResticBin: "restic",
		API: AgentAPIConfig{
			Listen:         "127.0.0.1:9081",
			SecretFile:     "/run/credentials/hogs-agent.secret",
			AllowedOrigins: []string{"https://games.example.test"},
		},
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

func TestRCONResponseCombinesPackets(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		_ = writeRCONPacket(server, 2, 0, "Available commands:\n/help ")
		_ = writeRCONPacket(server, 2, 0, "<command>\n/players")
	}()

	response, err := readRCONResponse(client, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := "Available commands:\n/help <command>\n/players"
	if response != want {
		t.Fatalf("response = %q, want %q", response, want)
	}
}

func TestRCONResponseAcceptsEmptyEOF(t *testing.T) {
	client, server := net.Pipe()
	go server.Close()
	defer client.Close()
	response, err := readRCONResponse(client, 2)
	if err != nil || response != "" {
		t.Fatalf("empty response=%q err=%v, want successful empty output", response, err)
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
		"8378.028 Info RemoteCommandProcessor.cpp:245: New RCON connection from IP ADDR:({127.0.0.1:60184})",
	} {
		if !isRoutineRCONConnectionLine(line) {
			t.Fatalf("routine RCON line was not filtered: %q", line)
		}
	}
	if isRoutineRCONConnectionLine("[Server thread/INFO] Player joined the game") {
		t.Fatal("normal server output was filtered")
	}
}

func TestCommitConsoleStreamFlushesHeadersImmediately(t *testing.T) {
	recorder := httptest.NewRecorder()
	commitConsoleStream(recorder, recorder)

	if recorder.Code != http.StatusOK {
		t.Fatalf("console stream status=%d, want %d", recorder.Code, http.StatusOK)
	}
	if !recorder.Flushed {
		t.Fatal("console stream did not flush its response headers")
	}
}

func TestParseSystemdResourceValues(t *testing.T) {
	bytes := parseSystemdBytes("1073741824")
	if bytes == nil || *bytes != 1073741824 {
		t.Fatalf("memory value = %v", bytes)
	}
	for _, value := range []string{"", "[not set]", "infinity", "invalid"} {
		if parsed := parseSystemdBytes(value); parsed != nil {
			t.Fatalf("parseSystemdBytes(%q) = %d, want nil", value, *parsed)
		}
	}

	duration := parseSystemdDuration("500ms")
	if duration == nil || *duration != 500*time.Millisecond {
		t.Fatalf("CPU quota = %v", duration)
	}
	for _, value := range []string{"", "[not set]", "infinity", "invalid"} {
		if parsed := parseSystemdDuration(value); parsed != nil {
			t.Fatalf("parseSystemdDuration(%q) = %v, want nil", value, *parsed)
		}
	}
}

func TestSampleCPUPercentUsesUsageDelta(t *testing.T) {
	unit := "resource-test.service"
	start := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	resourceSampleMu.Lock()
	delete(resourceSamples, unit)
	resourceSampleMu.Unlock()

	if first := sampleCPUPercent(unit, uint64(time.Second), start, true); first != nil {
		t.Fatalf("first CPU sample = %v, want nil", *first)
	}
	percent := sampleCPUPercent(unit, uint64(2*time.Second), start.Add(2*time.Second), true)
	if percent == nil || *percent != 50 {
		t.Fatalf("CPU percent = %v, want 50", percent)
	}
	if stopped := sampleCPUPercent(unit, uint64(2*time.Second), start.Add(3*time.Second), false); stopped != nil {
		t.Fatalf("inactive CPU sample = %v, want nil", *stopped)
	}
}

func TestValidateConfigAcceptsNodeScopedServerAllowlist(t *testing.T) {
	cfg := testNodeConfig(t.TempDir())
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestAgentAPIAuthorizesScopedCapabilitiesAndCORS(t *testing.T) {
	agentConfig = testNodeConfig(t.TempDir())
	agentSecret = []byte("0123456789abcdef0123456789abcdef")
	handler := agentAPI()

	claims := capability.NewClaims("node-a", "hogs-control", http.MethodGet,
		"/v1/health", "", 0, time.Minute)
	token, err := capability.Sign(agentSecret, claims)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("authorized health status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/servers/alpha/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-route capability status=%d, want forbidden", recorder.Code)
	}

	req = httptest.NewRequest(http.MethodOptions, "/v1/servers/alpha/file", nil)
	req.Header.Set("Origin", "https://games.example.test")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNoContent ||
		recorder.Header().Get("Access-Control-Allow-Origin") != "https://games.example.test" {
		t.Fatalf("allowed preflight status=%d headers=%v", recorder.Code, recorder.Header())
	}

	req = httptest.NewRequest(http.MethodOptions, "/v1/servers/alpha/file", nil)
	req.Header.Set("Origin", "https://attacker.example.test")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("foreign origin preflight status=%d, want forbidden", recorder.Code)
	}
}

func TestAgentAPIFileOperationCapabilityBindsDestination(t *testing.T) {
	root := t.TempDir()
	agentConfig = testNodeConfig(root)
	agentSecret = []byte("0123456789abcdef0123456789abcdef")
	configDir := filepath.Join(agentConfig.Servers["alpha"].DataDir, "config")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "source.txt"), []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	claims := capability.NewClaims("node-a", "admin@example.test", http.MethodPost,
		"/v1/servers/alpha/file-operations", "config/source.txt", 0, time.Minute)
	claims.TargetPath = "config/copy.txt"
	token, err := capability.Sign(agentSecret, claims)
	if err != nil {
		t.Fatal(err)
	}
	handler := agentAPI()
	req := httptest.NewRequest(http.MethodPost,
		"/v1/servers/alpha/file-operations?operation=copy&path=config%2Fsource.txt&target=config%2Fcopy.txt", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("file operation status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost,
		"/v1/servers/alpha/file-operations?operation=copy&path=config%2Fsource.txt&target=config%2Fother.txt", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("changed destination status=%d, want forbidden", recorder.Code)
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

func TestFileOperationsRenameCopyAndMoveWithoutOverwrite(t *testing.T) {
	root := t.TempDir()
	server := ServerConfig{DataDir: filepath.Join(root, "alpha")}
	for _, dir := range []string{"config", "world"} {
		if err := os.MkdirAll(filepath.Join(server.DataDir, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(server.DataDir, "config", "source.txt"), []byte("managed data"), 0640); err != nil {
		t.Fatal(err)
	}

	if result := fileOperation(&server, "rename", "config/source.txt", "config/renamed.txt"); result["success"] != true {
		t.Fatalf("rename failed: %#v", result)
	}
	if result := fileOperation(&server, "copy", "config/renamed.txt", "config/copied.txt"); result["success"] != true {
		t.Fatalf("copy failed: %#v", result)
	}
	copied, err := os.ReadFile(filepath.Join(server.DataDir, "config", "copied.txt"))
	if err != nil || string(copied) != "managed data" {
		t.Fatalf("copied content=%q err=%v", copied, err)
	}
	if result := fileOperation(&server, "move", "config/copied.txt", "world/copied.txt"); result["success"] != true {
		t.Fatalf("move failed: %#v", result)
	}
	if _, err := os.Stat(filepath.Join(server.DataDir, "config", "copied.txt")); !os.IsNotExist(err) {
		t.Fatalf("move left source behind: %v", err)
	}

	if result := fileOperation(&server, "copy", "config/renamed.txt", "world/copied.txt"); result["success"] == true {
		t.Fatal("copy overwrote an existing destination")
	}
	if result := fileOperation(&server, "rename", "config/renamed.txt", "world/renamed.txt"); result["success"] == true {
		t.Fatal("rename crossed directories")
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
