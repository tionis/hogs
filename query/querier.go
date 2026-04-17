package query

import (
	"fmt"
	"time"

	"github.com/tionis/hogs/database"
)

type GameQuerier interface {
	Query(server *database.Server) (*ServerStatus, error)
}

type NoopQuerier struct{}

func (q *NoopQuerier) Query(server *database.Server) (*ServerStatus, error) {
	return &ServerStatus{
		Online:      false,
		LastUpdated: time.Now(),
		Error:       fmt.Sprintf("unsupported game type: %s", server.GameType),
	}, fmt.Errorf("unsupported game type: %s", server.GameType)
}

func NewQuerier(gameType string) GameQuerier {
	switch gameType {
	case "minecraft":
		return &MinecraftQuerier{}
	case "satisfactory":
		return &SatisfactoryQuerier{}
	case "factorio":
		return &FactorioQuerier{}
	default:
		return &NoopQuerier{}
	}
}
