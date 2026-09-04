package query

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tionis/hogs/database"
)

func satisfactoryTestServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Function string `json:"function"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		switch payload.Function {
		case "HealthCheck":
			_, _ = w.Write([]byte(`{"data":{"health":"healthy","serverCustomData":"","clientCustomData":"hogs"}}`))
		case "QueryServerState":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"errorCode":"insufficient_scope"}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":{"serverGameState":{"activeSessionName":"Tionis","numConnectedPlayers":3,"playerLimit":4,"techTier":9,"gamePhase":"PHASE-5","isGameRunning":true,"isGamePaused":false,"averageTickRate":30.0,"autoLoadSessionName":"Tionis"}}}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
}

func satisfactoryTestTarget(t *testing.T, server *httptest.Server, metadata map[string]string) *database.Server {
	t.Helper()
	host := strings.TrimPrefix(server.URL, "https://")
	if metadata == nil {
		metadata = map[string]string{}
	}
	metadata["directAddress"] = host
	return &database.Server{
		Name:     "satisfactory",
		Address:  "satisfactory.invalid",
		GameType: "satisfactory",
		Metadata: metadata,
	}
}

func TestSatisfactoryQuerierReportsSession(t *testing.T) {
	server := satisfactoryTestServer(t, "token")
	defer server.Close()

	status, err := (&SatisfactoryQuerier{}).Query(satisfactoryTestTarget(t, server, map[string]string{"api_token": "token"}))
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !status.Online || !status.PlayersKnown {
		t.Fatalf("status not fully known: %+v", status)
	}
	if status.Players != 3 || status.MaxPlayers != 4 || status.MapName != "Tionis" {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestSatisfactoryQuerierWithoutTokenReportsOnlineOnly(t *testing.T) {
	server := satisfactoryTestServer(t, "token")
	defer server.Close()

	status, err := (&SatisfactoryQuerier{}).Query(satisfactoryTestTarget(t, server, nil))
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !status.Online || status.PlayersKnown {
		t.Fatalf("expected online-only status: %+v", status)
	}
}

func TestSatisfactoryEndpointResolution(t *testing.T) {
	server := &database.Server{
		Address:  "satisfactory.invalid",
		Metadata: map[string]string{"directAddress": "destiny.tionis.dev:7777"},
	}
	host, port := satisfactoryEndpoint(server)
	if host != "destiny.tionis.dev" || port != 7777 {
		t.Fatalf("direct address not preferred: %s:%d", host, port)
	}
	server.Metadata = map[string]string{}
	host, port = satisfactoryEndpoint(server)
	if host != "satisfactory.invalid" || port != 7777 {
		t.Fatalf("fallback wrong: %s:%d", host, port)
	}
}
