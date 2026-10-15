package events

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/tionis/hogs/database"
)

type fakeStream struct {
	ttl     string
	records []map[string]interface{}
}

type fakePicoMQ struct {
	mu        sync.Mutex
	streams   map[string]*fakeStream
	producers map[string][2]int64 // stream|id -> epoch, expected seq
	failNext  int
}

func newFakePicoMQ(t *testing.T) (*fakePicoMQ, *httptest.Server) {
	fake := &fakePicoMQ{streams: map[string]*fakeStream{}, producers: map[string][2]int64{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodPut:
			if _, ok := fake.streams[r.URL.Path]; ok {
				w.WriteHeader(http.StatusOK)
				return
			}
			fake.streams[r.URL.Path] = &fakeStream{ttl: r.Header.Get("Pico-TTL")}
			w.WriteHeader(http.StatusCreated)
		case http.MethodPost:
			stream, ok := fake.streams[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if fake.failNext > 0 {
				fake.failNext--
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			epoch, _ := strconv.ParseInt(r.Header.Get("Pico-Producer-Epoch"), 10, 64)
			seq, _ := strconv.ParseInt(r.Header.Get("Pico-Producer-Seq"), 10, 64)
			key := r.URL.Path + "|" + r.Header.Get("Pico-Producer-Id")
			state, known := fake.producers[key]
			if known && epoch < state[0] {
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, `{"error":"conflict","message":"producer epoch is stale"}`)
				return
			}
			expected := int64(0)
			if known && epoch == state[0] {
				expected = state[1]
			}
			if seq == expected-1 {
				w.WriteHeader(http.StatusOK)
				return
			}
			if seq != expected {
				w.Header().Set("Pico-Expected-Seq", strconv.FormatInt(expected, 10))
				w.WriteHeader(http.StatusConflict)
				return
			}
			var record map[string]interface{}
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &record); err != nil {
				t.Errorf("record is not JSON: %v", err)
			}
			stream.records = append(stream.records, record)
			fake.producers[key] = [2]int64{epoch, expected + 1}
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(server.Close)
	return fake, server
}

func (f *fakePicoMQ) records(path string) []map[string]interface{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	if stream, ok := f.streams[path]; ok {
		return stream.records
	}
	return nil
}

func testPublisher(t *testing.T, url string) (*Publisher, *database.Store) {
	t.Helper()
	store, err := database.NewStore(filepath.Join(t.TempDir(), "hogs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DB.Close() })
	publisher, err := New(store.DB, Config{URL: url, Token: "test-token", Instance: "node-a"})
	if err != nil || publisher == nil {
		t.Fatalf("New: %v", err)
	}
	return publisher, store
}

func TestPublisherRegistersAndAppendsCloudEvents(t *testing.T) {
	fake, server := newFakePicoMQ(t)
	publisher, store := testPublisher(t, server.URL)
	publisher.Emit(ActionRecorded, "valheim", map[string]interface{}{
		"server": "valheim", "server_name": "Valheim", "action": "start", "actor": "eric", "result": "success", "origin": "web",
	})
	if err := publisher.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	registry := fake.records("/events/hogs/_registry")
	if len(registry) != len(Streams)+len(Types) {
		t.Fatalf("registry records = %d", len(registry))
	}
	if registry[0]["type"] != streamDefined || registry[len(registry)-1]["type"] != typeDefined {
		t.Fatalf("registry order: %v ... %v", registry[0]["type"], registry[len(registry)-1]["type"])
	}

	actions := fake.records("/events/hogs/actions")
	if len(actions) != 1 {
		t.Fatalf("actions = %d", len(actions))
	}
	event := actions[0]
	for key, want := range map[string]interface{}{
		"specversion": "1.0", "type": ActionRecorded, "source": "/hogs/node-a", "subject": "valheim",
		"dataschema": "picomq:/events/hogs/_registry#" + ActionRecorded,
	} {
		if event[key] != want {
			t.Errorf("%s = %v, want %v", key, event[key], want)
		}
	}
	if fake.streams["/events/hogs/servers"] != nil {
		t.Error("streams must only be created when they have events")
	}

	// A restart with unchanged definitions registers nothing new.
	again, err := New(store.DB, Config{URL: server.URL, Token: "test-token", Instance: "node-a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := again.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(fake.records("/events/hogs/_registry")); got != len(Streams)+len(Types) {
		t.Fatalf("registry grew to %d on an unchanged restart", got)
	}
}

func TestPublisherRetriesWithoutDuplicates(t *testing.T) {
	fake, server := newFakePicoMQ(t)
	publisher, store := testPublisher(t, server.URL)
	if err := publisher.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	publisher.Emit(ServerStatus, "valheim", map[string]interface{}{"server": "valheim", "server_name": "Valheim", "online": true})
	fake.failNext = 1
	if err := publisher.Flush(context.Background()); err == nil {
		t.Fatal("flush succeeded despite a failed append")
	}
	// Simulate an append that reached the server but whose outbox row
	// survived: the retry carries the same sequence and is not written twice.
	var body string
	if err := store.DB.QueryRow(`SELECT body FROM event_outbox WHERE stream='servers'`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var record map[string]interface{}
	_ = json.Unmarshal([]byte(body), &record)
	var epoch int64
	_ = store.DB.QueryRow(`SELECT epoch FROM event_producers WHERE stream='servers'`).Scan(&epoch)
	fake.mu.Lock()
	fake.streams["/events/hogs/servers"].records = append(fake.streams["/events/hogs/servers"].records, record)
	fake.producers["/events/hogs/servers|node-a"] = [2]int64{epoch, 1}
	fake.mu.Unlock()

	if err := publisher.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(fake.records("/events/hogs/servers")); got != 1 {
		t.Fatalf("servers records = %d, want 1", got)
	}
	var queued int
	_ = store.DB.QueryRow(`SELECT COUNT(*) FROM event_outbox`).Scan(&queued)
	if queued != 0 {
		t.Fatalf("outbox still holds %d rows", queued)
	}
}

func TestPublisherRenumbersAfterStaleEpoch(t *testing.T) {
	fake, server := newFakePicoMQ(t)
	publisher, store := testPublisher(t, server.URL)
	if err := publisher.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, online := range []bool{true, false} {
		publisher.Emit(ServerStatus, "valheim", map[string]interface{}{"server": "valheim", "server_name": "Valheim", "online": online})
	}
	// The server has seen a newer epoch, for example after a database restore.
	fake.mu.Lock()
	fake.producers["/events/hogs/servers|node-a"] = [2]int64{1 << 40, 7}
	fake.mu.Unlock()
	if err := publisher.Flush(context.Background()); err == nil {
		t.Fatal("flush succeeded against a stale epoch")
	}
	var epoch int64
	_ = store.DB.QueryRow(`SELECT epoch FROM event_producers WHERE stream='servers'`).Scan(&epoch)
	fake.mu.Lock()
	fake.producers["/events/hogs/servers|node-a"] = [2]int64{epoch - 1, 7}
	fake.mu.Unlock()
	if err := publisher.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	records := fake.records("/events/hogs/servers")
	if len(records) != 2 || records[0]["data"].(map[string]interface{})["online"] != true {
		t.Fatalf("records after renumbering = %v", records)
	}
}

func TestNilPublisherDropsEvents(t *testing.T) {
	var publisher *Publisher
	publisher.Emit(ActionRecorded, "x", map[string]interface{}{})
	publisher.Run(context.Background())
}
