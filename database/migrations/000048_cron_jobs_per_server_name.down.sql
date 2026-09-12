DROP INDEX IF EXISTS idx_cron_jobs_server_name_unique;

CREATE UNIQUE INDEX IF NOT EXISTS idx_cron_jobs_name_unique
    ON cron_jobs(name);
