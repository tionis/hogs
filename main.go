package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/tionis/hogs/agent"
	"github.com/tionis/hogs/api"
	"github.com/tionis/hogs/auth"
	"github.com/tionis/hogs/config"
	hogscron "github.com/tionis/hogs/cron"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/engine"
	"github.com/tionis/hogs/notify"
	"github.com/tionis/hogs/query"
	"github.com/tionis/hogs/scim"
	"github.com/tionis/hogs/web"
	"github.com/tionis/hogs/webhook"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/mux"
)

func securityHeadersMiddleware(cfg *config.Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; img-src 'self' data:; connect-src 'self' https://cdn.jsdelivr.net;")
			w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
			if cfg.TLSCert != "" && cfg.TLSKey != "" {
				w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

func main() {
	cfg := config.LoadConfig()

	// Set API key hashing pepper (uses dedicated pepper or session secret as fallback)
	if cfg.APIKeyPepper != "" {
		database.APIKeyPepper = cfg.APIKeyPepper
	} else if cfg.SessionSecret != "" {
		database.APIKeyPepper = cfg.SessionSecret
		log.Println("WARNING: API_KEY_PEPPER not set, using SESSION_SECRET as fallback. Set API_KEY_PEPPER for better security.")
	} else {
		log.Fatalln("FATAL: Either API_KEY_PEPPER or SESSION_SECRET must be configured.")
	}

	csrfSecret := cfg.CSRFSecret
	if csrfSecret == "" {
		csrfSecret = cfg.SessionSecret
		if csrfSecret == "" {
			log.Fatalln("FATAL: Either CSRF_SECRET or SESSION_SECRET must be configured.")
		}
		log.Println("WARNING: CSRF_SECRET not set, using SESSION_SECRET as fallback. Set CSRF_SECRET for better security.")
	}

	store, err := database.NewStore(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("could not initialize database: %s\n", err)
	}

	bgDir := filepath.Join(cfg.GameDataPath, "backgrounds")
	if err := store.ComputeMissingHashes(bgDir); err != nil {
		log.Printf("Warning: failed to compute missing background hashes: %v", err)
	}

	cache := query.NewServerStatusCache()

	notifyService := notify.NewService(store)

	eng := engine.NewEngine(store, cfg, cache)
	eng.SetNotifier(notifyService)

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
	var consoleHandler *api.ConsoleHandler
	if cfg.AgentEnabled {
		agentHub = agent.NewHub(store, cfg)
		agentHub.SetNotifier(notifyService)
		agentHub.LoadAndRecoverPendingOps()
		agentHub.StartPendingOpsCleanup()
		agentService = agent.NewAgentService(store, agentHub)
		agentHandler = api.NewAgentHandler(store, agentService, agentHub)
		consoleHandler = api.NewConsoleHandler(agentHub, authenticator)
		log.Println("Agent WebSocket endpoint enabled at /agent/ws")
	}

	pteroHandler := api.NewPterodactylHandler(store, cfg, eng, agentHub, authenticator)
	automationHandler := api.NewAutomationHandler(store, cfg, eng)
	dashboardHandler := api.NewDashboardHandler(store, cfg, eng, agentHub)
	apiKeyHandler := api.NewAPIKeyHandler(store)
	templateHandler := api.NewTemplateHandler(store)
	webhookDispatcher := webhook.NewDispatcher(store)
	webhookHandler := api.NewWebhookHandler(store, webhookDispatcher)
	notificationHandler := api.NewNotificationHandler(store, notifyService)

	var scimHandler *scim.Handler
	if cfg.SCIMEnabled && cfg.SCIMBearerToken != "" {
		scimHandler = scim.NewHandler(store, cfg, authenticator)
		log.Println("SCIM 2.0 endpoint enabled at /scim/v2/")
	}

	var scheduler *hogscron.Scheduler
	if cfg.CronEnabled {
		scheduler = hogscron.NewScheduler(store, eng)
		scheduler.SetNotifier(notifyService)
		if err := scheduler.Start(); err != nil {
			log.Printf("Warning: cron scheduler failed to start: %v", err)
		}
	}

	// Server status change notifications
	cache.SetOnChange(func(serverName string, oldStatus, newStatus *query.ServerStatus) {
		if oldStatus.Online != newStatus.Online {
			if newStatus.Online {
				notifyService.Send("server_up", fmt.Sprintf("Server %s is now online (%d/%d players)", serverName, newStatus.Players, newStatus.MaxPlayers))
			} else {
				notifyService.Send("server_down", fmt.Sprintf("Server %s is now offline", serverName))
			}
		}
	})

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

	csrfExemptPrefixes := []string{"/agent/ws", "/scim/v2", "/auth/callback", "/auth/backchannel-logout", "/api/"}
	isSecureFunc := func(r *http.Request) bool {
		if cfg.TLSCert != "" {
			return true
		}
		if cfg.TrustProxyHeaders && r.Header.Get("X-Forwarded-Proto") == "https" {
			return true
		}
		return false
	}
	csrfRouter := auth.CSRFMiddleware(csrfSecret, isSecureFunc, csrfExemptPrefixes, router)
	apiKeyRouter := auth.APIKeyMiddleware(store, csrfRouter)

	router.HandleFunc("/", webHandler.Home).Methods("GET")

	if authenticator != nil {
		router.Handle("/login", loginLimiter.Middleware(http.HandlerFunc(authenticator.HandleLogin))).Methods("GET")
		router.HandleFunc("/logout", authenticator.HandleLogout).Methods("GET")
		router.HandleFunc("/auth/callback", authenticator.HandleCallback).Methods("GET")
		router.HandleFunc("/auth/backchannel-logout", authenticator.HandleBackChannelLogout).Methods("POST")

		router.Handle("/admin", authenticator.RequireRole("admin")(http.HandlerFunc(webHandler.Admin))).Methods("GET")
		router.Handle("/admin/dashboard", authenticator.RequireRole("admin")(http.HandlerFunc(webHandler.Dashboard))).Methods("GET")
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
		router.Handle("/admin/agents", authenticator.RequireRole("admin")(http.HandlerFunc(webHandler.Agents))).Methods("GET")
		router.Handle("/admin/audit", authenticator.RequireRole("admin")(http.HandlerFunc(webHandler.AuditLog))).Methods("GET")
		router.Handle("/admin/backups", authenticator.RequireRole("admin")(http.HandlerFunc(webHandler.Backups))).Methods("GET")

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
		router.Handle("/api/audit/export", authenticator.RequireRole("admin")(http.HandlerFunc(automationHandler.ExportAuditLog))).Methods("GET")
		router.Handle("/api/constraints/test", authenticator.RequireRole("admin")(http.HandlerFunc(automationHandler.TestConstraint))).Methods("POST")
		router.Handle("/api/admin/bulk-tags", authenticator.RequireRole("admin")(http.HandlerFunc(automationHandler.BulkTags))).Methods("POST")
		router.Handle("/api/admin/bulk-acl", authenticator.RequireRole("admin")(http.HandlerFunc(automationHandler.BulkACL))).Methods("POST")

		router.Handle("/api/dashboard", authenticator.RequireRole("admin")(http.HandlerFunc(dashboardHandler.Overview))).Methods("GET")
		router.Handle("/api/dashboard/agents", authenticator.RequireRole("admin")(http.HandlerFunc(dashboardHandler.AgentList))).Methods("GET")

		router.Handle("/api/api-keys", authenticator.RequireRole("admin")(http.HandlerFunc(apiKeyHandler.ListAPIKeys))).Methods("GET")
		router.Handle("/api/api-keys", authenticator.RequireRole("admin")(http.HandlerFunc(apiKeyHandler.CreateAPIKey))).Methods("POST")
		router.Handle("/api/api-keys/delete", authenticator.RequireRole("admin")(http.HandlerFunc(apiKeyHandler.DeleteAPIKey))).Methods("POST")

		router.Handle("/api/templates", authenticator.RequireRole("admin")(http.HandlerFunc(templateHandler.ListTemplates))).Methods("GET")
		router.Handle("/api/templates/create", authenticator.RequireRole("admin")(http.HandlerFunc(templateHandler.CreateTemplate))).Methods("POST")
		router.Handle("/api/templates/delete", authenticator.RequireRole("admin")(http.HandlerFunc(templateHandler.DeleteTemplate))).Methods("POST")

		router.Handle("/api/webhooks", authenticator.RequireRole("admin")(http.HandlerFunc(webhookHandler.ListWebhooks))).Methods("GET")
		router.Handle("/api/webhooks/create", authenticator.RequireRole("admin")(http.HandlerFunc(webhookHandler.CreateWebhook))).Methods("POST")
		router.Handle("/api/webhooks/delete", authenticator.RequireRole("admin")(http.HandlerFunc(webhookHandler.DeleteWebhook))).Methods("POST")
		router.Handle("/api/webhooks/test", authenticator.RequireRole("admin")(http.HandlerFunc(webhookHandler.TestWebhook))).Methods("GET")

		router.Handle("/api/notifications", authenticator.RequireRole("admin")(http.HandlerFunc(notificationHandler.ListChannels))).Methods("GET")
		router.Handle("/api/notifications/create", authenticator.RequireRole("admin")(http.HandlerFunc(notificationHandler.CreateChannel))).Methods("POST")
		router.Handle("/api/notifications/delete", authenticator.RequireRole("admin")(http.HandlerFunc(notificationHandler.DeleteChannel))).Methods("POST")
		router.Handle("/api/notifications/test", authenticator.RequireRole("admin")(http.HandlerFunc(notificationHandler.TestChannel))).Methods("GET")
	}
	router.HandleFunc("/help", webHandler.Help).Methods("GET")
	router.HandleFunc("/help/api.md", webHandler.HelpMarkdown).Methods("GET")

	if agentHub != nil {
		router.HandleFunc("/agent/ws", agentHub.ServeWS)
	}

	if consoleHandler != nil && authenticator != nil {
		router.Handle("/servers/{serverName}/console", authenticator.RequireRole("admin", "user")(http.HandlerFunc(consoleHandler.ServeWS)))
	}

	if agentHandler != nil && authenticator != nil {
		router.Handle("/api/agents", authenticator.RequireRole("admin")(http.HandlerFunc(agentHandler.ListAgents))).Methods("GET")
		router.Handle("/api/agents", authenticator.RequireRole("admin")(http.HandlerFunc(agentHandler.CreateAgent))).Methods("POST")
		router.Handle("/api/agents/{id}", authenticator.RequireRole("admin")(http.HandlerFunc(agentHandler.GetAgent))).Methods("GET")
		router.Handle("/api/agents/{id}", authenticator.RequireRole("admin")(http.HandlerFunc(agentHandler.UpdateAgent))).Methods("PUT")
		router.Handle("/api/agents/{id}/regenerate-token", authenticator.RequireRole("admin")(http.HandlerFunc(agentHandler.RegenerateToken))).Methods("POST")
		router.Handle("/api/agents/delete", authenticator.RequireRole("admin")(http.HandlerFunc(agentHandler.DeleteAgent))).Methods("POST")

		router.Handle("/api/agents/{serverName}/files", authenticator.RequireRole("admin")(http.HandlerFunc(agentHandler.AgentFileList))).Methods("GET")
		router.Handle("/api/agents/{serverName}/files/read", authenticator.RequireRole("admin")(http.HandlerFunc(agentHandler.AgentFileRead))).Methods("GET")
		router.Handle("/api/agents/{serverName}/files/write", authenticator.RequireRole("admin")(http.HandlerFunc(agentHandler.AgentFileWrite))).Methods("POST")
		router.Handle("/api/agents/{serverName}/files/delete", authenticator.RequireRole("admin")(http.HandlerFunc(agentHandler.AgentFileDelete))).Methods("POST")
		router.Handle("/api/agents/{serverName}/files/mkdir", authenticator.RequireRole("admin")(http.HandlerFunc(agentHandler.AgentMkdir))).Methods("POST")

		router.Handle("/api/agents/{serverName}/backup/create", authenticator.RequireRole("admin")(http.HandlerFunc(agentHandler.AgentBackupCreate))).Methods("POST")
		router.Handle("/api/agents/{serverName}/backup/restore", authenticator.RequireRole("admin")(http.HandlerFunc(agentHandler.AgentBackupRestore))).Methods("POST")
		router.Handle("/api/agents/{serverName}/backup/list", authenticator.RequireRole("admin")(http.HandlerFunc(agentHandler.AgentBackupList))).Methods("POST")
		router.Handle("/api/agents/{serverName}/backup/init", authenticator.RequireRole("admin")(http.HandlerFunc(agentHandler.AgentBackupInit))).Methods("POST")
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

	// Add security headers middleware
	secureHandler := securityHeadersMiddleware(cfg)(apiKeyRouter)

	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: secureHandler,
	}

	go func() {
		log.Printf("Starting server on :%s", cfg.Port)
		if cfg.TLSCert != "" && cfg.TLSKey != "" {
			if err := srv.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey); err != nil && err != http.ErrServerClosed {
				log.Fatalf("could not start server: %s\n", err)
			}
		} else {
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("could not start server: %s\n", err)
			}
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
