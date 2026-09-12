-- Automation rule names are only unique within a server. The original global
-- uniqueness made two servers unable to use the same rule name and produced a
-- raw SQLite constraint error in the GUI.
DROP INDEX IF EXISTS idx_cron_jobs_name_unique;

CREATE UNIQUE INDEX IF NOT EXISTS idx_cron_jobs_server_name_unique
    ON cron_jobs(server_id, name);
