package query

import (
	"sync"
	"time"
)

const (
	CacheExpiration      = 60 * time.Second
	ErrorCacheExpiration = 10 * time.Second
)

type cacheEntry struct {
	Status    *ServerStatus
	Timestamp time.Time
}

type StatusChangeCallback func(serverID string, oldStatus, newStatus *ServerStatus)

type ServerStatusCache struct {
	mu       sync.RWMutex
	cache    map[string]*cacheEntry
	onChange StatusChangeCallback
}

func NewServerStatusCache() *ServerStatusCache {
	return &ServerStatusCache{
		cache: make(map[string]*cacheEntry),
	}
}

func (c *ServerStatusCache) SetOnChange(cb StatusChangeCallback) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onChange = cb
}

func (c *ServerStatusCache) Get(serverID string) (*ServerStatus, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, found := c.cache[serverID]
	if !found {
		return nil, false
	}

	expiration := CacheExpiration
	if !entry.Status.Online {
		expiration = ErrorCacheExpiration
	}

	if time.Since(entry.Timestamp) < expiration {
		return entry.Status, true
	}

	return nil, false
}

// Latest returns the most recent observation even if it is too old to satisfy
// an ordinary status request. Error pages use it only as explanatory context.
func (c *ServerStatusCache) Latest(serverID string) (*ServerStatus, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, found := c.cache[serverID]
	if !found || entry.Status == nil {
		return nil, false
	}
	status := *entry.Status
	return &status, true
}

func (c *ServerStatusCache) Set(serverID string, status *ServerStatus) {
	c.mu.Lock()
	oldEntry := c.cache[serverID]
	c.cache[serverID] = &cacheEntry{
		Status:    status,
		Timestamp: time.Now(),
	}
	onChange := c.onChange
	c.mu.Unlock()

	if onChange != nil && oldEntry != nil && oldEntry.Status.Online != status.Online {
		onChange(serverID, oldEntry.Status, status)
	}
}

// SetAgentObservation updates process reachability and authoritative occupancy
// without discarding richer game-protocol fields already cached by the control
// plane (MOTD, version, player samples, and protocol metadata).
func (c *ServerStatusCache) SetAgentObservation(serverID string, observation *ServerStatus) {
	c.mu.Lock()
	oldEntry := c.cache[serverID]
	status := observation
	if observation.Online && oldEntry != nil && oldEntry.Status != nil {
		merged := *oldEntry.Status
		merged.Online = observation.Online
		merged.Players = observation.Players
		merged.MaxPlayers = observation.MaxPlayers
		merged.PlayersKnown = observation.PlayersKnown
		if observation.Version != "" {
			merged.Version = observation.Version
		}
		merged.LastUpdated = observation.LastUpdated
		merged.Error = observation.Error
		status = &merged
	}
	c.cache[serverID] = &cacheEntry{Status: status, Timestamp: time.Now()}
	onChange := c.onChange
	c.mu.Unlock()

	if onChange != nil && oldEntry != nil && oldEntry.Status.Online != status.Online {
		onChange(serverID, oldEntry.Status, status)
	}
}
