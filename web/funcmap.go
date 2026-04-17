package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
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
			icons := map[string]string{
				"minecraft":    `<svg class="game-icon" viewBox="0 0 16 16" fill="currentColor"><rect x="4" y="4" width="3" height="3"/><rect x="9" y="4" width="3" height="3"/><rect x="4" y="9" width="3" height="3"/><rect x="9" y="9" width="3" height="3"/></svg>`,
				"satisfactory": `<svg class="game-icon" viewBox="0 0 16 16" fill="currentColor"><circle cx="8" cy="8" r="3"/><circle cx="8" cy="2" r="1.5"/><circle cx="8" cy="14" r="1.5"/><circle cx="2" cy="8" r="1.5"/><circle cx="14" cy="8" r="1.5"/></svg>`,
				"factorio":     `<svg class="game-icon" viewBox="0 0 16 16" fill="currentColor"><polygon points="8,1 15,8 8,15 1,8"/></svg>`,
				"valheim":      `<svg class="game-icon" viewBox="0 0 16 16" fill="currentColor"><path d="M8 1L3 5v2l2-1v4l-2 2v2h3v-2l1-1 1 1v2h3v-2l-2-2V6l2 1V5L8 1z"/></svg>`,
			}
			if icon, ok := icons[s]; ok {
				return template.HTML(icon)
			}
			return ""
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
	}
}
