ALTER TABLE cron_jobs ADD COLUMN condition TEXT NOT NULL DEFAULT 'true';
ALTER TABLE cron_jobs ADD COLUMN stability_seconds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE cron_jobs ADD COLUMN cooldown_seconds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE cron_jobs ADD COLUMN condition_true_since TEXT;
ALTER TABLE cron_jobs ADD COLUMN last_action_at TEXT;
