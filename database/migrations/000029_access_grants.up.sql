CREATE TABLE server_access_grants (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id    INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    subject_type TEXT NOT NULL CHECK (subject_type IN ('user', 'group', 'authenticated')),
    subject      TEXT NOT NULL,
    capabilities TEXT NOT NULL DEFAULT '[]',
    UNIQUE(server_id, subject_type, subject)
);

CREATE TABLE game_identities (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_email TEXT NOT NULL,
    game_type  TEXT NOT NULL,
    username   TEXT NOT NULL,
    source     TEXT NOT NULL DEFAULT 'self' CHECK (source IN ('self', 'admin')),
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_email, game_type)
);

CREATE INDEX idx_server_access_grants_server ON server_access_grants(server_id);
CREATE INDEX idx_game_identities_user ON game_identities(user_email);
