# GEMINI.md - Project Context: go-analytics

## Project Overview
**go-analytics** is a high-performance URL shortener with real-time analytics. It is designed to handle high redirect volumes by offloading click tracking to background workers using a "buffer-and-batch" strategy.

### Core Technologies
- **Language:** Go 1.24
- **Web Framework:** [Chi](https://github.com/go-chi/chi) (v5)
- **Database:** PostgreSQL (using [pgx](https://github.com/jackc/pgx) and [sqlc](https://sqlc.dev/))
- **Background Processing:** [Asynq](https://github.com/hibiken/asynq) (Redis-backed)
- **Caching/Rate Limiting:** Redis
- **Logging:** [zerolog](https://github.com/rs/zerolog)

### Architecture
- `cmd/api`: The web server handling link creation, redirection, and analytics API.
- `cmd/worker`: Background worker processing click-log tasks and periodic rollups.
- `internal/`: Core business logic, handlers, and database operations.
- `migrations/`: SQL migration files for database schema versioning.
- `sql/`: Contains `schema.sql` and `queries/` used by `sqlc` for Go code generation.

---

## Building and Running

### Prerequisites
- Docker & Docker Compose
- Go 1.24+
- `sqlc` (for code generation)
- `golang-migrate` (for migrations)

### Key Commands
- **Infrastructure:** `make dev` (starts Postgres and Redis via Docker)
- **Migrations:** `make migrate-up` (applies database changes)
- **Code Generation:** `make sqlc` (generates Go DB code from SQL)
- **Run API:** `make api` (runs `./cmd/api`)
- **Run Worker:** `make worker` (runs `./cmd/worker`)
- **Testing:** `make test` (runs race-detector tests)

---

## Development Conventions

### Logging & Error Handling
- Use structured logging via `pkg/logger`.
- In `main` functions, prefer `logg.Error()` and `return` over `logg.Fatal()` to ensure `defer` cleanups (DB pools, Redis clients) are executed.
- Handlers should use `logger.FromChiContext(r.Context())` to retrieve the logger with request-scoped context.

### Database Operations
- **Surgical Updates:** Use `sqlc` for most DB operations.
- **Batching:** Use PostgreSQL `COPY` or batch inserts for high-volume click logging (planned).
- **Schema:** Ensure `sql/schema.sql` is kept in sync with files in `migrations/`.

### Configuration
- Configuration is managed via environment variables and loaded in `internal/config/config.go`.
- Required variables include `DATABASE_URL` and `REDIS_ADDR`.
- `URL_SHORTENER_SALT` is used for slug generation (Sqids).

---

## Current Status & Known Issues
- **Schema Mismatch:** (CRITICAL) `sql/schema.sql` currently contains placeholder `users` and `jobs` tables, while `migrations/` contains the actual `links` and `click_logs` tables. Syncing these is a priority.
- **Worker Monitoring:** The `/worker/health` endpoint verifies the existence of active Asynq server processes in Redis.
- **Pending Features:** Link creation (`POST /links`), slug generation logic, and the batch click-drainer worker are defined in `todo.md` but not yet fully implemented.
