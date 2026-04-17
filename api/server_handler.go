package api

import (
	"encoding/json"
	"fmt"
	"github.com/tionis/hogs/auth"
	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/modmanager"
	"github.com/tionis/hogs/query"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/mux"
)

// ServerHandler holds dependencies for API handlers.
type ServerHandler struct {
	Store  *database.Store
	Config *config.Config
	Cache  *query.ServerStatusCache
	Auth   *auth.Authenticator
}

// NewServerHandler creates a new ServerHandler.
func NewServerHandler(store *database.Store, cfg *config.Config, cache *query.ServerStatusCache, auth *auth.Authenticator) *ServerHandler {
	return &ServerHandler{
		Store:  store,
		Config: cfg,
		Cache:  cache,
		Auth:   auth,
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

	isAdmin := h.Auth != nil && h.Auth.IsAuthenticated(r)

	var public []interface{}
	for i := range servers {
		if isAdmin {
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

	// Check cache first
	if cachedStatus, found := h.Cache.Get(serverName); found {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(cachedStatus); err != nil {
			log.Printf("Error encoding cached server status to JSON: %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
		return
	}

	server, err := h.Store.GetServerByName(serverName)
	if err != nil {
		log.Printf("Error getting server %s from database: %v", serverName, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if server == nil {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	// If server is disabled, we return an offline status
	if server.State == "offline" {
		offlineStatus := &query.ServerStatus{
			Online:      false,
			LastUpdated: time.Now(),
			Error:       "Server is currently disabled.",
		}
		h.Cache.Set(serverName, offlineStatus) // Cache disabled status
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(offlineStatus); err != nil {
			log.Printf("Error encoding offline status to JSON: %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
		return
	}

	querier := query.NewQuerier(server.GameType)
	status, err := querier.Query(server)
	if err != nil {
		log.Printf("Error querying %s server %s (%s): %v", server.GameType, server.Name, server.Address, err)
		// Even if there's an error, the status object will contain error information.
		// We still cache it to avoid hammering the server.
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
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"status": "unhealthy", "error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
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
	if server.MapURL == "" {
		http.Error(w, "Map URL not configured for this server", http.StatusNotFound)
		return
	}

	targetURL, err := url.Parse(server.MapURL)
	if err != nil {
		log.Printf("Invalid map URL for server %s: %v", serverName, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	// Custom director to rewrite the request to the target
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		// Set the Host header to the target host
		req.Host = targetURL.Host
		req.URL.Scheme = targetURL.Scheme
		req.URL.Host = targetURL.Host

		// Rewrite the path to remove the /<serverName>/map prefix
		prefix := fmt.Sprintf("/%s/map", serverName)
		req.URL.Path = strings.TrimPrefix(req.URL.Path, prefix)
		req.URL.RawPath = strings.TrimPrefix(req.URL.RawPath, prefix)
		if req.URL.Path == "" {
			req.URL.Path = "/"
			req.URL.RawPath = "/"
		}
	}

	proxy.ServeHTTP(w, r)
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
	// Simple validation: alphanumeric, hyphens, underscores only.
	// Prevents ".." and other malicious path components.
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
