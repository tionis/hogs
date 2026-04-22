package notify

import (
	"testing"

	"github.com/tionis/hogs/database"
)

func TestMatchesEventWildcard(t *testing.T) {
	if !matchesEvent([]byte("[]"), "server_up") {
		t.Error("expected empty events to match everything")
	}
	if !matchesEvent(nil, "server_up") {
		t.Error("expected nil events to match everything")
	}
}

func TestMatchesEventSpecific(t *testing.T) {
	events := []byte(`["server_up", "server_down"]`)
	if !matchesEvent(events, "server_up") {
		t.Error("expected server_up to match")
	}
	if !matchesEvent(events, "server_down") {
		t.Error("expected server_down to match")
	}
	if matchesEvent(events, "agent_connect") {
		t.Error("expected agent_connect NOT to match")
	}
}

func TestMatchesEventStar(t *testing.T) {
	events := []byte(`["*"]`)
	if !matchesEvent(events, "anything") {
		t.Error("expected * to match everything")
	}
}

func TestServiceSendNoChannels(t *testing.T) {
	store := testStore(t)
	svc := NewService(store)
	// Should not panic with no channels
	svc.Send("server_up", "test message")
}

func testStore(t *testing.T) *database.Store {
	t.Helper()
	dbPath := t.TempDir() + "/test.db"
	store, err := database.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() {
		store.DB.Close()
	})
	return store
}
