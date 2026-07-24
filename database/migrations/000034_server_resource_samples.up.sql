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

CREATE INDEX idx_server_resource_samples_server_time
    ON server_resource_samples(server_name, timestamp);
