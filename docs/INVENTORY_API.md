# Inventory Reconciliation API

HOGS exposes a declarative control-plane API for Gandalf and other inventory
reconcilers. The API owns game-management configuration while agents report
runtime observations independently. Interactive OIDC sessions are not accepted
on this surface.

The current contract version is `hogs.tionis.dev/v1alpha1`. It is intentionally
allowed to make breaking changes while the primary game-management model is
being established.

## Authentication

All inventory endpoints require an HOGS API key with the `admin` role:

```http
Authorization: Bearer hogs_...
Content-Type: application/json
```

For a manually managed installation, create the bootstrap key in the API-key
administration UI and place it in the reconciler's secret store. A declarative
deployment may instead supply the same vaulted credential through
`BOOTSTRAP_ADMIN_API_KEY` and optionally set `BOOTSTRAP_ADMIN_API_KEY_NAME`
(default `gandalf`). HOGS creates or rotates only that named identity at startup
and stores only its hash. Do not put the key or an agent credential in inventory
source, logs, or command-line arguments. Render agent tokens into the request
body from a vault only for the duration of apply.

## Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/api/v1/inventory/plan` | Validate and diff desired state without mutation. |
| `PUT` | `/api/v1/inventory` | Transactionally apply the complete desired state. |
| `GET` | `/api/v1/inventory` | Read redacted desired state and current observations. |
| `GET` | `/api/v1/inventory/events?after=<cursor>` | Poll immutable reconciliation events. |

The manifest is authoritative for nodes, servers and their backends and policy,
commands, constraints, schedules, templates, webhooks, notification channels,
and settings. Omitting one of those existing resources requests its deletion.
SCIM remains authoritative for users and roles. API keys are deliberately
outside the manifest so a bad apply cannot remove the reconciliation identity.

## Apply protocol

1. Send the candidate manifest to `plan`.
2. Review `changes` and `destructive`.
3. Send the same manifest to `PUT /api/v1/inventory` with the plan's
   `currentDigest` in `If-Match`.
4. If the plan contains deletes, also send `X-HOGS-Confirm-Prune: true`.
5. Verify the returned digest with `GET /api/v1/inventory`, then retain the
   event cursor for the next reconciliation.

`If-Match` prevents applying a plan against changed state and returns `412` on
a mismatch. An unconfirmed destructive apply returns `409`. Applying an
identical manifest is safe and produces no resource changes.
The database update is one transaction. Enabled schedules are reloaded after
commit without restarting HOGS.

Every resource uses its manifest `name` as its stable reconciliation key. The
top-level `generation` should identify the source inventory revision, for
example a Gandalf Git commit. HOGS also returns a canonical SHA-256 digest.

## Manifest

```json
{
  "apiVersion": "hogs.tionis.dev/v1alpha1",
  "generation": "git:0123456789abcdef",
  "nodes": [
    {
      "name": "destiny",
      "nodeName": "destiny",
      "labels": {"site": "netcup"},
      "desiredCapabilities": ["backup", "command", "console", "file", "restart", "start", "status", "stop"],
      "tokenState": "active",
      "token": "hogs_<injected-from-vault>"
    }
  ],
  "servers": [
    {
      "name": "cog",
      "address": "cog.internal:25565",
      "description": "Managed Minecraft server",
      "mapUrl": "",
      "modUrl": "",
      "state": "online",
      "gameType": "minecraft",
      "showMotd": true,
      "metadata": {"edition": "java"},
      "tags": ["game", "minecraft"],
      "unit": "cog.service",
      "dataPath": "/srv/cog",
      "backend": {"type": "agent", "node": "destiny"},
      "policy": {
        "aclRule": "user.Role == \"admin\"",
        "allowedActions": ["restart", "start", "stop"],
        "operators": ["games-admins"],
        "console": true,
        "rcon": false,
        "start": true,
        "stop": true,
        "backup": true,
        "restore": true,
        "writablePaths": ["/srv/cog/config", "/srv/cog/world"]
      },
      "commands": []
    }
  ],
  "constraints": [],
  "schedules": [],
  "templates": [],
  "webhooks": [],
  "notifications": [],
  "settings": {}
}
```

An agent backend requires a known node plus `unit` and an absolute `dataPath`.
Writable paths must be absolute descendants of that data path. Schedules use
six-field cron expressions (`second minute hour day-of-month month day-of-week`).
A node agent's local allowlist must contain matching server names, units, and
data paths as documented in [the agent contract](AGENT.md).
A Pterodactyl backend uses `type: "pterodactyl"` and requires `externalId`; a
display-only server uses `type: "none"`.

`desiredCapabilities` is policy intent. The agent's `capabilities` value under
`observed.agents` is reported at registration and is never overwritten by the
manifest. A reconciler can therefore detect missing capabilities.

## Credentials and revocation

Active nodes require a random `hogs_` token injected from Gandalf's vault. HOGS
strips the token before hashing, diffing, or storing desired state and persists
only its keyed hash. A different value is rejected unless `rotateToken: true`
is set for that generation. The flag is treated as an operation and excluded
from stored desired state. Replaying the same rotation after a timeout is safe.
Set `tokenState: "revoked"` and omit `token` to clear the stored hash and
disconnect/reject authentication with that credential. Rotation and revocation
cannot be requested together.

Ordinary readback redacts webhook secrets, notification URLs, secret-like
settings, and secret-like server metadata. It never returns API keys, agent
tokens, or token hashes, including in apply responses and events.

## Desired and observed state

`GET /api/v1/inventory` returns:

- `manifest`, `digest`, `appliedAt`, and `actor` for desired state;
- `observed.agents`, including connection state, last seen time, token prefix,
  and reported capabilities;
- `observed.servers` with credential-bearing metadata removed;
- `observed.metrics`, keyed by server name, with the latest health, version,
  player, CPU, memory, and disk report or `null` when no report exists;
- `observed.users`, sourced from OIDC/SCIM, including effective role and active
  state.

An active desired node with `online: false` or stale `lastSeen` is unreachable;
it is not rewritten as a desired configuration change.

## Events

Events are ordered by an integer cursor and include generation, timestamp,
API-key actor, resource type, stable resource key, and `create`, `update`,
`delete`, or `rotate` action. Poll with the last processed cursor. A response contains at most 1,000
events and returns the new cursor even when the page is empty.

Events record reconciliation changes, including destructive changes and token
operations, but never contain secret values.
