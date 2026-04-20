ALTER TABLE users ADD COLUMN external_id TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN active INTEGER NOT NULL DEFAULT 1;

CREATE TABLE scim_groups (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    external_id TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL UNIQUE,
    created_at  TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE scim_group_members (
    group_id    INTEGER NOT NULL REFERENCES scim_groups(id) ON DELETE CASCADE,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, user_id)
);

CREATE INDEX idx_users_external_id ON users(external_id);