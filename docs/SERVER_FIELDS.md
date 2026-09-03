# Server fields and secrets

HOGS keeps presentation placement, disclosure, and authorization independent.
An instance administrator manages fields from a server's **Settings** tab.

## Placement

- `summary` appears on the home dashboard and in Server Info.
- `details` appears only in Server Info.
- `internal` is never rendered and is reserved for backend credentials.

## Disclosure

- `plain` is included in ordinary page and API representations appropriate to
  its placement.
- `reveal` is rendered as a mask. The browser obtains its value only after an
  explicit, CSRF-protected request by a user with `secret.read`.
- `write_only` can be set or replaced by an instance administrator but is never
  returned through the UI. Game drivers receive it internally by its field key.

Reveal fields must use `details`; write-only fields must use `internal`. These
constraints prevent a secret from accidentally becoming dashboard metadata.
Join passwords are reveal fields with the reserved key `join_password`. Set or
replace one under **Server → Settings → Join access**; it is intentionally not
configured through the generic field editor. In shared-password admission mode,
`server.join` can reveal only this field. RCON passwords, API bearer tokens,
other reveal fields, and management credentials remain separate:
operator-facing reveal fields require `secret.read`, while backend credentials
are write-only fields.

Secret field values use authenticated AES-GCM encryption at rest, bound to the
server ID and field key. Ordinary reads return no secret value. Reveal responses
use `Cache-Control: no-store`, and every successful, failed, or denied reveal is
audited without its value.

Set `SERVER_SECRET_KEY` to a stable random value of at least 32 bytes. HOGS
falls back to `SESSION_SECRET` for compatibility, but a dedicated key is
recommended. Losing or changing this key makes stored secrets unreadable, so it
must be backed up with the database and rotated only through a future explicit
re-encryption workflow.

Migration 40 converts ordinary legacy metadata to summary fields and moves the
known `api_token` and `rcon_password` metadata keys to encrypted write-only
fields. Secret-like values are rejected from inventory metadata, but the
reconciliation manifest accepts them under `secretFields` (and
`PUT /api/v1/servers/{serverName}/secret-fields` applies them imperatively):
only `api_token` and `rcon_password` are automation-managed, values persist
as HMAC fingerprints in inventory state and sealed ciphertext in server
fields, and an empty value removes the field. Server fields remain
application-managed and survive inventory reconciliation; a dashboard field
edit round-trips manifest-managed secrets untouched.
