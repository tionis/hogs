package query

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tionis/hogs/database"
)

type SatisfactoryQuerier struct{}

type satisfactoryHealthResponse struct {
	Health string `json:"health"`
	Status string `json:"status"`
}

type satisfactoryServerInfoResponse struct {
	Data struct {
		ServerName        string `json:"serverName"`
		NumActivePlayers  int    `json:"numActivePlayers"`
		MaxPlayers        int    `json:"maxPlayers"`
		ActiveSessionName string `json:"activeSessionName"`
	} `json:"data"`
}

func (q *SatisfactoryQuerier) Query(server *database.Server) (*ServerStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverStatus := &ServerStatus{
		Online:      false,
		LastUpdated: time.Now(),
	}

	host := server.Address
	port := 15777

	if strings.Contains(host, ":") {
		parts := strings.Split(host, ":")
		if len(parts) == 2 {
			host = parts[0]
			p, err := strconv.ParseUint(parts[1], 10, 16)
			if err == nil {
				port = int(p)
			}
		}
	}

	baseURL := fmt.Sprintf("http://%s:%d", host, port)
	client := &http.Client{Timeout: 5 * time.Second}

	healthReq, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/api/v1/health", nil)
	if err != nil {
		serverStatus.Error = err.Error()
		return serverStatus, err
	}
	q.addAuthHeader(healthReq, server)

	healthResp, err := client.Do(healthReq)
	if err != nil {
		serverStatus.Error = err.Error()
		return serverStatus, fmt.Errorf("failed to query satisfactory server: %w", err)
	}
	defer healthResp.Body.Close()

	if healthResp.StatusCode != 200 {
		serverStatus.Error = fmt.Sprintf("health check returned status %d", healthResp.StatusCode)
		return serverStatus, fmt.Errorf("health check returned status %d", healthResp.StatusCode)
	}

	serverStatus.Online = true

	infoReq, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/api/v1/server-info", nil)
	if err != nil {
		return serverStatus, nil
	}
	q.addAuthHeader(infoReq, server)

	infoResp, err := client.Do(infoReq)
	if err != nil {
		return serverStatus, nil
	}
	defer infoResp.Body.Close()

	if infoResp.StatusCode != 200 {
		return serverStatus, nil
	}

	var info satisfactoryServerInfoResponse
	if err := json.NewDecoder(infoResp.Body).Decode(&info); err != nil {
		return serverStatus, nil
	}

	serverStatus.Players = info.Data.NumActivePlayers
	serverStatus.MaxPlayers = info.Data.MaxPlayers
	serverStatus.MapName = info.Data.ActiveSessionName
	serverStatus.ServerMessage = info.Data.ServerName

	return serverStatus, nil
}

func (q *SatisfactoryQuerier) addAuthHeader(req *http.Request, server *database.Server) {
	if token, ok := server.Metadata["api_token"]; ok && token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}
