package main

import (
	"errors"
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
	if active && driver.SupportsCommandWhitelist() {
		return onlineWhitelistOperation(server, driver, request)
	}
	fileAllowed := driver.SupportsFileWhitelist() && (!active ||
		(request.Operation == "list" && driver.Whitelist.File.AllowReadWhileRunning) ||
		(request.Operation != "list" && driver.Whitelist.File.AllowWriteWhileRunning))
	if fileAllowed {
		return fileWhitelistOperation(server, driver, request, active)
	}
	if active {
		return nil, &backend.WhitelistError{
			Code: "online_unsupported", Message: "online whitelist management is not supported for this game type",
		}
	}
	return nil, &backend.WhitelistError{
		Code: "offline_unsupported", Message: "offline whitelist management is not supported for this game type",
	}
}

func onlineWhitelistOperation(server *ServerConfig, driver gametypes.Driver, request backend.WhitelistRequest) (*backend.WhitelistResult, *backend.WhitelistError) {
	commands := driver.Whitelist.Commands
	var command string
	switch request.Operation {
	case "list":
		command = commands.ListCommand
	case "add":
		if !driver.IdentityValid(request.Username) {
			return nil, &backend.WhitelistError{Code: "invalid_identity", Message: "invalid in-game username"}
		}
		command = commands.AddCommand(request.Username)
	case "remove":
		if !driver.IdentityValid(request.Username) {
			return nil, &backend.WhitelistError{Code: "invalid_identity", Message: "invalid in-game username"}
		}
		command = commands.RemoveCommand(request.Username)
	default:
		return nil, &backend.WhitelistError{Code: "invalid_operation", Message: "invalid whitelist operation"}
	}
	output, err := executeCommand(server, command)
	if err != nil {
		return nil, &backend.WhitelistError{Code: "rcon_failed", Message: "RCON whitelist operation failed: " + err.Error()}
	}
	if request.Operation == "add" && request.PreviousUsername != "" &&
		!driver.IdentitiesEqual(request.PreviousUsername, request.Username) {
		if !driver.IdentityValid(request.PreviousUsername) {
			return nil, &backend.WhitelistError{Code: "invalid_identity", Message: "invalid previous in-game username"}
		}
		removeOutput, removeErr := executeCommand(server, commands.RemoveCommand(request.PreviousUsername))
		if removeErr != nil {
			return nil, &backend.WhitelistError{
				Code: "rcon_failed", Message: "new whitelist entry was added but the previous entry could not be removed: " + removeErr.Error(),
			}
		}
		if removeOutput != "" {
			output = strings.TrimSpace(output + "\n" + removeOutput)
		}
	}
	entries := onlineWhitelistEntries(server, driver, output)
	return &backend.WhitelistResult{Mode: "online", Output: output, Entries: entries}, nil
}

func onlineWhitelistEntries(server *ServerConfig, driver gametypes.Driver, output string) []backend.WhitelistEntry {
	if driver.Whitelist.File != nil {
		if entries, path, err := readFileWhitelist(server, driver.Whitelist.File); err == nil {
			if _, statErr := os.Stat(path); statErr == nil {
				return entries
			}
		}
	}
	var entries []backend.WhitelistEntry
	if driver.Whitelist.Commands != nil && driver.Whitelist.Commands.ParseList != nil {
		for _, name := range driver.Whitelist.Commands.ParseList(output) {
			entries = append(entries, backend.WhitelistEntry{Name: name})
		}
	}
	return entries
}

func fileWhitelistOperation(server *ServerConfig, driver gametypes.Driver, request backend.WhitelistRequest, running bool) (*backend.WhitelistResult, *backend.WhitelistError) {
	fileBackend := driver.Whitelist.File
	if fileBackend == nil || fileBackend.Path == "" || fileBackend.Decode == nil || fileBackend.Encode == nil {
		return nil, &backend.WhitelistError{
			Code: "offline_unsupported", Message: "offline whitelist management is not supported for this game type",
		}
	}
	entries, path, err := readFileWhitelist(server, fileBackend)
	if err != nil {
		return nil, &backend.WhitelistError{Code: "read_failed", Message: err.Error()}
	}
	mode := "offline"
	if running {
		mode = "online"
		if fileBackend.ChangesRequireRestart {
			mode = "pending_restart"
		}
	}
	if request.Operation == "list" {
		return &backend.WhitelistResult{Mode: mode, Entries: entries}, nil
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
			if driver.IdentitiesEqual(entry.Name, request.Username) {
				changed = true
				continue
			}
			filtered = append(filtered, entry)
		}
		entries = filtered
	} else {
		entry := backend.WhitelistEntry{Name: request.Username, UUID: request.ExternalID}
		if fileBackend.BuildEntry != nil {
			entry, err = fileBackend.BuildEntry(request.Username, request.ExternalID, func(relativePath string) ([]byte, error) {
				path, resolveErr := resolvePath(server, relativePath)
				if resolveErr != nil {
					return nil, resolveErr
				}
				return os.ReadFile(path)
			})
		}
		if err != nil {
			if errors.Is(err, gametypes.ErrExternalIdentityRequired) {
				return nil, &backend.WhitelistError{
					Code:    "identity_required",
					Message: "a verified external identity is required for this offline whitelist change",
				}
			}
			return nil, &backend.WhitelistError{
				Code: "invalid_identity", Message: err.Error(),
			}
		}
		if request.PreviousUsername != "" && !driver.IdentitiesEqual(request.PreviousUsername, request.Username) {
			filtered := entries[:0]
			for _, entry := range entries {
				if driver.IdentitiesEqual(entry.Name, request.PreviousUsername) {
					changed = true
					continue
				}
				filtered = append(filtered, entry)
			}
			entries = filtered
		}
		found := false
		for i := range entries {
			if driver.IdentitiesEqual(entries[i].Name, request.Username) ||
				(entry.UUID != "" && strings.EqualFold(entries[i].UUID, entry.UUID)) {
				found = true
				if entries[i] != entry {
					entries[i] = entry
					changed = true
				}
				break
			}
		}
		if !found {
			entries = append(entries, entry)
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
		if active != running {
			if running {
				return nil, &backend.WhitelistError{
					Code: "server_stopped", Message: "the game server stopped while its whitelist was being changed; retry the operation",
				}
			}
			return nil, &backend.WhitelistError{
				Code: "server_started", Message: "the game server started while its offline whitelist was being changed; retry the operation",
			}
		}
		encoded, encodeErr := fileBackend.Encode(entries)
		if encodeErr != nil {
			return nil, &backend.WhitelistError{Code: "write_failed", Message: "encode whitelist without modifying it: " + encodeErr.Error()}
		}
		if err := writeFileWhitelist(path, server.DataDir, encoded); err != nil {
			return nil, &backend.WhitelistError{Code: "write_failed", Message: err.Error()}
		}
	}
	return &backend.WhitelistResult{Mode: mode, Entries: entries}, nil
}

func readFileWhitelist(server *ServerConfig, fileBackend *gametypes.FileWhitelistDriver) ([]backend.WhitelistEntry, string, error) {
	path, err := resolvePath(server, fileBackend.Path)
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
	entries, err := fileBackend.Decode(raw)
	if err != nil {
		return nil, "", fmt.Errorf("parse whitelist file without modifying it: %w", err)
	}
	if entries == nil {
		entries = []backend.WhitelistEntry{}
	}
	return entries, path, nil
}

func writeFileWhitelist(path, dataDir string, encoded []byte) error {
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
	if _, err := temp.Write(encoded); err != nil {
		temp.Close()
		return fmt.Errorf("write whitelist file: %w", err)
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
