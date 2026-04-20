# HOGS Automation System: Design & Implementation

## Overview

This document describes the HOGS Automation System — a declarative rule engine that adds parameterized commands, expression-based ACLs, resource constraints, and cron scheduling to HOGS. The system replaces the current flat `allowed_actions` JSON array and static command model with a flexible, composable, and safe alternative. No embedded scripting runtime is required.

## Goals

1. **Parameterized commands** — Commands with typed parameters, validation, and template-based rendering
2. **Expression-based ACLs** — Context-aware access control using `expr` expressions instead of flat allowlists
3. **Resource constraints** — Node/tag-based run conditions (e.g., "only one Minecraft server per node")
4. **Cron scheduling** — Time-based server actions that flow through the same constraint/ACL pipeline
5. **Self-documenting** — Embedded help page + `/help/api.md` endpoint returning machine-readable markdown for LLM agents

## Non-Goals

- Arbitrary scripting (JS runtime, Lua, etc.) — declarative rules cover 90% of use cases
- Per-user permission granularity beyond what `expr` can express
- Distributed locking across multiple HOGS instances

---

## Architecture

```
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│  User Request│     │  Cron Trigger│     │  API Request │
└──────┬───────┘     └──────┬───────┘     └──────┬───────┘
       │                    │                    │
       ▼                    ▼                    ▼
┌──────────────────────────────────────────────────────┐
│                   Action Pipeline                     │
│                                                       │
│  1. Resolve Command  ──►  Validate Params             │
│  2. Evaluate ACL     ──►  (deny? → 403)              │
│  3. Evaluate Constraints ──► (block? → strategy)      │
│  4. Execute Action   ──►  Pterodactyl API             │
│  5. Audit Log                                         │
└──────────────────────────────────────────────────────┘
```

All action paths (user-triggered, cron-triggered, API-triggered) go through the same pipeline.

---

## New Packages

| Package | Purpose |
|---------|---------|
| `engine/` | Core automation engine: constraint evaluation, ACL evaluation, action pipeline |
| `cron/` | Cron scheduler wrapping `robfig/cron/v3` with HOGS-specific job management |

## Modified Packages

| Package | Changes |
|---------|---------|
| `database/` | New tables: `command_schemas`, `constraints`, `cron_jobs`, `audit_log`, `server_tags`; Extended `pterodactyl_servers` with `acl_rule` column |
| `api/` | New `AutomationHandler`; `PterodactylHandler` routes through `engine/` |
| `web/` | New admin pages for managing commands, constraints, cron; help page |
| `config/` | New config fields for automation |
| `main.go` | Wire engine, cron, new routes |

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

Tags classify servers for constraint matching. Examples: `minecraft`, `game`, `highmem`, `java`, `production`. The Pterodactyl node a server runs on is also auto-discovered and stored:

```sql
ALTER TABLE pterodactyl_servers ADD COLUMN node TEXT NOT NULL DEFAULT '';
```

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
| `server` | `ServerEnv` | Target server: `.ID`, `.Name`, `.GameType`, `.Tags`, `.Node` |
| `servers` | `[]ServerEnv` | All known servers with their current running state |
| `user` | `UserEnv` | Requesting user: `.Email`, `.Role` |
| `time` | `TimeEnv` | `.Hour`, `.Weekday`, `.Now` (Go `time.Time`) |

**`ServerEnv`** extended fields:

| Field | Type | Description |
|-------|------|-------------|
| `.ID` | `int` | Server ID |
| `.Name` | `string` | Server name |
| `.GameType` | `string` | Game type |
| `.Tags` | `[]string` | Server tags |
| `.Node` | `string` | Pterodactyl node name |
| `.Running` | `bool` | Whether server is currently online |

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

```sql
ALTER TABLE pterodactyl_servers ADD COLUMN acl_rule TEXT NOT NULL DEFAULT '';
```

The ACL rule is an `expr` expression that returns `true` to allow or `false` to deny. It has access to the same environment as constraints, plus:

| Variable | Type | Description |
|----------|------|-------------|
| `action` | `string` | The action being requested |

**Example ACL rules**:

```
// Allow only start/stop/restart for users, everything for admins
user.Role == "admin" || action in ["start", "stop", "restart"]

// Allow commands only for users in a specific tag
action in ["start", "stop"] || (hasTag(server, "minecraft") && user.Role == "user" && action matches "^command:")

// Whitelist only for minecraft servers
action != "whitelist" || server.GameType == "minecraft"
```

**Migration strategy**: New column `acl_rule` defaults to `""`. When `acl_rule` is empty, the engine uses the legacy `allowed_actions` JSON. Admins can migrate at their own pace by setting `acl_rule` on each server.

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

When a constraint blocks an action, the strategy determines what happens:

| Strategy | HTTP Response | Behavior |
|----------|--------------|----------|
| `deny` | `409 Conflict` | Action is rejected. Response body includes constraint name and reason. |
| `queue` | `202 Accepted` | Action is queued. Background goroutine retries every 30s for up to 5 minutes. If still blocked, logs as skipped. |
| `stop_oldest` | Proceeds | The longest-running conflicting server is stopped first, then the action proceeds. Logs both actions. |

Constraints are evaluated in priority order (highest first). The first constraint that returns `false` determines the strategy applied.

---

## Action Pipeline (detailed)

```go
// engine/engine.go

type Engine struct {
    Store  *database.Store
    Config *config.Config
    Cache  *query.ServerStatusCache
    Cron   *cron.Scheduler
}

type ActionResult struct {
    Allowed bool
    Result string  // "allowed", "denied", "blocked", "queued"
    Reason string
    Status int     // HTTP status code
}

func (e *Engine) Evaluate(server *database.Server, action string, params map[string]string, user *UserEnv) *ActionResult
```

Pipeline steps:

1. **Resolve command** — if `action` starts with `command:`, look up the `command_schema`, validate params against the schema, render the template. If validation fails, return 400.
2. **Evaluate ACL** — if `acl_rule` is set, evaluate it. If empty, fall back to legacy `allowed_actions` check. If denied, return 403.
3. **Evaluate constraints** — iterate enabled constraints by priority. If any returns false, apply its strategy.
4. **Execute** — call Pterodactyl API (power action or send command).
5. **Audit** — log to `audit_log`.

---

## Help System

### Embedded Help Page

Route: `GET /help`

Server-side rendered HTML page using a new template. Contains:
- Overview of the automation system
- Parameter type reference
- ACL expression reference with examples
- Constraint expression reference with examples
- Cron syntax reference
- Available variables and functions
- Links to the markdown endpoint

### Markdown API Endpoint

Route: `GET /help/api.md`

Returns the full help content as GitHub-flavored markdown, suitable for LLM agents. This is the same content as the help page but in markdown format. The markdown is versioned and includes:

- All available actions and their parameters
- Server-specific command schemas and parameter types
- Current ACL rules (sanitized — no sensitive metadata)
- Active constraints and their descriptions
- Cron schedule format
- Expression language reference

The markdown is generated dynamically from the DB so it always reflects the current configuration. A `Server-Hogs-Help-Version` header with a content hash allows agents to cache it.

---

## HTTP API additions

| Route | Method | Auth | Handler | Description |
|-------|--------|------|---------|-------------|
| `/help` | GET | Public | `web.Help` | Rendered help page |
| `/help/api.md` | GET | Public | `api.HelpMarkdown` | Markdown help for LLMs |
| `/admin/commands/{serverId}` | GET | Admin | `web.CommandManager` | Manage parameterized commands |
| `/admin/commands/add` | POST | Admin | `api.AddCommandSchema` | Create command schema |
| `/admin/commands/update` | POST | Admin | `api.UpdateCommandSchema` | Update command schema |
| `/admin/commands/delete` | POST | Admin | `api.DeleteCommandSchema` | Delete command schema |
| `/admin/constraints` | GET | Admin | `web.ConstraintManager` | Manage constraints |
| `/admin/constraints/add` | POST | Admin | `api.AddConstraint` | Create constraint |
| `/admin/constraints/update` | POST | Admin | `api.UpdateConstraint` | Update constraint |
| `/admin/constraints/delete` | POST | Admin | `api.DeleteConstraint` | Delete constraint |
| `/admin/cron` | GET | Admin | `web.CronManager` | Manage cron jobs |
| `/admin/cron/add` | POST | Admin | `api.AddCronJob` | Create cron job |
| `/admin/cron/update` | POST | Admin | `api.UpdateCronJob` | Update cron job |
| `/admin/cron/delete` | POST | Admin | `api.DeleteCronJob` | Delete cron job |
| `/admin/tags/{serverId}` | POST | Admin | `api.UpdateServerTags` | Update server tags |
| `/admin/acl/{serverId}` | POST | Admin | `api.UpdateACLRule` | Update server ACL rule |
| `/api/audit` | GET | Admin | `api.GetAuditLog` | Query audit log |
| `/api/constraints/test` | POST | Admin | `api.TestConstraint` | Test a constraint expression |

The `/servers/{serverName}/action` and `/servers/{serverName}/command` routes remain unchanged but now flow through `engine.Evaluate()`.

---

## Configuration

New environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `HOGS_CRON_ENABLED` | `true` | Enable/disable cron scheduler |
| `HOGS_CRON_QUEUE_RETRY_INTERVAL` | `30` | Seconds between retries for queued actions |
| `HOGS_CRON_QUEUE_MAX_RETRY` | `10` | Max retries before giving up on queued actions |
| `HOGS_AUDIT_LOG_RETENTION_DAYS` | `90` | Days to retain audit log entries |
| `HOGS_PTERO_NODE_REFRESH_INTERVAL` | `300` | Seconds between Pterodactyl node info refreshes |

---

## Dependencies

New Go dependencies:

| Package | Version | Purpose |
|---------|---------|---------|
| `github.com/expr-lang/expr` | v1.16+ | Expression evaluation engine for ACLs and constraints |
| `github.com/robfig/cron/v3` | v3.0+ | Cron scheduling |

Both are pure Go, no CGO required (already required for SQLite anyway).

---

## Database Migrations

Migration `000016` adds all new tables and columns:

```sql
-- 000016_automation.up.sql

CREATE TABLE command_schemas (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id   INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    display_name TEXT NOT NULL,
    template    TEXT NOT NULL,
    params      TEXT NOT NULL DEFAULT '{}',
    acl_rule    TEXT NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE server_tags (
    server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    tag       TEXT NOT NULL,
    PRIMARY KEY (server_id, tag)
);

CREATE TABLE constraints (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    condition   TEXT NOT NULL,
    strategy    TEXT NOT NULL DEFAULT 'deny',
    priority    INTEGER NOT NULL DEFAULT 0,
    enabled     INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE cron_jobs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL,
    schedule    TEXT NOT NULL,
    server_name TEXT NOT NULL,
    action      TEXT NOT NULL,
    params      TEXT NOT NULL DEFAULT '{}',
    acl_rule    TEXT NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL DEFAULT 1,
    last_run    TEXT,
    next_run    TEXT
);

CREATE TABLE audit_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp   TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    user_email  TEXT NOT NULL,
    server_name TEXT NOT NULL,
    action      TEXT NOT NULL,
    params      TEXT NOT NULL DEFAULT '{}',
    result      TEXT NOT NULL,
    reason      TEXT NOT NULL DEFAULT '',
    source      TEXT NOT NULL DEFAULT 'user'
);

ALTER TABLE pterodactyl_servers ADD COLUMN acl_rule TEXT NOT NULL DEFAULT '';
ALTER TABLE pterodactyl_servers ADD COLUMN node TEXT NOT NULL DEFAULT '';
```

```sql
-- 000016_automation.down.sql

DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS cron_jobs;
DROP TABLE IF EXISTS constraints;
DROP TABLE IF EXISTS server_tags;
DROP TABLE IF EXISTS command_schemas;

-- SQLite doesn't support DROP COLUMN, so we recreate pterodactyl_servers
-- without the new columns. This is a destructive down migration.
ALTER TABLE pterodactyl_servers RENAME TO pterodactyl_servers_old;
CREATE TABLE pterodactyl_servers (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id       INTEGER NOT NULL UNIQUE,
    ptero_server_id TEXT NOT NULL,
    ptero_identifier TEXT NOT NULL DEFAULT '',
    allowed_actions TEXT NOT NULL DEFAULT '[]'
);
INSERT INTO pterodactyl_servers (id, server_id, ptero_server_id, ptero_identifier, allowed_actions)
    SELECT id, server_id, ptero_server_id, ptero_identifier, allowed_actions
    FROM pterodactyl_servers_old;
DROP TABLE pterodactyl_servers_old;
```

---

## Implementation Order

### Phase 1: Foundation (packages: `database/`, `engine/`, `config/`)

1. Add new dependencies to `go.mod` (`expr`, `robfig/cron`)
2. Run `go mod vendor`
3. Create migration `000016`
4. Add DB CRUD methods for `command_schemas`, `constraints`, `cron_jobs`, `audit_log`, `server_tags`
5. Add `acl_rule` and `node` fields to `PterodactylLink` struct and DB methods
6. Update `config.Config` with new fields

### Phase 2: Expression Engine (`engine/`)

1. Define `Engine` struct with `Store`, `Config`, `Cache`
2. Define environment types: `ServerEnv`, `UserEnv`, `TimeEnv`
3. Implement `buildEnv()` — assembles the expression environment from current state
4. Implement `EvaluateACL()` — evaluates ACL rule (with legacy fallback)
5. Implement `EvaluateConstraints()` — evaluates all constraints by priority
6. Implement `ValidateParams()` — validates params against command schema
7. Implement `RenderTemplate()` — substitutes validated params into command template
8. Implement `Evaluate()` — the full pipeline
9. Implement helper functions: `hasTag`, `serversOnNode`, `runningOnNode`, `countRunning`, `filterByTag`, `weekday`
10. Implement node discovery — periodic Pterodactyl API call to populate `node` field

### Phase 3: Cron Scheduler (`cron/`)

1. Define `Scheduler` struct wrapping `robfig/cron/v3`
2. Implement `Start()` / `Stop()` with graceful shutdown
3. Implement `LoadJobs()` — reads from DB, registers with cron
4. Implement `AddJob()` / `RemoveJob()` / `UpdateJob()` — DB + runtime
5. Job execution calls `engine.Evaluate()` with `"system"` user role

### Phase 4: HTTP Handlers (`api/`, `web/`)

1. Create `AutomationHandler` in `api/`
2. Modify `PterodactylHandler.ServerAction` and `SendCommand` to route through `engine.Evaluate()`
3. Add CRUD endpoints for command schemas, constraints, cron jobs, tags, ACL rules
4. Add `/api/constraints/test` endpoint
5. Add `/api/audit` endpoint
6. Add admin pages: command manager, constraint manager, cron manager
7. Add server tags to the server edit page
8. Add ACL rule editor to the Pterodactyl link section

### Phase 5: Help System

1. Create `/help` template with rendered HTML
2. Create `/help/api.md` endpoint returning dynamically generated markdown
3. Include expression reference, available functions, parameter types, cron syntax
4. Include current server commands, constraints, and cron jobs (sanitized)

### Phase 6: Integration & Wiring (`main.go`)

1. Create `Engine` instance
2. Create `Scheduler` instance, load jobs from DB, start
3. Wire `Engine` into `PterodactylHandler`
4. Register new routes
5. Ensure graceful shutdown stops cron scheduler

---

## Security Considerations

- **Expression sandboxing**: `expr` runs in a sandboxed VM with no access to filesystem, network, or Go stdlib. Only explicitly exposed variables and functions are available.
- **Param validation**: All command parameters are validated against their schema before template rendering. No raw user input reaches Pterodactyl.
- **Template injection**: Template rendering uses simple `{name}` substitution, not full template engines. Values are not re-interpreted.
- **Audit logging**: Every action attempt is recorded, including denied/blocked attempts.
- **System user**: Cron runs as a `"system"` user, auditable separately from human users.
- **Help endpoint**: The `/help/api.md` endpoint does not expose sensitive metadata (API tokens, passwords).

---

## Backward Compatibility

- Existing `pterodactyl_commands` are **not** dropped. The migration creates `command_schemas` alongside. A data migration step can convert old commands, or they can coexist (old commands used when no schema exists for a given command name).
- Existing `allowed_actions` JSON continues to work when `acl_rule` is empty.
- All existing routes and handlers remain functional until explicitly migrated.
- The admin UI will show both legacy and new options during transition.

---

## Future Extensions

- **Pipeline steps**: Composable pre/post-action steps (e.g., "warn players in chat, wait 5 min, then restart") defined as a step chain in config
- **Condition-based cron**: Cron jobs with `expr` conditions that must also pass before execution
- **Webhook notifications**: Post-action webhooks for Discord/Slack on constraint violations or cron results
- **Metrics**: Prometheus-style metrics for command execution, constraint evaluations, cron runs