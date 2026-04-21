package main

import (
	"context"
	"path/filepath"

	"github.com/tionis/hogs/agent"
	"github.com/tionis/hogs/api"
	"github.com/tionis/hogs/auth"
	"github.com/tionis/hogs/config"
	hogscron "github.com/tionis/hogs/cron"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/engine"
	"github.com/tionis/hogs/query"
	"github.com/tionis/hogs/scim"
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

	eng := engine.NewEngine(store, cfg, cache)

	authenticator, err := auth.NewAuthenticator(cfg, store)
	if err != nil {
		log.Printf("Warning: OIDC authentication could not be initialized: %v", err)
	} else if authenticator == nil {
		log.Println("OIDC authentication is disabled (not configured).")
	} else {
		log.Println("OIDC authentication initialized.")
	}

	if authenticator != nil {
		go func() {
			authenticator.CleanupSessions()
			ticker := time.NewTicker(15 * time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				authenticator.CleanupSessions()
			}
		}()
	}

	go func() {
		store.CleanupAuditLog(cfg.AuditLogRetentionDays)
		store.CleanupServerMetrics(cfg.MetricsRetentionDays)
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			store.CleanupAuditLog(cfg.AuditLogRetentionDays)
			store.CleanupServerMetrics(cfg.MetricsRetentionDays)
		}
	}()

	serverHandler := api.NewServerHandler(store, cfg, cache, authenticator)
	webHandler := web.NewWebHandler(store, cfg, authenticator, eng)

	var agentHub *agent.Hub
	var agentHandler *api.AgentHandler
	var agentService *agent.AgentService
	if cfg.AgentEnabled {
		agentHub = agent.NewHub(store, cfg)
		agentService = agent.NewAgentService(store, agentHub)
		agentHandler = api.NewAgentHandler(store, agentService)
		log.Println("Agent WebSocket endpoint enabled at /agent/ws")
	}

	pteroHandler := api.NewPterodactylHandler(store, cfg, eng, agentHub)
	automationHandler := api.NewAutomationHandler(store, cfg, eng)

	var scimHandler *scim.Handler
	if cfg.SCIMEnabled && cfg.SCIMBearerToken != "" {
		scimHandler = scim.NewHandler(store, cfg, authenticator)
		log.Println("SCIM 2.0 endpoint enabled at /scim/v2/")
	}

	var scheduler *hogscron.Scheduler
	if cfg.CronEnabled {
		scheduler = hogscron.NewScheduler(store, eng)
		if err := scheduler.Start(); err != nil {
			log.Printf("Warning: cron scheduler failed to start: %v", err)
		}
	}

	loginLimiter := api.NewRateLimiter(cfg.RateLimitLogin, time.Minute)
	apiLimiter := api.NewRateLimiter(cfg.RateLimitAPI, time.Minute)
	scimLimiter := api.NewRateLimiter(cfg.RateLimitSCIM, time.Minute)

	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			loginLimiter.Cleanup()
			apiLimiter.Cleanup()
			scimLimiter.Cleanup()
		}
	}()

	router := mux.NewRouter()

	csrfSecret := cfg.SessionSecret
	csrfExemptPrefixes := []string{"/agent/ws", "/scim/v2", "/auth/callback", "/auth/backchannel-logout", "/api/"}
	csrfRouter := auth.CSRFMiddleware(csrfSecret, csrfExemptPrefixes, router)

	router.HandleFunc("/", webHandler.Home).Methods("GET")

	if authenticator != nil {
		router.Handle("/login", loginLimiter.Middleware(http.HandlerFunc(authenticator.HandleLogin))).Methods("GET")
		router.HandleFunc("/logout", authenticator.HandleLogout).Methods("GET")
		router.HandleFunc("/auth/callback", authenticator.HandleCallback).Methods("GET")
		router.HandleFunc("/auth/backchannel-logout", authenticator.HandleBackChannelLogout).Methods("POST")

		router.Handle("/admin", authenticator.RequireRole("admin")(http.HandlerFunc(webHandler.Admin))).Methods("GET")
		router.Handle("/admin/servers/{id}", authenticator.RequireRole("admin")(http.HandlerFunc(webHandler.ServerEdit))).Methods("GET")
		router.Handle("/admin/servers/add", authenticator.RequireRole("admin")(http.HandlerFunc(webHandler.HandleServerCreate))).Methods("POST")
		router.Handle("/admin/servers/update", authenticator.RequireRole("admin")(http.HandlerFunc(webHandler.HandleServerUpdate))).Methods("POST")
		router.Handle("/admin/servers/delete", authenticator.RequireRole("admin")(http.HandlerFunc(webHandler.HandleServerDelete))).Methods("POST")

		router.Handle("/admin/files/{serverName}", authenticator.RequireRole("admin")(http.HandlerFunc(webHandler.FileManager))).Methods("GET")
		router.Handle("/admin/files/upload", authenticator.RequireRole("admin")(http.HandlerFunc(webHandler.HandleFileUpload))).Methods("POST")
		router.Handle("/admin/files/delete", authenticator.RequireRole("admin")(http.HandlerFunc(webHandler.HandleFileDelete))).Methods("POST")
		router.Handle("/admin/files/mkdir", authenticator.RequireRole("admin")(http.HandlerFunc(webHandler.HandleMkdir))).Methods("POST")
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

	router.Handle("/api/servers", apiLimiter.Middleware(http.HandlerFunc(serverHandler.GetServers))).Methods("GET")
	router.Handle("/api/servers/{serverName}/status", apiLimiter.Middleware(http.HandlerFunc(serverHandler.GetServerStatus))).Methods("GET")
	router.Handle("/api/servers/{serverName}/mods", apiLimiter.Middleware(http.HandlerFunc(serverHandler.GetServerMods))).Methods("GET")
	router.HandleFunc("/api/backgrounds", serverHandler.GetBackground).Methods("GET")
	router.HandleFunc("/backgrounds/{contentHash}/{filename}", serverHandler.ServeBackgroundFile).Methods("GET")
	router.HandleFunc("/healthz", serverHandler.Healthz).Methods("GET")
	router.HandleFunc("/api/servers/{serverName}/metrics", serverHandler.GetServerMetrics).Methods("GET")

	if authenticator != nil {
		router.Handle("/servers/{serverName}/action", authenticator.RequireRole("admin", "user")(http.HandlerFunc(pteroHandler.ServerAction))).Methods("POST")
		router.Handle("/servers/{serverName}/command", authenticator.RequireRole("admin", "user")(http.HandlerFunc(pteroHandler.SendCommand))).Methods("POST")
		router.Handle("/servers/{serverName}/whitelist", authenticator.RequireRole("admin", "user")(http.HandlerFunc(pteroHandler.WhitelistSet))).Methods("POST")
		router.Handle("/servers/{serverName}/whitelist", authenticator.RequireRole("admin", "user")(http.HandlerFunc(pteroHandler.WhitelistStatus))).Methods("GET")
	}

	if authenticator != nil {
		router.Handle("/admin/backgrounds", authenticator.RequireRole("admin")(http.HandlerFunc(webHandler.BackgroundManager))).Methods("GET")
		router.Handle("/admin/backgrounds/upload", authenticator.RequireRole("admin")(http.HandlerFunc(serverHandler.UploadBackground))).Methods("POST")
		router.Handle("/admin/backgrounds/update", authenticator.RequireRole("admin")(http.HandlerFunc(serverHandler.BulkUpdateBackgrounds))).Methods("POST")
		router.Handle("/admin/backgrounds/delete", authenticator.RequireRole("admin")(http.HandlerFunc(serverHandler.DeleteBackground))).Methods("POST")
		router.Handle("/admin/settings", authenticator.RequireRole("admin")(http.HandlerFunc(webHandler.Settings))).Methods("GET", "POST")

		router.Handle("/admin/users", authenticator.RequireRole("admin")(http.HandlerFunc(webHandler.Users))).Methods("GET")
		router.Handle("/admin/users/update", authenticator.RequireRole("admin")(http.HandlerFunc(webHandler.HandleUserUpdate))).Methods("POST")

		router.Handle("/admin/pterodactyl/link", authenticator.RequireRole("admin")(http.HandlerFunc(pteroHandler.LinkServer))).Methods("POST")
		router.Handle("/admin/pterodactyl/unlink", authenticator.RequireRole("admin")(http.HandlerFunc(pteroHandler.UnlinkServer))).Methods("POST")
		router.Handle("/admin/pterodactyl/commands/add", authenticator.RequireRole("admin")(http.HandlerFunc(pteroHandler.AddCommand))).Methods("POST")
		router.Handle("/admin/pterodactyl/commands/delete", authenticator.RequireRole("admin")(http.HandlerFunc(pteroHandler.DeleteCommand))).Methods("POST")

		router.Handle("/api/pterodactyl/servers", authenticator.RequireRole("admin")(http.HandlerFunc(pteroHandler.ListPteroServers))).Methods("GET")

		router.Handle("/my-servers", authenticator.RequireRole("admin", "user")(http.HandlerFunc(webHandler.MyServers))).Methods("GET")

		router.Handle("/admin/commands/{serverId}", authenticator.RequireRole("admin")(http.HandlerFunc(webHandler.CommandManager))).Methods("GET")
		router.Handle("/admin/commands/add", authenticator.RequireRole("admin")(http.HandlerFunc(automationHandler.AddCommandSchema))).Methods("POST")
		router.Handle("/admin/commands/update", authenticator.RequireRole("admin")(http.HandlerFunc(automationHandler.UpdateCommandSchema))).Methods("POST")
		router.Handle("/admin/commands/delete", authenticator.RequireRole("admin")(http.HandlerFunc(automationHandler.DeleteCommandSchema))).Methods("POST")

		router.Handle("/admin/constraints", authenticator.RequireRole("admin")(http.HandlerFunc(webHandler.ConstraintManager))).Methods("GET")
		router.Handle("/admin/constraints/add", authenticator.RequireRole("admin")(http.HandlerFunc(automationHandler.AddConstraint))).Methods("POST")
		router.Handle("/admin/constraints/update", authenticator.RequireRole("admin")(http.HandlerFunc(automationHandler.UpdateConstraint))).Methods("POST")
		router.Handle("/admin/constraints/delete", authenticator.RequireRole("admin")(http.HandlerFunc(automationHandler.DeleteConstraint))).Methods("POST")

		router.Handle("/admin/cron", authenticator.RequireRole("admin")(http.HandlerFunc(webHandler.CronManager))).Methods("GET")
		router.Handle("/admin/cron/add", authenticator.RequireRole("admin")(http.HandlerFunc(automationHandler.AddCronJob))).Methods("POST")
		router.Handle("/admin/cron/update", authenticator.RequireRole("admin")(http.HandlerFunc(automationHandler.UpdateCronJob))).Methods("POST")
		router.Handle("/admin/cron/delete", authenticator.RequireRole("admin")(http.HandlerFunc(automationHandler.DeleteCronJob))).Methods("POST")

		router.Handle("/admin/tags/{serverId}", authenticator.RequireRole("admin")(http.HandlerFunc(automationHandler.UpdateServerTags))).Methods("POST")
		router.Handle("/admin/acl/{serverId}", authenticator.RequireRole("admin")(http.HandlerFunc(automationHandler.UpdateACLRule))).Methods("POST")

		router.Handle("/api/audit", authenticator.RequireRole("admin")(http.HandlerFunc(automationHandler.GetAuditLog))).Methods("GET")
		router.Handle("/api/constraints/test", authenticator.RequireRole("admin")(http.HandlerFunc(automationHandler.TestConstraint))).Methods("POST")
	}
	router.HandleFunc("/help", webHandler.Help).Methods("GET")
	router.HandleFunc("/help/api.md", webHandler.HelpMarkdown).Methods("GET")

	if agentHub != nil {
		router.HandleFunc("/agent/ws", agentHub.ServeWS)
	}

	if agentHandler != nil && authenticator != nil {
		router.Handle("/api/agents", authenticator.RequireRole("admin")(http.HandlerFunc(agentHandler.ListAgents))).Methods("GET")
		router.Handle("/api/agents", authenticator.RequireRole("admin")(http.HandlerFunc(agentHandler.CreateAgent))).Methods("POST")
		router.Handle("/api/agents/delete", authenticator.RequireRole("admin")(http.HandlerFunc(agentHandler.DeleteAgent))).Methods("POST")

		router.Handle("/api/agents/{serverName}/files", authenticator.RequireRole("admin")(http.HandlerFunc(agentHandler.AgentFileList))).Methods("GET")
		router.Handle("/api/agents/{serverName}/files/read", authenticator.RequireRole("admin")(http.HandlerFunc(agentHandler.AgentFileRead))).Methods("GET")
		router.Handle("/api/agents/{serverName}/files/write", authenticator.RequireRole("admin")(http.HandlerFunc(agentHandler.AgentFileWrite))).Methods("POST")
		router.Handle("/api/agents/{serverName}/files/delete", authenticator.RequireRole("admin")(http.HandlerFunc(agentHandler.AgentFileDelete))).Methods("POST")
		router.Handle("/api/agents/{serverName}/files/mkdir", authenticator.RequireRole("admin")(http.HandlerFunc(agentHandler.AgentMkdir))).Methods("POST")

		router.Handle("/api/agents/{serverName}/backup/create", authenticator.RequireRole("admin")(http.HandlerFunc(agentHandler.AgentBackupCreate))).Methods("POST")
		router.Handle("/api/agents/{serverName}/backup/restore", authenticator.RequireRole("admin")(http.HandlerFunc(agentHandler.AgentBackupRestore))).Methods("POST")
		router.Handle("/api/agents/{serverName}/backup/list", authenticator.RequireRole("admin")(http.HandlerFunc(agentHandler.AgentBackupList))).Methods("POST")
	}

	if scimHandler != nil {
		scimRouter := router.PathPrefix("/scim/v2").Subrouter()
		scimRouter.Use(scimHandler.BearerAuth)
		scimRouter.Use(func(next http.Handler) http.Handler {
			return scimLimiter.Middleware(next)
		})

		scimRouter.HandleFunc("/ServiceProviderConfig", scimHandler.ServiceProviderConfig).Methods("GET")
		scimRouter.HandleFunc("/Schemas", scimHandler.Schemas).Methods("GET")
		scimRouter.HandleFunc("/Schemas/urn:ietf:params:scim:schemas:core:2.0:User", scimHandler.SchemaUser).Methods("GET")
		scimRouter.HandleFunc("/Schemas/urn:ietf:params:scim:schemas:core:2.0:Group", scimHandler.SchemaGroup).Methods("GET")

		scimRouter.HandleFunc("/Users", scimHandler.ListUsers).Methods("GET")
		scimRouter.HandleFunc("/Users", scimHandler.CreateUser).Methods("POST")
		scimRouter.HandleFunc("/Users/{id}", scimHandler.GetUser).Methods("GET")
		scimRouter.HandleFunc("/Users/{id}", scimHandler.ReplaceUser).Methods("PUT")
		scimRouter.HandleFunc("/Users/{id}", scimHandler.PatchUser).Methods("PATCH")
		scimRouter.HandleFunc("/Users/{id}", scimHandler.DeleteUser).Methods("DELETE")

		scimRouter.HandleFunc("/Groups", scimHandler.ListGroups).Methods("GET")
		scimRouter.HandleFunc("/Groups", scimHandler.CreateGroup).Methods("POST")
		scimRouter.HandleFunc("/Groups/{id}", scimHandler.GetGroup).Methods("GET")
		scimRouter.HandleFunc("/Groups/{id}", scimHandler.ReplaceGroup).Methods("PUT")
		scimRouter.HandleFunc("/Groups/{id}", scimHandler.PatchGroup).Methods("PATCH")
		scimRouter.HandleFunc("/Groups/{id}", scimHandler.DeleteGroup).Methods("DELETE")
	}

	router.PathPrefix("/{serverName}/map/").HandlerFunc(serverHandler.MapProxy)
	router.PathPrefix("/files/{serverName}/mods/").Handler(http.HandlerFunc(serverHandler.ServeModFiles))
	router.PathPrefix("/assets/").Handler(http.HandlerFunc(webHandler.ServeAssets))

	router.HandleFunc("/{serverName}", webHandler.ServerDetail).Methods("GET")

	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: csrfRouter,
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

	if scheduler != nil {
		scheduler.Stop()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced to shutdown: %s\n", err)
	}

	log.Println("Server exited gracefully.")
}
