ALTER TABLE servers RENAME COLUMN map_url TO blue_map_url;
ALTER TABLE servers RENAME COLUMN mod_url TO modpack_url;
ALTER TABLE servers DROP COLUMN game_type;
