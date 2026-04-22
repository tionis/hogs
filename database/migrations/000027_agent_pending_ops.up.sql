CREATE TABLE agent_pending_ops (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    request_id TEXT NOT NULL UNIQUE,
    agent_id INTEGER NOT NULL,
    op_type TEXT NOT NULL,
    payload TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at TEXT NOT NULL,
    resolved INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_agent_pending_ops_agent ON agent_pending_ops(agent_id);
CREATE INDEX idx_agent_pending_ops_resolved ON agent_pending_ops(resolved);
