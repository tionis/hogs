# Node Agent

`hogs-agent` is one node-scoped process with an explicit allowlist of game
servers. It manages already-provisioned systemd units, data roots, consoles,
and backups. It does not install games, allocate ports, or provision hosts.

## Transport model

The agent exposes one HTTP API. HOGS supports two transport modes around that
same contract:

- `direct`: the agent is available at a public HTTPS URL. HOGS uses that URL
  for control operations and gives an authenticated browser a short-lived,
  narrowly scoped capability for direct file operations.
- `tunneled`: the agent API is reached over a private HTTP transport. HOGS
  proxies browser file operations because the browser cannot reach the private
  address. This mode is reserved for a future userspace WireGuard transport.

Direct mode is the current deployment mode. TLS may terminate in a local
reverse proxy or in the agent itself. HOGS refuses non-HTTPS direct URLs.

The HOGS-side configuration is separate from inventory reconciliation because
it contains transport credentials:

```yaml
nodes:
  - node: destiny
    mode: direct
    control_url: https://agent-destiny.example.test
    public_url: https://agent-destiny.example.test
    secret_file: /etc/hogs/agent-secrets/destiny
```

`control_url` is where HOGS reaches the agent. `public_url` is where a browser
reaches it and is required for direct mode. A future tunneled node will use an
HTTP `control_url` inside its tunnel and omit `public_url`.

The secret file and initial worker identity are host-provisioned. Instance
administrators can subsequently change the worker's display label, transport
mode, control URL, browser-facing URL, and server assignments from the HOGS
admin UI. These endpoint changes are stored in HOGS and take effect without a
restart; credentials are never shown in the browser.

## Agent configuration

Only `HOGS_AGENT_CONFIG` is required in the environment:

```yaml
node: destiny
state_dir: /var/lib/hogs-agent
restic_bin: /usr/bin/restic
health_addr: 127.0.0.1:9080
api:
  listen: 127.0.0.1:9081
  secret_file: /etc/hogs-agent/control.secret
  allowed_origins:
    - https://games.example.test
servers:
  cog:
    unit: minecraft-cog.service
    game_type: minecraft
    data_dir: /srv/mc/cog
    gateway:
      type: minecraft
      listen: ":25565"
      backend: 127.0.0.1:25566
      readiness_timeout: 10m
      connect_timeout: 2s
      wake_cooldown: 30s
      login_rate_limit: 5s
      default_boot_estimate: 2m
      starting_message: "The server is starting. Reconnect in about {minutes} minute(s)."
    console:
      type: rcon
      host: 127.0.0.1
      port: 25575
      password_file: /etc/hogs-agent/cog-rcon-password
    backup:
      environment_file: /etc/restic/restic.env
```

### Satisfactory API token file

Satisfactory has no RCON, so occupancy comes from the node-local Server
Manager HTTPS API instead. Set `api_token_file` to a root-only file holding
the bearer token (minted with `server.GenerateAPIToken` at claim time); the
agent reads it per query against `127.0.0.1` on the game port from `address`
and never stores the token in configuration:

```yaml
servers:
  satisfactory:
    unit: satisfactory.service
    game_type: satisfactory
    data_dir: /srv/satisfactory
    address: satisfactory.example.test:7777
    api_token_file: /srv/satisfactory/.api-token
```

Without the file (or when the API is unreachable) the server reports unknown
occupancy rather than failing the whole status response.

### On-demand Minecraft gateway

A server can opt into the embedded Minecraft TCP gateway shown above. The
actual Minecraft process must listen on a different, private backend address;
the gateway owns the public game port. While the backend is available, the
gateway transparently relays the connection. While it is unavailable:

- server-list status pings receive a native Minecraft "sleeping" response and
  never start the service;
- a syntactically valid login starts the allowlisted systemd unit once and
  receives a native disconnect message with the estimated wait;
- concurrent attempts share that startup, and repeated offline login attempts
  from one source address are rate limited; and
- the gateway polls the backend until it is ready, then records the boot
  duration under `state_dir`.

The estimate is the 75th percentile of the latest 20 successful boot samples,
falling back to `default_boot_estimate`. Gateway state, counters, connection
counts, the estimate, and recent timestamps are included in the normal agent
status observation under `extras.gateway`.

The username in the initial login packet is not authenticated and is used only
for the startup message. It must never authorize a wake or bypass access
control. Minecraft remains authoritative for authentication, whitelist
membership, and bans after the backend is ready. This also means a deliberately
formed login from a non-member may wake the service; preventing that requires a
separate authenticated pre-login design.

For native TLS, set both `api.tls_cert_file` and `api.tls_key_file` and bind
the agent to the public listener. When a reverse proxy owns TLS, bind the agent
to loopback and proxy the complete path and query string without changing the
HTTP method.

## Authentication and browser access

Each node shares a random secret with HOGS. HOGS signs short-lived HMAC-SHA256
capabilities containing the exact node, subject, HTTP method, route, optional
file path, upload-size limit, and expiry. The agent verifies every field before
dispatching a request. Capabilities are not reusable for a different node,
operation, or file.

HOGS performs its normal session and role checks before issuing browser
capabilities. File management remains restricted to HOGS administrators.
Direct browser requests must also have an origin listed in
`api.allowed_origins`; the agent returns a specific CORS origin rather than a
wildcard. Download links may carry their short-lived capability in the query
string so native browser downloads work. Other requests use a bearer header.

Rotate a node by deploying a new random secret to HOGS and that one agent.
Removing the node from HOGS revokes its control-plane access immediately;
rotating the agent secret invalidates capabilities that have already been
issued.

The key below `servers` is the immutable server ID. It must match the inventory
manifest's server `id`; changing the panel's display name never changes this
key or any worker route.

## Local confinement and streaming

Every request carries an immutable server ID. Unknown IDs are rejected locally.
Systemd actions use only the configured unit, and file and restore targets are
confined to the selected server's `data_dir`. Symlink path components are
rejected.

File downloads stream and support HTTP ranges. Uploads stream into a temporary
file on the destination filesystem, sync it, then rename it atomically.
Conditional writes use `If-Match` and the ETag returned by reads so an editor
cannot silently overwrite a concurrently changed file. Large payloads therefore
do not need to be buffered by HOGS, and independent HTTP requests can proceed
concurrently.

This is a small HTTP capability API rather than WebDAV. HOGS authorizes the
user, server, operation, path allowlist, and upload size before minting a
short-lived token scoped to one exact agent request. The browser then streams
directly to or from a direct agent. A tunneled agent exposes the same API
privately and HOGS relays the request.

Console output is an NDJSON HTTP stream backed by the systemd journal.
Commands, status queries, backups, file transfers, and console streams use
independent requests. RCON and restic credentials remain in node-local files.

## Transactional restores

Restore is a separately deployable capability and should be enabled only for
instance administrators. A restore request carries a hexadecimal snapshot ID
and must repeat the immutable server ID as confirmation. The worker then:

1. serializes the restore against lifecycle, command, whitelist, backup, and
   mutating file operations for that server;
2. records whether the systemd unit was running and stops it when necessary;
3. creates a tagged pre-restore safety snapshot of the configured `data_dir`;
4. confirms that the requested snapshot contains that path, calculates its
   restore size, and requires enough free space for staging plus a reserve;
5. restores only `data_dir` into a sibling staging directory on the same
   filesystem and asks restic to verify the restored file contents;
6. verifies that the staged root is a real directory, renames the live data to
   a rollback path, and atomically renames the staged data into place;
7. returns the unit to its previous running or stopped state and verifies the
   resulting systemd state; and
8. reactivates the original data and service automatically if startup or
   verification fails.

After successful verification the temporary rollback directory is removed.
The tagged safety snapshot remains in restic and its ID is returned to HOGS.
Restic restores file metadata, including the game-data root ownership and mode,
from the selected snapshot. Snapshot lists are filtered to snapshots whose
backup roots contain the server's configured data path, including broader
host-level `/srv` snapshots.

The worker never accepts an arbitrary restore target. This prevents a panel
request from restoring outside the locally allowlisted server data directory or
from creating restic's absolute-path tree inside the live directory.

Game-driver operations use dedicated endpoints rather than general file access.
For Minecraft and Factorio, `GET` and `POST
/v1/servers/{serverID}/whitelist` use RCON while the unit is running and
atomically read or update the game's native whitelist file while it is stopped.
Valheim uses its native `permittedlist.txt` in both states because its dedicated
server has no corresponding management command. A running Valheim server does
not reload this file through an administrative command, so the API marks those
saved changes as requiring a restart.

File-backed updates reject symlinks and malformed data, preserve ownership and
permissions, sync before rename, and recheck the unit state immediately before
replacement. Lifecycle and whitelist changes share a per-server lock. An
online-mode Minecraft addition requires a profile UUID verified by HOGS;
offline-mode UUIDs are derived locally using Minecraft's standard algorithm.
