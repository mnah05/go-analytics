# URL Shortener with Analytics

A URL shortener that tracks every click, including geolocation, referrers, and real-time analytics.
Built with Go, PostgreSQL, Redis Streams, and Asynq.

---

## What This Is

**Short:** A [bit.ly](http://bit.ly) clone with a dashboard that shows click stats.

**Long:** When someone visits your short link (`mysite.com/abc123`), you immediately redirect them and also track:
their country, what site referred them, their device type, and when they clicked. Then you show clear charts.

---

## Architecture

```
┌────────────┐     ┌────────────┐     ┌───────────────┐
│   Client   │────▶│  API (Chi) │────▶│  PostgreSQL   │
│            │     │  :8080     │     │  (pgx pool)   │
└────────────┘     └─────┬──────┘     └───────────────┘
                         │                    ▲
                    XADD │ click              │ batch INSERT
                    to   │ event              │ (every 5s)
                    Redis│                    │
                         ▼                    │
                   ┌───────────┐      ┌──────┴────────┐
                   │  Redis    │◀────▶│ Worker (Asynq) │
                   │  Stream   │      │ + Scheduler    │
                   └───────────┘      └───────────────┘
```

**Two processes:**
- `make api` — HTTP server (Chi router, port 8080)
- `make worker` — Asynq worker server + cron scheduler

---

## Core Flows

### 1. Create Short Link

```
POST /links/
{ "url": "https://very-long-url.com/something" }
→ Returns: { "id": 123, "slug": "abc123", "original_url": "..." }
```

Slug is generated via [Sqids](https://sqids.org/) from the link's numeric ID. Cached in Redis (30-day TTL).

### 2. Visit Short Link (The Critical Path)

```
GET /abc123
→ Check Redis cache for original URL (faster than DB)
→ If miss: query PostgreSQL, populate cache
→ Fire-and-forget: XADD click event to Redis Stream
→ 302 Redirect to original URL
```

**Rate limited at HTTP layer:** 10 req/s per IP via `httprate`.

### 3. Background Processing (Async)

```
Asynq Scheduler triggers cron:process_clicks every 5s
→ XREADGROUP from Redis Stream (consumer group "click-workers")
→ Parse up to 1000 messages per batch
→ Batch INSERT into click_logs (single transaction)
→ XACK processed messages
```

```
Asynq Scheduler triggers cron:stats_aggregate (configurable cron)
→ Aggregate click_logs into daily_stats (clicks, unique visitors)
→ Build country and referrer JSONB breakdowns
→ Increment links.total_clicks
→ Idempotent via Redis key (stats:processed:{date})
```

### 4. View Analytics

```
GET /links/{slug}/stats?start=2026-01-01&end=2026-03-31
→ Query pre-computed daily_stats (fast reads)
→ Return: { "total_clicks": 5000, "daily": [...], "countries": {...}, "referers": {...} }
```

---

## Why This Architecture

| Problem                | Solution                                                   |
| ---------------------- | ---------------------------------------------------------- |
| Redirect must be fast  | Redis cache lookup + fire-and-forget to stream (not DB)    |
| Too many DB writes     | Buffer via Redis Stream, batch INSERT in 5s cron cycles    |
| Analytics queries slow | Pre-computed daily_stats rollups (scan ~30 rows, not millions) |
| Rate limiting          | In-memory `httprate` (10 req/s per IP) before touching DB  |
| Idempotency            | Redis key `stats:processed:{date}` prevents double-aggregation |

---

## API Endpoints

| Method | Path                    | Purpose                    |
| ------ | ----------------------- | -------------------------- |
| GET    | `/health`               | DB + Redis + stream health |
| POST   | `/links/`               | Create short link          |
| DELETE | `/links/{slug}`         | Soft-delete link           |
| GET    | `/links/{slug}/stats`   | Analytics for a link       |
| GET    | `/{slug}`               | 302 redirect               |
| GET    | `/worker/health`        | Worker health check        |
| GET    | `/worker/status`        | Worker queue details       |
| POST   | `/worker/ping`          | Enqueue test ping task     |

---

## Database Schema

```sql
-- Short links
CREATE TABLE links (
    id BIGSERIAL PRIMARY KEY,
    slug CHAR(11) UNIQUE DEFAULT '',          -- sqids-generated, 11 chars
    original_url TEXT NOT NULL,                -- where to redirect
    total_clicks BIGINT NOT NULL DEFAULT 0,    -- cached counter (bumped by aggregator)
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    is_deleted BOOLEAN NOT NULL DEFAULT FALSE  -- soft delete
);

-- Individual clicks (high-write table)
CREATE TABLE click_logs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    link_id BIGINT NOT NULL REFERENCES links(id),
    ip_address INET,
    referer TEXT,
    country VARCHAR(2),                        -- not currently populated (geolocation not implemented)
    user_agent TEXT,
    clicked_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_click_logs_link_clicked ON click_logs(link_id, clicked_at DESC);
CREATE INDEX idx_click_logs_clicked ON click_logs(clicked_at DESC);

-- Daily summaries (small table, fast reads)
CREATE TABLE daily_stats (
    link_id BIGINT NOT NULL REFERENCES links(id),
    date DATE NOT NULL,
    clicks BIGINT NOT NULL DEFAULT 0,
    unique_visitors BIGINT NOT NULL DEFAULT 0,
    countries JSONB NOT NULL DEFAULT '{}',     -- {"US": 500, "IN": 300}
    referers JSONB NOT NULL DEFAULT '{}',      -- {"google.com": 200, "twitter.com": 100}
    PRIMARY KEY (link_id, date)
);

CREATE INDEX idx_daily_stats_link_date ON daily_stats(link_id, date DESC);
```

---

## Redis Usage

| Pattern        | Key                    | Type       | Purpose                             |
| -------------- | ---------------------- | ---------- | ----------------------------------- |
| Link cache     | `link:{slug}`          | String     | 30-day TTL cache of original URL    |
| Click stream   | `clicks:stream`        | Stream     | Async click event buffer (max 100k) |
| Idempotency    | `stats:processed:{id}` | Key (7d)   | Prevents duplicate aggregation      |
| Stream metrics | `clicks:stream`        | Stream     | XLEN + XPENDING for health checks   |

---

## Worker Schedules (Asynq Scheduler)

| Task Type                 | Default Schedule | Purpose                                           |
| ------------------------- | ---------------- | ------------------------------------------------- |
| `worker:ping`             | `@every 30s`     | Heartbeat — verifies worker is alive              |
| `cron:process_clicks`     | `@every 5s`      | Drain Redis Stream → batch INSERT into click_logs |
| `cron:stats_aggregate`    | `@every 30s`     | Aggregate click_logs → daily_stats                |

Stats aggregate schedule is configurable via `STATS_AGGREGATE_CRON` env var. For production, use `0 1 * * *` (daily at 1 AM).

---

## Setup

### Prerequisites

- Go 1.25+
- PostgreSQL
- Redis
- [golang-migrate](https://github.com/golang-migrate/migrate) CLI
- [sqlc](https://sqlc.dev/) (only if modifying SQL queries)

### 1. Configure environment

```bash
cp .env.example .env
# Edit .env with your database and redis credentials
```

Required env vars:

| Variable              | Description                         |
| --------------------- | ----------------------------------- |
| `DATABASE_URL`        | PostgreSQL connection string        |
| `REDIS_ADDR`          | Redis address (e.g. `localhost:6379`) |
| `URL_SHORTENER_SALT`  | Numeric salt for Sqids slug generation |
| `APP_PORT`            | API port (default: 8080)            |
| `STATS_AGGREGATE_CRON`| Cron for stats aggregation          |

### 2. Run migrations

```bash
make migrate-up
```

### 3. Start services

```bash
# Terminal 1: API server
make api

# Terminal 2: Worker + scheduler
make worker
```

### 4. Start Redis (via Docker)

```bash
make dev
```

---

## Key Patterns

### Click Flow (Redis Stream)

Instead of writing to PostgreSQL on every redirect (expensive), clicks are buffered to a Redis Stream:

```go
// API side — fire and forget, doesn't block redirect
h.redis.XAdd(ctx, &redis.XAddArgs{
    Stream: "clicks:stream",
    MaxLen: 100000,
    Approx: true,
    Values: map[string]interface{}{"data": payloadJSON},
})
```

```go
// Worker side — batch drain every 5s
messages, _ := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
    Group: "click-workers",
    Consumer: consumerID,
    Streams: []string{"clicks:stream", ">"},
    Count: 1000,
})
// Insert all messages in a single transaction
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

---

## Project Structure

```
go-analytics/
├── cmd/
│   ├── api/main.go              # HTTP server entrypoint
│   └── worker/main.go           # Asynq worker + scheduler entrypoint
├── internal/
│   ├── config/config.go         # Env config with validation
│   ├── db/                      # sqlc-generated types and queries
│   ├── handler/                 # Chi HTTP handlers (link, stats, health, worker)
│   ├── helper/shortener.go      # Sqids slug generation
│   ├── middleware/logger.go     # Per-request zerolog in chi context
│   ├── queue/queue.go           # Asynq queue priorities
│   ├── redis/stream.go          # Redis Stream helpers (XADD, XREADGROUP, XACK)
│   ├── scheduler/client.go      # Asynq client wrapper
│   ├── tasks/tasks.go           # Task type constants + payloads
│   ├── validator/validator.go   # go-playground/validator wrapper
│   └── worker/
│       ├── process_clicks.go    # Stream → batch INSERT into click_logs
│       └── stats_aggregate.go   # click_logs → daily_stats rollup
├── migrations/                  # golang-migrate SQL files
├── sql/
│   ├── schema.sql               # Canonical DDL for sqlc
│   └── queries/                 # SQL query definitions for sqlc
├── pkg/logger/                  # Zerolog factory
├── docker-compose.yml           # Redis (PostgreSQL commented out)
├── Makefile                     # Build, migrate, sqlc, dev commands
├── sqlc.yaml                    # sqlc config
└── .env.example                 # Environment template
```

---

## What's Not Implemented

| Feature                  | Status |
| ------------------------ | ------ |
| Geolocation (IP → country) | Not implemented — `country` column is always NULL |
| `pgx.CopyFrom` batch insert | Not used — individual INSERT statements in a transaction |
| Redis sliding window rate limit | Not used — in-memory `httprate` instead |
| Dashboard / frontend     | Not built |
| Tests                    | None (were deleted in `8cf578a`) |
| Custom slugs             | Stretch goal |
| Link expiration          | Stretch goal |
| CSV export               | Stretch goal |
| Password-protected links | Stretch goal |
| k6 load tests            | Not created |

---

## Stretch Goals

1. **Custom slugs** — `POST /links/ { "slug": "my-sale" }`
2. **Link expiration** — auto-delete after date
3. **CSV export** — async job generates download
4. **Password-protected links**
5. **Geolocation** — MaxMind GeoLite2 or similar for IP-to-country
6. **Dashboard** — HTML/JS with charts (clicks over time, top countries, referrers)
7. **Redis-based rate limiting** — sliding window for multi-instance deployments
8. **Tests** — unit + integration tests

---

## Testing Checklist

- [ ] Create link, redirect works
- [ ] Rate limit blocks after 10 req/s per IP
- [ ] Clicks tracked via Redis Stream and batch-inserted to DB
- [ ] Stats endpoint returns correct totals from daily_stats
- [ ] Stats aggregator runs and populates daily_stats
- [ ] Worker health/status endpoints respond correctly
