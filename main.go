package main

import (
	"context"
	"path/filepath"

	"github.com/tionis/hogs/api"
	"github.com/tionis/hogs/auth"
	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/query"
	"github.com/tionis/hogs/web"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/mux"
)

func main() {
	cfg := config.LoadConfig()

	store, err := database.NewStore(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("could not initialize database: %s\n", err)
	}

	bgDir := filepath.Join(cfg.GameDataPath, "backgrounds")
	if err := store.ComputeMissingHashes(bgDir); err != nil {
		log.Printf("Warning: failed to compute missing background hashes: %v", err)
	}

	cache := query.NewServerStatusCache()

	authenticator, err := auth.NewAuthenticator(cfg)
	if err != nil {
		log.Printf("Warning: OIDC authentication could not be initialized: %v", err)
	} else if authenticator == nil {
		log.Println("OIDC authentication is disabled (not configured).")
	} else {
		log.Println("OIDC authentication initialized.")
	}

	serverHandler := api.NewServerHandler(store, cfg, cache, authenticator)
	webHandler := web.NewWebHandler(store, cfg, authenticator)

	router := mux.NewRouter()

	router.HandleFunc("/", webHandler.Home).Methods("GET")

	if authenticator != nil {
		router.HandleFunc("/login", authenticator.HandleLogin).Methods("GET")
		router.HandleFunc("/logout", authenticator.HandleLogout).Methods("GET")
		router.HandleFunc("/auth/callback", authenticator.HandleCallback).Methods("GET")

		router.Handle("/admin", authenticator.Middleware(http.HandlerFunc(webHandler.Admin))).Methods("GET")
		router.Handle("/admin/servers/add", authenticator.Middleware(http.HandlerFunc(webHandler.HandleServerCreate))).Methods("POST")
		router.Handle("/admin/servers/update", authenticator.Middleware(http.HandlerFunc(webHandler.HandleServerUpdate))).Methods("POST")
		router.Handle("/admin/servers/delete", authenticator.Middleware(http.HandlerFunc(webHandler.HandleServerDelete))).Methods("POST")

		router.Handle("/admin/files/{serverName}", authenticator.Middleware(http.HandlerFunc(webHandler.FileManager))).Methods("GET")
		router.Handle("/admin/files/upload", authenticator.Middleware(http.HandlerFunc(webHandler.HandleFileUpload))).Methods("POST")
		router.Handle("/admin/files/delete", authenticator.Middleware(http.HandlerFunc(webHandler.HandleFileDelete))).Methods("POST")
		router.Handle("/admin/files/mkdir", authenticator.Middleware(http.HandlerFunc(webHandler.HandleMkdir))).Methods("POST")
	} else {
		router.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "Authentication is not configured", http.StatusServiceUnavailable)
		}).Methods("GET")
		router.HandleFunc("/logout", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/", http.StatusFound)
		}).Methods("GET")
		router.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "Authentication is not configured", http.StatusServiceUnavailable)
		}).Methods("GET")
	}

	router.HandleFunc("/api/servers", serverHandler.GetServers).Methods("GET")
	router.HandleFunc("/api/servers/{serverName}/status", serverHandler.GetServerStatus).Methods("GET")
	router.HandleFunc("/api/servers/{serverName}/mods", serverHandler.GetServerMods).Methods("GET")
	router.HandleFunc("/api/backgrounds", serverHandler.GetBackground).Methods("GET")
	router.HandleFunc("/backgrounds/{contentHash}/{filename}", serverHandler.ServeBackgroundFile).Methods("GET")
	router.HandleFunc("/healthz", serverHandler.Healthz).Methods("GET")

	if authenticator != nil {
		router.Handle("/admin/backgrounds", authenticator.Middleware(http.HandlerFunc(webHandler.BackgroundManager))).Methods("GET")
		router.Handle("/admin/backgrounds/upload", authenticator.Middleware(http.HandlerFunc(serverHandler.UploadBackground))).Methods("POST")
		router.Handle("/admin/backgrounds/update", authenticator.Middleware(http.HandlerFunc(serverHandler.UpdateBackground))).Methods("POST")
		router.Handle("/admin/backgrounds/delete", authenticator.Middleware(http.HandlerFunc(serverHandler.DeleteBackground))).Methods("POST")
		router.Handle("/admin/settings", authenticator.Middleware(http.HandlerFunc(webHandler.Settings))).Methods("GET", "POST")
	}
	router.PathPrefix("/{serverName}/map/").HandlerFunc(serverHandler.MapProxy)
	router.PathPrefix("/files/{serverName}/mods/").Handler(http.HandlerFunc(serverHandler.ServeModFiles))
	router.PathPrefix("/assets/").Handler(http.HandlerFunc(webHandler.ServeAssets))

	router.HandleFunc("/{serverName}", webHandler.ServerDetail).Methods("GET")

	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: router,
	}

	go func() {
		log.Printf("Starting server on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("could not start server: %s\n", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced to shutdown: %s\n", err)
	}

	log.Println("Server exited gracefully.")
}
