-- CRITICAL: Every query filters deleted links

CREATE INDEX idx_links_is_deleted ON links (is_deleted)
WHERE
    is_deleted = FALSE;
-- NICE TO HAVE: Recent links queries

CREATE INDEX idx_links_created ON links (created_at DESC)
WHERE
    is_deleted = FALSE;

DROP INDEX idx_daily_stats_link_date;