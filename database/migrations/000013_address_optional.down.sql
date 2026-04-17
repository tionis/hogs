CREATE TABLE servers_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    address TEXT NOT NULL,
    description TEXT DEFAULT '',
    map_url TEXT DEFAULT '',
    mod_url TEXT DEFAULT '',
    state TEXT DEFAULT 'online',
    game_type TEXT DEFAULT 'minecraft',
    show_motd INTEGER DEFAULT 1,
    metadata TEXT DEFAULT '{}'
);
INSERT INTO servers_new SELECT id, name, address, description, map_url, mod_url, state, game_type, show_motd, metadata FROM servers;
DROP TABLE servers;
ALTER TABLE servers_new RENAME TO servers;