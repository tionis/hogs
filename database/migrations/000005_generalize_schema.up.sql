ALTER TABLE servers RENAME COLUMN blue_map_url TO map_url;
ALTER TABLE servers RENAME COLUMN modpack_url TO mod_url;
ALTER TABLE servers ADD COLUMN game_type TEXT DEFAULT 'minecraft';
