CREATE TABLE server_join_settings (
    server_id        INTEGER PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    enforcement_mode TEXT NOT NULL DEFAULT 'auto'
        CHECK (enforcement_mode IN ('auto', 'whitelist', 'password'))
);
