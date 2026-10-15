package events

// Stream is one HOGS event stream below /events/<producer>/.
type Stream struct {
	Name             string
	Title            string
	Description      string
	RetentionSeconds int64 // 0 keeps records forever
}

// Type is one versioned event type and the JSON Schema of its data.
type Type struct {
	Type        string
	Stream      string
	Title       string
	Description string
	Schema      map[string]interface{}
	Example     map[string]interface{}
}

const (
	ActionRecorded  = "dev.tionis.hogs.action.recorded.v1"
	ServerStatus    = "dev.tionis.hogs.server.status.changed.v1"
	ServerOccupancy = "dev.tionis.hogs.server.players.changed.v1"
)

var Streams = []Stream{
	{
		Name:             "actions",
		Title:            "HOGS audited actions",
		Description:      "Every action HOGS records in its audit log: lifecycle, updates, backups, restores, whitelist and access changes, with the actor and result.",
		RetentionSeconds: 0,
	},
	{
		Name:             "servers",
		Title:            "HOGS server observations",
		Description:      "Observed server state: online/offline transitions and player-count changes.",
		RetentionSeconds: 90 * 24 * 3600,
	},
}

const jsonSchema = "https://json-schema.org/draft/2020-12/schema"

var serverProperties = map[string]interface{}{
	"server":      map[string]interface{}{"type": "string", "description": "Immutable HOGS server id (management id)."},
	"server_name": map[string]interface{}{"type": "string", "description": "Display name at the time of the event."},
}

func withServer(properties map[string]interface{}) map[string]interface{} {
	merged := map[string]interface{}{}
	for key, value := range serverProperties {
		merged[key] = value
	}
	for key, value := range properties {
		merged[key] = value
	}
	return merged
}

var Types = []Type{
	{
		Type:        ActionRecorded,
		Stream:      "actions",
		Title:       "Action recorded",
		Description: "HOGS performed, refused, or failed an audited action. `result` is the audit outcome (for example success, failed, denied, blocked).",
		Schema: map[string]interface{}{
			"$schema":              jsonSchema,
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"server", "server_name", "action", "actor", "result", "origin"},
			"properties": withServer(map[string]interface{}{
				"action": map[string]interface{}{"type": "string", "description": "Capability or action name, for example start, stop, server.update, backup.restore."},
				"actor":  map[string]interface{}{"type": "string", "description": "Username, api-key:<name>, or the automation that acted."},
				"result": map[string]interface{}{"type": "string"},
				"reason": map[string]interface{}{"type": "string"},
				"origin": map[string]interface{}{"type": "string", "description": "Where the action came from: web, api, cron, ..."},
				"params": map[string]interface{}{"type": "object", "additionalProperties": map[string]interface{}{"type": "string"}},
			}),
		},
		Example: map[string]interface{}{
			"server": "valheim", "server_name": "Valheim", "action": "server.update", "actor": "eric",
			"result": "success", "reason": "server update completed", "origin": "web",
			"params": map[string]interface{}{"safetySnapshot": "b482a047"},
		},
	},
	{
		Type:        ServerStatus,
		Stream:      "servers",
		Title:       "Server came online or went offline",
		Description: "The observed process state of a managed server changed.",
		Schema: map[string]interface{}{
			"$schema":              jsonSchema,
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"server", "server_name", "online"},
			"properties": withServer(map[string]interface{}{
				"online":        map[string]interface{}{"type": "boolean"},
				"players":       map[string]interface{}{"type": "integer", "minimum": 0},
				"max_players":   map[string]interface{}{"type": "integer", "minimum": 0},
				"players_known": map[string]interface{}{"type": "boolean"},
				"version":       map[string]interface{}{"type": "string"},
			}),
		},
		Example: map[string]interface{}{
			"server": "valheim", "server_name": "Valheim", "online": true,
			"players": 0, "max_players": 10, "players_known": true, "version": "0.221.4",
		},
	},
	{
		Type:        ServerOccupancy,
		Stream:      "servers",
		Title:       "Player count changed",
		Description: "The known number of players on an online server changed. `player_names` is present only when the game reports names.",
		Schema: map[string]interface{}{
			"$schema":              jsonSchema,
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"server", "server_name", "players", "previous_players"},
			"properties": withServer(map[string]interface{}{
				"players":          map[string]interface{}{"type": "integer", "minimum": 0},
				"previous_players": map[string]interface{}{"type": "integer", "minimum": 0},
				"max_players":      map[string]interface{}{"type": "integer", "minimum": 0},
				"player_names":     map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			}),
		},
		Example: map[string]interface{}{
			"server": "valheim", "server_name": "Valheim", "players": 2, "previous_players": 1,
			"max_players": 10, "player_names": []interface{}{"Ragnar", "Lagertha"},
		},
	},
}

var typeByName = func() map[string]Type {
	result := make(map[string]Type, len(Types))
	for _, definition := range Types {
		result[definition.Type] = definition
	}
	return result
}()
