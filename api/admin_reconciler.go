package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/tionis/hogs/access"
	"github.com/tionis/hogs/agent"
	"github.com/tionis/hogs/backend"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/gametypes"
)

type AdminReconcileResult struct {
	Server             string `json:"server"`
	Eligible           int    `json:"eligible"`
	Desired            int    `json:"desired"`
	MissingIdentity    int    `json:"missingIdentity"`
	UnverifiedIdentity int    `json:"unverifiedIdentity"`
	InvalidIdentity    int    `json:"invalidIdentity"`
	Added              int    `json:"added"`
	Removed            int    `json:"removed"`
	Manual             int    `json:"manual"`
}

// adminFileBackend moves native admin-list files through an agent file
// backend. *agent.AgentBackend implements it without any agent changes.
type adminFileBackend interface {
	FileRead(ctx context.Context, serverPath string) ([]byte, error)
	FileWrite(ctx context.Context, serverPath string, content []byte) error
}

type desiredAdminIdentity struct {
	username string
	resolved gametypes.ResolvedIdentity
}

// reconcileAdmins syncs the game's native administrator list (for example
// Valheim adminlist.txt) from HOGS users holding the server.admin
// capability. It runs independently of the join-enforcement mode: admin
// rights are orthogonal to admission. HOGS-owned entries are added and
// revoked; manual entries are preserved and counted.
func (r *WhitelistReconciler) reconcileAdmins(ctx context.Context, server *database.Server, driver gametypes.Driver) (AdminReconcileResult, error) {
	result := AdminReconcileResult{Server: server.Name}
	if !driver.SupportsAdminList() || driver.IdentityProvider == "" {
		return result, nil
	}
	link, err := r.handler.Store.GetPterodactylLink(server.ID)
	if err != nil || link == nil {
		return result, fmt.Errorf("server has no management backend")
	}
	gameBackend, err := r.handler.resolveBackend(server, link)
	if err != nil {
		return result, err
	}
	fileBackend, ok := gameBackend.(adminFileBackend)
	if !ok {
		return result, fmt.Errorf("%s backend does not expose managed file access", gameBackend.Name())
	}
	raw, err := fileBackend.FileRead(ctx, driver.AdminList.Path)
	if err != nil {
		if errors.Is(err, agent.ErrFileNotFound) {
			raw = []byte{}
		} else {
			return result, fmt.Errorf("read admin list: %w", err)
		}
	}
	decoded, err := driver.AdminList.Decode(raw)
	if err != nil {
		return result, fmt.Errorf("decode admin list: %w", err)
	}
	actual := map[string]backend.WhitelistEntry{}
	for _, entry := range decoded {
		actual[identityKey(driver, entry.Name)] = entry
	}

	users, err := r.handler.Store.ListUsers()
	if err != nil {
		return result, err
	}
	desired := map[string]desiredAdminIdentity{}
	desiredNames := map[string]bool{}
	for _, user := range users {
		if !user.Active {
			continue
		}
		groups, groupErr := r.handler.Store.GetSCIMGroupsForUser(user.ID)
		if groupErr != nil {
			return result, groupErr
		}
		groupNames := make([]string, 0, len(groups))
		for _, group := range groups {
			groupNames = append(groupNames, group.DisplayName)
		}
		decision, decisionErr := r.handler.Store.EvaluateServerAccess(server.ID, user.Username, groupNames, access.ServerAdmin)
		if decisionErr != nil {
			return result, decisionErr
		}
		if user.Role != "admin" && user.Role != "system" && !decision.Allowed {
			continue
		}
		result.Eligible++
		identity, identityErr := r.handler.Store.GetGameIdentity(user.Username, driver.IdentityProvider)
		if identityErr != nil {
			return result, identityErr
		}
		if identity == nil {
			result.MissingIdentity++
			continue
		}
		if identity.Source != "scim" {
			result.UnverifiedIdentity++
			continue
		}
		resolved, valid := driver.AuthentikIdentity(identity.Username, identity.ExternalID)
		if !valid {
			result.InvalidIdentity++
			continue
		}
		key := identityKey(driver, resolved.Username)
		desired[user.Username] = desiredAdminIdentity{username: user.Username, resolved: resolved}
		desiredNames[key] = true
	}
	result.Desired = len(desired)

	ownedRows, err := r.handler.Store.ListUserAdmins(server.ID)
	if err != nil {
		return result, err
	}
	owned := map[string]database.UserAdmin{}
	for _, entry := range ownedRows {
		owned[entry.UserUsername] = entry
	}
	ownedByKey := map[string]database.UserAdmin{}
	for _, entry := range ownedRows {
		ownedByKey[identityKey(driver, entry.Username)] = entry
	}

	kept := make([]backend.WhitelistEntry, 0, len(actual)+len(desired))
	keptKeys := map[string]bool{}
	for _, entry := range decoded {
		key := identityKey(driver, entry.Name)
		if keptKeys[key] {
			continue
		}
		if desiredNames[key] {
			kept = append(kept, entry)
			keptKeys[key] = true
			continue
		}
		if stale, hasOwned := ownedByKey[key]; hasOwned {
			if _, exists := actual[key]; exists {
				result.Removed++
			}
			if err := r.handler.Store.DeleteUserAdmin(stale.UserUsername, server.ID); err != nil {
				return result, err
			}
			delete(owned, stale.UserUsername)
			continue
		}
		kept = append(kept, entry)
		keptKeys[key] = true
		result.Manual++
	}
	for username, wanted := range desired {
		key := identityKey(driver, wanted.resolved.Username)
		if keptKeys[key] {
			if current, hasOwned := owned[username]; hasOwned && !driver.IdentitiesEqual(current.Username, wanted.resolved.Username) {
				if err := r.handler.Store.DeleteUserAdmin(username, server.ID); err != nil {
					return result, err
				}
			} else if hasOwned {
				if err := r.handler.Store.SetUserAdminForIdentity(
					username, server.ID, wanted.resolved.Username, driver.IdentityCaseSensitive,
				); err != nil {
					return result, err
				}
			}
			// Desired identity already present without an owned row stays
			// an external entry; never adopt it.
			delete(owned, username)
			continue
		}
		kept = append(kept, backend.WhitelistEntry{Name: wanted.resolved.Username})
		keptKeys[key] = true
		result.Added++
		if err := r.handler.Store.SetUserAdminForIdentity(
			username, server.ID, wanted.resolved.Username, driver.IdentityCaseSensitive,
		); err != nil {
			return result, err
		}
		delete(owned, username)
	}
	for username := range owned {
		if err := r.handler.Store.DeleteUserAdmin(username, server.ID); err != nil {
			return result, err
		}
	}

	if result.Added > 0 || result.Removed > 0 {
		encoded, err := driver.AdminList.Encode(kept)
		if err != nil {
			return result, fmt.Errorf("encode admin list: %w", err)
		}
		if err := fileBackend.FileWrite(ctx, driver.AdminList.Path, encoded); err != nil {
			return result, fmt.Errorf("write admin list: %w", err)
		}
	}
	if r.handler.Engine != nil && (result.Added > 0 || result.Removed > 0) {
		r.handler.Engine.LogAction(
			server.Name, "admin.reconcile", "system", "success",
			"native admin list matched current identity and access state", "system",
			map[string]string{
				"eligible": fmt.Sprint(result.Eligible), "desired": fmt.Sprint(result.Desired),
				"missingIdentity":    fmt.Sprint(result.MissingIdentity),
				"unverifiedIdentity": fmt.Sprint(result.UnverifiedIdentity),
				"invalidIdentity":    fmt.Sprint(result.InvalidIdentity), "added": fmt.Sprint(result.Added),
				"removed": fmt.Sprint(result.Removed), "manualPreserved": fmt.Sprint(result.Manual),
			},
		)
	}
	return result, nil
}
