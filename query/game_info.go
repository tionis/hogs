package query

type GameInfo struct {
	Type        string
	DisplayName string
	Icon        string
	BadgeCSS    string
	PlayerNoun  string
}

var gameInfoRegistry = map[string]GameInfo{
	"minecraft": {
		Type:        "minecraft",
		DisplayName: "Minecraft",
		Icon:        `<svg class="game-icon" viewBox="0 0 16 16" fill="currentColor"><rect x="4" y="4" width="3" height="3"/><rect x="9" y="4" width="3" height="3"/><rect x="4" y="9" width="3" height="3"/><rect x="9" y="9" width="3" height="3"/></svg>`,
		BadgeCSS:    "background: linear-gradient(135deg, #4caf50, #2e7d32); color: #fff;",
		PlayerNoun:  "Players",
	},
	"satisfactory": {
		Type:        "satisfactory",
		DisplayName: "Satisfactory",
		Icon:        `<svg class="game-icon" viewBox="0 0 16 16" fill="currentColor"><circle cx="8" cy="8" r="3"/><circle cx="8" cy="2" r="1.5"/><circle cx="8" cy="14" r="1.5"/><circle cx="2" cy="8" r="1.5"/><circle cx="14" cy="8" r="1.5"/></svg>`,
		BadgeCSS:    "background: linear-gradient(135deg, #ff9800, #e65100); color: #fff;",
		PlayerNoun:  "Engineers",
	},
	"factorio": {
		Type:        "factorio",
		DisplayName: "Factorio",
		Icon:        `<svg class="game-icon" viewBox="0 0 16 16" fill="currentColor"><polygon points="8,1 15,8 8,15 1,8"/></svg>`,
		BadgeCSS:    "background: linear-gradient(135deg, #cddc39, #827717); color: #222;",
		PlayerNoun:  "Engineers",
	},
	"valheim": {
		Type:        "valheim",
		DisplayName: "Valheim",
		Icon:        `<svg class="game-icon" viewBox="0 0 16 16" fill="currentColor"><path d="M8 1L3 5v2l2-1v4l-2 2v2h3v-2l1-1 1 1v2h3v-2l-2-2V6l2 1V5L8 1z"/></svg>`,
		BadgeCSS:    "background: linear-gradient(135deg, #5d4037, #3e2723); color: #ffe0b2;",
		PlayerNoun:  "Vikings",
	},
}

func RegisterGameInfo(info GameInfo) {
	gameInfoRegistry[info.Type] = info
}

func GetGameInfo(gameType string) GameInfo {
	if info, ok := gameInfoRegistry[gameType]; ok {
		return info
	}
	return GameInfo{
		Type:        gameType,
		DisplayName: gameType,
		Icon:        "",
		BadgeCSS:    "background: #666; color: #fff;",
		PlayerNoun:  "Players",
	}
}

func AllGameInfo() []GameInfo {
	infos := make([]GameInfo, 0, len(gameInfoRegistry))
	for _, info := range gameInfoRegistry {
		infos = append(infos, info)
	}
	return infos
}
