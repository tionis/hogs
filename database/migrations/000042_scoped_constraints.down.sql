DROP INDEX IF EXISTS idx_constraints_scope_priority;
ALTER TABLE constraints DROP COLUMN mode;
ALTER TABLE constraints DROP COLUMN server_id;
