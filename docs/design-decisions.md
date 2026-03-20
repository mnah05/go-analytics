# Design Decisions

Documenting key architecture and design decisions for future reference.

---

## Incremental `total_clicks` Update (2026-03-17)

**Context:** We needed a way to keep `links.total_clicks` in sync with `click_logs`.

**Rejected approaches:**

1. **Per-click `IncrementTotal`** (`UPDATE links SET total_clicks = total_clicks + 1 WHERE id = $1`)
   - N clicks = N UPDATE round trips
   - Row-level lock contention on hot links
   - WAL write overhead per click

2. **Full table scan `BatchUpdateTotalLink`** (`COUNT(*) FROM click_logs GROUP BY link_id`)
   - Scans the entire `click_logs` table every run
   - Gets progressively slower as the table grows
   - Wasteful when most rows haven't changed

**Chosen approach: `IncrementTotalClicksSince`**

```sql
UPDATE links
SET total_clicks = total_clicks + sub.cnt
FROM (
    SELECT link_id, COUNT(*) AS cnt
    FROM click_logs
    WHERE clicked_at > $1
    GROUP BY link_id
) AS sub
WHERE links.id = sub.link_id;
```

- Takes a timestamp param (`$1`) = last run time
- Only counts clicks since that timestamp, leveraging `idx_click_logs_clicked` index
- Adds the delta to `total_clicks` instead of recomputing from scratch
- Runs via asynq worker every few minutes for near-realtime accuracy
- Worker must track and pass the last run timestamp
