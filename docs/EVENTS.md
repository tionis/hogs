# Event streams

HOGS can publish its activity as CloudEvents 1.0 to a PicoMQ server that
follows the streams contract served at `/_spec`; its registry,
`/events/hogs/_registry`, is listed in `/events/_registry` below the root
`/_registry`. Publication is
off unless both `HOGS_EVENTS_URL` and `HOGS_EVENTS_TOKEN` are set.

| Variable | Meaning |
| --- | --- |
| `HOGS_EVENTS_URL` | PicoMQ stream API, for example `http://picomq:4437`. |
| `HOGS_EVENTS_TOKEN` | Write-only token scoped to `/events/hogs/`. |
| `HOGS_EVENTS_INSTANCE` | Producer id and default source suffix, for example `games.example.test`. |
| `HOGS_EVENTS_SOURCE` | CloudEvents `source`; defaults to `/hogs/<instance>`. |

## Streams and types

| Stream | Type | Emitted when |
| --- | --- | --- |
| `/events/hogs/actions` (kept forever) | `dev.tionis.hogs.action.recorded.v1` | every audit-log entry: lifecycle, updates, backups, restores, whitelist and access changes, with actor and result |
| `/events/hogs/servers` (90 days) | `dev.tionis.hogs.server.status.changed.v1` | a managed server goes online or offline |
| `/events/hogs/servers` | `dev.tionis.hogs.server.players.changed.v1` | the known player count of an online server changes |

`subject` is the server's immutable management id. The full JSON Schemas and
examples are in `events/definitions.go` and are appended to
`/events/hogs/_registry` on startup whenever they change, so consumers read
them from PicoMQ.

## Delivery

Events go to a SQLite outbox (`event_outbox`) in the same transaction that
assigns their per-stream producer sequence. A background flusher appends them
in order with `Pico-Producer-Id/-Epoch/-Seq`, so a retry is acknowledged
without a duplicate, and publication never blocks or fails the action that
caused the event. A conflicting epoch (for example after restoring the
database) renumbers the queue under a new epoch; consumers deduplicate by
`(source, id)`.
