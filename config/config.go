package config

import (
	"os"
	"strconv"
	"strings"
)

// Config holds the application configuration.
type Config struct {
	Port          string
	DatabasePath  string
	GameDataPath  string
	CacheDuration int // Seconds

	// Map proxy cache
	MapCacheDir            string
	MapCacheMaxBytes       int64
	MapCacheMaxItemBytes   int64
	MapCacheDefaultTTL     int
	MapCacheStaleTTL       int
	MapProxyAllowedOrigins []string

	// OIDC Configuration
	OIDCProviderURL  string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string
	SessionSecret    string

	// Security Secrets
	APIKeyPepper             string
	CSRFSecret               string
	ServerSecretKey          string
	BootstrapAdminAPIKey     string
	BootstrapAdminAPIKeyName string

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

	// Agent Configuration
	AgentEnabled      bool
	AgentHeartbeatSec int
	AgentConfigPath   string

	// Metrics Configuration
	MetricsRetentionDays int

	// Rate Limiting
	RateLimitLogin int
	RateLimitAPI   int
	RateLimitSCIM  int

	// TLS
	TLSCert string
	TLSKey  string

	// Reverse proxy support
	TrustProxyHeaders bool
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

		MapCacheDir:            getEnv("HOGS_MAP_CACHE_DIR", "data/map-cache"),
		MapCacheMaxBytes:       mustAtoi64(getEnv("HOGS_MAP_CACHE_MAX_BYTES", "2147483648")),
		MapCacheMaxItemBytes:   mustAtoi64(getEnv("HOGS_MAP_CACHE_MAX_ITEM_BYTES", "134217728")),
		MapCacheDefaultTTL:     mustAtoi(getEnv("HOGS_MAP_CACHE_DEFAULT_TTL_SEC", "300")),
		MapCacheStaleTTL:       mustAtoi(getEnv("HOGS_MAP_CACHE_STALE_TTL_SEC", "86400")),
		MapProxyAllowedOrigins: splitList(getEnv("HOGS_MAP_PROXY_ALLOWED_ORIGINS", "")),

		OIDCProviderURL:  getEnv("OIDC_PROVIDER_URL", ""),
		OIDCClientID:     getEnv("OIDC_CLIENT_ID", ""),
		OIDCClientSecret: getEnv("OIDC_CLIENT_SECRET", ""),
		OIDCRedirectURL:  getEnv("OIDC_REDIRECT_URL", "http://localhost:8080/auth/callback"),
		SessionSecret:    getEnv("SESSION_SECRET", ""),

		APIKeyPepper:             getEnv("API_KEY_PEPPER", ""),
		CSRFSecret:               getEnv("CSRF_SECRET", ""),
		ServerSecretKey:          getEnv("SERVER_SECRET_KEY", ""),
		BootstrapAdminAPIKey:     getEnv("BOOTSTRAP_ADMIN_API_KEY", ""),
		BootstrapAdminAPIKeyName: getEnv("BOOTSTRAP_ADMIN_API_KEY_NAME", "gandalf"),

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

		AgentEnabled:      getEnv("HOGS_AGENT_ENABLED", "true") == "true",
		AgentHeartbeatSec: mustAtoi(getEnv("HOGS_AGENT_HEARTBEAT_SEC", "30")),
		AgentConfigPath:   getEnv("HOGS_AGENT_CONFIG", "/etc/hogs/agents.yaml"),

		MetricsRetentionDays: mustAtoi(getEnv("HOGS_METRICS_RETENTION_DAYS", "7")),

		RateLimitLogin: mustAtoi(getEnv("HOGS_RATE_LIMIT_LOGIN", "5")),
		RateLimitAPI:   mustAtoi(getEnv("HOGS_RATE_LIMIT_API", "60")),
		RateLimitSCIM:  mustAtoi(getEnv("HOGS_RATE_LIMIT_SCIM", "100")),

		TLSCert: getEnv("TLS_CERT", ""),
		TLSKey:  getEnv("TLS_KEY", ""),

		TrustProxyHeaders: getEnv("TRUST_PROXY_HEADERS", "") == "true",
	}
}

func splitList(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func mustAtoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

func mustAtoi64(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
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
