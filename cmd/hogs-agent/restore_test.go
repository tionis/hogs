package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func configureRestoreTest(t *testing.T, initiallyRunning bool) (*ServerConfig, *bool, *int) {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "game-data")
	if err := os.MkdirAll(dataDir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "state.txt"), []byte("original"), 0640); err != nil {
		t.Fatal(err)
	}
	server := &ServerConfig{Unit: "restore-" + filepath.Base(t.TempDir()) + ".service", DataDir: dataDir}
	running := initiallyRunning
	actionCount := 0

	oldState := restoreServiceState
	oldAction := restoreAction
	oldBackup := restoreSafetyBackup
	oldSize := restoreSnapshotSize
	oldRestic := restoreRunRestic
	oldSpace := restoreDiskSpace
	oldReady := restoreReady
	t.Cleanup(func() {
		restoreServiceState = oldState
		restoreAction = oldAction
		restoreSafetyBackup = oldBackup
		restoreSnapshotSize = oldSize
		restoreRunRestic = oldRestic
		restoreDiskSpace = oldSpace
		restoreReady = oldReady
	})

	restoreServiceState = func(string) (bool, error) { return running, nil }
	restoreAction = func(_ *ServerConfig, action string) map[string]interface{} {
		actionCount++
		switch action {
		case "stop":
			running = false
		case "start":
			running = true
		}
		return map[string]interface{}{"success": true, "message": action + " ok"}
	}
	restoreSafetyBackup = func(_ *ServerConfig, _ []string, tags []string) map[string]interface{} {
		if !containsString(tags, "hogs-pre-restore") {
			t.Fatalf("safety backup tags = %v", tags)
		}
		return map[string]interface{}{"success": true, "snapshotId": "feedface1234"}
	}
	restoreSnapshotSize = func(_ *ServerConfig, snapshot string) (uint64, error) {
		if snapshot != "0123456789abcdef" {
			t.Fatalf("snapshot = %q", snapshot)
		}
		return 1024, nil
	}
	restoreDiskSpace = func(string) (uint64, error) { return requiredRestoreSpace(1024) + 1, nil }
	restoreReady = func(*ServerConfig) error { return nil }
	restoreRunRestic = func(current *ServerConfig, _ string, staging string) error {
		restored := filepath.Join(staging, strings.TrimPrefix(filepath.Clean(current.DataDir), string(filepath.Separator)))
		if err := os.MkdirAll(restored, 0750); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(restored, "state.txt"), []byte("restored"), 0640)
	}
	return server, &running, &actionCount
}

func TestRestoreSnapshotReplacesDataAndPreservesStoppedState(t *testing.T) {
	server, running, actions := configureRestoreTest(t, false)
	result := restoreSnapshot("alpha", server, "0123456789abcdef", "alpha")
	if success, _ := result["success"].(bool); !success {
		t.Fatalf("restore failed: %#v", result)
	}
	data, err := os.ReadFile(filepath.Join(server.DataDir, "state.txt"))
	if err != nil || string(data) != "restored" {
		t.Fatalf("restored data=%q err=%v", data, err)
	}
	if *running || *actions != 0 {
		t.Fatalf("stopped service state changed: running=%t actions=%d", *running, *actions)
	}
	if result["safetySnapshotId"] != "feedface1234" {
		t.Fatalf("missing safety snapshot: %#v", result)
	}
}

func TestRestoreSnapshotStopsAndRestartsRunningService(t *testing.T) {
	server, running, actions := configureRestoreTest(t, true)
	result := restoreSnapshot("alpha", server, "0123456789abcdef", "alpha")
	if success, _ := result["success"].(bool); !success {
		t.Fatalf("restore failed: %#v", result)
	}
	if !*running || *actions != 2 {
		t.Fatalf("running service was not stopped and restarted: running=%t actions=%d", *running, *actions)
	}
	if restarted, _ := result["serviceRestarted"].(bool); !restarted {
		t.Fatalf("restore result did not report restart: %#v", result)
	}
}

func TestRestoreSnapshotRollsBackWhenRestoredServiceFailsToStart(t *testing.T) {
	server, running, actions := configureRestoreTest(t, true)
	startAttempts := 0
	restoreAction = func(_ *ServerConfig, action string) map[string]interface{} {
		*actions++
		if action == "stop" {
			*running = false
			return map[string]interface{}{"success": true, "message": "stop ok"}
		}
		startAttempts++
		if startAttempts == 1 {
			return map[string]interface{}{"success": false, "message": "new data rejected"}
		}
		*running = true
		return map[string]interface{}{"success": true, "message": "original restarted"}
	}

	result := restoreSnapshot("alpha", server, "0123456789abcdef", "alpha")
	if success, _ := result["success"].(bool); success {
		t.Fatalf("restore unexpectedly succeeded: %#v", result)
	}
	if rolledBack, _ := result["rollbackRestored"].(bool); !rolledBack {
		t.Fatalf("restore did not report rollback: %#v", result)
	}
	data, err := os.ReadFile(filepath.Join(server.DataDir, "state.txt"))
	if err != nil || string(data) != "original" {
		t.Fatalf("rollback data=%q err=%v", data, err)
	}
	if !*running {
		t.Fatal("original service was not restarted after rollback")
	}
}

func TestRestoreSnapshotRejectsWrongConfirmationBeforeMutation(t *testing.T) {
	server, _, actions := configureRestoreTest(t, true)
	result := restoreSnapshot("alpha", server, "0123456789abcdef", "different")
	if success, _ := result["success"].(bool); success {
		t.Fatalf("restore unexpectedly succeeded: %#v", result)
	}
	if *actions != 0 {
		t.Fatalf("restore mutated service before validating confirmation: actions=%d", *actions)
	}
	data, err := os.ReadFile(filepath.Join(server.DataDir, "state.txt"))
	if err != nil || string(data) != "original" {
		t.Fatalf("live data changed: %q err=%v", data, err)
	}
}

func TestResticRestoreStagesOnlyConfiguredDataPath(t *testing.T) {
	resticPath, err := exec.LookPath("restic")
	if err != nil {
		t.Skip("restic is not installed")
	}
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	dataDir := filepath.Join(root, "host", "game")
	if err := os.MkdirAll(dataDir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "save.dat"), []byte("known-save"), 0640); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(root, "restic.env")
	if err := os.WriteFile(profile, []byte(
		"RESTIC_REPOSITORY="+repository+"\nRESTIC_PASSWORD=test-only-password\nRESTIC_CACHE_DIR="+filepath.Join(root, "cache")+"\n",
	), 0600); err != nil {
		t.Fatal(err)
	}
	server := &ServerConfig{DataDir: dataDir, Backup: BackupConfig{EnvironmentFile: profile}}
	oldConfig := agentConfig
	agentConfig.ResticBin = resticPath
	t.Cleanup(func() { agentConfig = oldConfig })
	env, err := resticEnv(server)
	if err != nil {
		t.Fatal(err)
	}
	run := func(arguments ...string) []byte {
		command := exec.Command(resticPath, arguments...)
		command.Env = env
		output, commandErr := command.CombinedOutput()
		if commandErr != nil {
			t.Fatalf("restic %v: %v: %s", arguments, commandErr, output)
		}
		return output
	}
	run("init")
	run("backup", filepath.Dir(dataDir))
	var snapshots []struct {
		ID string `json:"id"`
	}
	snapshotCommand := exec.Command(resticPath, "snapshots", "--json")
	snapshotCommand.Env = env
	snapshotOutput, snapshotErr := snapshotCommand.Output()
	if snapshotErr != nil {
		t.Fatal(snapshotErr)
	}
	if err := json.Unmarshal(snapshotOutput, &snapshots); err != nil || len(snapshots) != 1 {
		t.Fatalf("snapshots=%v err=%v", snapshots, err)
	}
	size, err := resticRestoreSize(server, snapshots[0].ID)
	if err != nil || size == 0 {
		t.Fatalf("restore size=%d err=%v", size, err)
	}
	if os.Geteuid() != 0 {
		t.Skip("restic metadata-preserving restore requires root")
	}
	staging := filepath.Join(root, "staging")
	if err := os.Mkdir(staging, 0700); err != nil {
		t.Fatal(err)
	}
	if err := runResticRestore(server, snapshots[0].ID, staging); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(staging, strings.TrimPrefix(dataDir, string(filepath.Separator)), "save.dat")
	content, err := os.ReadFile(restored)
	if err != nil || string(content) != "known-save" {
		t.Fatalf("staged content=%q err=%v", content, err)
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
