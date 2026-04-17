CREATE TABLE user_whitelists (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_email  TEXT NOT NULL,
    server_id   INTEGER NOT NULL,
    username    TEXT NOT NULL,
    UNIQUE(user_email, server_id),
    FOREIGN KEY(server_id) REFERENCES servers(id) ON DELETE CASCADE
);