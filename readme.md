# URL Shortener with Analytics

A URL shortener that tracks every click, including geolocation, referrers, and real-time analytics.
Learn PostgreSQL scaling, Asynq background jobs, and Redis patterns. Build this in 3–4 days.

---

## What This Is

**Short:** A [bit.ly](http://bit.ly) clone with a dashboard that shows click stats.

**Long:** When someone visits your short link (`mysite.com/abc123`), you immediately redirect them and also track:
their country, what site referred them, their device type, and when they clicked. Then you show clear charts.

---

## Core Flows

### 1. Create Short Link

```
POST /api/links
{ "url": "<https://very-long-url.com/something>" }
→ Returns: { "short_url": "mysite.com/abc123", "slug": "abc123" }
```

### 2. Visit Short Link (The Critical Path)

```
GET /abc123
→ Check Redis rate limit (prevent spam)
→ Lookup original URL from PostgreSQL
→ Enqueue "track this click" job to Asynq (async, don't wait)
→ 302 Redirect to original URL
```

**Must complete in under 50ms.**

### 3. Background Processing (Async)

```
Asynq Worker receives click job
→ Geolocate IP → country code
→ Buffer to Redis list
→ Every 5 seconds: batch insert 1000 clicks to PostgreSQL
```

### 4. View Analytics

```
GET /api/links/abc123/stats
→ Query pre-computed daily rollups (fast)
→ Return: { "total_clicks": 5000, "by_country": {...}, "by_referrer": {...} }
```

---

## Why This Architecture

| Problem                | Solution                                                        |
| ---------------------- | --------------------------------------------------------------- |
| Redirect must be fast  | Don't wait for click tracking—enqueue an async job              |
| Too many DB writes     | Buffer to Redis, then batch insert with COPY                    |
| Analytics queries slow | Pre-compute daily summaries, and have the dashboard query those |
| Rate limiting          | Redis sliding window—check before touching PostgreSQL           |

---

## Database Tables

```sql
-- Short links
CREATE TABLE links (
    id UUID PRIMARY KEY,
    slug VARCHAR(16) UNIQUE NOT NULL,        -- "abc123"
    original_url TEXT NOT NULL,               -- where to redirect
    total_clicks BIGINT DEFAULT 0,            -- cached counter
    created_at TIMESTAMPTZ DEFAULT NOW(),
    deleted_at TIMESTAMPTZ                    -- soft delete
);

-- Individual clicks (huge table, heavy writes)
CREATE TABLE click_logs (
    id UUID PRIMARY KEY,
    link_id UUID REFERENCES links(id),
    ip_address INET,
    country_code VARCHAR(2),                  -- "US", "IN"
    user_agent TEXT,
    referrer TEXT,
    clicked_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX idx_click_logs_link_clicked ON click_logs(link_id, clicked_at);
CREATE INDEX idx_click_logs_clicked ON click_logs(clicked_at);

-- Daily summaries (small table, fast reads)
CREATE TABLE daily_stats (
    link_id UUID REFERENCES links(id),
    date DATE NOT NULL,
    clicks BIGINT DEFAULT 0,
    unique_visitors BIGINT DEFAULT 0,
    top_countries JSONB,                    -- {"US": 500, "IN": 300}
    top_referrers JSONB,
    PRIMARY KEY (link_id, date)
);

CREATE INDEX idx_daily_stats_date ON daily_stats(date DESC);

-- Prevent duplicate job processing
CREATE TABLE processed_events (
    event_id VARCHAR(64) PRIMARY KEY,         -- Asynq task ID
    job_type VARCHAR(32),
    processed_at TIMESTAMPTZ DEFAULT NOW()
);
```

---

## Day-by-Day Build

### Day 1: Links + Redirects

**Morning:**

- Setup Go project with Chi router
- Create `links` table
- `POST /api/links` — generate a random slug and store the URL
- `GET /:slug` — look up the URL and return a 302 redirect

**Afternoon:**

- Add a slug uniqueness check with a retry loop
- Add basic Redis rate limiting (sliding window on IP)
- Test: Create a link, visit it, and confirm it redirects

### Day 2: Async Click Tracking

**Morning:**

- Setup Asynq
- On redirect: enqueue a "track_click" job instead of inserting directly
- Create `click_logs` table

**Afternoon:**

- Implement click processor worker
- Add geolocation (use free MaxMind GeoLite2 or mock it)
- Buffer clicks to a Redis list instead of inserting immediately

**Evening:**

- Batch drainer job: every 5 seconds, LPOP 1000 from Redis, COPY to PostgreSQL
- Test: Visit a link 100 times and verify clicks land in the DB via batch insert

### Day 3: Analytics + Dashboard

**Morning:**

- Setup Asynq server with 2 cron jobs:
  **Job A: Batch Inserter** (runs every 5 seconds)
  ```go
  // Register the handler
  mux.HandleFunc("batch_inserter", batchInserterHandler)

  // Schedule it: every 5 seconds
  scheduler.Register("*/5 * * * * *", "batch_inserter", nil)
  ```
  What it does: Drains Redis buffer and batch inserts to PostgreSQL via COPY
  **Job B: Daily Rollup** (runs every 5 minutes for dev, 1 AM for prod)
  ```go
  // For development - every 5 minutes so you can see results
  scheduler.Register("*/5 * * * *", "rollup_job", nil)

  // For production - daily at 1 AM
  // scheduler.Register("0 1 * * *", "rollup_job", nil)
  ```
  What it does: Aggregates `click_logs` → `daily_stats` for the dashboard
- Test cron jobs with simple log output to verify they're firing

**Afternoon:**

- `GET /api/links/:slug/stats` — query `daily_stats`
- Build a simple HTML dashboard showing:
  - Total clicks
  - Clicks over time (line chart)
  - Top countries (bar chart)
  - Top referrers (pie chart)

**Evening:**

- Auto-refresh dashboard every 30 seconds
- Add a "last 24 hours" view vs "all time" toggle

### Day 4: Polish + Load Test

**Morning:**

- Add `processed_events` table for idempotency
- Handle edge cases: deleted links, invalid slugs, rate limit exceeded

**Afternoon:**

- Load test with k6: 1000 redirects/second for 60 seconds
- Verify PostgreSQL handles it (check connection pool, batch inserts working)
- Check dashboard accuracy matches raw click count

---

## Key Patterns Explained

### Batch Insert via COPY

Instead of:

```go
for _, click := range clicks {
    db.Exec("INSERT...", click)  -- 1000 round trips!
}
```

Use:

```go
copyFrom := pgx.CopyFromSlice(len(clicks), func(i int) ([]interface{}, error) {
    return []interface{}{clicks[i].ID, clicks[i].LinkID, ...}, nil
})
conn.CopyFrom(ctx, pgx.Identifier{"click_logs"}, columns, copyFrom)
// 1 round trip, 10-100x faster
```

### Why Pre-Computed Rollups?

Dashboard query on raw data:

```sql
SELECT date_trunc('day', clicked_at), count(*)
FROM click_logs
WHERE link_id = '...' AND clicked_at > NOW() - INTERVAL '30 days'
GROUP BY 1;
-- Scans millions of rows, slow
```

Same query on rollups:

```sql
SELECT date, clicks FROM daily_stats
WHERE link_id = '...' AND date > NOW() - INTERVAL '30 days';
-- Scans ~30 rows, instant
```

### Redis Sliding Window Rate Limit

```
Key: rate_limit:ip:192.168.1.1 (sorted set)

1. ZREMRANGEBYSCORE key 0 (now - 60s)  -- remove old entries
2. ZCARD key                           -- count entries = current requests
3. If count < 100:
     ZADD key now now                   -- add current request
     EXPIRE key 60s
     ALLOW
   Else:
     REJECT
```

Atomic, no race conditions.

---

## Stretch Goals

1. **Custom slugs** — `POST /api/links { "slug": "my-sale" }`
2. **Link expiration** — auto-delete after date
3. **CSV export** — async job generates download
4. **Password-protected links**

---

## Testing Checklist

- [ ] Create link, redirect works
- [ ] Rate limit blocks after 100 req/min
- [ ] 1000 rapid clicks all tracked (no lost data)
- [ ] Dashboard shows correct totals
- [ ] Daily stats update after aggregator runs
- [ ] Works under k6 load test
