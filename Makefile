# ---------- run ----------
api:
	go run ./cmd/api

worker:
	go run ./cmd/worker

# ---------- stop ----------
stop-api:
	pkill -f "go run ./cmd/api" || echo "API not running"

stop-worker:
	pkill -f "go run ./cmd/worker" || echo "Worker not running"

stop-migrate:
	pkill -f "migrate" || echo "No migrations running"

stop: stop-api stop-worker stop-migrate
	@echo "All services stopped"

# ---------- test ----------
test:
	go test -race ./...

# ---------- database ----------
migrate-up:
	@if [ -f .env ]; then export $$(grep -v '^#' .env | xargs); fi; \
	migrate -path ./migrations -database $$DATABASE_URL up

migrate-down:
	@if [ -f .env ]; then export $$(grep -v '^#' .env | xargs); fi; \
	migrate -path ./migrations -database $$DATABASE_URL down 1

migrate-create:
	migrate create -ext sql -dir migrations -seq $(name)

migrate-force:
	@if [ -f .env ]; then export $$(grep -v '^#' .env | xargs); fi; \
	migrate -path ./migrations -database $$DATABASE_URL force $(version)

migrate-version:
	@if [ -f .env ]; then export $$(grep -v '^#' .env | xargs); fi; \
	migrate -path ./migrations -database $$DATABASE_URL version

# ---------- sqlc ----------
sqlc:
	sqlc generate

# ---------- dev ----------
dev:
	docker compose up -d

dev-down:
	docker compose down

dev-stop: dev-down
	@echo "Docker services stopped"

# ---------- build ----------
build-api:
	go build -o bin/api ./cmd/api

build-worker:
	go build -o bin/worker ./cmd/worker

build: build-api build-worker
	@echo "Build complete"

# ---------- clean ----------
clean: stop dev-down
	@rm -rf bin/
	@echo "Cleaned up"

.PHONY: api worker stop-api stop-worker stop-migrate stop test migrate-up migrate-down migrate-create migrate-force migrate-version sqlc dev dev-down dev-stop build-api build-worker build clean
