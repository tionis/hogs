package query

import "time"

type Player struct {
	Name string `json:"name"`
	ID   string `json:"id,omitempty"`
}

type ServerStatus struct {
	Online        bool                   `json:"online"`
	Players       int                    `json:"players"`
	MaxPlayers    int                    `json:"maxPlayers"`
	PlayerList    []Player               `json:"playerList,omitempty"`
	Version       string                 `json:"version,omitempty"`
	MapName       string                 `json:"mapName,omitempty"`
	ServerMessage string                 `json:"serverMessage,omitempty"`
	LastUpdated   time.Time              `json:"lastUpdated"`
	Error         string                 `json:"error,omitempty"`
	Extras        map[string]interface{} `json:"extras,omitempty"`
}
