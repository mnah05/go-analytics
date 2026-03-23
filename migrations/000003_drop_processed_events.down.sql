CREATE TABLE processed_events (
    task_id TEXT PRIMARY KEY,
    job_type TEXT NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT NOW ()
);
