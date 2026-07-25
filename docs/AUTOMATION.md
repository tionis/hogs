# Automation rules

Automation rules evaluate a condition on a six-field cron schedule and, when
that condition is satisfied, run `start`, `stop`, or `restart` through the same
worker backend and final constraint checks as an interactive action.

Each server's `/servers/<name>/automation` tab includes templates for idle
shutdown, nightly restart, scheduled start, and a nightly empty-server
shutdown. Instance administrators and users with that server's
`automation.manage` capability can use it. Templates only populate the editor:
every generated field remains editable before the rule is created. Existing
rules expand into the same editor and show their latest evaluation history.

## Conditions

Conditions are boolean expressions with these values:

- `server.ID`, `server.Name`, and `server.Running`
- `activity.Online`, `activity.Fresh`, `activity.PlayersKnown`,
  `activity.Players`, `activity.MaxPlayers`, and
  `activity.ObservationAgeSeconds`
- `time.Now`, `time.Hour`, and `time.Weekday`
- `duration("15m")`, which converts a Go duration to seconds

For example:

```text
server.Running && activity.Fresh && activity.PlayersKnown && activity.Players == 0
```

Set a 900-second stability period on that condition to mean “the server has
been continuously running with authoritative zero-player observations for 15
minutes.” The timer is persisted across HOGS restarts. It resets whenever the
condition becomes false, including when telemetry becomes stale, the worker is
unreachable, or the game driver cannot report occupancy.

The cooldown starts only after a successful action. It prevents another action
while a condition remains true. Conditions and cooldowns never bypass normal
server access constraints; those are evaluated immediately before execution.

## Inventory

Inventory-managed rules use the existing `schedules` collection:

```json
{
  "name": "stop_when_idle",
  "schedule": "0 * * * * *",
  "serverId": "example",
  "action": "stop",
  "condition": "server.Running && activity.Fresh && activity.PlayersKnown && activity.Players == 0",
  "stabilitySeconds": 900,
  "cooldownSeconds": 900,
  "enabled": true
}
```

The server is referenced by immutable management ID. Inventory reconciliation
reloads the running scheduler after applying changes.
