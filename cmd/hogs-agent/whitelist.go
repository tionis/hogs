package main

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/tionis/hogs/backend"
	"github.com/tionis/hogs/gametypes"
)

var whitelistServiceRunningState = serviceRunningState

func serviceRunningState(unit string) (bool, error) {
	out, err := exec.Command("systemctl", "show", unit, "--property=LoadState,ActiveState").Output()
	if err != nil {
		return false, fmt.Errorf("query service state: %w", err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		key, value, found := strings.Cut(line, "=")
		if found {
			values[key] = value
		}
	}
	if values["LoadState"] != "loaded" {
		return false, fmt.Errorf("managed service is not loaded")
	}
	switch values["ActiveState"] {
	case "inactive", "failed":
		return false, nil
	case "active", "activating", "deactivating", "reloading":
		return true, nil
	default:
		return false, fmt.Errorf("managed service returned unknown state %q", values["ActiveState"])
	}
}

func whitelistOperation(server *ServerConfig, request backend.WhitelistRequest) (*backend.WhitelistResult, *backend.WhitelistError) {
	driver, ok := gametypes.Embedded(server.GameType)
	if !ok || !driver.SupportsWhitelist() {
		return nil, &backend.WhitelistError{
			Code: "unsupported", Message: "whitelist management is not supported for this game type",
		}
	}
	active, stateErr := whitelistServiceRunningState(server.Unit)
	if stateErr != nil {
		return nil, &backend.WhitelistError{
			Code: "status_unknown", Message: "could not determine the managed service state: " + stateErr.Error(),
		}
	}
	if active {
		return onlineWhitelistOperation(server, driver, request)
	}
	return offlineWhitelistOperation(server, driver, request)
}

func onlineWhitelistOperation(server *ServerConfig, driver gametypes.Driver, request backend.WhitelistRequest) (*backend.WhitelistResult, *backend.WhitelistError) {
	var command string
	switch request.Operation {
	case "list":
		command = driver.Whitelist.ListCommand
	case "add":
		if !driver.IdentityValid(request.Username) {
			return nil, &backend.WhitelistError{Code: "invalid_identity", Message: "invalid in-game username"}
		}
		command = driver.Whitelist.AddCommand(request.Username)
	case "remove":
		if !driver.IdentityValid(request.Username) {
			return nil, &backend.WhitelistError{Code: "invalid_identity", Message: "invalid in-game username"}
		}
		command = driver.Whitelist.RemoveCommand(request.Username)
	default:
		return nil, &backend.WhitelistError{Code: "invalid_operation", Message: "invalid whitelist operation"}
	}
	output, err := executeCommand(server, command)
	if err != nil {
		return nil, &backend.WhitelistError{Code: "rcon_failed", Message: "RCON whitelist operation failed: " + err.Error()}
	}
	if request.Operation == "add" && request.PreviousUsername != "" &&
		!strings.EqualFold(request.PreviousUsername, request.Username) {
		if !driver.IdentityValid(request.PreviousUsername) {
			return nil, &backend.WhitelistError{Code: "invalid_identity", Message: "invalid previous in-game username"}
		}
		removeOutput, removeErr := executeCommand(server, driver.Whitelist.RemoveCommand(request.PreviousUsername))
		if removeErr != nil {
			return nil, &backend.WhitelistError{
				Code: "rcon_failed", Message: "new whitelist entry was added but the previous entry could not be removed: " + removeErr.Error(),
			}
		}
		if removeOutput != "" {
			output = strings.TrimSpace(output + "\n" + removeOutput)
		}
	}
	return &backend.WhitelistResult{Mode: "online", Output: output, Entries: []backend.WhitelistEntry{}}, nil
}

func offlineWhitelistOperation(server *ServerConfig, driver gametypes.Driver, request backend.WhitelistRequest) (*backend.WhitelistResult, *backend.WhitelistError) {
	if driver.Whitelist.OfflineFile == "" {
		return nil, &backend.WhitelistError{
			Code: "offline_unsupported", Message: "offline whitelist management is not supported for this game type",
		}
	}
	entries, path, err := readOfflineWhitelist(server, driver.Whitelist.OfflineFile)
	if err != nil {
		return nil, &backend.WhitelistError{Code: "read_failed", Message: err.Error()}
	}
	if request.Operation == "list" {
		return &backend.WhitelistResult{Mode: "offline", Entries: entries}, nil
	}
	if request.Operation != "add" && request.Operation != "remove" {
		return nil, &backend.WhitelistError{Code: "invalid_operation", Message: "invalid whitelist operation"}
	}
	if !driver.IdentityValid(request.Username) {
		return nil, &backend.WhitelistError{Code: "invalid_identity", Message: "invalid in-game username"}
	}

	changed := false
	if request.Operation == "remove" {
		filtered := entries[:0]
		for _, entry := range entries {
			if strings.EqualFold(entry.Name, request.Username) {
				changed = true
				continue
			}
			filtered = append(filtered, entry)
		}
		entries = filtered
	} else {
		uuid := request.ExternalID
		if !minecraftServerOnlineMode(server) {
			uuid = offlineMinecraftUUID(request.Username)
		}
		uuid, err = formatMinecraftUUID(uuid)
		if err != nil {
			return nil, &backend.WhitelistError{
				Code:    "identity_required",
				Message: "a verified Minecraft UUID is required for offline whitelist changes on an online-mode server",
			}
		}
		if request.PreviousUsername != "" && !strings.EqualFold(request.PreviousUsername, request.Username) {
			filtered := entries[:0]
			for _, entry := range entries {
				if strings.EqualFold(entry.Name, request.PreviousUsername) {
					changed = true
					continue
				}
				filtered = append(filtered, entry)
			}
			entries = filtered
		}
		found := false
		for i := range entries {
			if strings.EqualFold(entries[i].Name, request.Username) || strings.EqualFold(entries[i].UUID, uuid) {
				found = true
				if entries[i].Name != request.Username || entries[i].UUID != uuid {
					entries[i] = backend.WhitelistEntry{UUID: uuid, Name: request.Username}
					changed = true
				}
				break
			}
		}
		if !found {
			entries = append(entries, backend.WhitelistEntry{UUID: uuid, Name: request.Username})
			changed = true
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	if changed {
		active, stateErr := whitelistServiceRunningState(server.Unit)
		if stateErr != nil {
			return nil, &backend.WhitelistError{
				Code: "status_unknown", Message: "could not recheck the managed service state: " + stateErr.Error(),
			}
		}
		if active {
			return nil, &backend.WhitelistError{
				Code: "server_started", Message: "the game server started while its offline whitelist was being changed; retry the operation",
			}
		}
		if err := writeOfflineWhitelist(path, server.DataDir, entries); err != nil {
			return nil, &backend.WhitelistError{Code: "write_failed", Message: err.Error()}
		}
	}
	return &backend.WhitelistResult{Mode: "offline", Entries: entries}, nil
}

func readOfflineWhitelist(server *ServerConfig, relativePath string) ([]backend.WhitelistEntry, string, error) {
	path, err := resolvePath(server, relativePath)
	if err != nil {
		return nil, "", fmt.Errorf("resolve whitelist file: %w", err)
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []backend.WhitelistEntry{}, path, nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("read whitelist file: %w", err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return []backend.WhitelistEntry{}, path, nil
	}
	var entries []backend.WhitelistEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, "", fmt.Errorf("parse whitelist file without modifying it: %w", err)
	}
	if entries == nil {
		entries = []backend.WhitelistEntry{}
	}
	return entries, path, nil
}

func writeOfflineWhitelist(path, dataDir string, entries []backend.WhitelistEntry) error {
	dir := filepath.Dir(path)
	mode := os.FileMode(0644)
	ownershipSource := path
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	} else if os.IsNotExist(err) {
		ownershipSource = dataDir
	} else {
		return fmt.Errorf("inspect whitelist file: %w", err)
	}
	ownerUID, ownerGID := -1, -1
	if info, err := os.Stat(ownershipSource); err == nil {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			ownerUID, ownerGID = int(stat.Uid), int(stat.Gid)
		}
	}

	temp, err := os.CreateTemp(dir, ".hogs-whitelist-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary whitelist file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return fmt.Errorf("preserve whitelist permissions: %w", err)
	}
	if ownerUID >= 0 {
		if err := temp.Chown(ownerUID, ownerGID); err != nil {
			temp.Close()
			return fmt.Errorf("preserve whitelist ownership: %w", err)
		}
	}
	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(entries); err != nil {
		temp.Close()
		return fmt.Errorf("encode whitelist file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync whitelist file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close whitelist file: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("atomically replace whitelist file: %w", err)
	}
	if directory, err := os.Open(dir); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

func minecraftServerOnlineMode(server *ServerConfig) bool {
	path, err := resolvePath(server, "server.properties")
	if err != nil {
		return true
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if found && strings.TrimSpace(key) == "online-mode" {
			return !strings.EqualFold(strings.TrimSpace(value), "false")
		}
	}
	return true
}

func offlineMinecraftUUID(username string) string {
	sum := md5.Sum([]byte("OfflinePlayer:" + username))
	sum[6] = (sum[6] & 0x0f) | 0x30
	sum[8] = (sum[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

func formatMinecraftUUID(value string) (string, error) {
	compact := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "-", ""))
	if len(compact) != 32 {
		return "", fmt.Errorf("invalid Minecraft UUID")
	}
	for _, char := range compact {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return "", fmt.Errorf("invalid Minecraft UUID")
		}
	}
	return compact[0:8] + "-" + compact[8:12] + "-" + compact[12:16] + "-" +
		compact[16:20] + "-" + compact[20:32], nil
}
