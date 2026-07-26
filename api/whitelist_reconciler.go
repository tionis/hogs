package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/mux"
	"github.com/tionis/hogs/access"
	"github.com/tionis/hogs/backend"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/gametypes"
)

type WhitelistReconcileResult struct {
	Server  string `json:"server"`
	Desired int    `json:"desired"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Manual  int    `json:"manual"`
}

// WhitelistReconciler serializes and debounces event-driven updates. It also
// performs a periodic safety reconciliation so temporary worker outages do not
// leave access state stale.
type WhitelistReconciler struct {
	handler        *PterodactylHandler
	queue          chan int
	stop           chan struct{}
	retryScheduled atomic.Bool
}

func NewWhitelistReconciler(handler *PterodactylHandler) *WhitelistReconciler {
	return &WhitelistReconciler{
		handler: handler, queue: make(chan int, 4096), stop: make(chan struct{}),
	}
}

func (r *WhitelistReconciler) Start() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case serverID := <-r.queue:
				pending := map[int]bool{serverID: true}
			drain:
				for {
					select {
					case id := <-r.queue:
						pending[id] = true
					default:
						break drain
					}
				}
				for id := range pending {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
					_, err := r.ReconcileServer(ctx, id)
					cancel()
					if err != nil {
						log.Printf("automatic whitelist reconciliation for server %d failed: %v", id, err)
					}
				}
			case <-ticker.C:
				r.TriggerAll()
			case <-r.stop:
				return
			}
		}
	}()
	r.scheduleTriggerAllRetry()
}

func (r *WhitelistReconciler) Close() {
	select {
	case <-r.stop:
	default:
		close(r.stop)
	}
}

func (r *WhitelistReconciler) Trigger(serverID int) {
	if serverID <= 0 {
		return
	}
	select {
	case r.queue <- serverID:
	default:
		log.Printf("automatic whitelist queue full; periodic reconciliation will retry server %d", serverID)
	}
}

func (r *WhitelistReconciler) TriggerAll() {
	servers, err := r.handler.Store.ListServers()
	if err != nil {
		log.Printf("list servers for whitelist reconciliation: %v", err)
		r.scheduleTriggerAllRetry()
		return
	}
	for i := range servers {
		driver := r.handler.Store.ResolveGameDriver(servers[i].GameType)
		if driver.SupportsWhitelist() && driver.IdentityProvider != "" {
			r.Trigger(servers[i].ID)
		}
	}
}

func (r *WhitelistReconciler) scheduleTriggerAllRetry() {
	if !r.retryScheduled.CompareAndSwap(false, true) {
		return
	}
	time.AfterFunc(5*time.Second, func() {
		r.retryScheduled.Store(false)
		select {
		case <-r.stop:
			return
		default:
			r.TriggerAll()
		}
	})
}

type desiredWhitelistIdentity struct {
	resolved gametypes.ResolvedIdentity
}

func (r *WhitelistReconciler) ReconcileServer(ctx context.Context, serverID int) (WhitelistReconcileResult, error) {
	server, err := r.handler.Store.GetServer(serverID)
	if err != nil || server == nil {
		return WhitelistReconcileResult{}, fmt.Errorf("load server: %w", err)
	}
	result := WhitelistReconcileResult{Server: server.Name}
	driver := r.handler.Store.ResolveGameDriver(server.GameType)
	if !driver.SupportsWhitelist() || driver.IdentityProvider == "" {
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
	managed, ok := gameBackend.(backend.WhitelistBackend)
	if !ok {
		return result, fmt.Errorf("%s backend does not expose structured whitelist management", gameBackend.Name())
	}
	list, _, err := r.handler.structuredWhitelist(ctx, managed, driver, backend.WhitelistRequest{Operation: "list"})
	if err != nil {
		return result, fmt.Errorf("list whitelist: %w", err)
	}
	actual := map[string]backend.WhitelistEntry{}
	for _, entry := range list.Entries {
		actual[identityKey(driver, entry.Name)] = entry
	}

	users, err := r.handler.Store.ListUsers()
	if err != nil {
		return result, err
	}
	desired := map[string]desiredWhitelistIdentity{}
	desiredNames := map[string]bool{}
	desiredOwner := map[string]string{}
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
		decision, decisionErr := r.handler.Store.EvaluateServerAccess(server.ID, user.Username, groupNames, access.ServerJoin)
		if decisionErr != nil {
			return result, decisionErr
		}
		if user.Role != "admin" && user.Role != "system" && !decision.Allowed {
			continue
		}
		identity, identityErr := r.handler.Store.GetGameIdentity(user.Username, driver.IdentityProvider)
		if identityErr != nil {
			return result, identityErr
		}
		if identity == nil || identity.Source != "scim" {
			continue
		}
		resolved, valid := driver.AuthentikIdentity(identity.Username, identity.ExternalID)
		if !valid {
			continue
		}
		key := identityKey(driver, resolved.Username)
		desired[user.Username] = desiredWhitelistIdentity{resolved: resolved}
		desiredNames[key] = true
		if desiredOwner[key] == "" {
			desiredOwner[key] = user.Username
		}
	}
	result.Desired = len(desired)

	ownedRows, err := r.handler.Store.ListUserWhitelists(server.ID)
	if err != nil {
		return result, err
	}
	owned := map[string]database.UserWhitelist{}
	for _, entry := range ownedRows {
		owned[entry.UserUsername] = entry
	}

	for username, wanted := range desired {
		key := identityKey(driver, wanted.resolved.Username)
		current, hasOwned := owned[username]
		_, exists := actual[key]
		sameOwned := hasOwned && driver.IdentitiesEqual(current.Username, wanted.resolved.Username)
		addedByHOGS := false
		if !exists {
			if _, _, err := r.handler.structuredWhitelist(ctx, managed, driver, backend.WhitelistRequest{
				Operation: "add", Username: wanted.resolved.Username, ExternalID: wanted.resolved.ExternalID,
			}); err != nil {
				return result, fmt.Errorf("add %s: %w", wanted.resolved.Username, err)
			}
			actual[key] = backend.WhitelistEntry{Name: wanted.resolved.Username}
			result.Added++
			addedByHOGS = true
		}
		if hasOwned && !sameOwned && !desiredNames[identityKey(driver, current.Username)] {
			if _, exists := actual[identityKey(driver, current.Username)]; exists {
				if _, _, err := r.handler.structuredWhitelist(ctx, managed, driver, backend.WhitelistRequest{
					Operation: "remove", Username: current.Username,
				}); err != nil {
					return result, fmt.Errorf("remove stale identity %s: %w", current.Username, err)
				}
				delete(actual, identityKey(driver, current.Username))
				result.Removed++
			}
		}
		switch {
		case sameOwned || addedByHOGS:
			if err := r.handler.Store.SetUserWhitelistForIdentity(
				username, server.ID, wanted.resolved.Username, driver.IdentityCaseSensitive,
			); err != nil {
				return result, err
			}
		case hasOwned:
			// The desired identity was already present but was not added by
			// HOGS. Do not adopt it; it remains an external/manual entry.
			if err := r.handler.Store.DeleteUserWhitelist(username, server.ID); err != nil {
				return result, err
			}
		}
		delete(owned, username)
	}

	for username, stale := range owned {
		key := identityKey(driver, stale.Username)
		if !desiredNames[key] {
			if _, exists := actual[key]; exists {
				if _, _, err := r.handler.structuredWhitelist(ctx, managed, driver, backend.WhitelistRequest{
					Operation: "remove", Username: stale.Username,
				}); err != nil {
					return result, fmt.Errorf("remove revoked identity %s: %w", stale.Username, err)
				}
				delete(actual, key)
				result.Removed++
			}
		} else if recipient := desiredOwner[key]; recipient != "" && recipient != username {
			// The same verified game identity moved to another active HOGS
			// account. Preserve HOGS ownership so a later revocation still
			// removes the entry instead of accidentally turning it manual.
			if err := r.handler.Store.SetUserWhitelistForIdentity(
				recipient, server.ID, stale.Username, driver.IdentityCaseSensitive,
			); err != nil {
				return result, err
			}
		}
		if err := r.handler.Store.DeleteUserWhitelist(username, server.ID); err != nil {
			return result, err
		}
	}

	for key := range actual {
		if !desiredNames[key] {
			result.Manual++
		}
	}
	if r.handler.Engine != nil && (result.Added > 0 || result.Removed > 0) {
		r.handler.Engine.LogAction(
			server.Name, "whitelist.reconcile", "system", "success",
			"automatic whitelist matched current identity and access state", "system",
			map[string]string{
				"desired": fmt.Sprint(result.Desired), "added": fmt.Sprint(result.Added),
				"removed": fmt.Sprint(result.Removed), "manualPreserved": fmt.Sprint(result.Manual),
			},
		)
	}
	return result, nil
}

func (r *WhitelistReconciler) HandleReconcile(w http.ResponseWriter, request *http.Request) {
	server, err := r.handler.Store.GetServerByName(mux.Vars(request)["serverName"])
	if err != nil || server == nil {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}
	user := r.handler.getUserEnv(request)
	if user.Role != "admin" && user.Role != "system" {
		decision, decisionErr := r.handler.Store.EvaluateServerAccess(
			server.ID, user.Username, user.Groups, access.WhitelistManage,
		)
		if decisionErr != nil || !decision.Allowed {
			http.Error(w, "Whitelist management permission required", http.StatusForbidden)
			return
		}
	}
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Minute)
	defer cancel()
	result, err := r.ReconcileServer(ctx, server.ID)
	if err != nil {
		http.Error(w, "Whitelist reconciliation failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func identityKey(driver gametypes.Driver, username string) string {
	if driver.IdentityCaseSensitive {
		return username
	}
	return strings.ToLower(username)
}
