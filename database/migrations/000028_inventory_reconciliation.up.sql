CREATE TABLE inventory_state (
    singleton   INTEGER PRIMARY KEY CHECK (singleton = 1),
    generation  TEXT NOT NULL,
    digest      TEXT NOT NULL,
    manifest    TEXT NOT NULL,
    applied_at  TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    actor       TEXT NOT NULL
);

CREATE TABLE inventory_events (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    generation    TEXT NOT NULL,
    timestamp     TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resource_type TEXT NOT NULL,
    resource_key  TEXT NOT NULL,
    action        TEXT NOT NULL,
    actor         TEXT NOT NULL,
    details       TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_inventory_events_generation ON inventory_events(generation);
CREATE INDEX idx_inventory_events_timestamp ON inventory_events(timestamp);

CREATE UNIQUE INDEX idx_cron_jobs_name_unique ON cron_jobs(name);
CREATE UNIQUE INDEX idx_server_templates_name_unique ON server_templates(name);
CREATE UNIQUE INDEX idx_webhooks_name_unique ON webhooks(name);
CREATE UNIQUE INDEX idx_notification_channels_name_unique ON notification_channels(name);
CREATE UNIQUE INDEX idx_command_schemas_server_name_unique ON command_schemas(server_id, name);

CREATE TABLE server_management (
    server_id       INTEGER PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    unit_name       TEXT NOT NULL,
    data_path       TEXT NOT NULL,
    operators       TEXT NOT NULL DEFAULT '[]',
    console_enabled INTEGER NOT NULL DEFAULT 0,
    rcon_enabled    INTEGER NOT NULL DEFAULT 0,
    start_enabled   INTEGER NOT NULL DEFAULT 0,
    stop_enabled    INTEGER NOT NULL DEFAULT 0,
    backup_enabled  INTEGER NOT NULL DEFAULT 0,
    restore_enabled INTEGER NOT NULL DEFAULT 0,
    writable_paths  TEXT NOT NULL DEFAULT '[]'
);
