package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// updateOutputLines bounds the update unit's journal tail returned to HOGS.
const updateOutputLines = 200

var (
	updateServiceState = serviceRunningState
	updateAction       = executeAction
	updateSafetyBackup = backupCreate
	updateRunUnit      = runUpdateUnit
	updateReady        = waitForRestoreReadiness
	updateClock        = time.Now
)

// runUpdateUnit starts the allowlisted oneshot update unit and waits for it to
// finish. systemctl start blocks until a Type=oneshot unit has exited, and
// reports a failed run as a non-zero exit. The journal tail of exactly this
// invocation is returned in both cases so HOGS can show what happened.
func runUpdateUnit(unit string) (string, error) {
	startOutput, startErr := exec.Command("systemctl", "start", unit).CombinedOutput()
	output := updateUnitOutput(unit)
	if startErr != nil {
		if output == "" {
			output = strings.TrimSpace(string(startOutput))
		}
		return output, fmt.Errorf("systemctl start %s failed: %w", unit, startErr)
	}
	return output, nil
}

func updateUnitOutput(unit string) string {
	invocation, err := exec.Command("systemctl", "show", unit, "--property=InvocationID", "--value").Output()
	if err != nil || strings.TrimSpace(string(invocation)) == "" {
		return ""
	}
	journal, err := exec.Command("journalctl", "_SYSTEMD_INVOCATION_ID="+strings.TrimSpace(string(invocation)),
		"--no-pager", "--no-hostname", "-o", "cat", "-n", fmt.Sprint(updateOutputLines)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(journal))
}

func updateFailure(message string, fields map[string]interface{}) map[string]interface{} {
	return restoreFailure(message, fields)
}

// updateServer runs the server's allowlisted update unit transactionally with
// respect to the game service: it stops a running server, takes a tagged
// safety snapshot, runs the update, and returns the server to its previous
// running or stopped state. The update payload itself (for example a SteamCMD
// install tree) is owned by the update unit; the snapshot protects game data,
// which cannot be downgraded once a newer version has opened it.
func updateServer(serverID string, server *ServerConfig, confirmation string) map[string]interface{} {
	if server.Update.Unit == "" {
		return updateFailure("server has no update unit configured", map[string]interface{}{"code": "unsupported"})
	}
	if confirmation != serverID {
		return updateFailure("update confirmation does not match the immutable server ID", nil)
	}

	lock := serverOperationLock(server)
	lock.Lock()
	defer lock.Unlock()

	wasRunning, err := updateServiceState(server.Unit)
	if err != nil {
		return updateFailure("determine service state: "+err.Error(), nil)
	}
	stoppedByUpdate := false
	startOriginal := func() error {
		if !wasRunning || !stoppedByUpdate {
			return nil
		}
		result := updateAction(server, "start")
		if success, _ := result["success"].(bool); !success {
			return fmt.Errorf("%v", result["message"])
		}
		if err := waitForUpdateServiceState(server.Unit, true); err != nil {
			return err
		}
		return updateReady(server)
	}
	failWithRestart := func(message string, fields map[string]interface{}) map[string]interface{} {
		if restartErr := startOriginal(); restartErr != nil {
			message += "; additionally failed to restart the server: " + restartErr.Error()
		}
		return updateFailure(message, fields)
	}

	if wasRunning {
		result := updateAction(server, "stop")
		if success, _ := result["success"].(bool); !success {
			return updateFailure(fmt.Sprintf("stop service before update: %v", result["message"]), nil)
		}
		stoppedByUpdate = true
		if err := waitForUpdateServiceState(server.Unit, false); err != nil {
			return failWithRestart("wait for service to stop: "+err.Error(), nil)
		}
	}

	safety := updateSafetyBackup(server, nil, []string{"hogs-pre-update", "hogs-server-" + serverID})
	if success, _ := safety["success"].(bool); !success {
		return failWithRestart(fmt.Sprintf("create pre-update safety snapshot: %v", safety["error"]), nil)
	}
	safetySnapshot, _ := safety["snapshotId"].(string)
	if safetySnapshot == "" {
		return failWithRestart("pre-update safety snapshot did not return an ID", nil)
	}

	output, runErr := updateRunUnit(server.Update.Unit)
	if runErr != nil {
		return failWithRestart("run update: "+runErr.Error(), map[string]interface{}{
			"safetySnapshotId": safetySnapshot, "output": output,
		})
	}

	result := map[string]interface{}{
		"success":          true,
		"safetySnapshotId": safetySnapshot,
		"output":           output,
		"serviceRestarted": wasRunning,
		"completedAt":      updateClock().UTC().Format(time.RFC3339),
	}
	if err := startOriginal(); err != nil {
		return updateFailure("update succeeded but the server did not come back: "+err.Error(), map[string]interface{}{
			"safetySnapshotId": safetySnapshot, "output": output, "updated": true,
		})
	}
	if !wasRunning {
		if running, stateErr := updateServiceState(server.Unit); stateErr == nil && running {
			result["warning"] = "the server was stopped before the update but is now running"
		}
	}
	return result
}

func waitForUpdateServiceState(unit string, running bool) error {
	deadline := updateClock().Add(restoreStateTimeout)
	for {
		current, err := updateServiceState(unit)
		if err != nil {
			return err
		}
		if current == running {
			return nil
		}
		if !updateClock().Before(deadline) {
			return fmt.Errorf("timed out waiting for service running=%t", running)
		}
		time.Sleep(250 * time.Millisecond)
	}
}
