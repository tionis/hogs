package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/query"
)

type mapRoundTripper struct {
	mu       sync.Mutex
	requests int
	response func(*http.Request, int) (*http.Response, error)
}

func (t *mapRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.requests++
	count := t.requests
	t.mu.Unlock()
	return t.response(request, count)
}

func (t *mapRoundTripper) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.requests
}

func mapProxyFixture(t *testing.T, lifecycle string, transport http.RoundTripper) (*ServerHandler, *query.ServerStatusCache) {
	t.Helper()
	store, err := database.NewStore(t.TempDir() + "/hogs.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.DB.Close() })
	server := &database.Server{
		Name: "cog", GameType: "minecraft", State: "online",
		MapURL:   "https://maps.example.test/base",
		Metadata: map[string]string{"map_lifecycle": lifecycle},
	}
	if err := store.CreateServer(server); err != nil {
		t.Fatal(err)
	}
	statusCache := query.NewServerStatusCache()
	handler := NewServerHandler(store, &config.Config{
		MapCacheDir: t.TempDir(), MapCacheMaxBytes: 1 << 20,
		MapCacheMaxItemBytes: 1 << 18, MapCacheDefaultTTL: 60,
		MapCacheStaleTTL: 3600,
	}, statusCache, nil)
	handler.MapTransport = transport
	return handler, statusCache
}

func mapRequest(path string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request = mux.SetURLVars(request, map[string]string{"serverName": "cog"})
	return request
}

func mapResponse(status int, body, cacheControl string) *http.Response {
	header := make(http.Header)
	header.Set("Content-Type", "text/plain")
	if cacheControl != "" {
		header.Set("Cache-Control", cacheControl)
	}
	return &http.Response{
		StatusCode: status, Status: http.StatusText(status),
		Header: header, Body: io.NopCloser(strings.NewReader(body)),
	}
}

func TestMapProxyCachesAndServesRanges(t *testing.T) {
	transport := &mapRoundTripper{response: func(request *http.Request, _ int) (*http.Response, error) {
		if request.URL.String() != "https://maps.example.test/base/assets/tile.bin?v=1" {
			t.Fatalf("target URL = %s", request.URL)
		}
		if request.Header.Get("Cookie") != "" || request.Header.Get("Authorization") != "" {
			t.Fatal("HOGS credentials were forwarded to the map origin")
		}
		return mapResponse(http.StatusOK, "abcdef", "public, max-age=60"), nil
	}}
	handler, _ := mapProxyFixture(t, "game", transport)

	first := httptest.NewRecorder()
	request := mapRequest("/cog/map/assets/tile.bin?v=1")
	request.Header.Set("Cookie", "hogs_session=secret")
	request.Header.Set("Authorization", "Bearer secret")
	handler.MapProxy(first, request)
	if first.Code != http.StatusOK || first.Body.String() != "abcdef" {
		t.Fatalf("first response = %d %q", first.Code, first.Body.String())
	}
	if got := first.Header().Get("X-HOGS-Map-Cache"); got != "MISS" {
		t.Fatalf("first cache state = %q", got)
	}

	second := httptest.NewRecorder()
	rangeRequest := mapRequest("/cog/map/assets/tile.bin?v=1")
	rangeRequest.Header.Set("Range", "bytes=1-3")
	handler.MapProxy(second, rangeRequest)
	if second.Code != http.StatusPartialContent || second.Body.String() != "bcd" {
		t.Fatalf("range response = %d %q", second.Code, second.Body.String())
	}
	if got := second.Header().Get("X-HOGS-Map-Cache"); got != "HIT" {
		t.Fatalf("second cache state = %q", got)
	}
	if transport.count() != 1 {
		t.Fatalf("origin requests = %d, want 1", transport.count())
	}
}

func TestMapProxyServesStaleResponseOnOriginFailure(t *testing.T) {
	transport := &mapRoundTripper{response: func(_ *http.Request, count int) (*http.Response, error) {
		if count == 1 {
			return mapResponse(http.StatusOK, "cached map", "public, max-age=1"), nil
		}
		return mapResponse(http.StatusBadGateway, "bad gateway", "no-store"), nil
	}}
	handler, _ := mapProxyFixture(t, "game", transport)
	request := mapRequest("/cog/map/")
	first := httptest.NewRecorder()
	handler.MapProxy(first, request)

	key := mapCacheKey("https://maps.example.test/base/")
	entry, found := handler.MapCache.lookup(key)
	if !found {
		t.Fatal("cache entry was not stored")
	}
	entry.meta.FreshUntil = time.Now().Add(-time.Minute)
	_, metadataPath := handler.MapCache.paths(key)
	raw, err := json.Marshal(entry.meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, raw, 0600); err != nil {
		t.Fatal(err)
	}

	second := httptest.NewRecorder()
	handler.MapProxy(second, mapRequest("/cog/map/"))
	if second.Code != http.StatusOK || second.Body.String() != "cached map" {
		t.Fatalf("stale response = %d %q", second.Code, second.Body.String())
	}
	if got := second.Header().Get("X-HOGS-Map-Cache"); got != "STALE" {
		t.Fatalf("cache state = %q", got)
	}
	if second.Header().Get("Warning") == "" {
		t.Fatal("stale response has no Warning header")
	}
}

func TestMapProxyRevalidatesStaleResponse(t *testing.T) {
	transport := &mapRoundTripper{response: func(request *http.Request, count int) (*http.Response, error) {
		if count == 1 {
			response := mapResponse(http.StatusOK, "cached map", "public, max-age=1")
			response.Header.Set("ETag", `"map-v1"`)
			return response, nil
		}
		if request.Header.Get("If-None-Match") != `"map-v1"` {
			t.Fatalf("If-None-Match = %q", request.Header.Get("If-None-Match"))
		}
		return mapResponse(http.StatusNotModified, "", "public, max-age=60"), nil
	}}
	handler, _ := mapProxyFixture(t, "game", transport)
	handler.MapProxy(httptest.NewRecorder(), mapRequest("/cog/map/"))

	key := mapCacheKey("https://maps.example.test/base/")
	entry, found := handler.MapCache.lookup(key)
	if !found {
		t.Fatal("cache entry was not stored")
	}
	entry.meta.FreshUntil = time.Now().Add(-time.Minute)
	_, metadataPath := handler.MapCache.paths(key)
	raw, err := json.Marshal(entry.meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, raw, 0600); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	handler.MapProxy(recorder, mapRequest("/cog/map/"))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "cached map" {
		t.Fatalf("revalidated response = %d %q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("X-HOGS-Map-Cache"); got != "REVALIDATED" {
		t.Fatalf("cache state = %q", got)
	}
}

func TestMapProxyUnavailablePageUsesLifecycleAndGameStatus(t *testing.T) {
	for _, test := range []struct {
		name      string
		lifecycle string
		online    bool
		want      string
	}{
		{name: "game offline", lifecycle: "game", online: false, want: "game server is currently offline"},
		{name: "game online", lifecycle: "game", online: true, want: "game server is online"},
		{name: "independent", lifecycle: "independent", online: false, want: "runs independently"},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &mapRoundTripper{response: func(_ *http.Request, _ int) (*http.Response, error) {
				return mapResponse(http.StatusBadGateway, "", "no-store"), nil
			}}
			handler, statusCache := mapProxyFixture(t, test.lifecycle, transport)
			statusCache.Set("cog", &query.ServerStatus{Online: test.online, LastUpdated: time.Now()})
			request := mapRequest("/cog/map/")
			request.Header.Set("Accept", "text/html")
			recorder := httptest.NewRecorder()
			handler.MapProxy(recorder, request)
			if recorder.Code != http.StatusBadGateway {
				t.Fatalf("status = %d", recorder.Code)
			}
			if !strings.Contains(strings.ToLower(recorder.Body.String()), test.want) {
				t.Fatalf("body does not contain %q: %s", test.want, recorder.Body.String())
			}
			if recorder.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q", recorder.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestMapProxyDoesNotCacheNoStoreResponse(t *testing.T) {
	transport := &mapRoundTripper{response: func(_ *http.Request, _ int) (*http.Response, error) {
		return mapResponse(http.StatusOK, "live", "no-store"), nil
	}}
	handler, _ := mapProxyFixture(t, "game", transport)
	for range 2 {
		recorder := httptest.NewRecorder()
		handler.MapProxy(recorder, mapRequest("/cog/map/live.json"))
		if recorder.Header().Get("X-HOGS-Map-Cache") != "BYPASS" {
			t.Fatalf("cache state = %q", recorder.Header().Get("X-HOGS-Map-Cache"))
		}
	}
	if transport.count() != 2 {
		t.Fatalf("origin requests = %d, want 2", transport.count())
	}
}

func TestMapProxyPrivateOriginRequiresExactAllowlist(t *testing.T) {
	target, err := url.Parse("http://10.0.0.8:8100")
	if err != nil {
		t.Fatal(err)
	}
	if isMapURLAllowed(target, nil) {
		t.Fatal("private origin was allowed without an allowlist")
	}
	if !isMapURLAllowed(target, []string{"http://10.0.0.8:8100"}) {
		t.Fatal("exactly allowlisted private origin was rejected")
	}
	if isMapURLAllowed(target, []string{"http://10.0.0.8:8101"}) {
		t.Fatal("private origin with a different port was allowed")
	}
}
