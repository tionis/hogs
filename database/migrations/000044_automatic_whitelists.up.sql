-- Joining a server is an access decision. Whitelist synchronization is one
-- enforcement mechanism for that decision, not an action performed by users.
UPDATE server_access_grants
SET capabilities = replace(capabilities, '"whitelist.self"', '"server.join"')
WHERE capabilities LIKE '%"whitelist.self"%';

ALTER TABLE game_identities RENAME TO game_identities_legacy;

CREATE TABLE game_identities (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    user_username TEXT NOT NULL,
    game_type     TEXT NOT NULL,
    username      TEXT NOT NULL,
    source        TEXT NOT NULL DEFAULT 'scim' CHECK (source IN ('self', 'admin', 'scim')),
    updated_at    TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    external_id   TEXT NOT NULL DEFAULT '',
    UNIQUE(user_username, game_type)
);

INSERT INTO game_identities(id,user_username,game_type,username,source,updated_at,external_id)
SELECT id,user_username,game_type,username,source,updated_at,external_id
FROM game_identities_legacy;

DROP TABLE game_identities_legacy;
CREATE INDEX idx_game_identities_user ON game_identities(user_username);
