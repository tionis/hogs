package config

import (
	"os"
	"strconv"
)

// Config holds the application configuration.
type Config struct {
	Port          string
	DatabasePath  string
	GameDataPath  string
	CacheDuration int // Seconds

	// OIDC Configuration
	OIDCProviderURL  string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string
	SessionSecret    string

	// OIDC Role Configuration
	OIDCAdminGroup  string
	OIDCUserGroup   string
	OIDCGroupsClaim string

	// OIDC Back-Channel Logout
	OIDCBackChannelLogoutEnabled bool

	// SCIM Configuration
	SCIMEnabled     bool
	SCIMBearerToken string

	// Pterodactyl Configuration
	PterodactylURL       string
	PterodactylAppKey    string
	PterodactylClientKey string

	// Automation Configuration
	CronEnabled              bool
	CronQueueRetryInterval   int
	CronQueueMaxRetry        int
	AuditLogRetentionDays    int
	PteroNodeRefreshInterval int
}

// LoadConfig reads configuration from environment variables or sets defaults.
func LoadConfig() *Config {
	gameDataPath := getEnv("GAME_DATA_PATH", "")
	if gameDataPath == "" {
		gameDataPath = getEnv("MOD_DATA_PATH", "data/game")
	}

	dbPath := getEnv("DB_PATH", "./hogs.db")

	return &Config{
		Port:          getEnv("PORT", "8080"),
		DatabasePath:  dbPath,
		GameDataPath:  gameDataPath,
		CacheDuration: 60,

		OIDCProviderURL:  getEnv("OIDC_PROVIDER_URL", ""),
		OIDCClientID:     getEnv("OIDC_CLIENT_ID", ""),
		OIDCClientSecret: getEnv("OIDC_CLIENT_SECRET", ""),
		OIDCRedirectURL:  getEnv("OIDC_REDIRECT_URL", "http://localhost:8080/auth/callback"),
		SessionSecret:    getEnv("SESSION_SECRET", "super-secret-key-change-me"),

		OIDCAdminGroup:  getEnv("OIDC_ADMIN_GROUP", "admins"),
		OIDCUserGroup:   getEnv("OIDC_USER_GROUP", ""),
		OIDCGroupsClaim: getEnv("OIDC_GROUPS_CLAIM", "groups"),

		OIDCBackChannelLogoutEnabled: getEnv("OIDC_BACKCHANNEL_LOGOUT", "true") == "true",

		SCIMEnabled:     getEnv("SCIM_ENABLED", "false") == "true",
		SCIMBearerToken: getEnv("SCIM_BEARER_TOKEN", ""),

		PterodactylURL:       getEnv("PTERODACTYL_URL", ""),
		PterodactylAppKey:    getEnv("PTERODACTYL_APP_KEY", ""),
		PterodactylClientKey: getEnv("PTERODACTYL_CLIENT_KEY", ""),

		CronEnabled:              getEnv("HOGS_CRON_ENABLED", "true") == "true",
		CronQueueRetryInterval:   mustAtoi(getEnv("HOGS_CRON_QUEUE_RETRY_INTERVAL", "30")),
		CronQueueMaxRetry:        mustAtoi(getEnv("HOGS_CRON_QUEUE_MAX_RETRY", "10")),
		AuditLogRetentionDays:    mustAtoi(getEnv("HOGS_AUDIT_LOG_RETENTION_DAYS", "90")),
		PteroNodeRefreshInterval: mustAtoi(getEnv("HOGS_PTERO_NODE_REFRESH_INTERVAL", "300")),
	}
}

func mustAtoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// getEnv retrieves an environment variable or returns a default value.
func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}
