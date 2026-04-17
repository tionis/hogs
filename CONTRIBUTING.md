# Contributing to HOGS

## Adding a New Game Type

HOGS supports multiple game types through the `GameQuerier` interface. To add support for a new game (e.g., Valheim, Rust, ARK), follow these steps:

### 1. Implement the GameQuerier interface

Create a new file in `query/<gamename>.go`:

```go
package query

import (
    "github.com/tionis/hogs/database"
)

type MyGameQuerier struct{}

func (q *MyGameQuerier) Query(server *database.Server) (*ServerStatus, error) {
    // Query the game server using its protocol/API
    // Populate and return a ServerStatus
}
```

The `ServerStatus` struct (defined in `query/types.go`) has these fields:

| Field | Type | Description |
|-------|------|-------------|
| `Online` | `bool` | Whether the server is reachable |
| `Players` | `int` | Current player count |
| `MaxPlayers` | `int` | Maximum player capacity |
| `PlayerList` | `[]Player` | List of online players (with `Name` and optional `ID`) |
| `Version` | `string` | Server/game version string |
| `MapName` | `string` | Current map/session name |
| `ServerMessage` | `string` | Server MOTD or announcement |
| `LastUpdated` | `time.Time` | Timestamp of this query |
| `Error` | `string` | Error message if query failed |
| `Extras` | `map[string]interface{}` | Game-specific extra data |

Use `server.Metadata` to access any credentials or config the admin stored (e.g., API tokens, passwords).

### 2. Register the querier

Add your game type to the registry map in `query/querier.go`:

```go
var queriers = map[string]GameQuerier{
    "minecraft":    &MinecraftQuerier{},
    "satisfactory": &SatisfactoryQuerier{},
    "factorio":     &FactorioQuerier{},
    "valheim":      &ValheimQuerier{},
    "mygame":       &MyGameQuerier{},  // Add here
}
```

Or use `RegisterQuerier("mygame", &MyGameQuerier{})` at init time.

### 3. Add the database migration

Create a migration file is not needed — the `game_type` column is a free-text field. Just make sure your game type string is consistent.

### 4. Add the badge styling

Add a CSS class in `web/templates/base.html`:

```css
.game-badge-mygame { background: #hexcolor; color: #fff; }
```

### 5. Add the game icon

Add an SVG icon entry to the `gameIcon` template function in `web/funcmap.go`:

```go
"mygame": `<svg class="game-icon" viewBox="0 0 16 16" fill="currentColor">...</svg>`,
```

### 6. Add the admin dropdown option

In `web/templates/admin.html`, add an `<option>` to both the Add and Edit modals' game type `<select>`:

```html
<option value="mygame">My Game</option>
```

### 7. Add per-game detail sections (optional)

In `web/templates/server.html`, add a conditional block for your game:

```html
{{if eq .Server.GameType "mygame"}}
<div id="game-detail-mygame" style="display:none;" class="mt-3">
  <!-- Game-specific info panels -->
</div>
{{end}}
```

And update the status fetch in the `<script>` block to populate it.

### 8. Update the status poller text (optional)

In `web/templates/base.html`, add a `case` to the `switch (gameType)` block in the status poller for game-appropriate text.

### 9. Document metadata keys

If your querier requires metadata keys (like `api_token` for Satisfactory or `rcon_password` for Factorio), document them in the README's game-specific section and in the admin modal's metadata help text.
