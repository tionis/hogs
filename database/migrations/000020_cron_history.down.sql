DROP TABLE IF EXISTS cron_job_logs;
ALTER TABLE cron_jobs DROP COLUMN last_result;
ALTER TABLE cron_jobs DROP COLUMN last_output;