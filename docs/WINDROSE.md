# Windrose servers

HOGS supports Windrose as an embedded, agent-managed game type. The node agent
provides start, stop, restart, systemd-journal console output, file management,
resource observations, and restic backups. Windrose has no documented status
query, RCON interface, or per-player allowlist, so HOGS uses worker process
observations for availability and the game's shared password for admission.

The upstream dedicated-server guide is the authority for the current image,
configuration fields, system requirements, and save format:
<https://playwindrose.com/dedicated-server-guide/>.

## Container deployment

The official image is `windroseserver/windroseserver:latest`. Its persistent
paths are:

- `/home/ue_user/app/R5/ServerDescription.json` for server configuration;
- `/home/ue_user/app/R5/Saved` for worlds, runtime data, and backups.

For direct connections, publish the selected port as both TCP and UDP. The
upstream default is `7777`; the same value must be used for
`DirectConnectionServerPort` in `ServerDescription.json`.

A Podman Quadlet can use the following shape (adapt paths and ownership to the
host):

```ini
[Unit]
Description=Windrose dedicated server

[Container]
Image=docker.io/windroseserver/windroseserver:latest
ContainerName=windrose
User=ue_user
PublishPort=7777:7777/tcp
PublishPort=7777:7777/udp
Volume=/srv/windrose/Saved:/home/ue_user/app/R5/Saved
Volume=/srv/windrose/ServerDescription.json:/home/ue_user/app/R5/ServerDescription.json

[Service]
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

Windrose must be stopped before editing `ServerDescription.json`. At minimum,
direct-connect deployments should set these persistent fields inside
`ServerDescription_Persistent`:

```json
{
  "UseDirectConnection": true,
  "DirectConnectionServerPort": 7777,
  "DirectConnectionProxyAddress": "0.0.0.0"
}
```

Do not replace or hand-edit `PersistentServerId`. Configure `Password` together
with a matching `IsPasswordProtected` value. The game generates its initial
configuration, invite code, and world during the first start.

## HOGS agent and inventory

Point the agent's `data_dir` at `/srv/windrose`, which keeps both the config and
save tree in file-manager and backup scope. The Quadlet above creates the
systemd unit `windrose.service`:

```yaml
servers:
  windrose:
    unit: windrose.service
    game_type: windrose
    data_dir: /srv/windrose
    backup:
      environment_file: /etc/restic/restic.env
```

Use `host.example:7777` as the HOGS connect address and configure the inventory
record with `gameType: windrose`. Store the game's password in the dedicated
join-password field and select password admission in the server Settings tab;
this lets users with `server.join` reveal it without granting general
`secret.read` access. If players connect through the generated invite code,
add it as a plain details field so it can be copied from the server page.

Windrose worlds are below
`Saved/SaveProfiles/Default/RocksDB_v2/<game-version>/Worlds/<world-id>`.
Back up the whole `/srv/windrose` data root while the server is stopped for the
strongest consistency. Changing a world's `WorldDescription.json` additionally
requires the upstream `R5WorldDescriptionUpdater` workflow before restart.
