package main

import (
	"errors"
	"strings"
	"testing"
)

type updateTestState struct {
	running bool
	events  []string
}

func configureUpdateTest(t *testing.T, initiallyRunning bool) (*ServerConfig, *updateTestState) {
	t.Helper()
	server := &ServerConfig{
		Unit:   "game-" + strings.ReplaceAll(t.Name(), "/", "-") + ".service",
		Update: UpdateConfig{Unit: "game-update.service"},
		Backup: BackupConfig{EnvironmentFile: "/etc/restic/env"},
	}
	state := &updateTestState{running: initiallyRunning}

	oldState, oldAction, oldBackup := updateServiceState, updateAction, updateSafetyBackup
	oldRun, oldReady := updateRunUnit, updateReady
	t.Cleanup(func() {
		updateServiceState, updateAction, updateSafetyBackup = oldState, oldAction, oldBackup
		updateRunUnit, updateReady = oldRun, oldReady
	})

	updateServiceState = func(string) (bool, error) { return state.running, nil }
	updateAction = func(_ *ServerConfig, action string) map[string]interface{} {
		state.events = append(state.events, action)
		state.running = action == "start"
		return map[string]interface{}{"success": true}
	}
	updateSafetyBackup = func(_ *ServerConfig, _ []string, tags []string) map[string]interface{} {
		if state.running {
			t.Fatal("safety snapshot taken while the server was running")
		}
		if !containsString(tags, "hogs-pre-update") || !containsString(tags, "hogs-server-alpha") {
			t.Fatalf("safety backup tags = %v", tags)
		}
		state.events = append(state.events, "backup")
		return map[string]interface{}{"success": true, "snapshotId": "feedface1234"}
	}
	updateRunUnit = func(unit string) (string, error) {
		if state.running {
			t.Fatal("update ran while the server was running")
		}
		if unit != "game-update.service" {
			t.Fatalf("update unit = %q", unit)
		}
		state.events = append(state.events, "update")
		return "Success! App fully installed.", nil
	}
	updateReady = func(*ServerConfig) error { return nil }
	return server, state
}

func TestUpdateServerStopsBacksUpUpdatesAndRestarts(t *testing.T) {
	server, state := configureUpdateTest(t, true)
	result := updateServer("alpha", server, "alpha")
	if success, _ := result["success"].(bool); !success {
		t.Fatalf("update failed: %#v", result)
	}
	if got := strings.Join(state.events, ","); got != "stop,backup,update,start" {
		t.Fatalf("events = %s", got)
	}
	if !state.running || result["serviceRestarted"] != true || result["safetySnapshotId"] != "feedface1234" {
		t.Fatalf("unexpected result %#v running=%t", result, state.running)
	}
	if result["output"] != "Success! App fully installed." {
		t.Fatalf("output = %#v", result["output"])
	}
}

func TestUpdateServerPreservesStoppedState(t *testing.T) {
	server, state := configureUpdateTest(t, false)
	result := updateServer("alpha", server, "alpha")
	if success, _ := result["success"].(bool); !success {
		t.Fatalf("update failed: %#v", result)
	}
	if got := strings.Join(state.events, ","); got != "backup,update" {
		t.Fatalf("events = %s", got)
	}
	if state.running || result["serviceRestarted"] != false {
		t.Fatalf("stopped server was started: %#v", result)
	}
}

func TestUpdateServerRequiresConfirmation(t *testing.T) {
	server, state := configureUpdateTest(t, true)
	result := updateServer("alpha", server, "beta")
	if success, _ := result["success"].(bool); success || len(state.events) != 0 {
		t.Fatalf("unconfirmed update ran: %#v events=%v", result, state.events)
	}
}

func TestUpdateServerAbortsWithoutSafetySnapshot(t *testing.T) {
	server, state := configureUpdateTest(t, true)
	updateSafetyBackup = func(*ServerConfig, []string, []string) map[string]interface{} {
		return map[string]interface{}{"success": false, "error": "repository locked"}
	}
	result := updateServer("alpha", server, "alpha")
	if success, _ := result["success"].(bool); success {
		t.Fatalf("update succeeded without a snapshot: %#v", result)
	}
	if got := strings.Join(state.events, ","); got != "stop,start" {
		t.Fatalf("events = %s", got)
	}
	if !state.running {
		t.Fatal("server was not returned to its running state")
	}
}

func TestUpdateServerReportsFailedUpdateAndRestarts(t *testing.T) {
	server, state := configureUpdateTest(t, true)
	updateRunUnit = func(string) (string, error) {
		state.events = append(state.events, "update")
		return "ERROR! Failed to install app (No subscription)", errors.New("exit status 1")
	}
	result := updateServer("alpha", server, "alpha")
	if success, _ := result["success"].(bool); success {
		t.Fatalf("failed update reported success: %#v", result)
	}
	if result["safetySnapshotId"] != "feedface1234" || !strings.Contains(result["output"].(string), "No subscription") {
		t.Fatalf("failure lacks snapshot or output: %#v", result)
	}
	if got := strings.Join(state.events, ","); got != "stop,backup,update,start" || !state.running {
		t.Fatalf("events = %s running=%t", got, state.running)
	}
}

func TestUpdateServerWithoutUnitIsUnsupported(t *testing.T) {
	server, state := configureUpdateTest(t, true)
	server.Update.Unit = ""
	result := updateServer("alpha", server, "alpha")
	if result["code"] != "unsupported" || len(state.events) != 0 {
		t.Fatalf("result = %#v events=%v", result, state.events)
	}
}

func TestValidateUpdateConfig(t *testing.T) {
	base := ServerConfig{Unit: "game.service", Backup: BackupConfig{EnvironmentFile: "/etc/restic/env"}}
	cases := map[string]struct {
		unit   string
		backup string
		valid  bool
	}{
		"unset":          {"", "", true},
		"separate unit":  {"game-update.service", "/etc/restic/env", true},
		"game unit":      {"game.service", "/etc/restic/env", false},
		"path":           {"../game-update.service", "/etc/restic/env", false},
		"timer":          {"game-update.timer", "/etc/restic/env", false},
		"without backup": {"game-update.service", "", false},
	}
	for name, test := range cases {
		server := base
		server.Update.Unit = test.unit
		server.Backup.EnvironmentFile = test.backup
		if err := validateUpdateConfig("alpha", server); (err == nil) != test.valid {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
