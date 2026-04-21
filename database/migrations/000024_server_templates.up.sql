CREATE TABLE server_templates (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    name             TEXT NOT NULL,
    game_type        TEXT NOT NULL,
    default_settings TEXT NOT NULL DEFAULT '{}',
    default_commands TEXT NOT NULL DEFAULT '[]',
    default_acl     TEXT NOT NULL DEFAULT '',
    default_tags     TEXT NOT NULL DEFAULT '[]',
    description      TEXT NOT NULL DEFAULT ''
);