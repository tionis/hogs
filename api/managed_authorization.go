package api

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/tionis/hogs/access"
	"github.com/tionis/hogs/auth"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/engine"
)

type managedCapability string

const (
	managedConsoleRead   managedCapability = access.ConsoleRead
	managedConsoleWrite  managedCapability = access.ConsoleWrite
	managedFileRead      managedCapability = access.FileRead
	managedFileWrite     managedCapability = access.FileWrite
	managedBackupList    managedCapability = access.BackupList
	managedBackupCreate  managedCapability = access.BackupCreate
	managedBackupRestore managedCapability = access.BackupRestore
	managedStatus        managedCapability = access.Status
)

func userEnvFromRequest(store *database.Store, authenticator *auth.Authenticator, r *http.Request, trustProxyHeaders ...bool) *engine.UserEnv {
	trustProxy := len(trustProxyHeaders) > 0 && trustProxyHeaders[0]
	if key := auth.GetAPIKeyFromContext(r); key != nil {
		return &engine.UserEnv{
			Username: "api-key:" + key.Name, Role: key.Role, Groups: []string{},
			ClientIP: auth.ClientIP(r, trustProxy), CountryCode: auth.ClientCountry(r, trustProxy),
		}
	}
	username := "anonymous"
	role := "user"
	if authenticator != nil {
		username = authenticator.GetUsername(r)
		role = authenticator.GetUserRole(r)
	}
	if username == "" {
		username = "anonymous"
	}
	if role == "" {
		role = "user"
	}

	var groups []string
	if username != "anonymous" && store != nil {
		user, _ := store.GetUserByUsername(username)
		if user != nil {
			scimGroups, _ := store.GetSCIMGroupsForUser(user.ID)
			for _, group := range scimGroups {
				groups = append(groups, group.DisplayName)
			}
		}
	}
	return &engine.UserEnv{
		Username: username, Role: role, Groups: groups,
		ClientIP: auth.ClientIP(r, trustProxy), CountryCode: auth.ClientCountry(r, trustProxy),
	}
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
	case managedConsoleRead, managedConsoleWrite:
		if !management.ConsoleEnabled {
			return nil, nil, http.StatusForbidden, fmt.Errorf("console is disabled")
		}
	case managedFileRead, managedFileWrite:
		if len(management.WritablePaths) == 0 {
			return nil, nil, http.StatusForbidden, fmt.Errorf("file access is disabled")
		}
	case managedBackupList, managedBackupCreate:
		if !management.BackupEnabled {
			return nil, nil, http.StatusForbidden, fmt.Errorf("backup access is disabled")
		}
	case managedBackupRestore:
		if !management.RestoreEnabled {
			return nil, nil, http.StatusForbidden, fmt.Errorf("restore access is disabled")
		}
	case managedStatus:
		// Status is available for every managed server, subject to operator and ACL checks below.
	}

	trustProxy := eng != nil && eng.Config != nil && eng.Config.TrustProxyHeaders
	user := userEnvFromRequest(store, authenticator, r, trustProxy)
	if user.Role != "admin" {
		decision, accessErr := store.EvaluateServerAccess(server.ID, user.Username, user.Groups, string(capability))
		if accessErr != nil {
			return nil, nil, http.StatusInternalServerError, fmt.Errorf("evaluate server access: %w", accessErr)
		}
		if !decision.Allowed {
			if eng != nil {
				eng.LogAction(server.Name, string(capability), user.Username, "denied", decision.Reason, "web", nil)
			}
			return nil, nil, http.StatusForbidden, fmt.Errorf("access denied: %s", decision.Reason)
		}
	}
	if eng != nil {
		// Managed capabilities are enabled by server_management and granted by
		// the structured access model above. The legacy action allowlist belongs
		// to power/RCON actions and must not disable backups, files, or console.
		result, constraintErr := eng.EvaluateConstraints(server, string(capability), user)
		if constraintErr != nil {
			return nil, nil, http.StatusInternalServerError, fmt.Errorf("evaluate constraints: %w", constraintErr)
		}
		if !result.Allowed {
			status := result.Status
			if status == 0 {
				status = http.StatusForbidden
			}
			if eng != nil {
				eng.LogAction(server.Name, string(capability), user.Username, "blocked", result.Reason, "web", nil)
			}
			return nil, nil, status, fmt.Errorf("%s", result.Reason)
		}
	}
	return server, user, http.StatusOK, nil
}

func authorizeManagedPath(store *database.Store, eng *engine.Engine, authenticator *auth.Authenticator, r *http.Request, serverName, requestedPath string, capability managedCapability) (int, error) {
	server, _, status, err := authorizeManagedCapability(store, eng, authenticator, r, serverName, capability)
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
