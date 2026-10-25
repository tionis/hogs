package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func palworldAgentTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, password, ok := r.BasicAuth(); !ok || user != "admin" || password != "test-password" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/api/metrics":
			_, _ = w.Write([]byte(`{"serverfps":57,"currentplayernum":2,"serverframetime":16.7,"maxplayernum":16,"uptime":3600,"basecampnum":3,"days":4}`))
		case "/v1/api/players":
			_, _ = w.Write([]byte(`{"players":[{"name":"Alice","userId":"steam_1"},{"name":" Bob ","userId":"steam_2"}]}`))
		case "/v1/api/info":
			_, _ = w.Write([]byte(`{"version":"v1.0.0.12345","servername":"Test","description":"","worldguid":"ABC"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func palworldTestConfig(t *testing.T, url, password string) *ServerConfig {
	t.Helper()
	passwordFile := filepath.Join(t.TempDir(), "admin-password")
	if err := os.WriteFile(passwordFile, []byte(password+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return &ServerConfig{GameType: "palworld", APIURL: url, APITokenFile: passwordFile}
}

func TestPalworldPlayerStatusAndVersion(t *testing.T) {
	api := palworldAgentTestServer(t)
	defer api.Close()
	server := palworldTestConfig(t, api.URL+"/", "test-password")

	players, maxPlayers, known, names := palworldPlayerStatus(server)
	if !known || players != 2 || maxPlayers != 16 {
		t.Fatalf("players=%d max=%d known=%v", players, maxPlayers, known)
	}
	if len(names) != 2 || names[0] != "Alice" || names[1] != "Bob" {
		t.Fatalf("names=%#v", names)
	}
	if version := palworldVersion(server); version != "v1.0.0.12345" {
		t.Fatalf("version=%q", version)
	}
}

func TestPalworldStatusUnknownWithoutValidCredential(t *testing.T) {
	api := palworldAgentTestServer(t)
	defer api.Close()

	if _, _, known, _ := palworldPlayerStatus(palworldTestConfig(t, api.URL, "wrong")); known {
		t.Fatal("rejected credential must leave status unknown")
	}
	if _, _, known, _ := palworldPlayerStatus(&ServerConfig{GameType: "palworld", APIURL: api.URL}); known {
		t.Fatal("status without credential file must be unknown")
	}
	if version := palworldVersion(palworldTestConfig(t, api.URL, "wrong")); version != "" {
		t.Fatalf("version with rejected credential=%q", version)
	}
}
