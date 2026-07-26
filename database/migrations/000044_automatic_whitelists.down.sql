UPDATE server_access_grants
SET capabilities = replace(capabilities, '"server.join"', '"whitelist.self"')
WHERE capabilities LIKE '%"server.join"%';

DELETE FROM game_identities WHERE source = 'scim';
ALTER TABLE game_identities RENAME TO game_identities_automatic;

CREATE TABLE game_identities (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    user_username TEXT NOT NULL,
    game_type     TEXT NOT NULL,
    username      TEXT NOT NULL,
    source        TEXT NOT NULL DEFAULT 'self' CHECK (source IN ('self', 'admin')),
    updated_at    TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    external_id   TEXT NOT NULL DEFAULT '',
    UNIQUE(user_username, game_type)
);

INSERT INTO game_identities(id,user_username,game_type,username,source,updated_at,external_id)
SELECT id,user_username,game_type,username,source,updated_at,external_id
FROM game_identities_automatic;

DROP TABLE game_identities_automatic;
CREATE INDEX idx_game_identities_user ON game_identities(user_username);
