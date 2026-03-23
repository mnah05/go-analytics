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

-- name: UpsertDailyClicks :exec
INSERT INTO daily_stats (link_id, date, clicks, unique_visitors)
SELECT
    link_id,
    clicked_at::date AS date,
    COUNT(*) AS clicks,
    COUNT(DISTINCT md5(COALESCE(ip_address::text, '') || COALESCE(user_agent, ''))) AS unique_visitors
FROM click_logs
WHERE clicked_at >= $1 AND clicked_at < $2
GROUP BY link_id, clicked_at::date
ON CONFLICT (link_id, date)
DO UPDATE SET
    clicks = EXCLUDED.clicks,
    unique_visitors = EXCLUDED.unique_visitors;

-- name: GetCountryStats :many
SELECT link_id, clicked_at::date AS date, country, COUNT(*) AS cnt
FROM click_logs
WHERE clicked_at >= $1 AND clicked_at < $2
    AND country IS NOT NULL
GROUP BY link_id, clicked_at::date, country;

-- name: GetRefererStats :many
SELECT link_id, clicked_at::date AS date, referer, COUNT(*) AS cnt
FROM click_logs
WHERE clicked_at >= $1 AND clicked_at < $2
    AND referer IS NOT NULL
GROUP BY link_id, clicked_at::date, referer;

-- name: UpdateDailyCountries :exec
UPDATE daily_stats
SET countries = $3
WHERE link_id = $1 AND date = $2;

-- name: UpdateDailyReferers :exec
UPDATE daily_stats
SET referers = $3
WHERE link_id = $1 AND date = $2;

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
