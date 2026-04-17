---
title: HOGS Roadmap
tags:
  - roadmap
  - planning
---

# HOGS Roadmap

> [!info] Current State
> HOGS is a Go web application that serves as a landing page for game servers. It features OIDC auth with role-based access control (admin/user from OIDC groups), Pterodactyl integration for server actions, theme-aware backgrounds with immutable caching, multi-game status queries (Minecraft, Satisfactory, Factorio, Valheim), and an admin UI.

## Phase 1: More Game Types

**Effort**: Low per game (1-2 hours each)

The `GameQuerier` interface and `CONTRIBUTING.md` make adding games straightforward. New games need: a querier implementation, a registry entry, CSS badge, SVG icon, admin/background dropdown options, and optionally a status poller case.

### Planned Games

| Game | Protocol | Notes |
|------|----------|-------|
| Valheim | Steam A2S query (UDP) | Most requested. Standard query protocol. |
| Ark: Survival Ascended | Steam A2S | Same approach, different default port. |
| Palworld | Steam A2S or community REST API | |
| Counter-Strike 2 | A2S + RCON | Similar pattern to Minecraft. |

### Refactoring

With 6+ game types, the codebase needs cleanup:

- **Querier registry**: ✅ Done — `map[string]GameQuerier` registry with `RegisterQuerier()` and `RegisteredGameTypes()`.
- **Template game data**: Move game metadata (icon, badge color, status text template) into a central map or struct so templates and status poller JS don't need per-game switch/cases.

## Phase 2: Role-Based Access Control

**Prerequisite for Phase 3.**

Currently any authenticated user is treated as admin. This phase adds proper roles.

### 2a: User Table and OIDC Groups

#### Config

```
OIDC_ADMIN_GROUP=admins      # group claim value that grants admin role
OIDC_USER_GROUP=users        # group claim value that grants user role (optional)
OIDC_GROUPS_CLAIM=groups     # claim path in ID token to extract groups from
```

#### DB Migration 000010

```sql
CREATE TABLE users (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    email       TEXT NOT NULL UNIQUE,
    role        TEXT NOT NULL DEFAULT 'user',   -- 'admin' or 'user'
    first_seen  DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_login  DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

#### Auth Changes

- `auth/auth.go`: Extract groups from OIDC ID token claims using the configurable `OIDC_GROUPS_CLAIM`.
- On callback: auto-provision user in `users` table if first login, map OIDC group to role.
- Store `role` in session alongside `email`.
- Add `GetUserRole(r) string` method to `Authenticator`.
- Add `RequireRole(roles ...string)` middleware that checks session role.
- Admins can still override roles in the DB (IdP groups are only applied at first login).

#### Impact on Existing Code

- All current `authenticator.Middleware(...)` calls (admin routes) should switch to `authenticator.RequireRole("admin")`.
- New `RequireRole("admin", "user")` middleware for user-accessible routes.
- Public routes remain unchanged.

### 2b: Role-Based UI

#### Templates

- Navbar: "Admin" link only visible to admins (✅ implemented).
- Server detail: conditionally show Pterodactyl action buttons based on `allowed_actions` (✅ implemented).
- Admin panel: visible only to admin role (✅ implemented).

#### New Pages

- **My Servers** (`/servers`): filtered list of servers the user has actions on. (Not yet implemented.)

## Phase 3: Pterodactyl Integration

**Design decisions (confirmed)**:

- **Mapping**: Separate `pterodactyl_servers` table linking HOGS server IDs to Pterodactyl server UUIDs.
- **API scope**: Application API only (admin-level token). HOGS manages everything centrally.
- **User actions**: Configurable per-server via `allowed_actions` and `pterodactyl_commands` tables.
- **Panel scope**: Single panel (one URL + key in config).

### Config

```
PTERODACTYL_URL=https://panel.example.com
PTERODACTYL_APP_KEY=xxxx
```

### DB Migration 000011

```sql
CREATE TABLE pterodactyl_servers (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id         INTEGER NOT NULL,              -- FK to servers table
    ptero_server_id   TEXT NOT NULL,                  -- Pterodactyl server UUID
    allowed_actions   TEXT NOT NULL DEFAULT '[]',    -- JSON array
    UNIQUE(server_id),
    FOREIGN KEY(server_id) REFERENCES servers(id) ON DELETE CASCADE
);

CREATE TABLE pterodactyl_commands (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id         INTEGER NOT NULL,              -- FK to servers table
    command           TEXT NOT NULL,                  -- e.g. "seed", "time set"
    display_name      TEXT NOT NULL,                  -- e.g. "Random Seed", "Set Time"
    FOREIGN KEY(server_id) REFERENCES servers(id) ON DELETE CASCADE
);
```

`allowed_actions` is a JSON array. Possible values:

| Value | Meaning |
|-------|---------|
| `"whitelist"` | User can add/remove players from the whitelist |
| `"start"` | User can start the server |
| `"stop"` | User can stop the server |
| `"restart"` | User can restart the server |
| `"command:<name>"` | User can run a specific approved command |

### New Package: `pterodactyl/`

```
pterodactyl/
  client.go     -- HTTP client, Application API auth, request helpers
  servers.go    -- ListServers, GetServer, StartServer, StopServer, RestartServer, SendCommand
```

All requests use the Application API (`/api/application/servers/...`) with the admin token.

### API Endpoints

**Admin-only** (configure Pterodactyl linkage):

| Route | Method | Purpose |
|-------|--------|---------|
| `/admin/pterodactyl/link` | POST | Link a server to a Pterodactyl server UUID |
| `/admin/pterodactyl/unlink` | POST | Remove Pterodactyl linkage |
| `/admin/pterodactyl/commands/add` | POST | Add an approved command for a linked server |
| `/admin/pterodactyl/commands/delete` | POST | Remove an approved command |

**User-accessible** (role-checked, per-server permission-checked):

| Route | Method | Purpose |
|-------|--------|---------|
| `/servers/{serverName}/action` | POST | Start/stop/restart server (action in form data) |
| `/servers/{serverName}/command` | POST | Send an approved command |

### UI Changes

#### Server Detail Page

New "Server Actions" card for authenticated users (visible when Pterodactyl is configured and the server is linked):

- Power buttons (Start / Stop / Restart) — shown if corresponding action is in `allowed_actions` (✅ implemented)
- Command buttons — one button per entry in `pterodactyl_commands`, shown if `"command:<name>"` is in `allowed_actions` (✅ implemented)
- Whitelist panel — shown if `"whitelist"` is in `allowed_actions` (not yet implemented)

#### Admin Panel

New Pterodactyl section per server (only visible if `PTERODACTYL_URL` is configured) (✅ implemented):

- Link/unlink a Pterodactyl server UUID
- Edit `allowed_actions` as a JSON array
- Add/remove approved commands with display names

### Whitelisting Approach

For Minecraft servers, whitelisting uses the existing `MinecraftQuerier` RCON connection or Pterodactyl command sending (`whitelist add <player>`). For other games, Pterodactyl commands are used (e.g. Valheim has no native whitelist API, but has mods that respond to slash commands).

The `pterodactyl/whitelist.go` module will be game-aware:

- Minecraft: Send `whitelist add/remove <player>` via Pterodactyl command or RCON
- Other games: Send game-specific whitelist commands via Pterodactyl command API

## Phase 4: Future Integrations

Ideas for later phases, prioritized by likely demand.

### Discord Webhooks

**Effort**: Low

- Config: `DISCORD_WEBHOOK_URL` per server (stored in `metadata`)
- Events: server start/stop, player join/leave (if Pterodactyl WebSocket is available)
- Implementation: simple ` POST to webhook URL with JSON payload

### Player History Charts

**Effort**: Medium

- New `player_history` table: `id, server_id, players_online, timestamp`
- Periodic sampling (every 60s) during status cache refresh
- Chart.js graph on server detail page (last 24h, 7d)
- Could use the existing status cache update cycle as the sampling trigger

### Server Metrics (CPU/RAM)

**Effort**: Medium

- Pterodactyl Application API provides resource usage via `/api/application/servers/{id}/resources`
- Could poll on the same cache cycle as game queries
- Display as live graphs or simple indicators on server detail page
- May require WebSocket for truly live updates (Phase 5 territory)

### Auto-Mod Updates

**Effort**: Medium

- Watch mod URLs (`mod_url` field) for new versions
- Notify admins via UI badge or Discord webhook
- Could auto-update with admin approval

### Multi-Panel Support

**Effort**: Low

- If needed later, move `PTERODACTYL_URL` and `PTERODACTYL_APP_KEY` from global config to per-server `metadata` fields
- Each server links to its own panel
- Requires minor changes to `pterodactyl/` client to accept config per-request

## Implementation Order

Suggested sequence based on dependencies and impact:

```
1. Phase 2a — users table + OIDC groups + role middleware        ✅ DONE
2. Phase 1  — Valheim querier + registry refactor                ✅ DONE
3. Phase 2b — role-based UI (hide/show elements by role)         ✅ DONE
4. Phase 3a — pterodactyl/ client + DB tables                    ✅ DONE
5. Phase 3a — Pterodactyl admin UI (link, actions, commands)    ✅ DONE
6. Phase 3b — user-facing server actions (power, whitelist, commands) ✅ DONE
7. Phase 4  — pick based on demand (Discord webhooks likely first)
```

## Architecture Notes

### Current Stack

| Layer | Technology |
|--------|-----------|
| Language | Go 1.24+ |
| Router | gorilla/mux |
| Database | SQLite + golang-migrate |
| Templates | Go html/template (embedded in binary) |
| Frontend | Bootstrap 5, vanilla JS |
| Auth | OIDC via gorilla/sessions |
| Container | Podman/Docker via Containerfile |

### Key File Locations

| Path | Purpose |
|------|---------|
| `main.go` | Entry point, route wiring, graceful shutdown |
| `database/database.go` | Server CRUD, Background CRUD, Settings, Users, Pterodactyl CRUD |
| `database/migrations/` | SQL migrations |
| `query/` | GameQuerier interface + implementations |
| `pterodactyl/` | Pterodactyl Application API client |
| `auth/auth.go` | OIDC auth, sessions, role middleware |
| `api/server_handler.go` | Server status, mods, background API handlers |
| `api/pterodactyl_handler.go` | Pterodactyl link/unlink, actions, commands handlers |
| `web/handler.go` | All page handlers, Pterodactyl data passing |
| `web/funcmap.go` | Shared template functions (json, firstLine, nl2br, title, gameIcon, dict, inList) |
| `web/templates/` | HTML templates |
| `config/config.go` | Env var loading |
