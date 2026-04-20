CREATE TABLE agents (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL UNIQUE,
    token       TEXT NOT NULL,
    node_name   TEXT NOT NULL DEFAULT '',
    capabilities TEXT NOT NULL DEFAULT '[]',
    created_at  TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen   TEXT,
    online      INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_agents_token ON agents(token);
CREATE INDEX idx_agents_node_name ON agents(node_name);