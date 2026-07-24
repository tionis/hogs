DROP INDEX idx_cron_jobs_name_unique;
ALTER TABLE cron_job_logs RENAME TO cron_job_logs_stable;
ALTER TABLE cron_jobs RENAME TO cron_jobs_stable;
CREATE TABLE cron_jobs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL,
    schedule    TEXT NOT NULL,
    server_name TEXT NOT NULL,
    action      TEXT NOT NULL,
    params      TEXT NOT NULL DEFAULT '{}',
    acl_rule    TEXT NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL DEFAULT 1,
    last_run    TEXT,
    next_run    TEXT,
    last_result TEXT NOT NULL DEFAULT '',
    last_output TEXT NOT NULL DEFAULT ''
);
INSERT INTO cron_jobs
SELECT jobs.id, jobs.name, jobs.schedule, servers.name, jobs.action, jobs.params,
       jobs.acl_rule, jobs.enabled, jobs.last_run, jobs.next_run,
       jobs.last_result, jobs.last_output
FROM cron_jobs_stable AS jobs
JOIN servers ON servers.id = jobs.server_id;
CREATE TABLE cron_job_logs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    cron_job_id INTEGER NOT NULL REFERENCES cron_jobs(id) ON DELETE CASCADE,
    timestamp   TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    result      TEXT NOT NULL,
    output      TEXT NOT NULL DEFAULT '',
    duration_ms INTEGER NOT NULL DEFAULT 0
);
INSERT INTO cron_job_logs (
    id, cron_job_id, timestamp, result, output, duration_ms
)
SELECT logs.id, logs.cron_job_id, logs.timestamp, logs.result, logs.output,
       logs.duration_ms
FROM cron_job_logs_stable AS logs
JOIN cron_jobs ON cron_jobs.id = logs.cron_job_id;
DROP TABLE cron_job_logs_stable;
DROP TABLE cron_jobs_stable;
CREATE UNIQUE INDEX idx_cron_jobs_name_unique ON cron_jobs(name);

DROP INDEX idx_server_metrics_server;
DROP INDEX idx_server_metrics_timestamp;
ALTER TABLE server_metrics RENAME TO server_metrics_stable;
CREATE TABLE server_metrics (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    server_name  TEXT NOT NULL,
    agent_id     INTEGER REFERENCES agents(id) ON DELETE CASCADE,
    timestamp    TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    online       INTEGER NOT NULL DEFAULT 0,
    players      INTEGER NOT NULL DEFAULT 0,
    max_players  INTEGER NOT NULL DEFAULT 0,
    version      TEXT NOT NULL DEFAULT '',
    cpu_percent  REAL NOT NULL DEFAULT 0,
    memory_used  INTEGER NOT NULL DEFAULT 0,
    memory_total INTEGER NOT NULL DEFAULT 0,
    disk_used    INTEGER NOT NULL DEFAULT 0,
    disk_total   INTEGER NOT NULL DEFAULT 0
);
INSERT INTO server_metrics
SELECT metrics.id, servers.name, metrics.agent_id, metrics.timestamp,
       metrics.online, metrics.players, metrics.max_players, metrics.version,
       metrics.cpu_percent, metrics.memory_used, metrics.memory_total,
       metrics.disk_used, metrics.disk_total
FROM server_metrics_stable AS metrics
JOIN servers ON servers.id = metrics.server_id;
DROP TABLE server_metrics_stable;
CREATE INDEX idx_server_metrics_server ON server_metrics(server_name);
CREATE INDEX idx_server_metrics_timestamp ON server_metrics(timestamp);

DROP INDEX idx_server_resource_samples_server_time;
ALTER TABLE server_resource_samples RENAME TO server_resource_samples_stable;
CREATE TABLE server_resource_samples (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    server_name          TEXT NOT NULL,
    timestamp            TEXT NOT NULL,
    running              INTEGER NOT NULL DEFAULT 0,
    cpu_percent          REAL,
    cpu_limit_percent    REAL,
    memory_current_bytes INTEGER,
    memory_peak_bytes    INTEGER,
    memory_high_bytes    INTEGER,
    memory_limit_bytes   INTEGER
);
INSERT INTO server_resource_samples
SELECT samples.id, servers.name, samples.timestamp, samples.running,
       samples.cpu_percent, samples.cpu_limit_percent,
       samples.memory_current_bytes, samples.memory_peak_bytes,
       samples.memory_high_bytes, samples.memory_limit_bytes
FROM server_resource_samples_stable AS samples
JOIN servers ON servers.id = samples.server_id;
DROP TABLE server_resource_samples_stable;
CREATE INDEX idx_server_resource_samples_server_time
    ON server_resource_samples(server_name, timestamp);

DROP INDEX idx_servers_management_id;
ALTER TABLE servers DROP COLUMN management_id;
