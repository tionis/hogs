# HOGS Automation System: Design & Implementation

> **Note**: This document is a historical design reference. The automation system has been fully implemented. For the canonical roadmap (including remaining improvements), see **`docs/ROADMAP.md`**.

The sections below document the design as it was originally planned and implemented. They remain useful as reference for the data model, expression language, and API.

---

## Data Model

### Command Schemas (replaces `pterodactyl_commands`)

The existing `pterodactyl_commands` table is replaced with `command_schemas` that support typed parameters:

```sql
CREATE TABLE command_schemas (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id   INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,           -- internal name, e.g. "give_item"
    display_name TEXT NOT NULL,          -- UI label, e.g. "Give Item"
    template    TEXT NOT NULL,           -- e.g. "give {player} {item} {count}"
    params      TEXT NOT NULL DEFAULT '{}', -- JSON schema for parameters
    acl_rule    TEXT NOT NULL DEFAULT '',   -- optional per-command ACL override (expr)
    enabled     INTEGER NOT NULL DEFAULT 1
);

-- params JSON format:
-- {
--   "player": { "type": "string", "pattern": "^[a-zA-Z0-9_]{3,16}$", "required": true },
--   "item":   { "type": "enum", "values": ["diamond", "iron_ingot"], "required": true },
--   "count":  { "type": "int", "min": 1, "max": 64, "required": false, "default": 1 }
-- }
```

Parameter types: `string`, `int`, `float`, `enum`, `bool`.

Validation rules per type:
- `string`: optional `pattern` (Go regex), optional `minLength`, `maxLength`
- `int`/`float`: optional `min`, `max`
- `enum`: required `values` array
- `bool`: accepts `true`/`false`/`1`/`0`

Template rendering uses `{paramName}` placeholders. Only validated parameters are substituted. Missing optional params with defaults use the default value.

### Server Tags

```sql
CREATE TABLE server_tags (
    server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    tag       TEXT NOT NULL,
    PRIMARY KEY (server_id, tag)
);
```

Tags classify servers for constraint matching. Examples: `minecraft`, `game`, `highmem`, `java`, `production`.

### Constraints

```sql
CREATE TABLE constraints (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL UNIQUE,   -- e.g. "one_game_per_node"
    description TEXT NOT NULL DEFAULT '',
    condition   TEXT NOT NULL,          -- expr expression, must return bool
    strategy    TEXT NOT NULL DEFAULT 'deny',  -- deny | queue | stop_oldest
    priority    INTEGER NOT NULL DEFAULT 0,    -- higher = evaluated first
    enabled     INTEGER NOT NULL DEFAULT 1
);
```

Constraint evaluation environment exposes:

| Variable | Type | Description |
|----------|------|-------------|
| `action` | `string` | The requested action (`start`, `stop`, `restart`, `command:name`) |
| `server` | `ServerEnv` | Target server: `.ID`, `.Name`, `.GameType`, `.Tags`, `.Node`, `.Running` |
| `servers` | `[]ServerEnv` | All known servers with their current running state |
| `user` | `UserEnv` | Requesting user: `.Email`, `.Role` |
| `time` | `TimeEnv` | `.Hour`, `.Weekday`, `.Now` (Go `time.Time`) |

**Helper functions available in `expr`**:

| Function | Signature | Description |
|----------|-----------|-------------|
| `hasTag` | `(ServerEnv, string) bool` | Check if server has a tag |
| `serversOnNode` | `(string) []ServerEnv` | Get servers on a node |
| `runningOnNode` | `(string) []ServerEnv` | Get running servers on a node |
| `countRunning` | `([]ServerEnv) int` | Count running servers in a list |
| `filterByTag` | `([]ServerEnv, string) []ServerEnv` | Filter servers by tag |
| `weekday` | `(string) time.Weekday` | Parse weekday name |

**Example constraint expressions**:

```
// Only one game server per node at a time
countRunning(filterByTag(serversOnNode(server.Node), "game")) < 1

// Only one minecraft server per node
countRunning(filterByTag(serversOnNode(server.Node), "minecraft")) < 1

// Don't allow restarts on Saturday maintenance windows
!(time.Weekday == weekday("saturday") && time.Hour >= 2 && time.Hour < 6)

// Only admins can start servers with the "production" tag
!hasTag(server, "production") || user.Role == "admin"
```

### ACL Rules (replaces `allowed_actions`)

The flat `allowed_actions` JSON array is replaced by an expression-based ACL rule. Backward compatibility is maintained: if `acl_rule` is empty, the system falls back to parsing `allowed_actions` in the old format.

**Example ACL rules**:

```
// Allow only start/stop/restart for users, everything for admins
user.Role == "admin" || action in ["start", "stop", "restart"]

// Allow commands only for users in a specific tag
action in ["start", "stop"] || (hasTag(server, "minecraft") && user.Role == "user" && action matches "^command:")

// Whitelist only for minecraft servers
action != "whitelist" || server.GameType == "minecraft"
```

### Cron Jobs

```sql
CREATE TABLE cron_jobs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL,
    schedule    TEXT NOT NULL,           -- cron expression, e.g. "0 4 * * *"
    server_name TEXT NOT NULL,           -- target server name
    action      TEXT NOT NULL,           -- "start", "stop", "restart", "command:name"
    params      TEXT NOT NULL DEFAULT '{}', -- JSON params for parameterized commands
    acl_rule    TEXT NOT NULL DEFAULT '',   -- optional ACL override for cron execution
    enabled     INTEGER NOT NULL DEFAULT 1,
    last_run    TEXT,                    -- ISO8601 timestamp of last execution
    next_run    TEXT                     -- ISO8601 timestamp of next scheduled run
);
```

Cron jobs execute as a system user with role `"system"`. They flow through the same constraint and ACL pipeline. If a constraint blocks a cron job:

- `deny` strategy: job is skipped, logged as skipped
- `queue` strategy: job is retried every 30s up to 5 minutes, then skipped
- `stop_oldest` strategy: conflicting server is stopped, then action proceeds

### Audit Log

```sql
CREATE TABLE audit_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp   TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    user_email  TEXT NOT NULL,
    server_name TEXT NOT NULL,
    action      TEXT NOT NULL,
    params      TEXT NOT NULL DEFAULT '{}',
    result      TEXT NOT NULL,           -- "allowed", "denied", "blocked", "error"
    reason      TEXT NOT NULL DEFAULT '', -- human-readable explanation
    source      TEXT NOT NULL DEFAULT 'user' -- "user", "cron", "api"
);
```

Every action attempt is logged regardless of outcome.

---

## Constraint Strategies

| Strategy | HTTP Response | Behavior |
|----------|--------------|----------|
| `deny` | `409 Conflict` | Action is rejected. Response body includes constraint name and reason. |
| `queue` | `202 Accepted` | Action is queued. Background goroutine retries every 30s for up to 5 minutes. If still blocked, logs as skipped. |
| `stop_oldest` | Proceeds | The longest-running conflicting server is stopped first, then the action proceeds. Logs both actions. |

Constraints are evaluated in priority order (highest first). The first constraint that returns `false` determines the strategy applied.

---

## HTTP API

### Automation Endpoints

| Route | Method | Auth | Description |
|-------|--------|------|-------------|
| `/help` | GET | Public | Rendered help page |
| `/help/api.md` | GET | Public | Markdown help for LLMs |
| `/admin/commands/{serverId}` | GET | Admin | Manage parameterized commands |
| `/admin/commands/add` | POST | Admin | Create command schema |
| `/admin/commands/update` | POST | Admin | Update command schema |
| `/admin/commands/delete` | POST | Admin | Delete command schema |
| `/admin/constraints` | GET | Admin | Manage constraints |
| `/admin/constraints/add` | POST | Admin | Create constraint |
| `/admin/constraints/update` | POST | Admin | Update constraint |
| `/admin/constraints/delete` | POST | Admin | Delete constraint |
| `/admin/cron` | GET | Admin | Manage cron jobs |
| `/admin/cron/add` | POST | Admin | Create cron job |
| `/admin/cron/update` | POST | Admin | Update cron job |
| `/admin/cron/delete` | POST | Admin | Delete cron job |
| `/admin/tags/{serverId}` | POST | Admin | Update server tags |
| `/admin/acl/{serverId}` | POST | Admin | Update server ACL rule |
| `/api/audit` | GET | Admin | Query audit log |
| `/api/constraints/test` | POST | Admin | Test a constraint expression |

### SCIM 2.0 Endpoints

| Route | Method | Auth | Description |
|-------|--------|------|-------------|
| `/scim/v2/ServiceProviderConfig` | GET | Bearer | SCIM service provider config |
| `/scim/v2/Schemas` | GET | Bearer | Schema discovery |
| `/scim/v2/Users` | GET/POST | Bearer | List/Create users |
| `/scim/v2/Users/{id}` | GET/PUT/PATCH/DELETE | Bearer | User CRUD |
| `/scim/v2/Groups` | GET/POST | Bearer | List/Create groups |
| `/scim/v2/Groups/{id}` | GET/PUT/PATCH/DELETE | Bearer | Group CRUD |

### Agent Endpoints

| Route | Method | Auth | Description |
|-------|--------|------|-------------|
| `/agent/ws` | GET (WS) | Token | Agent WebSocket connection |
| `/api/agents` | GET/POST | Admin | List/Create agents |
| `/api/agents/delete` | POST | Admin | Delete agent |
| `/api/agents/{serverName}/files` | GET | Admin | List files |
| `/api/agents/{serverName}/files/read` | GET | Admin | Read file |
| `/api/agents/{serverName}/files/write` | POST | Admin | Write file |
| `/api/agents/{serverName}/files/delete` | POST | Admin | Delete file |
| `/api/agents/{serverName}/files/mkdir` | POST | Admin | Create directory |
| `/api/agents/{serverName}/backup/create` | POST | Admin | Create backup |
| `/api/agents/{serverName}/backup/restore` | POST | Admin | Restore backup |
| `/api/agents/{serverName}/backup/list` | POST | Admin | List backups |

### Auth Endpoints

| Route | Method | Description |
|-------|--------|-------------|
| `/login` | GET | OIDC login redirect |
| `/logout` | GET | Destroy session + redirect |
| `/auth/callback` | GET | OIDC callback |
| `/auth/backchannel-logout` | POST | OIDC back-channel logout |

---

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `HOGS_CRON_ENABLED` | `true` | Enable/disable cron scheduler |
| `HOGS_CRON_QUEUE_RETRY_INTERVAL` | `30` | Seconds between retries for queued actions |
| `HOGS_CRON_QUEUE_MAX_RETRY` | `10` | Max retries before giving up on queued actions |
| `HOGS_AUDIT_LOG_RETENTION_DAYS` | `90` | Days to retain audit log entries |
| `HOGS_PTERO_NODE_REFRESH_INTERVAL` | `300` | Seconds between Pterodactyl node info refreshes |
| `HOGS_AGENT_ENABLED` | `true` | Enable agent WebSocket endpoint |
| `HOGS_AGENT_HEARTBEAT_SEC` | `30` | Agent heartbeat interval |
| `SCIM_ENABLED` | `false` | Enable SCIM 2.0 endpoints |
| `SCIM_BEARER_TOKEN` | `""` | Bearer token for SCIM auth |
| `OIDC_BACKCHANNEL_LOGOUT` | `true` | Enable OIDC back-channel logout |

---

## Dependencies

| Package | Purpose |
|---------|---------|
| `github.com/expr-lang/expr` | Expression evaluation engine for ACLs and constraints |
| `github.com/robfig/cron/v3` | Cron scheduling |
| `github.com/gorilla/websocket` | Agent WebSocket connections |
| `github.com/coreos/go-oidc/v3` | OIDC authentication |
| `github.com/gorilla/sessions` | Session management |
| `github.com/golang-migrate/migrate/v4` | Database migrations |
| `github.com/gorilla/mux` | HTTP routing |
| `github.com/mattn/go-sqlite3` | SQLite driver |

---

## Security Considerations

- **Expression sandboxing**: `expr` runs in a sandboxed VM with no access to filesystem, network, or Go stdlib. Only explicitly exposed variables and functions are available.
- **Param validation**: All command parameters are validated against their schema before template rendering. No raw user input reaches Pterodactyl or agents.
- **Template injection**: Template rendering uses simple `{name}` substitution, not full template engines. Values are not re-interpreted.
- **Audit logging**: Every action attempt is recorded, including denied/blocked attempts.
- **System user**: Cron runs as a `"system"` user, auditable separately from human users.
- **Help endpoint**: The `/help/api.md` endpoint does not expose sensitive metadata (API tokens, passwords).
- **Back-channel logout**: OIDC back-channel logout invalidates sessions server-side when triggered by the IdP.
- **SCIM**: Bearer token auth, group membership changes trigger immediate session invalidation.
- **Agent auth**: Per-agent token, stored in DB, verified on WebSocket connect.

---

## Backward Compatibility

- Existing `pterodactyl_commands` are **not** dropped. The migration creates `command_schemas` alongside.
- Existing `allowed_actions` JSON continues to work when `acl_rule` is empty.
- All existing routes and handlers remain functional until explicitly migrated.
- The admin UI shows both legacy and new options during transition.

---

## Future Extensions

- **Pipeline steps**: Composable pre/post-action steps (e.g., "warn players in chat, wait 5 min, then restart") defined as a step chain in config
- **Condition-based cron**: Cron jobs with `expr` conditions that must also pass before execution
- **Metrics**: Prometheus-style metrics for command execution, constraint evaluations, cron runs