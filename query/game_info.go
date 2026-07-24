package query

import "github.com/tionis/hogs/gametypes"

type GameInfo struct {
	Type        string
	DisplayName string
	Icon        string
	BadgeCSS    string
	PlayerNoun  string
}

func infoFromDriver(driver gametypes.Driver) GameInfo {
	return GameInfo{
		Type: driver.Slug, DisplayName: driver.DisplayName, Icon: driver.Icon,
		BadgeCSS:   "background: " + driver.AccentColor + "; color: #fff;",
		PlayerNoun: driver.PlayerNoun,
	}
}

func GetGameInfo(gameType string) GameInfo {
	if driver, ok := gametypes.Embedded(gameType); ok {
		return infoFromDriver(driver)
	}
	return infoFromDriver(gametypes.Generic(gameType))
}

func AllGameInfo() []GameInfo {
	drivers := gametypes.AllEmbedded()
	infos := make([]GameInfo, 0, len(drivers))
	for _, driver := range drivers {
		infos = append(infos, infoFromDriver(driver))
	}
	return infos
}
