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

#### 1.1 Agent Admin UI
- Admin page at `/admin/agents` to list, create, delete agents
- Auto-generate agent tokens on creation
- Show online/offline status and last-seen timestamp
- One-click "copy install command" that outputs the `hogs-agent` systemd unit + env vars
- Edit agent name, node assignment, capabilities

#### 1.2 Agent File Manager UI
- Browse remote filesystem via agent WebSocket (directory listing, file read/write/delete)
- Upload files from browser (base64 over WS → agent writes to disk)
- Download files from agent (agent reads → base64 over WS → browser)
- Create/delete directories
- Show at `/admin/files/{serverName}` for agent-managed servers (reuse existing file manager pattern)

#### 1.3 Audit Log Viewer
- Admin page at `/admin/audit` showing recent entries with filtering (by user, server, action, result)
- Pagination (limit/offset already in API)
- Show all columns: timestamp, user, server, action, params, result, reason, source
- CSV/JSON export button

#### 1.4 Constraint Tester UI
- Add interactive expression tester to `/admin/constraints` page
- Pre-fill environment with server list and time context
- Show result (true/false) and any evaluation errors
- Live syntax highlighting/validation

#### 1.5 Console Streaming via journald
- WebSocket proxy from browser → HOGS server → agent for live console I/O
- Agent tails the container's systemd journal (`journalctl -u <unit> -f`) and streams lines as `console` messages
- HOGS buffers recent lines per-server (last 500 lines) for replay on connect
- For Pterodactyl-managed servers, proxy the existing Pterodactyl websocket console
- Show console on server detail page with input field for commands
- Console input is sent as `command` messages routed to `podman exec`

#### 1.6 Agent-Aware Server Edit Page
- Server edit page detects whether server is Pterodactyl-managed or agent-managed (via `node` field)
- For agent-managed servers: show agent connectivity status, file manager link, backup controls, console link, no Pterodactyl link form
- For Pterodactyl-managed servers: show existing Pterodactyl link form as-is
- Add node selector dropdown (populated from registered agents) on server edit page

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

#### 2.1 Backup Management UI
- Admin page at `/admin/backups` showing all backup policies per server
- Create/schedule backup policies (restic repo, paths, tags, cron schedule)
- One-click backup/restore buttons per server
- Backup history with snapshot ID, size, date
- Restic repo initialization from UI (`restic init`)

#### 2.2 Cron Job History
- Add `last_result` and `last_output` columns to `cron_jobs` table
- Show success/failure status in cron manager page
- Store last N results in a new `cron_job_logs` table for audit trail
- Show recent execution log inline on cron job row

#### 2.3 Notification/Alerting System
- New `notifications` table: id, type, destination, enabled
- Support channels: email (SMTP), webhook (Discord/Slack/custom), in-app
- Trigger events: server down/up, agent disconnect, backup failure, constraint violation, cron failure
- Configurable per-server and per-user notification preferences
- Notification queue with retry logic

#### 2.4 Dashboard Overview
- New `/admin/dashboard` or enhance existing `/admin` with:
  - Total servers, online/offline counts
  - Agent connectivity overview (connected/disconnected per node)
  - Recent events (last 10 audit log entries)
  - Quick action buttons (start all, stop all on node)
  - Resource usage summary if agents report metrics

#### 2.5 Server Resource Metrics
- Agent reports CPU, RAM, disk usage in `status` messages
- New `server_metrics` table: server_id, timestamp, cpu_percent, memory_used, memory_total, disk_used, disk_total, players, max_players
- Time-series aggregation (keep hourly/daily summaries, raw data for 7 days)
- Simple charts on server detail page (CPU/RAM over last 24h)
- Configurable retention period

#### 2.6 Mass Operations
- Select multiple servers on admin page → bulk start/stop/restart
- Bulk tag assignment
- Bulk ACL rule application
- Checkbox UI on server list with action bar

#### 2.7 User-Facing Server Controls
- `/my-servers` page shows servers where user has ACL-granted access
- Action buttons (start/stop/restart) that pass through engine.Evaluate()
- Command execution UI for parameterized commands (rendered from command schemas)
- Whitelist button (for games that support it)

#### 2.8 Rate Limiting
- Rate limit login attempts (5/minute per IP)
- Rate limit SCIM endpoints (100/minute per token)
- Rate limit agent WebSocket messages (per-connection throttle)
- Rate limit public API endpoints (60/minute per IP)
- Configurable via environment variables or admin settings

#### 2.9 CSRF Protection
- Add CSRF tokens to all state-changing POST forms
- Use gorilla/csrf or equivalent middleware
- Exempt API endpoints that use bearer auth (SCIM, agent WS)
- Exempt webhook endpoints

### Priority 3: Nice-to-Have (polish items)

#### 3.1 API Key Authentication
- New `api_keys` table: id, name, key_hash, role, created_at, last_used, expires_at
- API key auth middleware (alternative to OIDC session)
- Key generation with `hogs_` prefix
- Admin page at `/admin/api-keys` to manage keys
- Key permissions tied to role (admin/user) or scoped per-server
- Used for: cron scripts, CI/CD, external integrations

#### 3.2 Agent Provisioning Flow
- One-click "Add Agent" button generates token + shows install command:
  ```bash
  hogs-agent add-node --url https://hogs.example.com --token <generated>
  ```
- Auto-generates systemd unit file for the agent
- Downloadable agent binary page (or link to releases)
- Agent health dashboard with heartbeat latency

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

#### 3.5 Server Templates
- New `server_templates` table: id, name, game_type, default_settings, default_commands, default_acl, default_tags
- Pre-defined templates: "Vanilla Minecraft", "Modded Minecraft", "Valheim", "Satisfactory", "Factorio"
- Template selection on server creation page
- Auto-populate metadata, commands, tags from template

#### 3.6 Webhook Outgoing
- New `webhooks` table: id, url, secret, events[], enabled
- Post event payloads to external URLs on: server start/stop, constraint violation, cron result, backup completion
- Retry logic with exponential backoff
- HMAC signature for verification
- Admin UI at `/admin/webhooks`

#### 3.7 Dark/Light Theme Consistency
- Audit all admin pages for hardcoded colors
- Use CSS variables consistently
- Ensure agent/backup/cron pages match the dark/light theme system

#### 3.8 Localization/i18n
- Extract all UI strings into locale files (JSON per language)
- Support `Accept-Language` header + user preference
- Default to English, community-contributed translations
- Start with: English, German

#### 3.9 Secret Management Hardening
- Hash agent tokens in DB (bcrypt or SHA-256 with salt), compare against hash on connect
- Add SCIM/agent token rotation endpoint or admin UI
- Encrypt restic passwords at rest using a server-side encryption key (HOGS_ENCRYPTION_KEY env var)
- Add TLS option to hogs-agent (`HOGS_AGENT_TLS=true`, `HOGS_AGENT_TLS_CERT`, `HOGS_AGENT_TLS_KEY`)

#### 3.10 Health Check Endpoints
- Agent liveness probe: periodic heartbeat to HOGS `/agent/ws` with `type: ping`
- HOGS `/healthz` endpoint should also report: DB reachable, agent connection count, cron scheduler status
- Agent binary: built-in HTTP health endpoint (`/healthz`) for systemd watchdog integration

#### 3.11 Test Coverage ✅ (partial)
- **Unit tests for `engine/` package**: ACL evaluation, constraint evaluation, param validation, template rendering, helper functions (HasTag, CountRunning, FilterByTag, ParseWeekday), source detection in audit log, expression testing
- **Unit tests for `cron/` package**: scheduler creation, job loading, AddJob/UpdateJob/RemoveJob, enable/disable, Start/Stop
- **Unit tests for `agent/` package**: Hub creation, connection lookup, request ID allocation, pending request correlation, context cancellation, Envelope serialization, result type detection, ResolveBackend (no-link, Pterodactyl, agent), AgentService offline errors, ServeWS auth validation, AgentBackend.Name/Status
- **Bug fix**: `database/` agent scan methods (`GetAgent`, `GetAgentByToken`, `GetAgentByNodeName`, `ListAgents`) now correctly handle `json.RawMessage` column by scanning into `[]byte` first
- **Bug fix**: `config/` test defaults now properly unset env vars to avoid environment bleed
- Still needed: Integration tests for `backend/` package, SCIM endpoint integration tests

---

## Reference: Design Documents

For detailed architecture, data models, expression language reference, and implementation specifics, see:

- **`docs/DESIGN_AUTOMATION.md`** — Automation system design (expression engine, constraints, ACLs, cron, data model, migrations, API reference, security considerations). This document should be considered a historical design reference; the implementation is complete and the canonical roadmap is this file.