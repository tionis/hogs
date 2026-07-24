# Server access control

HOGS separates three concerns that were previously mixed into one expression:

1. **Deployment capability** declares what a server backend may do. Inventory
   reconciliation controls enabled actions, console/RCON, backups, restore, and
   writable path roots.
2. **Interactive access grants** declare who may use an enabled capability.
   A grant targets one user email, one OIDC/SCIM group, any authenticated user,
   or everyone (including public visitors), and lists explicit capabilities.
   Grants can allow or explicitly deny; deny always wins. Administrators are
   implicit superusers.
3. **Operational constraints** decide whether an otherwise authorized action
   may run now, for example requiring an empty server before stopping it.

Access is deny-by-default for non-administrators, including when a server has no
grants. The legacy expression ACL and operator list are retained only as
deployment compatibility fields and no longer authorize interactive access. A
grant cannot enable an action disabled by deployment policy.

Capabilities are explicit and independently grantable: `view`, `status`,
`start`, `stop`, `restart`, `command`, `console.read`, `console.write`,
`file.read`, `file.write`, `whitelist.self`, `whitelist.manage`, `backup.list`,
`backup.create`, `backup.restore`, and `access.manage`. `command` covers only
commands separately approved for the server. Arbitrary console input requires
`console.write`; read-only console users cannot send it. `whitelist.self` can
only manage the caller's linked identity, while `whitelist.manage` is intended
for server administrators.

Inventory manifests carry `accessGrants` with `subjectType`, `subject`,
`effect`, and `capabilities`. Reconciliation replaces the grant set for each
managed server, making the manifest the authoritative policy. The admin UI
shows the same capability catalog and clearly distinguishes allow from deny.
`GET /api/servers/{serverName}/effective-access` explains the current caller's
effective decision for each capability.

## Instance roles

The interactive role is recalculated at every OIDC login. Membership in
`OIDC_ADMIN_GROUP` grants `admin`; otherwise membership in `OIDC_USER_GROUP`
grants `user`. When no user group is configured, every authenticated
non-administrator receives `user`. Removing someone from the admin group
therefore demotes them at their next login instead of retaining an old database
role. The resolved role is copied into the login session.

## Game identities and whitelists

A game identity links an authenticated HOGS user to one in-game username per
game type. Users can manage their own links from **My Servers**; administrators
can assign or correct links from **Users and Game Identities**. Server
whitelisting reuses the linked identity and records the server-specific
membership separately.

Whitelist commands are game-type adapters, not administrator-provided command
templates. Minecraft Java and Factorio each use their native whitelist commands
and offline file format; unsupported game types do not expose whitelist
actions. This avoids turning identity fields into arbitrary console commands.

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
