# Node Agent

`hogs-agent` is one node-scoped process with an explicit allowlist of game
servers. It does not provision units or data. Gandalf renders the configuration,
private identity, and systemd sandbox.

The agent and control plane each embed `wireguard-go` with the userspace
netstack. No host TUN interface, route, network namespace, or `CAP_NET_ADMIN`
is required. Agents initiate the encrypted UDP session to the control plane and
expose an HTTP/2 API only on their dedicated overlay address. WireGuard peer
identity and `/128` AllowedIPs replace agent enrollment tokens.

Only `HOGS_AGENT_CONFIG` is required in the environment:

```yaml
node: destiny
restic_bin: /usr/bin/restic
health_addr: 127.0.0.1:9080
wireguard:
  address: "fd42:686f:6773::2"
  private_key_file: /etc/hogs-agent/wireguard.key
  listen_port: 0
  api_port: 9081
  peer:
    public_key: "<control-plane-public-key>"
    allowed_ip: "fd42:686f:6773::1/128"
    endpoint: "hogs.example.test:51829"
    persistent_keepalive: 25
servers:
  cog:
    unit: minecraft-cog.service
    game_type: minecraft
    data_dir: /srv/mc/cog
    console:
      type: rcon
      host: 127.0.0.1
      port: 25575
      password_file: /etc/hogs-agent/cog-rcon-password
    backup:
      environment_file: /etc/restic/restic.env
```

Every request carries a server name. Unknown names are rejected locally.
Systemd actions use only the configured unit, and file and restore targets are
confined to the selected server's `data_dir`. Symlink path components are
rejected.

File downloads stream and support HTTP ranges. Uploads stream into a temporary
file on the destination filesystem, sync it, then rename it atomically.
Conditional writes use `If-Match` and the ETag returned by reads so an editor
cannot silently overwrite a concurrently changed file.

Console output is an NDJSON HTTP/2 stream backed by the systemd journal.
Commands, status queries, backups, file transfers, and console streams use
independent HTTP/2 streams rather than sharing a serialized message channel.
RCON and restic credentials remain in node-local files.
