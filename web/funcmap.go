package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"strings"

	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/gametypes"
	"github.com/tionis/hogs/query"
)

func sharedFuncMap(stores ...*database.Store) template.FuncMap {
	var store *database.Store
	if len(stores) > 0 {
		store = stores[0]
	}
	gameInfo := func(gameType string) query.GameInfo {
		info := query.GetGameInfo(gameType)
		if store == nil {
			return info
		}
		custom, err := store.GetGameType(gameType)
		if err != nil || custom == nil {
			return info
		}
		info.DisplayName = custom.DisplayName
		info.PlayerNoun = custom.PlayerNoun
		info.BadgeCSS = fmt.Sprintf("background: %s; color: #fff;", custom.AccentColor)
		if custom.Icon != "" {
			info.Icon = `<span class="game-icon">` + template.HTMLEscapeString(custom.Icon) + `</span>`
		}
		return info
	}
	return template.FuncMap{
		"json": func(v interface{}) string {
			b, _ := json.Marshal(v)
			return string(b)
		},
		"firstLine": func(s string) string {
			if idx := strings.Index(s, "\n"); idx != -1 {
				return s[:idx]
			}
			return s
		},
		"nl2br": func(s string) template.HTML {
			return template.HTML(strings.ReplaceAll(template.HTMLEscapeString(s), "\n", "<br>"))
		},
		"title": func(s string) string {
			if len(s) == 0 {
				return s
			}
			return strings.ToUpper(s[:1]) + s[1:]
		},
		"gameIcon": func(s string) template.HTML {
			return template.HTML(gameInfo(s).Icon)
		},
		"gameBadgeCSS": func(s string) string {
			return gameInfo(s).BadgeCSS
		},
		"gamePlayerNoun": func(s string) string {
			return gameInfo(s).PlayerNoun
		},
		"gameDisplayName": func(s string) string {
			return gameInfo(s).DisplayName
		},
		"gameStatusProtocol": func(s string) string {
			if store == nil {
				if driver, ok := gametypes.Embedded(s); ok {
					return driver.StatusProtocol
				}
				return ""
			}
			return store.ResolveGameDriver(s).StatusProtocol
		},
		"gameSupportsWhitelist": func(s string) bool {
			if store == nil {
				driver, ok := gametypes.Embedded(s)
				return ok && driver.SupportsWhitelist()
			}
			return store.ResolveGameDriver(s).SupportsWhitelist()
		},
		"gameDetails": func(s string) []gametypes.DetailField {
			if store == nil {
				driver, _ := gametypes.Embedded(s)
				return driver.Details
			}
			return store.ResolveGameDriver(s).Details
		},
		"gameNounMapJS": func() template.JS {
			infos := query.AllGameInfo()
			m := make(map[string]string)
			for _, info := range infos {
				m[info.Type] = gameInfo(info.Type).PlayerNoun
			}
			if store != nil {
				custom, _ := store.ListGameTypes()
				for _, info := range custom {
					m[info.Slug] = info.PlayerNoun
				}
			}
			b, _ := json.Marshal(m)
			return template.JS(b)
		},
		"dict": func(values ...interface{}) (map[string]interface{}, error) {
			if len(values)%2 != 0 {
				return nil, fmt.Errorf("invalid dict call")
			}
			dict := make(map[string]interface{}, len(values)/2)
			for i := 0; i < len(values); i += 2 {
				key, ok := values[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict keys must be strings")
				}
				dict[key] = values[i+1]
			}
			return dict, nil
		},
		"inList": func(item string, list []string) bool {
			for _, v := range list {
				if v == item {
					return true
				}
			}
			return false
		},
		"sub": func(a, b int) int {
			return a - b
		},
	}
}
