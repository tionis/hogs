package query

import (
	"fmt"
	"time"

	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/gametypes"
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

var queriers = map[string]GameQuerier{
	"minecraft":    &MinecraftQuerier{},
	"satisfactory": &SatisfactoryQuerier{},
	"factorio":     &FactorioQuerier{},
	"valheim":      &ValheimQuerier{},
}

func RegisterQuerier(gameType string, q GameQuerier) {
	queriers[gameType] = q
}

func NewQuerier(gameType string) GameQuerier {
	driver, ok := gametypes.Embedded(gameType)
	if ok {
		return NewQuerierForDriver(driver)
	}
	if q, ok := queriers[gameType]; ok {
		return q
	}
	return &NoopQuerier{}
}

func NewQuerierForDriver(driver gametypes.Driver) GameQuerier {
	if q, ok := queriers[driver.StatusProtocol]; ok && driver.StatusProtocol != "" {
		return q
	}
	return &NoopQuerier{}
}

func RegisteredGameTypes() []string {
	types := make([]string, 0, len(queriers))
	for k := range queriers {
		types = append(types, k)
	}
	return types
}
