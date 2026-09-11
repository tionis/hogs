CREATE TABLE user_admins (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_username TEXT NOT NULL,
    server_id   INTEGER NOT NULL,
    username    TEXT NOT NULL,
    UNIQUE(user_username, server_id),
    FOREIGN KEY(server_id) REFERENCES servers(id) ON DELETE CASCADE
);
