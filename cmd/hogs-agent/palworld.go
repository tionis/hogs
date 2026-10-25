package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const palworldDefaultAPIURL = "http://127.0.0.1:8212"

type palworldMetrics struct {
	CurrentPlayerNum int `json:"currentplayernum"`
	MaxPlayerNum     int `json:"maxplayernum"`
}

type palworldPlayers struct {
	Players []struct {
		Name string `json:"name"`
	} `json:"players"`
}

type palworldInfo struct {
	Version string `json:"version"`
}

// palworldGet calls the node-local Palworld REST API. Palworld authenticates
// with HTTP Basic as user "admin" and the server's AdminPassword, which lives
// in a root-only file on the node so it never enters agent configuration.
// RCON is deprecated upstream, so the REST API is the only status interface.
func palworldGet(server *ServerConfig, path string, target any) error {
	if strings.TrimSpace(server.APITokenFile) == "" {
		return fmt.Errorf("no Palworld API credential configured")
	}
	passwordBytes, err := os.ReadFile(server.APITokenFile)
	if err != nil {
		return err
	}
	password := strings.TrimSpace(string(passwordBytes))
	if password == "" {
		return fmt.Errorf("empty Palworld API credential")
	}
	base := strings.TrimRight(strings.TrimSpace(server.APIURL), "/")
	if base == "" {
		base = palworldDefaultAPIURL
	}
	req, err := http.NewRequest(http.MethodGet, base+"/v1/api/"+path, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth("admin", password)
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("Palworld API %s returned %s", path, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(target)
}

func palworldPlayerStatus(server *ServerConfig) (players, maxPlayers int, known bool, names []string) {
	var metrics palworldMetrics
	if err := palworldGet(server, "metrics", &metrics); err != nil {
		return 0, 0, false, nil
	}
	var list palworldPlayers
	if err := palworldGet(server, "players", &list); err == nil {
		names = []string{}
		for _, player := range list.Players {
			if name := strings.TrimSpace(player.Name); name != "" {
				names = append(names, name)
			}
		}
	}
	return metrics.CurrentPlayerNum, metrics.MaxPlayerNum, true, names
}

func palworldVersion(server *ServerConfig) string {
	var info palworldInfo
	if err := palworldGet(server, "info", &info); err != nil {
		return ""
	}
	return strings.TrimSpace(info.Version)
}
