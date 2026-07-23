CREATE TABLE game_types (
    slug         TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    player_noun  TEXT NOT NULL DEFAULT 'Players',
    icon         TEXT NOT NULL DEFAULT '',
    accent_color TEXT NOT NULL DEFAULT '#666666',
    builtin      INTEGER NOT NULL DEFAULT 0
);

INSERT INTO game_types(slug, display_name, player_noun, icon, accent_color, builtin) VALUES
    ('minecraft', 'Minecraft', 'Players', '', '#2e7d32', 1),
    ('satisfactory', 'Satisfactory', 'Engineers', '', '#e65100', 1),
    ('factorio', 'Factorio', 'Engineers', '', '#827717', 1),
    ('valheim', 'Valheim', 'Vikings', '', '#3e2723', 1),
    ('starrupture', 'Star Rupture', 'Players', '', '#4a148c', 1);

INSERT OR IGNORE INTO game_types(slug, display_name)
SELECT DISTINCT game_type, game_type FROM servers WHERE game_type <> '';
