package config

import (
	"os"
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

	// Pterodactyl Configuration
	PterodactylURL    string
	PterodactylAppKey string
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

		PterodactylURL:    getEnv("PTERODACTYL_URL", ""),
		PterodactylAppKey: getEnv("PTERODACTYL_APP_KEY", ""),
	}
}

// getEnv retrieves an environment variable or returns a default value.
func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}
