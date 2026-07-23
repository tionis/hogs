CREATE TABLE console_history (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id   INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    stream      TEXT NOT NULL CHECK(stream IN ('server', 'command', 'response', 'error')),
    line        TEXT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_console_history_server_id
    ON console_history(server_id, id);
