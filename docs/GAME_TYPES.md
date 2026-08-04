# Game type drivers

HOGS separates common server management from optional game-specific behavior.
Lifecycle actions, console access, files, backups, resource metrics, maps, and
access control are generic. A game driver may add protocol status queries,
RCON-based player and version discovery, identity validation, whitelist
commands and offline storage, profile resolution, console-line filtering, and
dashboard detail fields.

## Kinds and availability

Every persisted game type has a `kind`:

- `embedded` selects a driver compiled into HOGS.
- `generic` has presentation metadata only and never selects embedded behavior,
  even if its slug resembles a supported game.

Game types created in the administration UI are always `generic`. Embedded
types can be disabled there. A disabled type cannot be assigned to a new
server, and existing assignments resolve to the generic driver until it is
enabled again. Their common management features remain usable.

This is intentionally not a plugin ABI. A future plugin or declarative-driver
system can introduce another persisted kind without making UI-created records
executable.

## Adding an embedded game

1. Add a `Driver` registration under `gametypes/`. Keep all hooks for the game
   together. Hooks are optional; omit behavior the game does not support.
2. If the game has a public status protocol, implement its querier in `query/`
   and register it by the driver's `StatusProtocol`.
3. Add the embedded seed to migration `000030` for fresh databases and add a
   forward migration for databases that already exist.
4. Add driver and protocol tests. Generic and disabled resolution must remain
   free of all specialized hooks.

Minecraft and Factorio embedded drivers declare their RCON commands and native
file codecs. The worker uses RCON while those servers run and their files while
they are stopped. Minecraft uses object entries in `whitelist.json`; Factorio
uses a string array in `server-whitelist.json`.

Whitelist support is a driver capability, not a claim that every instance has
enabled it. Each server selects automatic, managed-whitelist, or shared-password
admission in its Settings tab. Automatic uses the whitelist when the driver
supports one and otherwise uses the shared password. Selecting shared password
stops HOGS reconciliation even for Minecraft or Factorio, matching deployments
where the native whitelist has been disabled.

Valheim has no dedicated-server command for editing or reloading its allowlist.
Its embedded driver safely reads and atomically updates `permittedlist.txt`
while the server is either running or stopped. Changes saved while it runs are
reported as pending until the next restart. Identities are the case-sensitive
Platform User IDs emitted by Valheim, not display names.

Satisfactory and StarRupture currently provide join-password admission rather
than a native per-player allowlist. Their embedded drivers therefore do not
advertise whitelist support. Password management is a separate server-secret
workflow: configure a details/reveal server field and grant `server.join` to
the intended players. `secret.read` can still reveal the field independently
for an administrator or another trusted operator. Shared passwords must not be
represented as identity-based whitelisting.

Windrose likewise uses its native shared password and invite code rather than
a per-player allowlist. HOGS manages Windrose lifecycle, journal output, files,
backups, resource observations, and the join password through the agent. The
game does not document a status-query or administrative-console protocol, so
the embedded driver intentionally does not advertise either capability. See
[Windrose servers](WINDROSE.md) for deployment and configuration details.

Verified external identity IDs are stored with linked Minecraft identities so
future offline changes do not require another profile lookup.

Callers should use `Store.ResolveGameDriver`; they must not infer special
behavior from `Server.GameType` directly.
