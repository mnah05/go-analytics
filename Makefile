# ---------- run ----------
api:
	go run ./cmd/api

worker:
	go run ./cmd/worker

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

# ---------- sqlc ----------
sqlc:
	sqlc generate

# ---------- dev ----------
dev:
	docker compose up -d

dev-down:
	docker compose down
