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

type StatusChangeCallback func(serverName string, oldStatus, newStatus *ServerStatus)

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

func (c *ServerStatusCache) Get(serverName string) (*ServerStatus, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, found := c.cache[serverName]
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

func (c *ServerStatusCache) Set(serverName string, status *ServerStatus) {
	c.mu.Lock()
	oldEntry := c.cache[serverName]
	c.cache[serverName] = &cacheEntry{
		Status:    status,
		Timestamp: time.Now(),
	}
	onChange := c.onChange
	c.mu.Unlock()

	if onChange != nil && oldEntry != nil && oldEntry.Status.Online != status.Online {
		onChange(serverName, oldEntry.Status, status)
	}
}
