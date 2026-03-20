# Link Creation & Management — TODO

## Phase 1: Schema & SQL Queries (sqlc)

- [x] Add `links`, `click_logs`, `daily_stats`, `processed_events` tables to `sql/schema.sql` (mirror `migrations/000001_initial_tables.up.sql`)
- [x] Create `sql/queries/links.sql` with the following queries:
  - [x] `CreateLink` — insert a new link (slug, original_url), return the full row
  - [x] `GetLinkBySlug` — fetch a single link by slug (exclude soft-deleted)
  - [x] `GetLinkByID` — fetch a single link by id (exclude soft-deleted)
  - [x] `SoftDeleteLink` — set `is_deleted = TRUE` by id
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
- [x] Insert row into `links` table with a placeholder slug (e.g., empty or temp value)
- [x] Generate slug from the newly returned `id` using shortener
- [x] Update the row's slug with `UpdateLinkSlug`
- [x] Cache the mapping `slug -> original_url` in Redis
- [x] Return `201` with `{ slug, original_url, created_at }`

### Get Link — `GET /links/{slug}`

- [x] Extract `slug` from URL path
- [x] Check Redis cache first (`link:<slug>`)
- [x] On cache miss, query DB via `GetLinkBySlug`, then populate cache
- [x] Return `200` with link data or `404` if not found / soft-deleted

### Delete Link — `DELETE /links/{id}`

- [ ] Soft-delete via `SoftDeleteLink` (set `is_deleted = TRUE`)
- [ ] Evict slug from Redis cache
- [ ] Return `204 No Content`

## Phase 4: Redis Caching Strategy

- [ ] Define cache key format: `link:{slug}` → stores original_url
- [ ] Set TTL on cached entries (e.g., 24h or configurable)
- [ ] Cache-aside pattern: read from cache first, fallback to DB, then populate cache
- [ ] On create: write-through (cache immediately after DB insert)
- [ ] On delete: evict the key
- [ ] Handle cache miss gracefully (DB is source of truth, never fail on Redis errors)

## Phase 5: Route Registration

  ```
- [ ] Add a top-level redirect route: `GET /{slug}` → resolve and redirect (302) to original URL
  - [ ] Check cache, fallback to DB
  - [ ] Fire-and-forget: enqueue click tracking task via asynq (don't block the redirect)

## Phase 6: Redirect & Click Tracking (Stretch)

- [ ] `GET /{slug}` handler: resolve slug → 302 redirect to `original_url`
- [ ] On redirect, enqueue an asynq task to log the click asynchronously (link_id, ip, user_agent, referer)
  - [ ] This ties into the existing `click_logs` table and worker infrastructure
