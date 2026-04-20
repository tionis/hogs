DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS cron_jobs;
DROP TABLE IF EXISTS constraints;
DROP TABLE IF EXISTS server_tags;
DROP TABLE IF EXISTS command_schemas;

ALTER TABLE pterodactyl_servers RENAME TO pterodactyl_servers_old;
CREATE TABLE pterodactyl_servers (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id       INTEGER NOT NULL UNIQUE,
    ptero_server_id TEXT NOT NULL,
    ptero_identifier TEXT NOT NULL DEFAULT '',
    allowed_actions TEXT NOT NULL DEFAULT '[]'
);
INSERT INTO pterodactyl_servers (id, server_id, ptero_server_id, ptero_identifier, allowed_actions)
    SELECT id, server_id, ptero_server_id, ptero_identifier, allowed_actions
    FROM pterodactyl_servers_old;
DROP TABLE pterodactyl_servers_old;