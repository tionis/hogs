package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func satisfactoryAgentTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Function string `json:"function"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if payload.Function != "QueryServerState" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errorCode":"insufficient_scope"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"serverGameState":{"activeSessionName":"Tionis","numConnectedPlayers":2,"playerLimit":4,"isGameRunning":true}}}`))
	}))
}

func TestSatisfactoryPlayerStatus(t *testing.T) {
	server := satisfactoryAgentTestServer(t)
	defer server.Close()
	port := strings.TrimPrefix(server.URL, "https://127.0.0.1:")

	tokenFile := filepath.Join(t.TempDir(), "api-token")
	if err := os.WriteFile(tokenFile, []byte("test-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	players, maxPlayers, known := satisfactoryPlayerStatus(&ServerConfig{
		GameType:     "satisfactory",
		Address:      "satisfactory.invalid:" + port,
		APITokenFile: tokenFile,
	})
	if !known || players != 2 || maxPlayers != 4 {
		t.Fatalf("players=%d max=%d known=%v", players, maxPlayers, known)
	}
}

func TestSatisfactoryPlayerStatusWithoutToken(t *testing.T) {
	_, _, known := satisfactoryPlayerStatus(&ServerConfig{GameType: "satisfactory"})
	if known {
		t.Fatal("status without token file must be unknown")
	}
	_, _, known = satisfactoryPlayerStatus(&ServerConfig{
		GameType:     "satisfactory",
		APITokenFile: filepath.Join(t.TempDir(), "missing"),
	})
	if known {
		t.Fatal("status with missing token file must be unknown")
	}
}

func TestSatisfactoryGamePort(t *testing.T) {
	if port := satisfactoryGamePort(&ServerConfig{Address: "host.invalid:7777"}); port != 7777 {
		t.Fatalf("port=%d", port)
	}
	if port := satisfactoryGamePort(&ServerConfig{Address: "host.invalid"}); port != 7777 {
		t.Fatalf("default port=%d", port)
	}
}
