# Node Agent

`hogs-agent` is one node-scoped process with an explicit allowlist of game
servers. It does not provision units or data. Gandalf renders the configuration
and systemd sandbox; HOGS sends management requests for named allowlisted
servers over one outbound WebSocket.

Set `HOGS_AGENT_TOKEN` and `HOGS_AGENT_CONFIG` in a root-readable environment
file. The token is sent only as an Authorization header. The YAML configuration
has this shape:

```yaml
node: destiny
server_url: wss://games.example.test/agent/ws
restic_bin: /usr/bin/restic
health_addr: 127.0.0.1:9080
servers:
  cog:
    unit: minecraft-cog.service
    game_type: minecraft
    data_dir: /srv/mc/cog
    address: destiny.example.test:25565
    exclusive_group: destiny-games
    console:
      type: rcon
      host: 127.0.0.1
      port: 25575
      password_file: /etc/hogs-agent/cog-rcon-password
    backup:
      environment_file: /etc/restic/restic.env
```

Every operation carries a server name. Unknown names are rejected locally.
Systemd actions use only the configured unit, and file and restore targets are
confined to the selected server's `data_dir`. RCON and restic credentials are
read from node-local files and are never sent to HOGS or the browser. The
backup environment parser accepts literal `export KEY=value` entries without
executing shell syntax and requires a repository plus password source.
Pending-operation persistence stores no command, file, or backup request
payloads.

The agent registers its node, full server-name allowlist, and observed
capabilities. It sends an independent status report per server, so agent
reachability is not confused with a stopped game unit. For running Minecraft
and Factorio servers with RCON, the report includes a verified player count;
failed or unavailable queries are marked unknown instead of being reported as
an empty server.
