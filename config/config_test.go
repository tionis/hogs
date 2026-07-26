package config

import (
	"os"
	"testing"
)

func TestLoadConfigDefaults(t *testing.T) {
	cfg := LoadConfig()
	if cfg.Port != "8080" {
		t.Errorf("Port = %q, want 8080", cfg.Port)
	}
	if cfg.GameDataPath != "data/game" {
		t.Errorf("GameDataPath = %q, want data/game", cfg.GameDataPath)
	}
	if cfg.ServerConstraintMaxPriority != 99 {
		t.Errorf("ServerConstraintMaxPriority = %d, want 99", cfg.ServerConstraintMaxPriority)
	}
}

func TestServerConstraintMaxPriorityFromEnv(t *testing.T) {
	t.Setenv("HOGS_SERVER_CONSTRAINT_MAX_PRIORITY", "42")
	if got := LoadConfig().ServerConstraintMaxPriority; got != 42 {
		t.Fatalf("ServerConstraintMaxPriority = %d, want 42", got)
	}
}

func TestGameDataPathFromEnv(t *testing.T) {
	os.Setenv("GAME_DATA_PATH", "/custom/path")
	defer os.Unsetenv("GAME_DATA_PATH")

	cfg := LoadConfig()
	if cfg.GameDataPath != "/custom/path" {
		t.Errorf("GameDataPath = %q, want /custom/path", cfg.GameDataPath)
	}
}

func TestGameDataPathFallsBackToModDataPath(t *testing.T) {
	os.Setenv("MOD_DATA_PATH", "/old/mod/path")
	defer os.Unsetenv("MOD_DATA_PATH")

	cfg := LoadConfig()
	if cfg.GameDataPath != "/old/mod/path" {
		t.Errorf("GameDataPath = %q, want /old/mod/path (fallback from MOD_DATA_PATH)", cfg.GameDataPath)
	}
}

func TestGameDataPathEnvTakesPrecedence(t *testing.T) {
	os.Setenv("GAME_DATA_PATH", "/new/path")
	os.Setenv("MOD_DATA_PATH", "/old/path")
	defer os.Unsetenv("GAME_DATA_PATH")
	defer os.Unsetenv("MOD_DATA_PATH")

	cfg := LoadConfig()
	if cfg.GameDataPath != "/new/path" {
		t.Errorf("GameDataPath = %q, want /new/path (GAME_DATA_PATH takes precedence)", cfg.GameDataPath)
	}
}

func TestOIDCGroupConfigDefaults(t *testing.T) {
	os.Unsetenv("OIDC_ADMIN_GROUP")
	os.Unsetenv("OIDC_USER_GROUP")
	os.Unsetenv("OIDC_GROUPS_CLAIM")
	cfg := LoadConfig()
	if cfg.OIDCAdminGroup != "admins" {
		t.Errorf("OIDCAdminGroup = %q, want %q", cfg.OIDCAdminGroup, "admins")
	}
	if cfg.OIDCUserGroup != "" {
		t.Errorf("OIDCUserGroup = %q, want empty string default", cfg.OIDCUserGroup)
	}
	if cfg.OIDCGroupsClaim != "groups" {
		t.Errorf("OIDCGroupsClaim = %q, want %q", cfg.OIDCGroupsClaim, "groups")
	}
}

func TestOIDCGroupConfigFromEnv(t *testing.T) {
	os.Setenv("OIDC_ADMIN_GROUP", "my-admins")
	os.Setenv("OIDC_USER_GROUP", "my-users")
	os.Setenv("OIDC_GROUPS_CLAIM", "roles")
	defer os.Unsetenv("OIDC_ADMIN_GROUP")
	defer os.Unsetenv("OIDC_USER_GROUP")
	defer os.Unsetenv("OIDC_GROUPS_CLAIM")

	cfg := LoadConfig()
	if cfg.OIDCAdminGroup != "my-admins" {
		t.Errorf("OIDCAdminGroup = %q, want %q", cfg.OIDCAdminGroup, "my-admins")
	}
	if cfg.OIDCUserGroup != "my-users" {
		t.Errorf("OIDCUserGroup = %q, want %q", cfg.OIDCUserGroup, "my-users")
	}
	if cfg.OIDCGroupsClaim != "roles" {
		t.Errorf("OIDCGroupsClaim = %q, want %q", cfg.OIDCGroupsClaim, "roles")
	}
}

func TestGameIdentityLinkURLsFromEnv(t *testing.T) {
	t.Setenv("HOGS_GAME_IDENTITY_LINK_URLS", `{"Factorio":" https://auth.example.test/if/flow/link-factorio/ ","empty":""}`)
	cfg := LoadConfig()
	if len(cfg.GameIdentityLinkURLs) != 1 ||
		cfg.GameIdentityLinkURLs["factorio"] != "https://auth.example.test/if/flow/link-factorio/" {
		t.Fatalf("GameIdentityLinkURLs = %#v", cfg.GameIdentityLinkURLs)
	}
}

func TestInvalidGameIdentityLinkURLsAreIgnored(t *testing.T) {
	t.Setenv("HOGS_GAME_IDENTITY_LINK_URLS", `{invalid`)
	if got := LoadConfig().GameIdentityLinkURLs; len(got) != 0 {
		t.Fatalf("GameIdentityLinkURLs = %#v, want empty", got)
	}
}

func TestPterodactylConfigDefaults(t *testing.T) {
	os.Unsetenv("PTERODACTYL_URL")
	os.Unsetenv("PTERODACTYL_APP_KEY")
	os.Unsetenv("PTERODACTYL_CLIENT_KEY")
	cfg := LoadConfig()
	if cfg.PterodactylURL != "" {
		t.Errorf("PterodactylURL = %q, want empty default", cfg.PterodactylURL)
	}
	if cfg.PterodactylAppKey != "" {
		t.Errorf("PterodactylAppKey = %q, want empty default", cfg.PterodactylAppKey)
	}
	if cfg.PterodactylClientKey != "" {
		t.Errorf("PterodactylClientKey = %q, want empty default", cfg.PterodactylClientKey)
	}
}

func TestPterodactylConfigFromEnv(t *testing.T) {
	os.Setenv("PTERODACTYL_URL", "https://panel.example.com")
	os.Setenv("PTERODACTYL_APP_KEY", "ptla_xxxxxxxx")
	os.Setenv("PTERODACTYL_CLIENT_KEY", "ptlc_yyyyyyyy")
	defer os.Unsetenv("PTERODACTYL_URL")
	defer os.Unsetenv("PTERODACTYL_APP_KEY")
	defer os.Unsetenv("PTERODACTYL_CLIENT_KEY")

	cfg := LoadConfig()
	if cfg.PterodactylURL != "https://panel.example.com" {
		t.Errorf("PterodactylURL = %q, want %q", cfg.PterodactylURL, "https://panel.example.com")
	}
	if cfg.PterodactylAppKey != "ptla_xxxxxxxx" {
		t.Errorf("PterodactylAppKey = %q, want %q", cfg.PterodactylAppKey, "ptla_xxxxxxxx")
	}
	if cfg.PterodactylClientKey != "ptlc_yyyyyyyy" {
		t.Errorf("PterodactylClientKey = %q, want %q", cfg.PterodactylClientKey, "ptlc_yyyyyyyy")
	}
}

func TestMapProxyAllowedOriginsFromEnv(t *testing.T) {
	t.Setenv("HOGS_MAP_PROXY_ALLOWED_ORIGINS", "http://10.0.0.1:8100, https://maps.example.test")
	cfg := LoadConfig()
	if len(cfg.MapProxyAllowedOrigins) != 2 ||
		cfg.MapProxyAllowedOrigins[0] != "http://10.0.0.1:8100" ||
		cfg.MapProxyAllowedOrigins[1] != "https://maps.example.test" {
		t.Fatalf("MapProxyAllowedOrigins = %#v", cfg.MapProxyAllowedOrigins)
	}
}

func TestServerSecretKeyFromEnv(t *testing.T) {
	t.Setenv("SERVER_SECRET_KEY", "stable-test-key-material")
	cfg := LoadConfig()
	if cfg.ServerSecretKey != "stable-test-key-material" {
		t.Fatalf("ServerSecretKey = %q", cfg.ServerSecretKey)
	}
}
