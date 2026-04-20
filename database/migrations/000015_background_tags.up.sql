CREATE TABLE IF NOT EXISTS background_tags (
    background_id INTEGER NOT NULL,
    tag TEXT NOT NULL,
    PRIMARY KEY (background_id, tag),
    FOREIGN KEY (background_id) REFERENCES backgrounds(id) ON DELETE CASCADE
);

INSERT OR IGNORE INTO background_tags (background_id, tag)
    SELECT id, theme_mode FROM backgrounds WHERE theme_mode IN ('dark', 'light');

INSERT OR IGNORE INTO background_tags (background_id, tag)
    SELECT id, 'dark' FROM backgrounds WHERE theme_mode = 'all';

INSERT OR IGNORE INTO background_tags (background_id, tag)
    SELECT id, 'light' FROM backgrounds WHERE theme_mode = 'all';

INSERT OR IGNORE INTO background_tags (background_id, tag)
    SELECT id, game_type FROM backgrounds WHERE game_type != 'all';

INSERT OR IGNORE INTO background_tags (background_id, tag)
    SELECT id, 'home' FROM backgrounds WHERE game_type = 'all';