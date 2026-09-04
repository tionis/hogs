package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type satisfactoryStateEnvelope struct {
	Data struct {
		ServerGameState struct {
			NumConnectedPlayers int  `json:"numConnectedPlayers"`
			PlayerLimit         int  `json:"playerLimit"`
			IsGameRunning       bool `json:"isGameRunning"`
		} `json:"serverGameState"`
	} `json:"data"`
	ErrorCode string `json:"errorCode"`
}

// satisfactoryPlayerStatus queries the node-local Satisfactory Server Manager
// HTTPS API. The bearer token lives in a root-only file on the node (minted
// at claim time), so the token itself never enters agent configuration.
func satisfactoryPlayerStatus(server *ServerConfig) (players, maxPlayers int, known bool) {
	if strings.TrimSpace(server.APITokenFile) == "" {
		return 0, 0, false
	}
	tokenBytes, err := os.ReadFile(server.APITokenFile)
	if err != nil {
		return 0, 0, false
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		return 0, 0, false
	}
	port := satisfactoryGamePort(server)
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives:  true,
			DisableCompression: true,
			TLSClientConfig:    &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		},
	}
	body, _ := json.Marshal(map[string]string{"function": "QueryServerState"})
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("https://127.0.0.1:%d/api/v1/", port), bytes.NewReader(body))
	if err != nil {
		return 0, 0, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return 0, 0, false
	}
	var envelope satisfactoryStateEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return 0, 0, false
	}
	if envelope.ErrorCode != "" || !envelope.Data.ServerGameState.IsGameRunning {
		return 0, 0, false
	}
	game := envelope.Data.ServerGameState
	return game.NumConnectedPlayers, game.PlayerLimit, true
}

func satisfactoryGamePort(server *ServerConfig) int {
	if _, port, err := net.SplitHostPort(strings.TrimSpace(server.Address)); err == nil {
		if p, err := strconv.Atoi(port); err == nil && p > 0 {
			return p
		}
	}
	return 7777
}
