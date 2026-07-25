ALTER TABLE cron_jobs DROP COLUMN last_action_at;
ALTER TABLE cron_jobs DROP COLUMN condition_true_since;
ALTER TABLE cron_jobs DROP COLUMN cooldown_seconds;
ALTER TABLE cron_jobs DROP COLUMN stability_seconds;
ALTER TABLE cron_jobs DROP COLUMN condition;
