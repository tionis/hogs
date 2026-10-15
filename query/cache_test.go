package query

import "testing"

func TestStatusCacheReportsPlayerCountChanges(t *testing.T) {
	cache := NewServerStatusCache()
	var calls int
	cache.SetOnChange(func(string, *ServerStatus, *ServerStatus) { calls++ })
	cache.Set("s", &ServerStatus{Online: true, Players: 0, PlayersKnown: true})
	cache.Set("s", &ServerStatus{Online: true, Players: 0, PlayersKnown: true})
	cache.Set("s", &ServerStatus{Online: true, Players: 2, PlayersKnown: true})
	cache.Set("s", &ServerStatus{Online: true, Players: 3, PlayersKnown: false})
	cache.Set("s", &ServerStatus{Online: false})
	if calls != 2 {
		t.Fatalf("callbacks = %d, want 2 (one count change, one offline)", calls)
	}
}
