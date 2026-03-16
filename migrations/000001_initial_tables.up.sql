CREATE TABLE
    links (
        id BIGSERIAL PRIMARY KEY,
        slug CHAR(11) UNIQUE NOT NULL,
        original_url TEXT NOT NULL,
        total_clicks BIGINT NOT NULL DEFAULT 0,
        created_at TIMESTAMPTZ NOT NULL DEFAULT NOW (),
        is_deleted BOOLEAN NOT NULL DEFAULT FALSE
    );

CREATE TABLE
    click_logs (
        id UUID PRIMARY KEY DEFAULT gen_random_uuid (),
        link_id BIGINT NOT NULL REFERENCES links (id),
        ip_address INET,
        referer TEXT,
        country VARCHAR(2),
        user_agent TEXT,
        clicked_at TIMESTAMPTZ NOT NULL DEFAULT NOW ()
    );

CREATE INDEX idx_click_logs_link_clicked ON click_logs (link_id, clicked_at DESC);

CREATE INDEX idx_click_logs_clicked ON click_logs (clicked_at DESC);

CREATE TABLE
    daily_stats (
        link_id BIGINT NOT NULL REFERENCES links (id),
        date DATE NOT NULL,
        clicks BIGINT NOT NULL DEFAULT 0,
        unique_visitors BIGINT NOT NULL DEFAULT 0,
        countries JSONB NOT NULL DEFAULT '{}', -- {"US": 500, "IN": 300}
        referers JSONB NOT NULL DEFAULT '{}',
        PRIMARY KEY (link_id, date)
    );

CREATE INDEX idx_daily_stats_link_date ON daily_stats (link_id, date DESC);

CREATE TABLE
    processed_events (
        task_id TEXT PRIMARY KEY,
        job_type TEXT NOT NULL,
        processed_at TIMESTAMPTZ NOT NULL DEFAULT NOW ()
    );