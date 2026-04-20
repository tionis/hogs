DROP TABLE IF EXISTS scim_group_members;
DROP TABLE IF EXISTS scim_groups;

ALTER TABLE users RENAME TO users_old;
CREATE TABLE users (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    email       TEXT NOT NULL UNIQUE,
    role        TEXT NOT NULL DEFAULT 'user',
    first_seen  TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_login  TEXT
);
INSERT INTO users (id, email, role, first_seen, last_login) SELECT id, email, role, first_seen, last_login FROM users_old;
DROP TABLE users_old;