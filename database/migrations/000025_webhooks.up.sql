CREATE TABLE webhooks (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    name      TEXT NOT NULL,
    url       TEXT NOT NULL,
    secret    TEXT NOT NULL DEFAULT '',
    events    TEXT NOT NULL DEFAULT '[]',
    enabled   INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);