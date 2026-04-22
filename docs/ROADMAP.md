---
title: HOGS Roadmap
tags:
  - roadmap
  - planning
---

# HOGS Roadmap

> [!info] Current State
> HOGS is a Go web application that serves as a landing page and management panel for game servers. It features OIDC auth with role-based access control, Pterodactyl integration for server actions, an automation engine (expression-based ACLs, resource constraints, cron scheduling), SCIM 2.0 for user provisioning from Authentik, a WebSocket-based agent system for managing servers without Pterodactyl, and an admin UI.

## Design Philosophy: Manage, Don't Provision

HOGS is **not** a server provisioning platform like Pterodactyl. Server administrators deploy game servers themselves (via Ansible, manual setup, or other tooling) alongside the `hogs-agent`. HOGS then provides management: start/stop/restart, console access, file management, backups, constraints, and scheduling. This means:

- No quadlet/container generation UI — admins deploy quadlets via their own tooling
- No port allocation — ports are configured in the quadlet, not managed by HOGS
- No game installer scripts — the server binary must already be in place
- Console history comes from `journalctl` (systemd logs), not a custom ring buffer
- Safe server deletion means unlinking from HOGS (stop managing), not wiping data directories

---

## Completed Phases

### Phase 1: Game Types ✅

The `GameQuerier` interface and `CONTRIBUTING.md` make adding games straightforward. Implemented: Minecraft, Satisfactory, Factorio, Valheim.

**Refactoring done**: Querier registry (`map[string]GameQuerier` with `RegisterQuerier()` and `RegisteredGameTypes()`).

### Phase 2: Role-Based Access Control ✅

- User table + OIDC groups mapping (migration 000010)
- `RequireRole(roles ...string)` middleware
- Role-based UI: Admin link, server actions, admin panel all gated by role
- **My Servers** page for users

### Phase 3: Pterodactyl Integration ✅

- `pterodactyl/` client package (Application API + Client API)
- `pterodactyl_servers` and `pterodactyl_commands` DB tables (migration 000011)
- Admin UI: link/unlink, allowed actions, command management
- User-facing: server power actions, command sending, whitelisting
- Pterodactyl identifier resolution (migration 000014)

### Phase 4: Automation System ✅

Design reference: see `docs/DESIGN_AUTOMATION.md` for the full data model, architecture, and implementation details.

**What was implemented:**

- **Expression engine** (`engine/`): ACL evaluation with legacy `allowed_actions` fallback, constraint evaluation (deny/queue/stop_oldest strategies), parameterized command schemas with typed validation, template rendering, audit logging
- **Cron scheduler** (`cron/`): wraps `robfig/cron/v3`, jobs flow through engine pipeline as system user
- **SCIM 2.0** (`scim/`): User and Group CRUD, PATCH, filtering, schema discovery, bearer token auth. Group membership changes trigger role recalculation and session invalidation.
- **DB-backed sessions** (`auth/`): Sessions stored in SQLite, not cookies. Enables OIDC back-channel logout from Authentik.
- **Admin UI**: Command schemas, constraints, cron jobs, server tags, ACL rules, help page

**Migrations**: 000016 (automation), 000017 (sessions), 000018 (SCIM)

### Phase 5: Agent System ✅

- **ServerBackend interface** (`backend/`): `PterodactylBackend` and `AgentBackend` implementations
- **WebSocket hub** (`agent/`): per-token auth, registration, heartbeat, console streaming, command/action dispatch
- **hogs-agent binary** (`cmd/hogs-agent/`): connects outbound to HOGS, systemd/podman quadlet process management (start/stop/restart via systemctl, commands via podman exec), file operations (list/read/write/delete/mkdir with base64 over WS), restic backup integration (create/restore/list snapshots)
- **Agent service** (`agent/`): AgentService with file and backup dispatch methods
- **Admin API**: agent CRUD, file management, backup endpoints

**Migration**: 000019 (agents table)

---

## Architecture

### Action Pipeline

All action paths (user-triggered, cron-triggered, API-triggered) go through the same pipeline:

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
│  4. Execute Action   ──►  Backend (Pterodactyl/Agent)  │
│  5. Audit Log                                         │
└──────────────────────────────────────────────────────┘
```

### Current Stack

| Layer | Technology |
|--------|-----------|
| Language | Go 1.24+ |
| Router | gorilla/mux |
| Database | SQLite + golang-migrate |
| Templates | Go html/template (embedded in binary) |
| Frontend | Bootstrap 5, vanilla JS |
| Auth | OIDC via gorilla/sessions (DB-backed) |
| Agent | WebSocket (gorilla/websocket) |
| Container | Podman/Docker via Containerfile |

### Key Packages

| Package | Purpose |
|---------|---------|
| `engine/` | Expression engine: ACL, constraints, param validation, audit |
| `cron/` | Cron scheduler wrapping robfig/cron/v3 |
| `agent/` | WebSocket hub, connection management, agent service |
| `backend/` | ServerBackend interface, PterodactylBackend, AgentBackend |
| `scim/` | SCIM 2.0 user/group provisioning endpoints |
| `pterodactyl/` | Pterodactyl Application/Client API client |
| `auth/` | OIDC auth, DB sessions, back-channel logout |
| `database/` | All DB models, CRUD, migrations |
| `query/` | GameQuerier interface + implementations |
| `config/` | Environment variable loading |

---

## Roadmap: Remaining Improvements

### Priority 1: Critical Gaps (panel feels incomplete without these)

#### 1.1 Agent Admin UI ✅
- GET /api/agents — list all agents with online/offline connectivity status from Hub
- GET /api/agents/{id} — agent details with real-time connectivity status
- POST /api/agents — create agent (auto-generates `hogs_`-prefixed token, returned once in response)
- PUT /api/agents/{id} — update agent name, node assignment, capabilities
- POST /api/agents/{id}/regenerate-token — token rotation, new token returned once
- POST /api/agents/delete — delete agent
- `/admin/agents` HTML page with agent list, create form, edit dialog, token regeneration, one-click install command display

#### 1.3 Audit Log Viewer ✅
- GET /api/audit — returns `{entries, limit, offset}` with pagination support
- GET /api/audit/export?format=json|csv — export full audit log as downloadable JSON or CSV
- `/admin/audit` HTML page with auto-populating table, pagination, result badges, and CSV/JSON export

#### 1.4 Constraint Tester ✅
- POST /api/constraints/test — evaluate expression with server/user/time environment
- Pre-fills environment with server list, user roles, time context
- Returns `{result, error}` — boolean result or compilation error
- Interactive UI in `/admin/constraints` page with server/user env prefill

#### 1.5 Console Streaming via journald ✅
- WebSocket proxy from browser → HOGS server → agent for live console I/O
- Agent tails the container's systemd journal (`journalctl -u <unit> -f`) and streams lines as `console` messages
- HOGS buffers recent lines per-server (last 500 lines) for replay on connect
- Show console on server detail page with input field for commands
- Console input is sent as `console_input` messages routed to `podman exec`
- **Still needed**: For Pterodactyl-managed servers, proxy the existing Pterodactyl websocket console

#### 1.6 Agent-Aware Server Edit ✅
- PterodactylHandler.LinkServer now accepts `node` form field to assign agent-managed servers
- Node field stored in PterodactylLink and used by resolveBackend() for routing
- Node selector dropdown in server edit page UI with agent list

#### 1.7 Backend Routing for Actions/Commands ✅
- PterodactylHandler now uses `resolveBackend()` to determine whether a server is agent-managed or Pterodactyl-managed
- When a server has `node` matching an agent, start/stop/restart/whitelist route through `AgentBackend`
- When `node` is empty or matches no agent, falls through to `PterodactylBackend` (existing behavior)
- `PterodactylHandler` takes `AgentHub` parameter; `main.go` wires it up

#### 1.8 Agent Whitelist Support ✅
- Whitelist (add/remove player) now routes through the correct backend (agent or Pterodactyl)
- For agent-managed servers: whitelist command sent through agent's command channel
- Game-specific whitelist commands (minecraft `whitelist add`, etc.) work identically regardless of backend

#### 1.9 Request-Response Agent Protocol ✅
- Currently agent operations are fire-and-forget: `SendAction`/`SendCommand` push messages but callers only get "sent" back
- **Implemented**: Request ID correlation in `Envelope` struct, pending-request map with `context.Context` timeouts (default 30s)
- Hub methods (`SendAction`, `SendCommand`, file ops, backup ops) now block until agent responds or timeout
- `AgentBackend.Start/Stop/Restart/SendCommand` return actual errors from agent responses
- `hogs-agent` echoes `requestId` back in all result messages for correlation
- `AgentService` and `AgentHandler` updated to use new blocking signatures

#### 1.10 Session Cleanup Goroutine ✅
- `CleanupSessions()` exists on `Authenticator` but is never called from `main.go`
- **Implemented**: Periodic goroutine (every 15 minutes) in `main.go` that calls `auth.CleanupSessions()`
- Also cleans up on server startup immediately

#### 1.11 Agent Reconnection State Recovery ✅ (partial)
- When an agent disconnects, `Hub.RemoveConn` now fails all pending requests for that agent immediately
- Pending request map tracks `agentID` so disconnection can resolve all matching requests
- Agents that reconnect re-register via the `register` message as before
- **Still needed**: Track pending operations in DB (`agent_pending_ops` table) so they survive HOGS restarts

### Priority 2: Important Gaps (needed for production use)

#### 2.1 Backup Management UI ✅ (basic)
- Admin page at `/admin/backups` showing all servers with backup actions
- One-click backup create and list snapshots buttons per server
- Calls existing agent backup API endpoints
- **Still needed**: Backup policy scheduling, backup history tracking, restic repo initialization UI

#### 2.2 Cron Job History ✅
- Added `last_result` and `last_output` columns to `cron_jobs` table (migration 000020)
- New `cron_job_logs` table: id, cron_job_id, timestamp, result, output, duration_ms
- Scheduler stores result and output after each job execution
- `UpdateCronJobResult()` updates the cron job's last_result/last_output
- `CreateCronJobLog()` and `ListCronJobLogs()` for audit trail
- Cron manager admin page shows last_result badge and expandable last_output

#### 2.3 Notification/Alerting System ✅
- Migration 000026 creates `notification_channels` table (id, name, type, url, events, enabled, created_at)
- Uses [shoutrrr](https://github.com/containrrr/shoutrrr) v0.8.0 as notification backend for maximum flexibility
- Supports 100+ services via Shoutrrr URL schema: Discord, Slack, Telegram, email SMTP, Pushover, Matrix, ntfy, Bark, Gotify, IFTTT, Join, Opsgenie, Pushbullet, RocketChat, Microsoft Teams, Zulip, and more
- `NotificationChannel` model with CRUD: ListNotificationChannels, CreateNotificationChannel, DeleteNotificationChannel
- `notify/service.go`: async notification dispatcher, filters by event type, supports wildcard
- API endpoints: GET /api/notifications, POST /api/notifications/create, POST /api/notifications/delete, GET /api/notifications/test
- All endpoints require admin role
- Event triggers: server_up, server_down, agent_connect, agent_disconnect, constraint_violation, cron_failure
- **Still needed**: per-server/per-user preferences

#### 2.4 Dashboard Overview ✅
- New `GET /api/dashboard` endpoint: total/online/offline/maintenance/planned server counts, game type breakdown, agent connectivity (connected/disconnected), cron status, last 10 audit entries
- New `GET /api/dashboard/agents` endpoint: list all agents with online/offline connection status
- Both endpoints require admin role
- `/admin/dashboard` HTML page with stat cards, game type breakdown, agent status, recent audit log, and system status

#### 2.5 Server Resource Metrics ✅
- Agent status reports now store metrics in `server_metrics` table (migration 000021)
- New `ServerMetric` model with `CreateServerMetric`, `GetLatestServerMetric`, `ListServerMetric`, `CleanupServerMetrics`
- New API endpoint: `GET /api/servers/{serverName}/metrics?limit=N` returns recent metrics
- Agent `status` messages also update `agents.online` status
- Configurable retention via `HOGS_METRICS_RETENTION_DAYS` (default 7)
- Periodic cleanup goroutine runs hourly (also handles audit log cleanup)

#### 2.6 Mass Operations ✅
- Select multiple servers on admin page → bulk start/stop/restart
- Checkbox UI on server list with action bar
- **Still needed**: Bulk tag assignment, bulk ACL rule application

#### 2.7 User-Facing Server Controls ✅
- `/my-servers` page shows servers where user has ACL-granted access
- Action buttons (start/stop/restart) with CSRF protection that pass through engine.Evaluate()
- Command execution UI for parameterized commands (rendered from command schemas)
- Whitelist modal with add/remove player support
- Success/error feedback with auto-dismiss

#### 2.8 Rate Limiting ✅
- New `api/ratelimit.go`: IP-based sliding window rate limiter
- Login endpoints: 5 requests/minute per IP (`HOGS_RATE_LIMIT_LOGIN`)
- Public API endpoints: 60 requests/minute per IP (`HOGS_RATE_LIMIT_API`)
- SCIM endpoints: 100 requests/minute per token (`HOGS_RATE_LIMIT_SCIM`)
- Respects `X-Forwarded-For` header for reverse proxy deployments
- Periodic cleanup goroutine removes expired entries every 5 minutes
- Agent WebSocket messages: not rate-limited (low volume, per-connection)

#### 2.9 CSRF Protection ✅
- Added `auth/CSRFMiddleware` using HMAC-signed double-submit cookie pattern
- Signs CSRF token with session secret (`SESSION_SECRET` env var)
- GET/HEAD/OPTIONS requests set the `hogs-csrf` cookie and pass through
- POST requests must include matching `csrf_token` form field or `X-CSRF-Token` header
- Exempts `/agent/ws`, `/scim/v2/`, `/auth/callback`, `/auth/backchannel-logout`, `/api/`
- `CSRFTokenFromRequest()` helper available for templates
- Tests: token generation/verification, exempt paths, GET passthrough, POST rejection, valid token acceptance

### Priority 3: Nice-to-Have (polish items)

#### 3.1 API Key Authentication ✅
- Migration 000022 creates `api_keys` table (id, name, key_hash, key_prefix, role, created_at, last_used, expires_at)
- `GenerateAPIKey()` generates `hogs_`-prefixed keys with SHA-256 hash storage
- `auth/APIKeyAuthenticator` validates Bearer tokens against stored hashes
- `auth/APIKeyMiddleware` runs before CSRF middleware, sets API key in request context
- Admin endpoints: `GET /api/api-keys` (list), `POST /api/api-keys` (create), `POST /api/api-keys/delete` (delete)
- Key expiry support via optional `expires_at` field
- `GetAPIKeyFromContext()` helper for role-based authorization in handlers

#### 3.2 Agent Provisioning Flow ✅ (partial)
- One-click "Add Agent" button generates token + shows install command
- Auto-generates systemd unit file for the agent with env vars
- Provisioning reference card with environment variable documentation
- Quick start commands for binary download and systemd setup
- **Still needed**: Downloadable agent binary page, agent health dashboard with heartbeat latency

#### 3.3 Restic Repo Init from UI
- Button in backup section to initialize a new restic repo
- Pre-fill common repo types: local path, SFTP, S3, B2
- Test connection button (runs `restic check`)
- Store encrypted repo credentials in DB (or reference env vars)

#### 3.4 Pterodactyl Migration Path
- Document step-by-step migration guide: Pterodactyl → HOGS agent
- Ansible playbook examples for deploying hogs-agent alongside game server containers
- Quadlet template examples per game type (Minecraft, Valheim, etc.)
- Import tool: read Pterodactyl allocation/server data → create HOGS servers + agents
- Once all servers are agent-managed, Pterodactyl dependency can be fully removed
- PterodactylBackend becomes optional; `PterodactylURL` can be empty

#### 3.5 Server Templates ✅
- Migration 000024 creates `server_templates` table (id, name, game_type, default_settings, default_commands, default_acl, default_tags, description)
- `ServerTemplate` model with JSON fields for settings, commands, and tags
- CRUD: ListServerTemplates, GetServerTemplate (by ID or name), CreateServerTemplate, DeleteServerTemplate
- Admin endpoints: GET /api/templates, POST /api/templates/create, POST /api/templates/delete
- Admin UI template selector in Add Server modal with client-side prefill of game type and settings

#### 3.6 Webhook Outgoing ✅
- Migration 000025 creates `webhooks` table (id, name, url, secret, events, enabled, created_at)
- `Webhook` model with HMAC-SHA256 signature verification
- `webhook/dispatcher.go`: async event dispatcher, filters by event type, supports wildcard
- Pre-built event constructors: `ServerEvent`, `CronEvent`, `AgentEvent`
- Admin endpoints: GET /api/webhooks, POST /api/webhooks/create, POST /api/webhooks/delete, GET /api/webhooks/test
- Secrets never exposed in API responses

#### 3.7 Dark/Light Theme Consistency
- Audit all admin pages for hardcoded colors
- Use CSS variables consistently
- Ensure agent/backup/cron pages match the dark/light theme system

#### 3.8 Localization/i18n
- Extract all UI strings into locale files (JSON per language)
- Support `Accept-Language` header + user preference
- Default to English, community-contributed translations
- Start with: English, German

#### 3.9 Secret Management Hardening ✅
- Agent tokens are now stored as SHA-256 hashes in DB (`token_hash` column)
- `token_prefix` column (first 8 chars) for display in admin UI instead of full token
- `Agent.Token` has `json:"token,omitempty"` — only populated on creation response, never in list/get
- `GetAgentByToken` hashes the provided token and looks up by `token_hash`
- `CreateAgent` auto-generates hash and prefix from plaintext token
- API key authentication uses same `HashAPIKey` function for consistent hashing
- Agent binary supports TLS client certificates: `HOGS_AGENT_TLS_CERT` and `HOGS_AGENT_TLS_KEY`
- **Still needed**: Token rotation endpoint/admin UI, encrypt restic passwords at rest

#### 3.10 Health Check Endpoints ✅
- HOGS `/healthz` endpoint now reports database connectivity with structured JSON response
- Agent binary has built-in HTTP health endpoint (`/healthz`) for systemd watchdog integration
- Controlled by `HOGS_AGENT_HEALTH_ADDR` env var (default: disabled, set e.g. `:8081` to enable)
- Agent status reports (already implemented: 30s heartbeat with online/players/max_players/version)

#### 3.11 Test Coverage ✅ (partial)
- **Unit tests for `engine/` package**: ACL evaluation, constraint evaluation, param validation, template rendering, helper functions (HasTag, CountRunning, FilterByTag, ParseWeekday), source detection in audit log, expression testing
- **Unit tests for `cron/` package**: scheduler creation, job loading, AddJob/UpdateJob/RemoveJob, enable/disable, Start/Stop
- **Unit tests for `agent/` package**: Hub creation, connection lookup, request ID allocation, pending request correlation, context cancellation, Envelope serialization, result type detection, ResolveBackend (no-link, Pterodactyl, agent), AgentService offline errors, ServeWS auth validation, AgentBackend.Name/Status, console buffer/broadcast/limit, client lifecycle, server name lookup
- **Unit tests for `web/` package**: Dashboard, Admin, Home, ServerDetail, ConstraintManager, CronManager rendering; auth integration; 404 behavior for offline servers
- **Bug fix**: `database/` agent scan methods (`GetAgent`, `GetAgentByToken`, `GetAgentByNodeName`, `ListAgents`) now correctly handle `json.RawMessage` column by scanning into `[]byte` first
- **Bug fix**: `config/` test defaults now properly unset env vars to avoid environment bleed
- Still needed: Integration tests for `backend/` package, SCIM endpoint integration tests, full end-to-end action pipeline tests

---

## Reference: Design Documents

For detailed architecture, data models, expression language reference, and implementation specifics, see:

- **`docs/DESIGN_AUTOMATION.md`** — Automation system design (expression engine, constraints, ACLs, cron, data model, migrations, API reference, security considerations). This document should be considered a historical design reference; the implementation is complete and the canonical roadmap is this file.