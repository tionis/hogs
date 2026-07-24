CREATE TABLE server_access_grants_v2 (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id    INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    subject_type TEXT NOT NULL CHECK (subject_type IN ('user', 'group', 'authenticated', 'everyone')),
    subject      TEXT NOT NULL,
    effect       TEXT NOT NULL DEFAULT 'allow' CHECK (effect IN ('allow', 'deny')),
    capabilities TEXT NOT NULL DEFAULT '[]',
    UNIQUE(server_id, subject_type, subject, effect)
);

INSERT INTO server_access_grants_v2(id,server_id,subject_type,subject,effect,capabilities)
SELECT id,server_id,subject_type,subject,'allow',
       COALESCE((
           SELECT json_group_array(capability)
           FROM (
               SELECT CASE value
                   WHEN 'console' THEN 'console.read'
                   WHEN 'file' THEN 'file.read'
                   WHEN 'whitelist' THEN 'whitelist.self'
                   WHEN 'backup' THEN 'backup.list'
                   WHEN 'restore' THEN 'backup.restore'
                   ELSE value
               END AS capability
               FROM json_each(server_access_grants.capabilities)
               UNION
               SELECT 'console.write' FROM json_each(server_access_grants.capabilities) WHERE value = 'console'
               UNION
               SELECT 'file.write' FROM json_each(server_access_grants.capabilities) WHERE value = 'file'
               UNION
               SELECT 'backup.create' FROM json_each(server_access_grants.capabilities) WHERE value = 'backup'
           )
       ), '[]')
FROM server_access_grants;

DROP TABLE server_access_grants;
ALTER TABLE server_access_grants_v2 RENAME TO server_access_grants;
CREATE INDEX idx_server_access_grants_server ON server_access_grants(server_id);
