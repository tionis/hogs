package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/tionis/hogs/gametypes"
)

const restoreStateTimeout = 90 * time.Second
const restoreReadinessTimeout = 5 * time.Minute

var (
	restoreServiceState = serviceRunningState
	restoreAction       = executeAction
	restoreSafetyBackup = backupCreate
	restoreSnapshotSize = resticRestoreSize
	restoreRunRestic    = runResticRestore
	restoreDiskSpace    = availableDiskSpace
	restoreReady        = waitForRestoreReadiness
	restoreClock        = time.Now
)

var snapshotIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8,64}$`)

type resticStats struct {
	TotalSize uint64 `json:"total_size"`
}

func resticRestoreSize(server *ServerConfig, snapshot string) (uint64, error) {
	env, err := resticEnv(server)
	if err != nil {
		return 0, err
	}
	statsCommand := exec.Command(agentConfig.ResticBin,
		"stats", snapshot, "--mode", "restore-size", "--path", filepath.Clean(server.DataDir), "--json")
	statsCommand.Env = env
	var statsError bytes.Buffer
	statsCommand.Stderr = &statsError
	statsOutput, err := statsCommand.Output()
	if err != nil {
		return 0, fmt.Errorf("inspect snapshot: %w: %s", err, strings.TrimSpace(statsError.String()))
	}
	var stats resticStats
	if err := json.Unmarshal(statsOutput, &stats); err != nil {
		return 0, fmt.Errorf("parse snapshot size: %w", err)
	}
	if stats.TotalSize == 0 {
		return 0, fmt.Errorf("snapshot does not contain data below %s", filepath.Clean(server.DataDir))
	}
	return stats.TotalSize, nil
}

func runResticRestore(server *ServerConfig, snapshot, stagingRoot string) error {
	env, err := resticEnv(server)
	if err != nil {
		return err
	}
	restoreCommand := exec.Command(agentConfig.ResticBin,
		"restore", snapshot, "--target", stagingRoot, "--include", filepath.Clean(server.DataDir), "--verify")
	restoreCommand.Env = env
	var output bytes.Buffer
	restoreCommand.Stdout = &output
	restoreCommand.Stderr = &output
	if err := restoreCommand.Run(); err != nil {
		return fmt.Errorf("restore snapshot: %w: %s", err, strings.TrimSpace(output.String()))
	}
	return nil
}

func availableDiskSpace(path string) (uint64, error) {
	var stats syscall.Statfs_t
	if err := syscall.Statfs(path, &stats); err != nil {
		return 0, err
	}
	return stats.Bavail * uint64(stats.Bsize), nil
}

func requiredRestoreSpace(restoreSize uint64) uint64 {
	const reserve = 64 * 1024 * 1024
	return restoreSize + restoreSize/20 + reserve
}

func waitForRestoreServiceState(unit string, running bool) error {
	deadline := restoreClock().Add(restoreStateTimeout)
	for {
		current, err := restoreServiceState(unit)
		if err != nil {
			return err
		}
		if current == running {
			return nil
		}
		if !restoreClock().Before(deadline) {
			return fmt.Errorf("timed out waiting for service running=%t", running)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func waitForRestoreReadiness(server *ServerConfig) error {
	driver, embedded := gametypes.Embedded(server.GameType)
	if !embedded || server.Console.Type != "rcon" || driver.VersionCommand == "" {
		return nil
	}
	deadline := time.Now().Add(restoreReadinessTimeout)
	for {
		running, err := restoreServiceState(server.Unit)
		if err != nil {
			return err
		}
		if !running {
			return fmt.Errorf("service stopped before its game API became ready")
		}
		if version := serverVersion(server, driver); version != "" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for the game API to become ready")
		}
		time.Sleep(2 * time.Second)
	}
}

func restoreFailure(message string, fields map[string]interface{}) map[string]interface{} {
	result := map[string]interface{}{"success": false, "error": message}
	for key, value := range fields {
		result[key] = value
	}
	return result
}

func restoreSnapshot(serverID string, server *ServerConfig, snapshot, confirmation string) map[string]interface{} {
	snapshot = strings.TrimSpace(snapshot)
	if !snapshotIDPattern.MatchString(snapshot) {
		return restoreFailure("snapshot must be a full or abbreviated hexadecimal restic snapshot ID", nil)
	}
	if confirmation != serverID {
		return restoreFailure("restore confirmation does not match the immutable server ID", nil)
	}
	dataDir := filepath.Clean(server.DataDir)
	if !filepath.IsAbs(dataDir) || dataDir == string(filepath.Separator) || filepath.Dir(dataDir) == dataDir {
		return restoreFailure("server data directory is not safe for transactional restore", nil)
	}
	liveInfo, err := os.Lstat(dataDir)
	if err != nil {
		return restoreFailure("inspect live server data: "+err.Error(), nil)
	}
	if !liveInfo.IsDir() || liveInfo.Mode()&os.ModeSymlink != 0 {
		return restoreFailure("live server data path must be a real directory", nil)
	}

	lock := serverOperationLock(server)
	lock.Lock()
	defer lock.Unlock()

	wasRunning, err := restoreServiceState(server.Unit)
	if err != nil {
		return restoreFailure("determine service state: "+err.Error(), nil)
	}
	stoppedByRestore := false
	restartOriginal := func() error {
		if !wasRunning || !stoppedByRestore {
			return nil
		}
		result := restoreAction(server, "start")
		if success, _ := result["success"].(bool); !success {
			return fmt.Errorf("%v", result["message"])
		}
		return waitForRestoreServiceState(server.Unit, true)
	}

	if wasRunning {
		result := restoreAction(server, "stop")
		if success, _ := result["success"].(bool); !success {
			return restoreFailure(fmt.Sprintf("stop service before restore: %v", result["message"]), nil)
		}
		stoppedByRestore = true
		if err := waitForRestoreServiceState(server.Unit, false); err != nil {
			_ = restartOriginal()
			return restoreFailure("wait for service to stop: "+err.Error(), nil)
		}
	}

	safety := restoreSafetyBackup(server, nil, []string{"hogs-pre-restore", "hogs-server-" + serverID})
	if success, _ := safety["success"].(bool); !success {
		restartErr := restartOriginal()
		message := fmt.Sprintf("create pre-restore safety snapshot: %v", safety["error"])
		if restartErr != nil {
			message += "; additionally failed to restart original service: " + restartErr.Error()
		}
		return restoreFailure(message, nil)
	}
	safetySnapshot, _ := safety["snapshotId"].(string)
	if safetySnapshot == "" {
		restartErr := restartOriginal()
		message := "pre-restore safety snapshot did not return an ID"
		if restartErr != nil {
			message += "; additionally failed to restart original service: " + restartErr.Error()
		}
		return restoreFailure(message, nil)
	}

	parent := filepath.Dir(dataDir)
	stagingRoot, err := os.MkdirTemp(parent, ".hogs-restore-"+filepath.Base(dataDir)+"-")
	if err != nil {
		_ = restartOriginal()
		return restoreFailure("create restore staging directory: "+err.Error(), map[string]interface{}{"safetySnapshotId": safetySnapshot})
	}
	defer os.RemoveAll(stagingRoot)

	restoreSize, err := restoreSnapshotSize(server, snapshot)
	if err != nil {
		restartErr := restartOriginal()
		message := err.Error()
		if restartErr != nil {
			message += "; additionally failed to restart original service: " + restartErr.Error()
		}
		return restoreFailure(message, map[string]interface{}{"safetySnapshotId": safetySnapshot})
	}
	available, err := restoreDiskSpace(parent)
	if err != nil {
		_ = restartOriginal()
		return restoreFailure("inspect restore disk space: "+err.Error(), map[string]interface{}{"safetySnapshotId": safetySnapshot})
	}
	if required := requiredRestoreSpace(restoreSize); available < required {
		_ = restartOriginal()
		return restoreFailure(
			fmt.Sprintf("insufficient free space for transactional restore: need %d bytes, have %d", required, available),
			map[string]interface{}{"safetySnapshotId": safetySnapshot, "restoreBytes": restoreSize},
		)
	}
	if err := restoreRunRestic(server, snapshot, stagingRoot); err != nil {
		restartErr := restartOriginal()
		message := err.Error()
		if restartErr != nil {
			message += "; additionally failed to restart original service: " + restartErr.Error()
		}
		return restoreFailure(message, map[string]interface{}{"safetySnapshotId": safetySnapshot})
	}

	restoredData := filepath.Join(stagingRoot, strings.TrimPrefix(dataDir, string(filepath.Separator)))
	restoredInfo, err := os.Lstat(restoredData)
	if err != nil || !restoredInfo.IsDir() || restoredInfo.Mode()&os.ModeSymlink != 0 {
		_ = restartOriginal()
		return restoreFailure(
			"staged snapshot does not contain the configured server data directory",
			map[string]interface{}{"safetySnapshotId": safetySnapshot},
		)
	}

	rollbackPath, err := os.MkdirTemp(parent, ".hogs-rollback-"+filepath.Base(dataDir)+"-")
	if err != nil {
		_ = restartOriginal()
		return restoreFailure("reserve rollback path: "+err.Error(), map[string]interface{}{"safetySnapshotId": safetySnapshot})
	}
	if err := os.Remove(rollbackPath); err != nil {
		_ = restartOriginal()
		return restoreFailure("prepare rollback path: "+err.Error(), map[string]interface{}{"safetySnapshotId": safetySnapshot})
	}
	swapped := false
	if err := os.Rename(dataDir, rollbackPath); err != nil {
		_ = restartOriginal()
		return restoreFailure("move live data to rollback path: "+err.Error(), map[string]interface{}{"safetySnapshotId": safetySnapshot})
	}
	if err := os.Rename(restoredData, dataDir); err != nil {
		rollbackErr := os.Rename(rollbackPath, dataDir)
		restartErr := restartOriginal()
		message := "activate restored data: " + err.Error()
		if rollbackErr != nil {
			message += "; failed to restore original data: " + rollbackErr.Error()
		}
		if restartErr != nil {
			message += "; failed to restart original service: " + restartErr.Error()
		}
		return restoreFailure(message, map[string]interface{}{"safetySnapshotId": safetySnapshot})
	}
	swapped = true
	_ = syncDirectory(parent)

	rollback := func(reason error) map[string]interface{} {
		if running, stateErr := restoreServiceState(server.Unit); stateErr == nil && running {
			stopResult := restoreAction(server, "stop")
			if success, _ := stopResult["success"].(bool); success {
				_ = waitForRestoreServiceState(server.Unit, false)
			}
		}
		failedPath, reserveErr := os.MkdirTemp(parent, ".hogs-failed-restore-"+filepath.Base(dataDir)+"-")
		if reserveErr == nil {
			reserveErr = os.Remove(failedPath)
		}
		if reserveErr == nil {
			reserveErr = os.Rename(dataDir, failedPath)
		}
		rollbackErr := os.Rename(rollbackPath, dataDir)
		_ = syncDirectory(parent)
		restartErr := restartOriginal()
		if reserveErr == nil && rollbackErr == nil {
			_ = os.RemoveAll(failedPath)
		}
		message := reason.Error()
		if reserveErr != nil {
			message += "; failed to preserve failed restore: " + reserveErr.Error()
		}
		if rollbackErr != nil {
			message += "; failed to reactivate original data: " + rollbackErr.Error()
		}
		if restartErr != nil {
			message += "; failed to restart original service: " + restartErr.Error()
		}
		return restoreFailure(message, map[string]interface{}{
			"safetySnapshotId": safetySnapshot,
			"rollbackRestored": rollbackErr == nil,
		})
	}

	if wasRunning {
		result := restoreAction(server, "start")
		if success, _ := result["success"].(bool); !success {
			return rollback(fmt.Errorf("start restored service: %v", result["message"]))
		}
		if err := waitForRestoreServiceState(server.Unit, true); err != nil {
			return rollback(fmt.Errorf("verify restored service: %w", err))
		}
		if err := restoreReady(server); err != nil {
			return rollback(fmt.Errorf("verify restored game readiness: %w", err))
		}
	} else {
		running, stateErr := restoreServiceState(server.Unit)
		if stateErr != nil || running {
			if stateErr == nil {
				stateErr = fmt.Errorf("service unexpectedly started")
			}
			return rollback(fmt.Errorf("verify stopped service after restore: %w", stateErr))
		}
	}

	if swapped {
		if err := os.RemoveAll(rollbackPath); err != nil {
			return map[string]interface{}{
				"success": true, "snapshotId": snapshot, "safetySnapshotId": safetySnapshot,
				"restoreBytes": restoreSize, "serviceRestarted": wasRunning,
				"completedAt": restoreClock().UTC().Format(time.RFC3339),
				"warning":     "restore succeeded but rollback data cleanup failed: " + err.Error(),
			}
		}
		_ = syncDirectory(parent)
	}
	return map[string]interface{}{
		"success":          true,
		"snapshotId":       snapshot,
		"safetySnapshotId": safetySnapshot,
		"restoreBytes":     restoreSize,
		"serviceRestarted": wasRunning,
		"completedAt":      restoreClock().UTC().Format(time.RFC3339),
	}
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
