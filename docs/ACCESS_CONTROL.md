# Server access control

HOGS separates three concerns that were previously mixed into one expression:

1. **Deployment capability** declares what a server backend may do. Inventory
   reconciliation controls enabled actions, console/RCON, backups, restore, and
   writable path roots.
2. **Interactive access grants** declare who may use an enabled capability.
   A grant targets one user email, one OIDC/SCIM group, or any authenticated
   user and lists explicit capabilities. Administrators are implicit
   superusers. File modification remains administrator-only.
3. **Operational constraints** decide whether an otherwise authorized action
   may run now, for example requiring an empty server before stopping it.

Once a server has at least one structured access grant, grants replace its
legacy expression ACL for non-administrators. The expression remains as an
advanced fallback for installations that have not migrated. A grant cannot
enable an action disabled by deployment policy.

Current grant capabilities are `status`, `start`, `stop`, `restart`, `command`,
`console`, `whitelist`, and `backup`. `command` covers only commands separately
approved for the server.

## Game identities and whitelists

A game identity links an authenticated HOGS user to one in-game username per
game type. Users can manage their own links from **My Servers**; administrators
can assign or correct links from **Users and Game Identities**. Server
whitelisting reuses the linked identity and records the server-specific
membership separately.

Whitelist commands are game-type adapters, not administrator-provided command
templates. Minecraft Java currently uses `whitelist add` and `whitelist
remove`; unsupported game types do not expose whitelist actions. This avoids
turning identity fields into arbitrary console commands.

Direct file transfers use short-lived, exact-request capability tokens as
described in [the agent contract](AGENT.md). Server access is decided by HOGS;
the agent independently confines the request to its local server and path
allowlist.

Machine administrators can manage the same state through:

- `GET`/`PUT /api/v1/servers/{serverName}/access-grants`
- `DELETE /api/v1/servers/{serverName}/access-grants/{grantID}`
- `GET`/`PUT`/`DELETE /api/v1/game-identities`

These endpoints require an admin API key. Interactive users can only change
their own identity; interactive administrators can manage identities and grants
through the corresponding UI.
