# Link Creation & Management — TODO

## Phase 1: Schema & SQL Queries (sqlc)

- [x] Add `links`, `click_logs`, `daily_stats`, `processed_events` tables to `sql/schema.sql` (mirror `migrations/000001_initial_tables.up.sql`)
- [x] Create `sql/queries/links.sql` with the following queries:
  - [x] `CreateLink` — insert a new link (slug, original_url), return the full row
  - [x] `GetLinkBySlug` — fetch a single link by slug (exclude soft-deleted)
  - [x] `GetLinkByID` — fetch a single link by id (exclude soft-deleted)
  - [x] `SoftDeleteLink` — set `is_deleted = TRUE` by slug
  - [x] `IncrementTotalClicks` — bump `total_clicks` by 1 for a given link id
- [x] Run `make sqlc` to generate Go code in `internal/db/`

## Phase 2: Slug Generation

- [x] Add sqids (or hashids) dependency for slug generation
- [x] Create `internal/helper/shortener.helper.go`:
  - [x] `NewShortener(salt uint64)` — initialize sqids encoder with the salt from env (`URL_SHORTENER_SALT`)
  - [x] `GenerateSlug(linkID int64) string` — encode the DB-assigned `id` into an 11-char slug
- [x] Wire shortener into the router / handler (salt is already parsed in `cmd/api/main.go`)

## Phase 3: Link Handler & Routes

- [x] Create `internal/handler/link.go` with a `LinkHandler` struct holding db pool, redis client, shortener, and logger
- [x] Implement the following handler methods:

### Create Link — `POST /links`

- [x] Accept JSON body: `{ "url": "https://example.com" }`
- [x] Validate the URL using `internal/validator` (required, valid URL format)
- [x] Insert + update slug in a single DB transaction (atomic)
- [x] Generate slug from the newly returned `id` using shortener
- [x] Update the row's slug with `UpdateLinkSlug`
- [x] Cache the mapping `slug -> original_url` in Redis (30-day TTL)
- [x] Return `201` with `{ slug, original_url, created_at }`

### Get Link / Redirect — `GET /{slug}`

- [x] Extract `slug` from URL path
- [x] Check Redis cache first (`link:<slug>`)
- [x] On cache miss, query DB via `GetLinkBySlug`, then populate cache
- [x] 302 redirect to original URL, or `404` if not found / soft-deleted

### Delete Link — `DELETE /links/{slug}`

- [x] Evict slug from Redis cache first (cache-safe ordering)
- [x] Soft-delete via `SoftDeleteLink` (set `is_deleted = TRUE`)
- [x] Return `200` with success message

## Phase 4: Redis Caching Strategy

- [x] Define cache key format: `link:{slug}` → stores original_url
- [x] Set TTL on cached entries (30 days)
- [x] Cache-aside pattern: read from cache first, fallback to DB, then populate cache
- [x] On create: write-through (cache immediately after DB commit)
- [x] On delete: evict before DB soft-delete (safe ordering — cache miss self-heals)
- [x] Handle cache miss gracefully (DB is source of truth, never fail on Redis errors)

## Phase 5: Route Registration

- [x] `POST /links/` → CreateLink
- [x] `DELETE /links/{slug}` → DeleteLink
- [x] `GET /{slug}` → GetLink (top-level redirect route, 302 to original URL)

## Phase 6: Click Tracking

- [x] On redirect (`GET /{slug}`), enqueue an asynq task to log the click asynchronously
  - [x] `tasks.TypeClickTrack` constant and `ClickTrackPayload` struct (slug, ip, user_agent, referer, request_id, clicked_at)
  - [x] Enqueue task in `GetLink` handler via goroutine (fire-and-forget, doesn't block redirect)
- [x] Implement click tracking task handler in worker (`cmd/worker/main.go`)
  - [x] Resolve slug → link_id via `GetLinkBySlug`
  - [x] Parse IP address from `RemoteAddr` (strips port)
  - [x] Insert row into `click_logs` table (link_id, ip, user_agent, referer, clicked_at)
  - [x] Log success/failure with request_id for traceability
- [x] Wire task handler into worker's asynq mux (`cmd/worker/main.go`)
- [x] Add sqlc queries: `InsertClickLog`, `GetClickLogsByLink`, `CountClickLogsByLink`

## Phase 7: Analytics Aggregation

### Step 1 — Task definition

- [ ] Add `TypeStatsAggregate` constant in `internal/tasks/tasks.go`
- [ ] Add `StatsAggregatePayload` struct: `StartAt time.Time`, `EndAt time.Time`, `RequestID string`

### Step 2 — Aggregation worker handler

- [ ] Register `TypeStatsAggregate` handler in `cmd/worker/main.go`
- [ ] Idempotency: check `IsEventProcessed` → skip if already done
- [ ] Call `UpsertDailyClicks(startAt, endAt)` — upserts clicks + unique visitors
- [ ] Call `GetCountryStats(startAt, endAt)` → build `{"US": 5, "IN": 3}` JSONB in Go → `UpdateDailyCountries` per (link_id, date)
- [ ] Call `GetRefererStats(startAt, endAt)` → build `{"twitter.com": 10}` JSONB in Go → `UpdateDailyReferers` per (link_id, date)
- [ ] Call `IncrementTotalClicksSince(startAt)` — bumps `links.total_clicks`
- [ ] Mark task as processed via `MarkEventProcessed` in a transaction
- [ ] Log success/failure with request_id

### Step 3 — Schedule the aggregation

- [ ] Use asynq periodic task scheduler in `cmd/worker/main.go`
- [ ] Schedule: daily at midnight UTC (or configurable via env `STATS_AGGREGATE_CRON`)
- [ ] Payload: `StartAt` = yesterday 00:00 UTC, `EndAt` = today 00:00 UTC

### Step 4 — Stats endpoint: `GET /links/{slug}/stats`

- [ ] Create `internal/handler/stats.go` with `StatsHandler` struct (db pool, redis)
- [ ] Implement `GetStats` handler:
  - Resolve slug → link_id via `GetLinkBySlug`
  - Call `GetLinkStatsSummary` for totals (total_clicks, total_unique_visitors)
  - Call `GetDailyStatsByLink` for daily breakdown (query params: `start`, `end`, default last 30 days)
  - Return `200` with `{ slug, total_clicks, total_unique_visitors, daily: [...] }`
  - Return `404` if link not found
- [ ] Register route: `GET /links/{slug}/stats` in `router.go` under `/links` group
