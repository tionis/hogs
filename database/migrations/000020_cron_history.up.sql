ALTER TABLE cron_jobs ADD COLUMN last_result TEXT DEFAULT '';
ALTER TABLE cron_jobs ADD COLUMN last_output TEXT DEFAULT '';

CREATE TABLE cron_job_logs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    cron_job_id INTEGER NOT NULL REFERENCES cron_jobs(id) ON DELETE CASCADE,
    timestamp   TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    result      TEXT NOT NULL,
    output      TEXT NOT NULL DEFAULT '',
    duration_ms INTEGER NOT NULL DEFAULT 0
);