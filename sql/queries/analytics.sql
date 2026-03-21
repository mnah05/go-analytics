-- name: IsEventProcessed :one
SELECT EXISTS(
    SELECT 1 FROM processed_events WHERE task_id = $1
) AS processed;

-- name: MarkEventProcessed :exec
INSERT INTO processed_events (task_id, job_type)
VALUES ($1, $2);

-- name: InsertClickLog :one
INSERT INTO click_logs (link_id, ip_address, referer, country, user_agent, clicked_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetClickLogsByLink :many
SELECT *
FROM click_logs
WHERE link_id = $1
ORDER BY clicked_at DESC
LIMIT $2 OFFSET $3;

-- name: CountClickLogsByLink :one
SELECT COUNT(*) AS total
FROM click_logs
WHERE link_id = $1;

-- name: UpsertDailyStats :exec
INSERT INTO daily_stats (link_id, date, clicks, unique_visitors, countries, referers)
SELECT
    cl.link_id,
    cl.clicked_at::date AS date,
    COUNT(*) AS clicks,
    COUNT(DISTINCT md5(COALESCE(cl.ip_address::text, '') || COALESCE(cl.user_agent, ''))) AS unique_visitors,
    COALESCE(jsonb_object_agg(cl.country, cl.country_cnt) FILTER (WHERE cl.country IS NOT NULL), '{}') AS countries,
    COALESCE(jsonb_object_agg(cl.referer, cl.referer_cnt) FILTER (WHERE cl.referer IS NOT NULL), '{}') AS referers
FROM (
    SELECT
        link_id,
        clicked_at,
        ip_address,
        user_agent,
        country,
        referer,
        COUNT(*) OVER (PARTITION BY link_id, clicked_at::date, country) AS country_cnt,
        COUNT(*) OVER (PARTITION BY link_id, clicked_at::date, referer) AS referer_cnt
    FROM click_logs
    WHERE clicked_at >= $1
      AND clicked_at < $2
) cl
GROUP BY cl.link_id, cl.clicked_at::date
ON CONFLICT (link_id, date)
DO UPDATE SET
    clicks = EXCLUDED.clicks,
    unique_visitors = EXCLUDED.unique_visitors,
    countries = EXCLUDED.countries,
    referers = EXCLUDED.referers;

-- name: GetDailyStatsByLink :many
SELECT *
FROM daily_stats
WHERE link_id = $1
    AND date >= $2
    AND date <= $3
ORDER BY date DESC;

-- name: GetLinkStatsSummary :one
SELECT
    COALESCE(SUM(clicks), 0)::bigint AS total_clicks,
    COALESCE(SUM(unique_visitors), 0)::bigint AS total_unique_visitors
FROM daily_stats
WHERE link_id = $1;
