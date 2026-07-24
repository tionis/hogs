package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tionis/hogs/backend"
	"github.com/tionis/hogs/gametypes"
)

func minecraftWhitelistFixture(t *testing.T, onlineMode bool, contents string) *ServerConfig {
	t.Helper()
	previousState := whitelistServiceRunningState
	whitelistServiceRunningState = func(string) (bool, error) { return false, nil }
	t.Cleanup(func() { whitelistServiceRunningState = previousState })
	dataDir := t.TempDir()
	mode := "true"
	if !onlineMode {
		mode = "false"
	}
	if err := os.WriteFile(filepath.Join(dataDir, "server.properties"), []byte("online-mode="+mode+"\n"), 0640); err != nil {
		t.Fatal(err)
	}
	if contents != "" {
		if err := os.WriteFile(filepath.Join(dataDir, "whitelist.json"), []byte(contents), 0640); err != nil {
			t.Fatal(err)
		}
	}
	return &ServerConfig{
		Unit: "hogs-whitelist-test-does-not-exist.service", GameType: "minecraft", DataDir: dataDir,
		Console: ConsoleConfig{Type: "rcon"},
	}
}

func factorioWhitelistFixture(t *testing.T, contents string) *ServerConfig {
	t.Helper()
	previousState := whitelistServiceRunningState
	whitelistServiceRunningState = func(string) (bool, error) { return false, nil }
	t.Cleanup(func() { whitelistServiceRunningState = previousState })
	dataDir := t.TempDir()
	installationDir := filepath.Join(dataDir, "factorio")
	if err := os.MkdirAll(installationDir, 0750); err != nil {
		t.Fatal(err)
	}
	if contents != "" {
		if err := os.WriteFile(filepath.Join(installationDir, "server-whitelist.json"), []byte(contents), 0640); err != nil {
			t.Fatal(err)
		}
	}
	return &ServerConfig{
		Unit: "hogs-whitelist-test-does-not-exist.service", GameType: "factorio", DataDir: dataDir,
		Console: ConsoleConfig{Type: "rcon"},
	}
}

func valheimWhitelistFixture(t *testing.T, running bool, contents string) *ServerConfig {
	t.Helper()
	previousState := whitelistServiceRunningState
	whitelistServiceRunningState = func(string) (bool, error) { return running, nil }
	t.Cleanup(func() { whitelistServiceRunningState = previousState })
	dataDir := t.TempDir()
	if contents != "" {
		if err := os.WriteFile(filepath.Join(dataDir, "permittedlist.txt"), []byte(contents), 0640); err != nil {
			t.Fatal(err)
		}
	}
	return &ServerConfig{
		Unit: "hogs-whitelist-test-does-not-exist.service", GameType: "valheim", DataDir: dataDir,
	}
}

func TestOfflineWhitelistAddRemoveAndList(t *testing.T) {
	server := minecraftWhitelistFixture(t, true, `[{"uuid":"00000000-0000-0000-0000-000000000001","name":"Alex"}]`)
	driver, _ := gametypes.Embedded("minecraft")
	result, operationErr := fileWhitelistOperation(server, driver, backend.WhitelistRequest{
		Operation: "add", Username: "Steve", ExternalID: "123456781234123412341234567890ab",
	}, false)
	if operationErr != nil {
		t.Fatal(operationErr)
	}
	if result.Mode != "offline" || len(result.Entries) != 2 ||
		result.Entries[1].UUID != "12345678-1234-1234-1234-1234567890ab" {
		t.Fatalf("unexpected add result: %#v", result)
	}
	info, err := os.Stat(filepath.Join(server.DataDir, "whitelist.json"))
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("whitelist permissions=%v err=%v", info.Mode().Perm(), err)
	}

	result, operationErr = fileWhitelistOperation(server, driver, backend.WhitelistRequest{
		Operation: "remove", Username: "alex",
	}, false)
	if operationErr != nil || len(result.Entries) != 1 || result.Entries[0].Name != "Steve" {
		t.Fatalf("unexpected remove result=%#v err=%v", result, operationErr)
	}
	raw, err := os.ReadFile(filepath.Join(server.DataDir, "whitelist.json"))
	if err != nil || !json.Valid(raw) || strings.Contains(string(raw), "Alex") {
		t.Fatalf("invalid persisted whitelist=%q err=%v", raw, err)
	}
}

func TestOnlineWhitelistEntriesPreferStructuredFileAndFallBackToCommandOutput(t *testing.T) {
	driver, _ := gametypes.Embedded("minecraft")
	server := minecraftWhitelistFixture(t, true, `[{"uuid":"00000000-0000-0000-0000-000000000001","name":"FilePlayer"}]`)
	entries := onlineWhitelistEntries(server, driver, "There are 1 whitelisted player(s): OutputPlayer")
	if len(entries) != 1 || entries[0].Name != "FilePlayer" || entries[0].UUID == "" {
		t.Fatalf("structured entries=%#v", entries)
	}

	server = minecraftWhitelistFixture(t, true, "")
	entries = onlineWhitelistEntries(server, driver, "There are 2 whitelisted player(s): Alex, Builder_42")
	if len(entries) != 2 || entries[0].Name != "Alex" || entries[1].Name != "Builder_42" {
		t.Fatalf("fallback entries=%#v", entries)
	}
}

func TestOfflineWhitelistRequiresVerifiedOnlineModeUUID(t *testing.T) {
	original := `[{"uuid":"00000000-0000-0000-0000-000000000001","name":"Alex"}]`
	server := minecraftWhitelistFixture(t, true, original)
	driver, _ := gametypes.Embedded("minecraft")
	_, operationErr := fileWhitelistOperation(server, driver, backend.WhitelistRequest{
		Operation: "add", Username: "Steve",
	}, false)
	if operationErr == nil || operationErr.Code != "identity_required" {
		t.Fatalf("operation error=%#v, want identity_required", operationErr)
	}
	raw, err := os.ReadFile(filepath.Join(server.DataDir, "whitelist.json"))
	if err != nil || string(raw) != original {
		t.Fatalf("whitelist changed after rejected operation: %q err=%v", raw, err)
	}
}

func TestOfflineWhitelistReplacesPreviousIdentityInOneWrite(t *testing.T) {
	server := minecraftWhitelistFixture(t, true, `[{"uuid":"00000000-0000-0000-0000-000000000001","name":"OldName"}]`)
	driver, _ := gametypes.Embedded("minecraft")
	result, operationErr := fileWhitelistOperation(server, driver, backend.WhitelistRequest{
		Operation: "add", Username: "NewName", PreviousUsername: "OldName",
		ExternalID: "123456781234123412341234567890ab",
	}, false)
	if operationErr != nil || len(result.Entries) != 1 || result.Entries[0].Name != "NewName" {
		t.Fatalf("replacement result=%#v err=%v", result, operationErr)
	}
}

func TestOfflineModeWhitelistDerivesMinecraftUUID(t *testing.T) {
	server := minecraftWhitelistFixture(t, false, "")
	driver, _ := gametypes.Embedded("minecraft")
	result, operationErr := fileWhitelistOperation(server, driver, backend.WhitelistRequest{
		Operation: "add", Username: "Notch",
	}, false)
	if operationErr != nil {
		t.Fatal(operationErr)
	}
	if len(result.Entries) != 1 || result.Entries[0].UUID != "b50ad385-829d-3141-a216-7e7d7539ba7f" {
		t.Fatalf("unexpected offline profile: %#v", result.Entries)
	}
}

func TestMalformedOfflineWhitelistIsNeverOverwritten(t *testing.T) {
	server := minecraftWhitelistFixture(t, true, `{not-json`)
	driver, _ := gametypes.Embedded("minecraft")
	_, operationErr := fileWhitelistOperation(server, driver, backend.WhitelistRequest{
		Operation: "remove", Username: "Alex",
	}, false)
	if operationErr == nil || operationErr.Code != "read_failed" {
		t.Fatalf("operation error=%#v, want read_failed", operationErr)
	}
	raw, err := os.ReadFile(filepath.Join(server.DataDir, "whitelist.json"))
	if err != nil || string(raw) != `{not-json` {
		t.Fatalf("malformed whitelist was overwritten: %q err=%v", raw, err)
	}
}

func TestFactorioOfflineWhitelistAddRemoveAndList(t *testing.T) {
	server := factorioWhitelistFixture(t, `["Alice"]`)
	driver, _ := gametypes.Embedded("factorio")
	result, operationErr := fileWhitelistOperation(server, driver, backend.WhitelistRequest{
		Operation: "add", Username: "Space Cadet",
	}, false)
	if operationErr != nil || result.Mode != "offline" || len(result.Entries) != 2 ||
		result.Entries[1].Name != "Space Cadet" || result.Entries[1].UUID != "" {
		t.Fatalf("unexpected add result=%#v err=%v", result, operationErr)
	}
	raw, err := os.ReadFile(filepath.Join(server.DataDir, "factorio", "server-whitelist.json"))
	if err != nil {
		t.Fatal(err)
	}
	var usernames []string
	if err := json.Unmarshal(raw, &usernames); err != nil || len(usernames) != 2 ||
		usernames[0] != "Alice" || usernames[1] != "Space Cadet" {
		t.Fatalf("persisted Factorio whitelist=%q usernames=%#v err=%v", raw, usernames, err)
	}

	result, operationErr = fileWhitelistOperation(server, driver, backend.WhitelistRequest{
		Operation: "remove", Username: "alice",
	}, false)
	if operationErr != nil || len(result.Entries) != 1 || result.Entries[0].Name != "Space Cadet" {
		t.Fatalf("unexpected remove result=%#v err=%v", result, operationErr)
	}
}

func TestFactorioMalformedOfflineWhitelistIsNeverOverwritten(t *testing.T) {
	original := `{"not":"a string array"}`
	server := factorioWhitelistFixture(t, original)
	driver, _ := gametypes.Embedded("factorio")
	_, operationErr := fileWhitelistOperation(server, driver, backend.WhitelistRequest{
		Operation: "add", Username: "Engineer",
	}, false)
	if operationErr == nil || operationErr.Code != "read_failed" {
		t.Fatalf("operation error=%#v, want read_failed", operationErr)
	}
	raw, err := os.ReadFile(filepath.Join(server.DataDir, "factorio", "server-whitelist.json"))
	if err != nil || string(raw) != original {
		t.Fatalf("malformed whitelist was overwritten: %q err=%v", raw, err)
	}
}

func TestOnlineFactorioWhitelistEntriesPreferStructuredFile(t *testing.T) {
	server := factorioWhitelistFixture(t, `["FileEngineer"]`)
	driver, _ := gametypes.Embedded("factorio")
	entries := onlineWhitelistEntries(server, driver, "Whitelisted players: OutputEngineer")
	if len(entries) != 1 || entries[0].Name != "FileEngineer" || entries[0].UUID != "" {
		t.Fatalf("structured entries=%#v", entries)
	}
}

func TestValheimWhitelistUsesPermissionFileWhileRunning(t *testing.T) {
	server := valheimWhitelistFixture(t, true, "Steam_123\n")
	result, operationErr := whitelistOperation(server, backend.WhitelistRequest{
		Operation: "add", Username: "Xbox_456",
	})
	if operationErr != nil || result.Mode != "pending_restart" || len(result.Entries) != 2 {
		t.Fatalf("running Valheim result=%#v err=%v", result, operationErr)
	}
	raw, err := os.ReadFile(filepath.Join(server.DataDir, "permittedlist.txt"))
	if err != nil || string(raw) != "// List permitted players ID ONE per line\nSteam_123\nXbox_456\n" {
		t.Fatalf("running permitted list=%q err=%v", raw, err)
	}
}

func TestValheimWhitelistUsesPermissionFileWhileStopped(t *testing.T) {
	server := valheimWhitelistFixture(t, false, "Steam_ABC\nsteam_ABC\n")
	result, operationErr := whitelistOperation(server, backend.WhitelistRequest{
		Operation: "remove", Username: "steam_ABC",
	})
	if operationErr != nil || result.Mode != "offline" || len(result.Entries) != 1 ||
		result.Entries[0].Name != "Steam_ABC" {
		t.Fatalf("stopped Valheim result=%#v err=%v", result, operationErr)
	}
	raw, err := os.ReadFile(filepath.Join(server.DataDir, "permittedlist.txt"))
	if err != nil || string(raw) != "// List permitted players ID ONE per line\nSteam_ABC\n" {
		t.Fatalf("stopped permitted list=%q err=%v", raw, err)
	}
}

func TestValheimWhitelistAbortsIfRunningStateChanges(t *testing.T) {
	server := valheimWhitelistFixture(t, true, "Steam_123\n")
	stateChecks := 0
	whitelistServiceRunningState = func(string) (bool, error) {
		stateChecks++
		return stateChecks == 1, nil
	}
	_, operationErr := whitelistOperation(server, backend.WhitelistRequest{
		Operation: "add", Username: "Xbox_456",
	})
	if operationErr == nil || operationErr.Code != "server_stopped" {
		t.Fatalf("operation error=%#v, want server_stopped", operationErr)
	}
	raw, err := os.ReadFile(filepath.Join(server.DataDir, "permittedlist.txt"))
	if err != nil || string(raw) != "Steam_123\n" {
		t.Fatalf("permitted list changed after stop race: %q err=%v", raw, err)
	}
}

func TestWhitelistFailsClosedWhenServiceStateIsUnknown(t *testing.T) {
	server := minecraftWhitelistFixture(t, true, `[]`)
	whitelistServiceRunningState = func(string) (bool, error) {
		return false, errors.New("systemd unavailable")
	}
	_, operationErr := whitelistOperation(server, backend.WhitelistRequest{Operation: "list"})
	if operationErr == nil || operationErr.Code != "status_unknown" {
		t.Fatalf("operation error=%#v, want status_unknown", operationErr)
	}
}

func TestOfflineWhitelistAbortsIfServerStartsBeforeReplace(t *testing.T) {
	original := `[]`
	server := minecraftWhitelistFixture(t, true, original)
	whitelistServiceRunningState = func(string) (bool, error) { return true, nil }
	driver, _ := gametypes.Embedded("minecraft")
	_, operationErr := fileWhitelistOperation(server, driver, backend.WhitelistRequest{
		Operation: "add", Username: "Steve", ExternalID: "123456781234123412341234567890ab",
	}, false)
	if operationErr == nil || operationErr.Code != "server_started" {
		t.Fatalf("operation error=%#v, want server_started", operationErr)
	}
	raw, err := os.ReadFile(filepath.Join(server.DataDir, "whitelist.json"))
	if err != nil || string(raw) != original {
		t.Fatalf("whitelist changed after start race: %q err=%v", raw, err)
	}
}
