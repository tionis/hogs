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
