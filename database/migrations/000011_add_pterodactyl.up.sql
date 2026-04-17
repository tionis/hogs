CREATE TABLE pterodactyl_servers (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id       INTEGER NOT NULL,
    ptero_server_id TEXT NOT NULL,
    allowed_actions TEXT NOT NULL DEFAULT '[]',
    FOREIGN KEY(server_id) REFERENCES servers(id) ON DELETE CASCADE,
    UNIQUE(server_id),
    UNIQUE(ptero_server_id)
);

CREATE TABLE pterodactyl_commands (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id    INTEGER NOT NULL,
    command      TEXT NOT NULL,
    display_name TEXT NOT NULL,
    FOREIGN KEY(server_id) REFERENCES servers(id) ON DELETE CASCADE
);