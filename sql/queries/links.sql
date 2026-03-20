-- name: CreateLink :one
INSERT INTO links (original_url)
VALUES ($1)
RETURNING *;
-- name: UpdateLinkSlug :one
UPDATE links
SET slug = $1
WHERE id = $2
RETURNING *;
-- name: GetLinkBySlug :one
SELECT *
FROM links
WHERE slug = $1
    AND is_deleted = FALSE;
-- name: GetLinkByID :one
SELECT *
FROM links
WHERE id = $1
    AND is_deleted = FALSE;
-- name: SoftDeleteLink :one
UPDATE links
SET is_deleted = TRUE
WHERE slug = $1
    AND is_deleted = FALSE
RETURNING *;
-- name: IncrementTotalClicksSince :exec
UPDATE links
SET total_clicks = total_clicks + sub.cnt
FROM (
        SELECT link_id,
            COUNT(*) AS cnt
        FROM click_logs
        WHERE clicked_at > $1
        GROUP BY link_id
    ) AS sub
WHERE links.id = sub.link_id;