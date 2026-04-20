CREATE TABLE sessions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  TEXT NOT NULL UNIQUE,
    user_sub    TEXT NOT NULL DEFAULT '',
    user_email  TEXT NOT NULL DEFAULT '',
    user_role   TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at  TEXT NOT NULL
);

CREATE INDEX idx_sessions_session_id ON sessions(session_id);
CREATE INDEX idx_sessions_user_sub ON sessions(user_sub);
CREATE INDEX idx_sessions_expires_at ON sessions(expires_at);