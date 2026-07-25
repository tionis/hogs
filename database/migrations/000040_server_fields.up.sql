CREATE TABLE server_fields (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id INTEGER NOT NULL,
    field_key TEXT NOT NULL,
    label TEXT NOT NULL,
    value TEXT NOT NULL DEFAULT '',
    placement TEXT NOT NULL CHECK (placement IN ('summary', 'details', 'internal')),
    disclosure TEXT NOT NULL CHECK (disclosure IN ('plain', 'reveal', 'write_only')),
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE CASCADE,
    UNIQUE (server_id, field_key)
);

CREATE INDEX idx_server_fields_server_order
    ON server_fields(server_id, sort_order, id);

-- Preserve the presentation of existing ordinary metadata. Backend-only keys
-- remain metadata because they have established runtime meaning.
INSERT INTO server_fields(server_id, field_key, label, value, placement, disclosure, sort_order)
SELECT servers.id, metadata.key, metadata.key, CAST(metadata.value AS TEXT),
       'summary', 'plain', CAST(metadata.id AS INTEGER)
FROM servers,
     json_each(CASE WHEN json_valid(servers.metadata) THEN servers.metadata ELSE '{}' END) AS metadata
WHERE metadata.type = 'text'
  AND metadata.key NOT IN ('api_token', 'rcon_password', 'directAddress', 'map_lifecycle');

-- Move known operational credentials out of plaintext server metadata. The
-- application seals these transitional values immediately after migrations.
INSERT INTO server_fields(server_id, field_key, label, value, placement, disclosure, sort_order)
SELECT servers.id, 'api_token', 'API token',
       json_extract(CASE WHEN json_valid(servers.metadata) THEN servers.metadata ELSE '{}' END, '$.api_token'),
       'internal', 'write_only', 1000
FROM servers
WHERE COALESCE(json_extract(CASE WHEN json_valid(servers.metadata) THEN servers.metadata ELSE '{}' END, '$.api_token'), '') <> '';

INSERT INTO server_fields(server_id, field_key, label, value, placement, disclosure, sort_order)
SELECT servers.id, 'rcon_password', 'RCON password',
       json_extract(CASE WHEN json_valid(servers.metadata) THEN servers.metadata ELSE '{}' END, '$.rcon_password'),
       'internal', 'write_only', 1001
FROM servers
WHERE COALESCE(json_extract(CASE WHEN json_valid(servers.metadata) THEN servers.metadata ELSE '{}' END, '$.rcon_password'), '') <> '';

UPDATE servers
SET metadata = json_remove(metadata, '$.api_token', '$.rcon_password')
WHERE json_type(CASE WHEN json_valid(metadata) THEN metadata ELSE '{}' END, '$.api_token') IS NOT NULL
   OR json_type(CASE WHEN json_valid(metadata) THEN metadata ELSE '{}' END, '$.rcon_password') IS NOT NULL;
