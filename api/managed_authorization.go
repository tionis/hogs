package api

import (
	"fmt"
	"net/http"

	"github.com/tionis/hogs/auth"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/engine"
)

type managedCapability string

const (
	managedConsole managedCapability = "console"
)

func userEnvFromRequest(store *database.Store, authenticator *auth.Authenticator, r *http.Request) *engine.UserEnv {
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
	if capability == managedConsole && !management.ConsoleEnabled {
		return nil, nil, http.StatusForbidden, fmt.Errorf("console is disabled")
	}

	user := userEnvFromRequest(store, authenticator, r)
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
