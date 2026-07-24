CREATE TABLE server_access_grants_v1 (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id    INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    subject_type TEXT NOT NULL CHECK (subject_type IN ('user', 'group', 'authenticated')),
    subject      TEXT NOT NULL,
    capabilities TEXT NOT NULL DEFAULT '[]',
    UNIQUE(server_id, subject_type, subject)
);

INSERT INTO server_access_grants_v1(server_id,subject_type,subject,capabilities)
SELECT server_id,subject_type,subject,capabilities
FROM server_access_grants
WHERE effect = 'allow' AND subject_type != 'everyone';

DROP TABLE server_access_grants;
ALTER TABLE server_access_grants_v1 RENAME TO server_access_grants;
CREATE INDEX idx_server_access_grants_server ON server_access_grants(server_id);
