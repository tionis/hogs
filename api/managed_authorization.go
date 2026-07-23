package api

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/tionis/hogs/auth"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/engine"
)

type managedCapability string

const (
	managedConsole managedCapability = "console"
	managedFile    managedCapability = "file"
	managedBackup  managedCapability = "backup"
	managedRestore managedCapability = "restore"
	managedStatus  managedCapability = "status"
)

func userEnvFromRequest(store *database.Store, authenticator *auth.Authenticator, r *http.Request) *engine.UserEnv {
	if key := auth.GetAPIKeyFromContext(r); key != nil {
		return &engine.UserEnv{Email: "api-key:" + key.Name, Role: key.Role, Groups: []string{}}
	}
	email := "anonymous"
	role := "user"
	if authenticator != nil {
		email = authenticator.GetUserEmail(r)
		role = authenticator.GetUserRole(r)
	}
	if email == "" {
		email = "anonymous"
	}
	if role == "" {
		role = "user"
	}

	var groups []string
	if email != "anonymous" && store != nil {
		user, _ := store.GetUserByEmail(email)
		if user != nil {
			scimGroups, _ := store.GetSCIMGroupsForUser(user.ID)
			for _, group := range scimGroups {
				groups = append(groups, group.DisplayName)
			}
		}
	}
	return &engine.UserEnv{Email: email, Role: role, Groups: groups}
}

func authorizeManagedCapability(store *database.Store, eng *engine.Engine, authenticator *auth.Authenticator, r *http.Request, serverName string, capability managedCapability) (*database.Server, *engine.UserEnv, int, error) {
	server, err := store.GetServerByName(serverName)
	if err != nil {
		return nil, nil, http.StatusInternalServerError, fmt.Errorf("load server: %w", err)
	}
	if server == nil {
		return nil, nil, http.StatusNotFound, fmt.Errorf("server not found")
	}
	management, err := store.GetServerManagement(server.ID)
	if err != nil {
		return nil, nil, http.StatusInternalServerError, fmt.Errorf("load server management policy: %w", err)
	}
	if management == nil {
		return nil, nil, http.StatusForbidden, fmt.Errorf("server is not managed")
	}
	switch capability {
	case managedConsole:
		if !management.ConsoleEnabled {
			return nil, nil, http.StatusForbidden, fmt.Errorf("console is disabled")
		}
	case managedFile:
		if len(management.WritablePaths) == 0 {
			return nil, nil, http.StatusForbidden, fmt.Errorf("file access is disabled")
		}
	case managedBackup:
		if !management.BackupEnabled {
			return nil, nil, http.StatusForbidden, fmt.Errorf("backup access is disabled")
		}
	case managedRestore:
		if !management.RestoreEnabled {
			return nil, nil, http.StatusForbidden, fmt.Errorf("restore access is disabled")
		}
	case managedStatus:
		// Status is available for every managed server, subject to operator and ACL checks below.
	}

	user := userEnvFromRequest(store, authenticator, r)
	if capability == managedFile && user.Role != "admin" {
		return nil, nil, http.StatusForbidden, fmt.Errorf("server administrator access required")
	}
	if user.Role != "admin" && !isManagedOperator(management.Operators, user) {
		return nil, nil, http.StatusForbidden, fmt.Errorf("server operator access required")
	}
	if eng != nil {
		result := eng.Evaluate(server, string(capability), nil, user)
		if !result.Allowed {
			status := result.Status
			if status == 0 {
				status = http.StatusForbidden
			}
			return nil, nil, status, fmt.Errorf("%s", result.Reason)
		}
	}
	return server, user, http.StatusOK, nil
}

func authorizeManagedPath(store *database.Store, eng *engine.Engine, authenticator *auth.Authenticator, r *http.Request, serverName, requestedPath string) (int, error) {
	server, _, status, err := authorizeManagedCapability(store, eng, authenticator, r, serverName, managedFile)
	if err != nil {
		return status, err
	}
	management, err := store.GetServerManagement(server.ID)
	if err != nil || management == nil {
		return http.StatusInternalServerError, fmt.Errorf("load server management policy")
	}
	if filepath.IsAbs(requestedPath) {
		return http.StatusBadRequest, fmt.Errorf("absolute paths are not allowed")
	}
	target := filepath.Clean(filepath.Join(management.DataPath, requestedPath))
	for _, allowedPath := range management.WritablePaths {
		allowed := filepath.Clean(allowedPath)
		relative, relErr := filepath.Rel(allowed, target)
		if relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return http.StatusOK, nil
		}
	}
	return http.StatusForbidden, fmt.Errorf("path is outside the managed file allowlist")
}

func isManagedOperator(operators []string, user *engine.UserEnv) bool {
	if user == nil {
		return false
	}
	for _, operator := range operators {
		if operator == user.Email {
			return true
		}
		for _, group := range user.Groups {
			if operator == group {
				return true
			}
		}
	}
	return false
}
