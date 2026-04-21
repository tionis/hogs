CREATE TABLE api_keys (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL,
    key_hash   TEXT NOT NULL UNIQUE,
    key_prefix TEXT NOT NULL,
    role       TEXT NOT NULL DEFAULT 'user',
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_used  TEXT,
    expires_at TEXT
);