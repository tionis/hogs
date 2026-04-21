CREATE TABLE server_metrics (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    server_name TEXT NOT NULL,
    agent_id    INTEGER REFERENCES agents(id) ON DELETE CASCADE,
    timestamp   TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    online      INTEGER NOT NULL DEFAULT 0,
    players     INTEGER NOT NULL DEFAULT 0,
    max_players INTEGER NOT NULL DEFAULT 0,
    version     TEXT NOT NULL DEFAULT '',
    cpu_percent REAL NOT NULL DEFAULT 0,
    memory_used INTEGER NOT NULL DEFAULT 0,
    memory_total INTEGER NOT NULL DEFAULT 0,
    disk_used   INTEGER NOT NULL DEFAULT 0,
    disk_total INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_server_metrics_server ON server_metrics(server_name);
CREATE INDEX idx_server_metrics_timestamp ON server_metrics(timestamp);