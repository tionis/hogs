package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type mapCache struct {
	dir          string
	maxBytes     int64
	maxItemBytes int64
	defaultTTL   time.Duration
	staleTTL     time.Duration

	locksMu sync.Mutex
	locks   map[string]*mapCacheLock
	pruneMu sync.Mutex
}

type mapCacheLock struct {
	mu   sync.Mutex
	refs int
}

type mapCacheMetadata struct {
	Status     int         `json:"status"`
	Header     http.Header `json:"header"`
	StoredAt   time.Time   `json:"storedAt"`
	FreshUntil time.Time   `json:"freshUntil"`
	Size       int64       `json:"size"`
}

type mapCacheEntry struct {
	bodyPath string
	meta     mapCacheMetadata
	fresh    bool
}

func newMapCache(dir string, maxBytes, maxItemBytes int64, defaultTTL, staleTTL time.Duration) *mapCache {
	return &mapCache{
		dir: dir, maxBytes: maxBytes, maxItemBytes: maxItemBytes,
		defaultTTL: defaultTTL, staleTTL: staleTTL, locks: make(map[string]*mapCacheLock),
	}
}

func (c *mapCache) enabled() bool {
	return c != nil && c.dir != "" && c.maxBytes > 0 && c.maxItemBytes > 0
}

func mapCacheKey(target string) string {
	sum := sha256.Sum256([]byte(target))
	return hex.EncodeToString(sum[:])
}

func (c *mapCache) paths(key string) (string, string) {
	return filepath.Join(c.dir, key+".body"), filepath.Join(c.dir, key+".json")
}

func (c *mapCache) lookup(key string) (*mapCacheEntry, bool) {
	if !c.enabled() {
		return nil, false
	}
	bodyPath, metadataPath := c.paths(key)
	raw, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil, false
	}
	var metadata mapCacheMetadata
	if json.Unmarshal(raw, &metadata) != nil || metadata.Status != http.StatusOK ||
		metadata.StoredAt.IsZero() || metadata.Size < 0 {
		c.remove(key)
		return nil, false
	}
	info, err := os.Stat(bodyPath)
	if err != nil || info.Size() != metadata.Size {
		c.remove(key)
		return nil, false
	}
	if time.Now().After(metadata.FreshUntil.Add(c.staleTTL)) {
		c.remove(key)
		return nil, false
	}
	return &mapCacheEntry{
		bodyPath: bodyPath,
		meta:     metadata,
		fresh:    time.Now().Before(metadata.FreshUntil),
	}, true
}

func (c *mapCache) serve(w http.ResponseWriter, r *http.Request, entry *mapCacheEntry, state string) error {
	file, err := os.Open(entry.bodyPath)
	if err != nil {
		return err
	}
	defer file.Close()
	for name, values := range entry.meta.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	age := int64(time.Since(entry.meta.StoredAt).Seconds())
	if age < 0 {
		age = 0
	}
	w.Header().Set("Age", strconv.FormatInt(age, 10))
	w.Header().Set("X-HOGS-Map-Cache", state)
	if state == "STALE" {
		w.Header().Add("Warning", `110 HOGS "Response is stale because the map origin is unavailable"`)
	}
	_ = os.Chtimes(entry.bodyPath, time.Now(), time.Now())
	http.ServeContent(w, r, filepath.Base(entry.bodyPath), entry.meta.StoredAt, file)
	return nil
}

func (c *mapCache) acquire(key string) func() {
	c.locksMu.Lock()
	lock := c.locks[key]
	if lock == nil {
		lock = &mapCacheLock{}
		c.locks[key] = lock
	}
	lock.refs++
	c.locksMu.Unlock()
	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		c.locksMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(c.locks, key)
		}
		c.locksMu.Unlock()
	}
}

func (c *mapCache) begin(key string) (*mapCacheWriter, error) {
	if !c.enabled() {
		return nil, errors.New("map cache is disabled")
	}
	if err := os.MkdirAll(c.dir, 0700); err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(c.dir, "."+key+".body-*")
	if err != nil {
		return nil, err
	}
	return &mapCacheWriter{cache: c, key: key, file: file, tempPath: file.Name()}, nil
}

type mapCacheWriter struct {
	cache    *mapCache
	key      string
	file     *os.File
	tempPath string
	size     int64
	exceeded bool
}

func (w *mapCacheWriter) Write(p []byte) (int, error) {
	if w.exceeded {
		return len(p), nil
	}
	if w.size+int64(len(p)) > w.cache.maxItemBytes {
		w.exceeded = true
		_ = w.file.Close()
		_ = os.Remove(w.tempPath)
		return len(p), nil
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *mapCacheWriter) abort() {
	if w == nil {
		return
	}
	_ = w.file.Close()
	_ = os.Remove(w.tempPath)
}

func (w *mapCacheWriter) commit(metadata mapCacheMetadata) error {
	if w == nil || w.exceeded {
		w.abort()
		return errors.New("map response exceeds cache item limit")
	}
	if err := w.file.Sync(); err != nil {
		w.abort()
		return err
	}
	if err := w.file.Close(); err != nil {
		w.abort()
		return err
	}
	bodyPath, _ := w.cache.paths(w.key)
	if err := os.Rename(w.tempPath, bodyPath); err != nil {
		w.abort()
		return err
	}
	metadata.Size = w.size
	if err := w.cache.writeMetadata(w.key, metadata); err != nil {
		w.cache.remove(w.key)
		return err
	}
	go w.cache.prune()
	return nil
}

func (c *mapCache) writeMetadata(key string, metadata mapCacheMetadata) error {
	raw, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	_, metadataPath := c.paths(key)
	metaTemp, err := os.CreateTemp(c.dir, "."+key+".json-*")
	if err != nil {
		return err
	}
	metaTempPath := metaTemp.Name()
	defer os.Remove(metaTempPath)
	if _, err = metaTemp.Write(raw); err == nil {
		err = metaTemp.Sync()
	}
	if closeErr := metaTemp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(metaTempPath, metadataPath)
	}
	return err
}

func (c *mapCache) remove(key string) {
	if c == nil {
		return
	}
	bodyPath, metadataPath := c.paths(key)
	_ = os.Remove(bodyPath)
	_ = os.Remove(metadataPath)
}

func (c *mapCache) prune() {
	if !c.enabled() {
		return
	}
	c.pruneMu.Lock()
	defer c.pruneMu.Unlock()
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	type body struct {
		key     string
		size    int64
		touched time.Time
	}
	var bodies []body
	var total int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".body") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		key := strings.TrimSuffix(entry.Name(), ".body")
		bodies = append(bodies, body{key: key, size: info.Size(), touched: info.ModTime()})
		total += info.Size()
	}
	if total <= c.maxBytes {
		return
	}
	sort.Slice(bodies, func(i, j int) bool { return bodies[i].touched.Before(bodies[j].touched) })
	for _, entry := range bodies {
		c.remove(entry.key)
		total -= entry.size
		if total <= c.maxBytes {
			return
		}
	}
}

func mapCacheTTL(header http.Header, fallback time.Duration) (time.Duration, bool) {
	directives := strings.Split(strings.ToLower(header.Get("Cache-Control")), ",")
	for _, raw := range directives {
		directive := strings.TrimSpace(raw)
		if directive == "no-store" || directive == "private" || directive == "no-cache" {
			return 0, false
		}
		for _, name := range []string{"s-maxage=", "max-age="} {
			if strings.HasPrefix(directive, name) {
				seconds, err := strconv.ParseInt(strings.TrimPrefix(directive, name), 10, 64)
				if err != nil || seconds <= 0 {
					return 0, false
				}
				return time.Duration(seconds) * time.Second, true
			}
		}
	}
	if expires, err := http.ParseTime(header.Get("Expires")); err == nil {
		if ttl := time.Until(expires); ttl > 0 {
			return ttl, true
		}
	}
	return fallback, fallback > 0
}

func cacheableMapResponse(request *http.Request, response *http.Response, maxItemBytes int64, fallback time.Duration) (time.Duration, bool) {
	if request.Method != http.MethodGet || request.Header.Get("Range") != "" ||
		response.StatusCode != http.StatusOK || response.Header.Get("Set-Cookie") != "" ||
		response.Header.Get("Vary") != "" {
		return 0, false
	}
	if response.ContentLength > maxItemBytes {
		return 0, false
	}
	return mapCacheTTL(response.Header, fallback)
}

func copyMapResponseHeaders(destination, source http.Header) {
	for name, values := range source {
		switch http.CanonicalHeaderKey(name) {
		case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
			"Te", "Trailer", "Transfer-Encoding", "Upgrade",
			"Content-Security-Policy", "Permissions-Policy", "Set-Cookie", "X-Frame-Options":
			continue
		}
		for _, value := range values {
			destination.Add(name, value)
		}
	}
}

func copyMapBody(destination io.Writer, source io.Reader, cacheWriter *mapCacheWriter) error {
	if cacheWriter == nil {
		_, err := io.Copy(destination, source)
		return err
	}
	_, err := io.Copy(io.MultiWriter(destination, cacheWriter), source)
	return err
}
