package query

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mcstatus-io/mcutil/v4/status"
	"github.com/tionis/hogs/database"
)

type MinecraftQuerier struct{}

func (q *MinecraftQuerier) Query(server *database.Server) (*ServerStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	host := server.Address
	port := uint16(25565)

	if strings.Contains(host, ":") {
		parts := strings.Split(host, ":")
		if len(parts) == 2 {
			host = parts[0]
			p, err := strconv.ParseUint(parts[1], 10, 16)
			if err == nil {
				port = uint16(p)
			}
		}
	} else {
		_, srvs, err := net.LookupSRV("minecraft", "tcp", host)
		if err == nil && len(srvs) > 0 {
			host = srvs[0].Target
			host = strings.TrimSuffix(host, ".")
			port = srvs[0].Port
		}
	}

	serverStatus := &ServerStatus{
		Online:      false,
		LastUpdated: time.Now(),
	}

	res, err := status.Modern(ctx, host, port)
	if err != nil {
		serverStatus.Error = err.Error()
		return serverStatus, fmt.Errorf("failed to query server: %w", err)
	}

	serverStatus.Online = true
	serverStatus.ServerMessage = res.MOTD.Clean

	if res.Players.Online != nil {
		serverStatus.Players = int(*res.Players.Online)
	}
	if res.Players.Max != nil {
		serverStatus.MaxPlayers = int(*res.Players.Max)
	}

	playerMap := make(map[string]bool)

	if res.Players.Sample != nil {
		for _, p := range res.Players.Sample {
			var name, id string
			if p.Name.Clean != "" {
				name = p.Name.Clean
			}
			if p.ID != "" {
				id = p.ID
			}
			serverStatus.PlayerList = append(serverStatus.PlayerList, Player{Name: name, ID: id})
			playerMap[name] = true
		}
	}

	if server.MapURL != "" {
		blueMapPlayers := fetchBlueMapPlayers(server.MapURL)
		for _, p := range blueMapPlayers {
			if !playerMap[p.Name] {
				serverStatus.PlayerList = append(serverStatus.PlayerList, p)
				playerMap[p.Name] = true
			}
		}
	}

	serverStatus.Version = res.Version.Name.Clean

	serverStatus.Extras = make(map[string]interface{})
	serverStatus.Extras["protocol"] = int(res.Version.Protocol)
	if res.Favicon != nil {
		serverStatus.Extras["favicon"] = *res.Favicon
	}

	return serverStatus, nil
}

type blueMapResponse struct {
	Players []struct {
		Name string `json:"name"`
		UUID string `json:"uuid"`
	} `json:"players"`
}

func fetchBlueMapPlayers(baseURL string) []Player {
	url := strings.TrimSuffix(baseURL, "/") + "/maps/world/live/players.json"

	client := http.Client{
		Timeout: 2 * time.Second,
	}

	resp, err := client.Get(url)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil
	}

	var bmResp blueMapResponse
	if err := json.NewDecoder(resp.Body).Decode(&bmResp); err != nil {
		return nil
	}

	var players []Player
	for _, p := range bmResp.Players {
		players = append(players, Player{Name: p.Name, ID: p.UUID})
	}
	return players
}
