package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"strings"

	"github.com/tionis/hogs/query"
)

func sharedFuncMap() template.FuncMap {
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
			return template.HTML(query.GetGameInfo(s).Icon)
		},
		"gameBadgeCSS": func(s string) string {
			return query.GetGameInfo(s).BadgeCSS
		},
		"gamePlayerNoun": func(s string) string {
			return query.GetGameInfo(s).PlayerNoun
		},
		"gameDisplayName": func(s string) string {
			return query.GetGameInfo(s).DisplayName
		},
		"gameNounMapJS": func() template.JS {
			infos := query.AllGameInfo()
			m := make(map[string]string)
			for _, info := range infos {
				m[info.Type] = info.PlayerNoun
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
