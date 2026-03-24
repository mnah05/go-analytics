DROP INDEX idx_links_is_deleted;

DROP INDEX idx_links_created;

CREATE INDEX idx_daily_stats_link_date ON daily_stats (link_id, date DESC);