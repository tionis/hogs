package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/tionis/hogs/access"
	"github.com/tionis/hogs/auth"
	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/modmanager"
	"github.com/tionis/hogs/query"
)

// ServerHandler holds dependencies for API handlers.
type ServerHandler struct {
	Store        *database.Store
	Config       *config.Config
	Cache        *query.ServerStatusCache
	Auth         *auth.Authenticator
	MapCache     *mapCache
	MapTransport http.RoundTripper
}

// NewServerHandler creates a new ServerHandler.
func NewServerHandler(store *database.Store, cfg *config.Config, cache *query.ServerStatusCache, auth *auth.Authenticator) *ServerHandler {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 20 * time.Second
	return &ServerHandler{
		Store: store, Config: cfg, Cache: cache, Auth: auth,
		MapCache: newMapCache(
			cfg.MapCacheDir, cfg.MapCacheMaxBytes, cfg.MapCacheMaxItemBytes,
			time.Duration(cfg.MapCacheDefaultTTL)*time.Second,
			time.Duration(cfg.MapCacheStaleTTL)*time.Second,
		),
		MapTransport: transport,
	}
}

// GetServers handles the API request to retrieve all servers.
func (h *ServerHandler) GetServers(w http.ResponseWriter, r *http.Request) {
	servers, err := h.Store.ListServers()
	if err != nil {
		log.Printf("Error fetching servers: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	user := userEnvFromRequest(h.Store, h.Auth, r)

	var public []interface{}
	for i := range servers {
		if !h.canViewServer(r, &servers[i]) {
			continue
		}
		if user.Role == "admin" || user.Role == "system" {
			public = append(public, servers[i])
		} else {
			public = append(public, servers[i].ToPublic())
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(public); err != nil {
		log.Printf("Error encoding servers to JSON: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// GetServerStatus handles the API request to retrieve the status of a specific Minecraft server.
func (h *ServerHandler) GetServerStatus(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	serverName := vars["serverName"]

	server, err := h.Store.GetServerByName(serverName)
	if err != nil {
		log.Printf("Error getting server %s from database: %v", serverName, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if server == nil || !h.canViewServer(r, server) || !h.canAccessServer(r, server, access.Status) {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	cachedStatus, cached := h.Cache.Get(serverName)
	// Agent observations intentionally contain only process and occupancy data.
	// A Minecraft result without protocol metadata still needs one modern status
	// query to populate its MOTD and real game version.
	needsMinecraftDetails := cached && server.GameType == "minecraft" && cachedStatus.Online && cachedStatus.Extras == nil
	if cached && !needsMinecraftDetails {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(cachedStatus); err != nil {
			log.Printf("Error encoding cached server status to JSON: %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
		return
	}
	if server.State != "online" && server.State != "auto" {
		stateStatus := &query.ServerStatus{
			Online:      false,
			LastUpdated: time.Now(),
			Error:       "Server is " + server.State + ".",
		}
		h.Cache.Set(serverName, stateStatus)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(stateStatus)
		return
	}

	if server.Address == "" {
		noAddrStatus := &query.ServerStatus{
			Online:      false,
			LastUpdated: time.Now(),
			Error:       "No address configured.",
		}
		h.Cache.Set(serverName, noAddrStatus)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(noAddrStatus)
		return
	}

	querier := query.NewQuerier(server.GameType)
	status, err := querier.Query(server)
	if err != nil {
		log.Printf("Error querying %s server %s (%s): %v", server.GameType, server.Name, server.Address, err)
		// Even if there's an error, the status object will contain error information.
		// We still cache it to avoid hammering the server.
	}
	if cached && cachedStatus.PlayersKnown && status.Online {
		status.Players = cachedStatus.Players
		status.MaxPlayers = cachedStatus.MaxPlayers
		status.PlayersKnown = true
	}

	h.Cache.Set(serverName, status) // Cache the new status

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(status); err != nil {
		log.Printf("Error encoding server status to JSON: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// GetServerMods handles the API request to retrieve the mod list for a specific server.
func (h *ServerHandler) GetServerMods(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	serverName := vars["serverName"]

	if !isValidServerName(serverName) {
		http.Error(w, "Invalid server name", http.StatusBadRequest)
		return
	}

	modTree, err := modmanager.ScanModDirectory(h.Config.GameDataPath, serverName)
	if err != nil {
		if strings.Contains(err.Error(), "not found") { // Check if directory doesn't exist
			http.Error(w, fmt.Sprintf("Mod directory for server %s not found", serverName), http.StatusNotFound)
		} else {
			log.Printf("Error scanning mod directory for server %s: %v", serverName, err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(modTree); err != nil {
		log.Printf("Error encoding mod tree to JSON for server %s: %v", serverName, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (h *ServerHandler) Healthz(w http.ResponseWriter, r *http.Request) {
	if err := h.Store.DB.Ping(); err != nil {
		log.Printf("Health check failed: database ping error: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "unhealthy",
			"checks": map[string]string{
				"database": "error",
			},
		})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "healthy",
		"checks": map[string]string{
			"database": "ok",
		},
	})
}

func (h *ServerHandler) GetServerMetrics(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	serverName := vars["serverName"]

	limitStr := r.URL.Query().Get("limit")
	limit := 100
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	metrics, err := h.Store.ListServerMetrics(serverName, limit)
	if err != nil {
		http.Error(w, "Failed to get metrics", http.StatusInternalServerError)
		return
	}
	if metrics == nil {
		metrics = []database.ServerMetric{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}

// MapProxy handles requests to proxy map instances (BlueMap for Minecraft, etc).
func (h *ServerHandler) MapProxy(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	serverName := vars["serverName"]

	server, err := h.Store.GetServerByName(serverName)
	if err != nil {
		log.Printf("Error getting server %s from database for map proxy: %v", serverName, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if server == nil {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}
	if !h.canViewServer(r, server) {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}
	if server.MapURL == "" {
		http.Error(w, "Map URL not configured for this server", http.StatusNotFound)
		return
	}

	targetURL, err := url.Parse(server.MapURL)
	if err != nil || targetURL.Host == "" || (targetURL.Scheme != "http" && targetURL.Scheme != "https") {
		log.Printf("Invalid map URL for server %s: %v", serverName, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if !isMapURLAllowed(targetURL, h.Config.MapProxyAllowedOrigins) {
		log.Printf("Blocked map proxy to private URL for server %s: %s", serverName, targetURL.String())
		http.Error(w, "Invalid map URL", http.StatusBadRequest)
		return
	}

	target := mapTargetURL(targetURL, r.URL, serverName)
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		h.serveCachedMap(w, r, server, target)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.Director = func(req *http.Request) {
		req.URL = mapTargetURL(targetURL, req.URL, serverName)
		req.Host = targetURL.Host
		req.Header.Del("Authorization")
		req.Header.Del("Cookie")
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		if response.StatusCode >= http.StatusInternalServerError {
			return fmt.Errorf("map origin returned %s", response.Status)
		}
		for _, header := range []string{"Content-Security-Policy", "Permissions-Policy", "Set-Cookie", "X-Frame-Options"} {
			response.Header.Del(header)
		}
		return nil
	}
	proxy.ErrorHandler = func(rw http.ResponseWriter, request *http.Request, proxyErr error) {
		log.Printf("Map origin unavailable for server %s: %v", serverName, proxyErr)
		h.writeMapUnavailable(rw, request, server)
	}
	proxy.ServeHTTP(w, r)
}

func (h *ServerHandler) canViewServer(r *http.Request, server *database.Server) bool {
	return h.canAccessServer(r, server, access.View)
}

func (h *ServerHandler) canAccessServer(r *http.Request, server *database.Server, capability string) bool {
	if server == nil {
		return false
	}
	user := userEnvFromRequest(h.Store, h.Auth, r)
	if user.Role == "admin" || user.Role == "system" {
		return true
	}
	decision, err := h.Store.EvaluateServerAccess(server.ID, user.Email, user.Groups, capability)
	return err == nil && decision.Allowed
}

func mapTargetURL(base, request *url.URL, serverName string) *url.URL {
	target := *base
	prefix := "/" + serverName + "/map"
	relative := strings.TrimPrefix(request.Path, prefix)
	if relative == "" {
		relative = "/"
	}
	target.Path = strings.TrimRight(base.Path, "/") + "/" + strings.TrimLeft(relative, "/")
	if target.Path == "" {
		target.Path = "/"
	}
	target.RawPath = ""
	target.RawQuery = request.RawQuery
	if base.RawQuery != "" {
		if target.RawQuery != "" {
			target.RawQuery = base.RawQuery + "&" + target.RawQuery
		} else {
			target.RawQuery = base.RawQuery
		}
	}
	return &target
}

func (h *ServerHandler) serveCachedMap(w http.ResponseWriter, r *http.Request, server *database.Server, target *url.URL) {
	key := mapCacheKey(target.String())
	if entry, found := h.MapCache.lookup(key); found && entry.fresh {
		if err := h.MapCache.serve(w, r, entry, "HIT"); err == nil {
			return
		}
		h.MapCache.remove(key)
	}

	release := h.MapCache.acquire(key)
	defer release()
	if entry, found := h.MapCache.lookup(key); found && entry.fresh {
		if err := h.MapCache.serve(w, r, entry, "HIT"); err == nil {
			return
		}
		h.MapCache.remove(key)
	}
	stale, hasStale := h.MapCache.lookup(key)

	outbound := r.Clone(r.Context())
	outbound.URL = target
	outbound.RequestURI = ""
	outbound.Host = target.Host
	outbound.Header = r.Header.Clone()
	outbound.Header.Del("Authorization")
	outbound.Header.Del("Cookie")
	outbound.Header.Del("Accept-Encoding")
	if hasStale {
		if outbound.Header.Get("If-None-Match") == "" {
			if etag := stale.meta.Header.Get("ETag"); etag != "" {
				outbound.Header.Set("If-None-Match", etag)
			}
		}
		if outbound.Header.Get("If-Modified-Since") == "" {
			if modified := stale.meta.Header.Get("Last-Modified"); modified != "" {
				outbound.Header.Set("If-Modified-Since", modified)
			}
		}
	}
	response, err := h.MapTransport.RoundTrip(outbound)
	if err != nil {
		log.Printf("Map origin request failed for server %s: %v", server.Name, err)
		if hasStale && h.MapCache.serve(w, r, stale, "STALE") == nil {
			return
		}
		h.writeMapUnavailable(w, r, server)
		return
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified && hasStale {
		ttl, cacheable := mapCacheTTL(response.Header, h.MapCache.defaultTTL)
		if !cacheable {
			ttl, cacheable = mapCacheTTL(stale.meta.Header, h.MapCache.defaultTTL)
		}
		if cacheable {
			stale.meta.FreshUntil = time.Now().UTC().Add(ttl)
			for _, header := range []string{"Cache-Control", "ETag", "Expires", "Last-Modified"} {
				if value := response.Header.Get(header); value != "" {
					stale.meta.Header.Set(header, value)
				}
			}
			if err := h.MapCache.writeMetadata(key, stale.meta); err != nil {
				log.Printf("Refresh map cache metadata for server %s: %v", server.Name, err)
			}
			stale.fresh = true
			if h.MapCache.serve(w, r, stale, "REVALIDATED") == nil {
				return
			}
		}
	}
	if response.StatusCode >= http.StatusInternalServerError {
		log.Printf("Map origin returned %s for server %s", response.Status, server.Name)
		if hasStale && h.MapCache.serve(w, r, stale, "STALE") == nil {
			return
		}
		h.writeMapUnavailable(w, r, server)
		return
	}

	copyMapResponseHeaders(w.Header(), response.Header)
	ttl, cacheable := cacheableMapResponse(
		outbound, response, h.MapCache.maxItemBytes, h.MapCache.defaultTTL,
	)
	cacheState := "BYPASS"
	var writer *mapCacheWriter
	if cacheable {
		writer, err = h.MapCache.begin(key)
		if err == nil {
			cacheState = "MISS"
			if w.Header().Get("Cache-Control") == "" {
				w.Header().Set("Cache-Control", "public, max-age="+strconv.FormatInt(int64(ttl.Seconds()), 10))
			}
		} else {
			log.Printf("Map cache unavailable for server %s: %v", server.Name, err)
		}
	}
	w.Header().Set("X-HOGS-Map-Cache", cacheState)
	w.WriteHeader(response.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	if err := copyMapBody(w, response.Body, writer); err != nil {
		if writer != nil {
			writer.abort()
		}
		return
	}
	if writer != nil {
		headers := w.Header().Clone()
		headers.Del("Content-Length")
		headers.Del("X-Hogs-Map-Cache")
		now := time.Now().UTC()
		if err := writer.commit(mapCacheMetadata{
			Status: response.StatusCode, Header: headers,
			StoredAt: now, FreshUntil: now.Add(ttl),
		}); err != nil && !writer.exceeded {
			log.Printf("Store map cache response for server %s: %v", server.Name, err)
		}
	}
}

var mapUnavailableTemplate = template.Must(template.New("map-unavailable").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Map temporarily unavailable</title>
  <style>
    :root { color-scheme: light dark; font-family: system-ui, sans-serif; }
    body { margin: 0; min-height: 100vh; display: grid; place-items: center; background: #111827; color: #f3f4f6; }
    main { width: min(34rem, calc(100% - 2rem)); padding: 2rem; border: 1px solid #374151; border-radius: 1rem; background: #1f2937; box-shadow: 0 1rem 3rem #0006; }
    h1 { margin-top: 0; font-size: 1.5rem; }
    p { color: #d1d5db; line-height: 1.55; }
    .actions { display: flex; gap: .75rem; flex-wrap: wrap; margin-top: 1.5rem; }
    a, button { border: 1px solid #60a5fa; border-radius: .5rem; padding: .65rem 1rem; background: #2563eb; color: white; text-decoration: none; cursor: pointer; font: inherit; }
    a { background: transparent; }
  </style>
</head>
<body>
  <main>
    <h1>{{.ServerName}} map is temporarily unavailable</h1>
    <p>{{.Explanation}}</p>
    <p>HOGS will continue retrying the map service. No cached copy was available for this request.</p>
    <div class="actions">
      <button type="button" onclick="location.reload()">Try again</button>
      <a href="/{{.ServerPath}}">Back to server</a>
    </div>
  </main>
</body>
</html>`))

func (h *ServerHandler) writeMapUnavailable(w http.ResponseWriter, r *http.Request, server *database.Server) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Retry-After", "15")
	w.Header().Set("X-HOGS-Map-Cache", "ERROR")
	if !mapRequestWantsHTML(r) {
		http.Error(w, "Map service temporarily unavailable", http.StatusBadGateway)
		return
	}
	explanation := "The map service is not responding. It may still be starting or updating."
	lifecycle := server.MapLifecycle()
	status, known := h.Cache.Latest(server.Name)
	if lifecycle == "independent" {
		explanation = "This map runs independently from the game server, and its map service is not responding."
	} else if known && !status.Online {
		explanation = "The game server is currently offline. This map is configured to run with the game server and should return after it starts."
	} else if known && status.Online {
		explanation = "The game server is online, but its map service is not responding. The map may still be starting or rendering."
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	_ = mapUnavailableTemplate.Execute(w, map[string]string{
		"ServerName": server.Name, "ServerPath": url.PathEscape(server.Name),
		"Explanation": explanation,
	})
}

func mapRequestWantsHTML(r *http.Request) bool {
	if strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/html") {
		return true
	}
	return strings.HasSuffix(r.URL.Path, "/") || strings.HasSuffix(strings.ToLower(r.URL.Path), ".html")
}

// ServeModFiles serves static files from the mod directory for a given server.
func (h *ServerHandler) ServeModFiles(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	serverName := vars["serverName"]

	if !isValidServerName(serverName) {
		http.Error(w, "Invalid server name", http.StatusBadRequest)
		return
	}

	// Construct the base directory for the server's mods using config
	modBaseDir := filepath.Join(h.Config.GameDataPath, serverName)

	// Create a file server for the constructed directory
	// http.StripPrefix is needed to remove the part of the URL path that gorilla/mux matched.
	http.StripPrefix(fmt.Sprintf("/files/%s/mods", serverName), http.FileServer(http.Dir(modBaseDir))).ServeHTTP(w, r)
}

// isValidServerName checks if the server name is safe to use in file paths.
func isValidServerName(name string) bool {
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func isMapURLAllowed(target *url.URL, allowedOrigins []string) bool {
	for _, rawOrigin := range allowedOrigins {
		origin, err := url.Parse(rawOrigin)
		if err == nil && strings.EqualFold(origin.Scheme, target.Scheme) &&
			strings.EqualFold(origin.Host, target.Host) {
			return true
		}
	}
	return !isPrivateURL(target.String())
}

// isPrivateURL checks if a URL points to a private/internal address.
func isPrivateURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return true
	}
	host := u.Hostname()
	if host == "" {
		return true
	}
	if strings.ToLower(host) == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	ip := net.ParseIP(host)
	if ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
	}
	return false
}

func (h *ServerHandler) GetBackground(w http.ResponseWriter, r *http.Request) {
	var tags []string
	if t := r.URL.Query().Get("theme"); t != "" {
		tags = append(tags, t)
	}
	if g := r.URL.Query().Get("game"); g != "" {
		tags = append(tags, g)
	}
	if len(tags) == 0 {
		tags = []string{"home", "dark"}
	}

	bg, err := h.Store.GetRandomBackground(tags)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if bg == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"background": nil})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"background": map[string]interface{}{
			"id":   bg.ID,
			"url":  bg.URL(),
			"tags": bg.Tags,
		},
	})
}

func (h *ServerHandler) ServeBackgroundFile(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	filename := vars["filename"]
	bgDir := filepath.Join(h.Config.GameDataPath, "backgrounds")

	// Prevent path traversal
	target := filepath.Join(bgDir, filename)
	cleanTarget := filepath.Clean(target)
	cleanDir := filepath.Clean(bgDir)
	if !strings.HasPrefix(cleanTarget, cleanDir+string(filepath.Separator)) && cleanTarget != cleanDir {
		http.Error(w, "Invalid filename", http.StatusBadRequest)
		return
	}

	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, cleanTarget)
}

func (h *ServerHandler) UploadBackground(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Missing file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	allowedExts := map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".svg": true}
	ext := strings.ToLower(filepath.Ext(header.Filename))
	if !allowedExts[ext] {
		http.Error(w, "Only image files are allowed", http.StatusBadRequest)
		return
	}

	fileData, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "Failed to read file", http.StatusInternalServerError)
		return
	}

	if len(fileData) > 32<<20 {
		http.Error(w, "File too large", http.StatusBadRequest)
		return
	}

	hash := sha256.Sum256(fileData)
	contentHash := hex.EncodeToString(hash[:])[:16]

	tags := r.Form["tags"]

	bgDir := filepath.Join(h.Config.GameDataPath, "backgrounds")
	if err := os.MkdirAll(bgDir, 0755); err != nil {
		http.Error(w, "Failed to create backgrounds directory", http.StatusInternalServerError)
		return
	}

	filename := fmt.Sprintf("%d%s", time.Now().UnixNano(), ext)
	dst := filepath.Join(bgDir, filename)

	if err := os.WriteFile(dst, fileData, 0644); err != nil {
		http.Error(w, "Failed to save file", http.StatusInternalServerError)
		return
	}

	bg := &database.Background{
		Filename:    filename,
		ContentHash: contentHash,
		Tags:        tags,
	}

	if err := h.Store.CreateBackground(bg); err != nil {
		os.Remove(dst)
		http.Error(w, "Failed to save background metadata", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/backgrounds", http.StatusFound)
}

func (h *ServerHandler) DeleteBackground(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	idStr := r.FormValue("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	bgs, err := h.Store.ListBackgrounds()
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	var filename string
	for _, bg := range bgs {
		if bg.ID == id {
			filename = bg.Filename
			break
		}
	}

	if err := h.Store.DeleteBackground(id); err != nil {
		http.Error(w, "Failed to delete background", http.StatusInternalServerError)
		return
	}

	if filename != "" {
		os.Remove(filepath.Join(h.Config.GameDataPath, "backgrounds", filename))
	}

	http.Redirect(w, r, "/admin/backgrounds", http.StatusFound)
}

func (h *ServerHandler) BulkUpdateBackgrounds(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	ids := r.Form["id"]
	for _, idStr := range ids {
		id, err := strconv.Atoi(idStr)
		if err != nil {
			continue
		}
		enabled := r.FormValue("enabled_"+idStr) == "on"
		tagKey := "tags_" + idStr
		tags := r.Form[tagKey]

		bg := &database.Background{
			ID:      id,
			Enabled: enabled,
			Tags:    tags,
		}

		if err := h.Store.UpdateBackground(bg); err != nil {
			http.Error(w, "Failed to update background", http.StatusInternalServerError)
			return
		}
	}

	http.Redirect(w, r, "/admin/backgrounds", http.StatusFound)
}
