ALTER TABLE constraints ADD COLUMN server_id INTEGER REFERENCES servers(id) ON DELETE CASCADE;
ALTER TABLE constraints ADD COLUMN mode TEXT NOT NULL DEFAULT 'require'
    CHECK (mode IN ('require', 'exempt'));

CREATE INDEX idx_constraints_scope_priority
    ON constraints(server_id, enabled, priority DESC);
