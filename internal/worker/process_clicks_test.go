package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"go-analytics/internal/db"
	redisstream "go-analytics/internal/redis"
	"go-analytics/internal/tasks"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

type testEnv struct {
	rdb     *redis.Client
	pool    *pgxpool.Pool
	queries *db.Queries
}

func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()
	godotenv.Load("../../.env")

	ctx := context.Background()

	// Redis
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379", DB: 1})
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("redis not available: %v", err)
	}
	rdb.Del(ctx, redisstream.StreamKey)
	redisstream.EnsureStreamExists(ctx, rdb)

	// Postgres
	dbURL := "postgresql://postgres.ppwltwpzarkfhpyzvrqh:W9HO5ytoaiiycsIM@aws-1-ap-southeast-1.pooler.supabase.com:5432/postgres"
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Skipf("database not available: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("database not reachable: %v", err)
	}

	queries := db.New(pool)

	t.Cleanup(func() {
		rdb.Del(ctx, redisstream.StreamKey)
		rdb.Close()
		pool.Close()
	})

	return &testEnv{rdb: rdb, pool: pool, queries: queries}
}

// createTestLink creates a link in DB and returns its slug and ID.
func createTestLink(t *testing.T, env *testEnv, url string) (string, int64) {
	t.Helper()
	ctx := context.Background()

	tx, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx)

	qtx := env.queries.WithTx(tx)
	link, err := qtx.CreateLink(ctx, url)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	slug := fmt.Sprintf("t%010d", time.Now().UnixNano()%10000000000) // exactly 11 chars for CHAR(11)
	link, err = qtx.UpdateLinkSlug(ctx, db.UpdateLinkSlugParams{
		Slug: pgtype.Text{String: slug, Valid: true},
		ID:   link.ID,
	})
	if err != nil {
		t.Fatalf("update slug: %v", err)
	}
	tx.Commit(ctx)

	t.Cleanup(func() {
		env.pool.Exec(context.Background(),
			"DELETE FROM click_logs WHERE link_id = $1", link.ID)
		env.pool.Exec(context.Background(),
			"DELETE FROM links WHERE id = $1", link.ID)
	})

	return link.Slug.String, link.ID
}

func addClickToStream(t *testing.T, rdb *redis.Client, slug string) {
	t.Helper()
	payload := tasks.ClickTrackPayload{
		Slug:      slug,
		IpAddress: "127.0.0.1:8080",
		UserAgent: "TestBot/1.0",
		Referer:   "https://test.com",
		RequestID: "test-req-1",
		ClickedAt: time.Now().UTC(),
	}
	data, _ := json.Marshal(payload)
	err := rdb.XAdd(context.Background(), &redis.XAddArgs{
		Stream: redisstream.StreamKey,
		Values: map[string]interface{}{"data": string(data)},
	}).Err()
	if err != nil {
		t.Fatalf("xadd failed: %v", err)
	}
}

func TestProcessPendingClicks_EmptyStream(t *testing.T) {
	env := setupTestEnv(t)
	logger := zerolog.Nop()
	cp := NewClickProcessor(env.rdb, env.pool, env.queries, logger)

	err := cp.ProcessPendingClicks(context.Background())
	if err != nil {
		t.Errorf("empty stream should not error: %v", err)
	}
}

func TestProcessPendingClicks_ProcessesBatch(t *testing.T) {
	env := setupTestEnv(t)
	logger := zerolog.Nop()
	cp := NewClickProcessor(env.rdb, env.pool, env.queries, logger)

	slug, linkID := createTestLink(t, env, "https://test-batch.com")

	// Add 5 clicks to the stream
	for i := 0; i < 5; i++ {
		addClickToStream(t, env.rdb, slug)
	}

	// Process
	err := cp.ProcessPendingClicks(context.Background())
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}

	// Verify click_logs in DB
	count, err := env.queries.CountClickLogsByLink(context.Background(), linkID)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 5 {
		t.Errorf("expected 5 click_logs, got %d", count)
	}

	// Verify stream entries are acknowledged (pending = 0)
	pending, _ := env.rdb.XPending(context.Background(), redisstream.StreamKey, redisstream.ConsumerGroup).Result()
	if pending.Count != 0 {
		t.Errorf("expected 0 pending after processing, got %d", pending.Count)
	}
}

func TestProcessPendingClicks_SkipsDeletedLink(t *testing.T) {
	env := setupTestEnv(t)
	logger := zerolog.Nop()
	cp := NewClickProcessor(env.rdb, env.pool, env.queries, logger)

	slug, _ := createTestLink(t, env, "https://to-delete.com")

	// Soft delete the link
	env.queries.SoftDeleteLink(context.Background(), pgtype.Text{String: slug, Valid: true})

	// Add a click for the deleted slug
	addClickToStream(t, env.rdb, slug)

	// Process — should not fail, just skip the click
	err := cp.ProcessPendingClicks(context.Background())
	if err != nil {
		t.Errorf("processing deleted link click should not error: %v", err)
	}

	// Stream entries should still be acknowledged
	pending, _ := env.rdb.XPending(context.Background(), redisstream.StreamKey, redisstream.ConsumerGroup).Result()
	if pending.Count != 0 {
		t.Errorf("expected 0 pending (acked even on skip), got %d", pending.Count)
	}
}

func TestProcessPendingClicks_SkipsMalformedPayload(t *testing.T) {
	env := setupTestEnv(t)
	logger := zerolog.Nop()
	cp := NewClickProcessor(env.rdb, env.pool, env.queries, logger)

	// Add a malformed entry
	env.rdb.XAdd(context.Background(), &redis.XAddArgs{
		Stream: redisstream.StreamKey,
		Values: map[string]interface{}{"data": "not-valid-json{{{"},
	})

	err := cp.ProcessPendingClicks(context.Background())
	if err != nil {
		t.Errorf("malformed payload should be skipped, not error: %v", err)
	}
}

func TestProcessPendingClicks_ParsesIPCorrectly(t *testing.T) {
	env := setupTestEnv(t)
	logger := zerolog.Nop()
	cp := NewClickProcessor(env.rdb, env.pool, env.queries, logger)

	slug, linkID := createTestLink(t, env, "https://test-ip.com")

	// Add click with host:port format IP
	payload := tasks.ClickTrackPayload{
		Slug:      slug,
		IpAddress: "192.168.1.1:54321",
		UserAgent: "IPTest/1.0",
		ClickedAt: time.Now().UTC(),
	}
	data, _ := json.Marshal(payload)
	env.rdb.XAdd(context.Background(), &redis.XAddArgs{
		Stream: redisstream.StreamKey,
		Values: map[string]interface{}{"data": string(data)},
	})

	err := cp.ProcessPendingClicks(context.Background())
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}

	// Verify click was inserted
	count, err := env.queries.CountClickLogsByLink(context.Background(), linkID)
	if err != nil {
		t.Fatalf("count failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 click_log, got %d", count)
	}
}
