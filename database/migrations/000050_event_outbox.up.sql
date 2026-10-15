CREATE TABLE event_outbox (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    stream     TEXT NOT NULL,
    seq        INTEGER NOT NULL,
    body       TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(stream, seq)
);

CREATE TABLE event_producers (
    stream   TEXT PRIMARY KEY,
    epoch    INTEGER NOT NULL,
    next_seq INTEGER NOT NULL
);

CREATE TABLE event_registry (
    producer TEXT PRIMARY KEY,
    digest   TEXT NOT NULL
);
