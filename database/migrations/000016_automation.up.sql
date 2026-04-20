CREATE TABLE command_schemas (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id   INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    display_name TEXT NOT NULL,
    template    TEXT NOT NULL,
    params      TEXT NOT NULL DEFAULT '{}',
    acl_rule    TEXT NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE server_tags (
    server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    tag       TEXT NOT NULL,
    PRIMARY KEY (server_id, tag)
);

CREATE TABLE constraints (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    condition   TEXT NOT NULL,
    strategy    TEXT NOT NULL DEFAULT 'deny',
    priority    INTEGER NOT NULL DEFAULT 0,
    enabled     INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE cron_jobs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL,
    schedule    TEXT NOT NULL,
    server_name TEXT NOT NULL,
    action      TEXT NOT NULL,
    params      TEXT NOT NULL DEFAULT '{}',
    acl_rule    TEXT NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL DEFAULT 1,
    last_run    TEXT,
    next_run    TEXT
);

CREATE TABLE audit_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp   TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    user_email  TEXT NOT NULL,
    server_name TEXT NOT NULL,
    action      TEXT NOT NULL,
    params      TEXT NOT NULL DEFAULT '{}',
    result      TEXT NOT NULL,
    reason      TEXT NOT NULL DEFAULT '',
    source      TEXT NOT NULL DEFAULT 'user'
);

ALTER TABLE pterodactyl_servers ADD COLUMN acl_rule TEXT NOT NULL DEFAULT '';
ALTER TABLE pterodactyl_servers ADD COLUMN node TEXT NOT NULL DEFAULT '';