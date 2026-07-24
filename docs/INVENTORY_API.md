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
and stores only its hash. Do not put the key in inventory source, logs, or
command-line arguments. Agent endpoint configuration and node secrets are
deployed separately and are not part of this API.

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

On first adoption, the plan also inventories resources created by older
interactive HOGS versions. Any such resource omitted from the first manifest
is reported as a delete and requires the same explicit prune confirmation.

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
      "desiredCapabilities": ["backup", "command", "console", "file", "restart", "start", "status", "stop"]
    }
  ],
  "servers": [
    {
      "name": "cog",
      "address": "cog.internal:25565",
      "description": "Managed Minecraft server",
      "mapUrl": "",
      "mapLifecycle": "game",
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
        "aclRule": "",
        "allowedActions": ["restart", "start", "stop"],
        "operators": [],
        "console": true,
        "rcon": false,
        "start": true,
        "stop": true,
        "backup": true,
        "restore": true,
        "writablePaths": ["/srv/cog/config", "/srv/cog/world"]
      },
      "commands": [],
      "accessGrants": [
        {
          "subjectType": "everyone",
          "subject": "*",
          "effect": "allow",
          "capabilities": ["status", "view"]
        },
        {
          "subjectType": "group",
          "subject": "games-admins",
          "effect": "allow",
          "capabilities": ["console.read", "console.write", "start", "stop"]
        }
      ]
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

`mapLifecycle` accepts `game` (the default) or `independent`. It describes
whether the map backend normally shares the game server's lifecycle, allowing
HOGS to explain map failures without incorrectly claiming that every map
requires its game server to be running.

An agent backend requires a known node plus `unit` and an absolute `dataPath`.
Writable paths must be absolute descendants of that data path. Schedules use
six-field cron expressions (`second minute hour day-of-month month day-of-week`).
A node agent's local allowlist must contain matching server names, units, and
data paths as documented in [the agent contract](AGENT.md).
A Pterodactyl backend uses `type: "pterodactyl"` and requires `externalId`; a
display-only server uses `type: "none"`.

`desiredCapabilities` is policy intent. Agent reachability and capabilities are
observed through the configured node transport and are never overwritten by the
manifest. Endpoint addition, credential rotation, and revocation happen through
the installation's deployment system.

Ordinary readback redacts webhook secrets, notification URLs, secret-like
settings, and secret-like server metadata. It never returns API keys.

## Desired and observed state

`GET /api/v1/inventory` returns:

- `manifest`, `digest`, `appliedAt`, and `actor` for desired state;
- `observed.agents`, including private-API reachability, last observation time,
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
API-key actor, resource type, stable resource key, and `create`, `update`, or
`delete` action. Poll with the last processed cursor. A response contains at
most 1,000 events and returns the new cursor even when the page is empty.

Events record reconciliation changes, including destructive changes, but never
contain secret values.
