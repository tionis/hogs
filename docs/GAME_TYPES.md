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
offline whitelist codecs. The worker selects the online or offline backend from
the managed systemd unit state; callers do not implement separate stopped-server
logic. Minecraft uses object entries in `whitelist.json` and Factorio uses a
string array in `server-whitelist.json`. Verified external identity IDs are
stored with linked Minecraft identities so future offline changes do not
require another profile lookup.

Callers should use `Store.ResolveGameDriver`; they must not infer special
behavior from `Server.GameType` directly.
