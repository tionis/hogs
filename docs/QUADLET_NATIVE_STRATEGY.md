# Quadlet-Native Strategy

HOGS should manage game servers without becoming the system that provisions
them. The provisioning source of truth remains Ansible and systemd quadlets.
HOGS provides authenticated runtime control, policy, audit, file access,
console access, and user-facing workflows on top of already-deployed services.

## Goals

- Keep Ansible responsible for installing servers, rendering quadlets, assigning
  ports, managing secrets, and defining backup policy.
- Run one `hogs-agent` per capable host or node, not one agent per game server.
- Let each node agent manage only an explicit allowlist of servers and paths.
- Route all user, moderator, API, and cron actions through the same ACL,
  constraint, and audit pipeline.
- Support per-server moderators who can manage runtime operations, files,
  console commands, and backups without SSH or Podman socket access.
- Support regular users who can set their own in-game identities, request
  starts/stops, and participate in server-specific stop policies.
- Keep game-specific behavior in modules rather than generic shell execution.

## Non-Goals

- HOGS does not generate quadlets.
- HOGS does not allocate public ports.
- HOGS does not install game binaries, mod loaders, or server jars.
- HOGS does not expose a host shell, Podman socket, or unrestricted `podman exec`
  to moderators or users.
- HOGS does not delete server data when a server is unlinked.

## Control Plane

The central HOGS service owns:

- OIDC login and session handling.
- SCIM-provisioned users and groups from Authentik.
- Per-server role grants and ACL expressions.
- User game identities such as Minecraft usernames.
- Start/stop/restart requests and approvals.
- Constraint evaluation such as one active game server per node.
- Audit log and notifications.
- Web UI and API.

It does not need privileged host access. It talks to node agents over the
existing outbound WebSocket channel.

## Node Agent

Each node agent should load a local declarative config rendered by Ansible:

```yaml
node: destiny
servers:
  cog:
    unit: minecraft-cog.service
    game_type: minecraft
    data_dir: /srv/mc/cog
    address: destiny.tionis.dev:25565
    exclusive_group: destiny-games
    console:
      type: rcon
      host: 127.0.0.1
      port: 25575
      password_file: /run/secrets/hogs/cog-rcon-password
```

Every agent request from HOGS must include `serverName`. The agent resolves that
name against its allowlist and rejects requests for unknown servers. All local
operations then use the selected server entry:

- `systemctl start|stop|restart <unit>`
- `journalctl -u <unit>` for log streaming
- file operations rooted at `data_dir`
- restic operations rooted at declared backup paths
- game module calls for console commands and identity changes

## Game Modules

Modules define safe operations for a game type. They are the only place where
game-specific command formatting and protocol handling should live.

Required module contract:

```text
status(server)
online_players(server)
start_allowed(server)
stop_allowed(server, request)
console(server, command)
add_identity(server, user, game_username)
remove_identity(server, user)
```

Examples:

- Minecraft console commands use RCON, never a generic shell.
- Minecraft whitelist changes use RCON commands such as `whitelist add`.
- Factorio console commands use Factorio RCON.
- Valheim may only support process control and query status until a safe admin
  protocol is configured.

The current generic `podman exec ... sh -c` path is a development shortcut and
should not be exposed to non-owner users.

## Roles And Policies

HOGS should distinguish at least these per-server permission levels:

- `viewer`: read status, logs, and public files.
- `player`: set own game identity, request start, request safe stop.
- `moderator`: console, file management, backups, start/stop/restart, whitelist
  moderation.
- `owner`: server configuration, ACLs, restore operations, destructive file
  actions.

Policies are server-specific and evaluated through the engine:

- `start_policy`: immediate, queued, schedule-limited, or exclusive-group gated.
- `stop_policy`: immediate, empty-only, moderator-force, unanimous-online,
  vote-threshold, or idle-timeout.
- `exclusive_group`: only one running server in the group at a time.

The existing expression engine should stay the policy boundary. Built-in helper
functions can grow as needed, but policy should remain declarative and auditable.

## Migration Plan

> 2026-09 note: the Pterodactyl backend stays in the tree as an optional,
> unconfigured backend. Our deployments never set `PTERODACTYL_*`; the
> preferred change is gating its routes and handlers behind configuration,
> not deleting the code, so the panel stays generic and mergeable.

1. Gate Pterodactyl-centered routes and handlers behind configuration instead
   of deleting them. Our deployments leave Pterodactyl unconfigured.
2. Change the agent protocol so all action, command, file, backup, and console
   requests include `serverName`.
3. Replace one-server agent environment variables with a local YAML config.
4. Add game modules and route console/identity operations through them.
5. Render the agent config from Ansible for each node.
6. Deploy HOGS for the existing `cog` Minecraft server.
7. Add additional game servers by adding quadlets and Ansible-managed server
   entries, then granting HOGS users or groups access.
