package query

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tionis/hogs/database"
)

type SatisfactoryQuerier struct{}

type satisfactoryAPIEnvelope struct {
	Data struct {
		Health          string `json:"health"`
		ServerGameState struct {
			ActiveSessionName   string `json:"activeSessionName"`
			NumConnectedPlayers int    `json:"numConnectedPlayers"`
			PlayerLimit         int    `json:"playerLimit"`
			IsGameRunning       bool   `json:"isGameRunning"`
		} `json:"serverGameState"`
	} `json:"data"`
	ErrorCode string `json:"errorCode"`
}

func (q *SatisfactoryQuerier) Query(server *database.Server) (*ServerStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverStatus := &ServerStatus{
		Online:      false,
		LastUpdated: time.Now(),
	}

	host, port := satisfactoryEndpoint(server)
	baseURL := fmt.Sprintf("https://%s:%d/api/v1/", host, port)
	// The dedicated server negotiates TLS with a self-signed certificate by
	// design; there is nothing to pin against, so verification is skipped
	// the same way game clients accept it.
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			// Fresh uncompressed connection per request: the server HTTP
			// stack does not reliably serve reused keep-alive connections,
			// and its gzip handling breaks Go's transparent decompression.
			// Game clients send plain requests, so do the same.
			DisableKeepAlives:  true,
			DisableCompression: true,
			TLSClientConfig:    &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		},
	}

	health, err := q.call(ctx, client, baseURL, "HealthCheck", map[string]string{"ClientCustomData": "hogs"}, "")
	if err != nil {
		serverStatus.Error = err.Error()
		return serverStatus, fmt.Errorf("failed to query satisfactory server: %w", err)
	}
	if health.Data.Health != "healthy" {
		serverStatus.Error = "health check did not report healthy"
		return serverStatus, fmt.Errorf("health check did not report healthy")
	}

	serverStatus.Online = true

	token := strings.TrimSpace(server.Metadata["api_token"])
	if token == "" {
		return serverStatus, nil
	}
	state, err := q.call(ctx, client, baseURL, "QueryServerState", nil, token)
	if err != nil {
		// TEMPORARY diagnostics for the Destiny deployment; remove once the
		// failure mode is identified. Never logs the bearer token.
		log.Printf("satisfactory QueryServerState failed for %s: %v", host, err)
		return serverStatus, nil
	}
	game := state.Data.ServerGameState
	if !game.IsGameRunning {
		return serverStatus, nil
	}
	serverStatus.Players = game.NumConnectedPlayers
	serverStatus.MaxPlayers = game.PlayerLimit
	serverStatus.PlayersKnown = true
	serverStatus.MapName = game.ActiveSessionName

	return serverStatus, nil
}

func (q *SatisfactoryQuerier) call(ctx context.Context, client *http.Client, baseURL, function string, data interface{}, token string) (*satisfactoryAPIEnvelope, error) {
	payload := map[string]interface{}{"function": function}
	if data != nil {
		payload["data"] = data
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%s returned status %d", function, resp.StatusCode)
	}
	var envelope satisfactoryAPIEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, err
	}
	if envelope.ErrorCode != "" {
		return nil, fmt.Errorf("%s returned %s", function, envelope.ErrorCode)
	}
	return &envelope, nil
}

// satisfactoryEndpoint prefers the direct game address (host:port) and falls
// back to the server address with the default 1.x game/API port.
func satisfactoryEndpoint(server *database.Server) (string, int) {
	for _, candidate := range []string{server.Metadata["directAddress"], server.Address} {
		if host, port := splitSatisfactoryEndpoint(candidate); host != "" {
			return host, port
		}
	}
	return server.Address, 7777
}

func splitSatisfactoryEndpoint(candidate string) (string, int) {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return "", 0
	}
	if host, port, err := net.SplitHostPort(candidate); err == nil {
		if p, err := strconv.Atoi(port); err == nil && p > 0 {
			return host, p
		}
		return host, 7777
	}
	return candidate, 7777
}
