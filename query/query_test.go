package query

import (
	"fmt"
	"testing"
	"time"

	"github.com/tionis/hogs/database"
)

func TestCacheSetGet(t *testing.T) {
	cache := NewServerStatusCache()

	status := &ServerStatus{
		Online:      true,
		Players:     5,
		MaxPlayers:  20,
		LastUpdated: time.Now(),
	}

	cache.Set("test-server", status)

	got, found := cache.Get("test-server")
	if !found {
		t.Fatal("expected cache hit")
	}
	if got.Players != 5 {
		t.Errorf("Players = %d, want 5", got.Players)
	}
}

func TestCacheMiss(t *testing.T) {
	cache := NewServerStatusCache()

	_, found := cache.Get("nonexistent")
	if found {
		t.Error("expected cache miss")
	}
}

func TestCacheExpiration(t *testing.T) {
	cache := NewServerStatusCache()

	status := &ServerStatus{
		Online:      true,
		Players:     5,
		MaxPlayers:  20,
		LastUpdated: time.Now(),
	}

	cache.Set("expiring-server", status)

	cache.mu.Lock()
	entry := cache.cache["expiring-server"]
	entry.Timestamp = time.Now().Add(-2 * time.Minute)
	cache.mu.Unlock()

	_, found := cache.Get("expiring-server")
	if found {
		t.Error("expected expired cache entry to be missed")
	}
}

func TestOfflineCacheShortExpiration(t *testing.T) {
	cache := NewServerStatusCache()

	status := &ServerStatus{
		Online:      false,
		LastUpdated: time.Now(),
		Error:       "timed out",
	}

	cache.Set("offline-server", status)

	cache.mu.Lock()
	entry := cache.cache["offline-server"]
	entry.Timestamp = time.Now().Add(-15 * time.Second)
	cache.mu.Unlock()

	_, found := cache.Get("offline-server")
	if found {
		t.Error("expected expired offline cache entry to be missed (10s error TTL)")
	}
}

func TestNoopQuerier(t *testing.T) {
	querier := &NoopQuerier{}
	srv := &database.Server{Name: "test", GameType: "unknown"}

	status, err := querier.Query(srv)
	if err == nil {
		t.Error("expected error from NoopQuerier")
	}
	if status.Online {
		t.Error("expected Offline status from NoopQuerier")
	}
	if status.Error == "" {
		t.Error("expected error message in status")
	}
}

func TestNewQuerierKnown(t *testing.T) {
	tests := []struct {
		gameType string
		want     string
	}{
		{"minecraft", "*query.MinecraftQuerier"},
		{"satisfactory", "*query.SatisfactoryQuerier"},
		{"factorio", "*query.FactorioQuerier"},
		{"valheim", "*query.ValheimQuerier"},
	}
	for _, tt := range tests {
		q := NewQuerier(tt.gameType)
		if got := formatType(q); got != tt.want {
			t.Errorf("NewQuerier(%q) = %s, want %s", tt.gameType, got, tt.want)
		}
	}
}

func TestNewQuerierUnknown(t *testing.T) {
	q := NewQuerier("unknown_game")
	if got := formatType(q); got != "*query.NoopQuerier" {
		t.Errorf("NewQuerier(unknown) = %s, want *query.NoopQuerier", got)
	}
}

func TestRegisterQuerier(t *testing.T) {
	custom := &NoopQuerier{}
	RegisterQuerier("custom_game", custom)
	q := NewQuerier("custom_game")
	if q != custom {
		t.Error("RegisterQuerier should make querier available via NewQuerier")
	}
	delete(queriers, "custom_game")
}

func TestRegisteredGameTypes(t *testing.T) {
	types := RegisteredGameTypes()
	found := false
	for _, gt := range types {
		if gt == "valheim" {
			found = true
		}
	}
	if !found {
		t.Errorf("RegisteredGameTypes() = %v, want valheim included", types)
	}
}

func formatType(v interface{}) string {
	return fmt.Sprintf("%T", v)
}
