package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-analytics/internal/config"
	redisstream "go-analytics/internal/redis"
	"go-analytics/internal/scheduler"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

type testRouter struct {
	handler http.Handler
	pool    *pgxpool.Pool
	rdb     *redis.Client
}

func setupRouter(t *testing.T) *testRouter {
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

	// Scheduler (asynq client for worker handler)
	redisOpt := asynq.RedisClientOpt{Addr: "localhost:6379", DB: 1}
	sched := scheduler.NewClient(redisOpt)

	cfg := &config.Config{HealthCheckTimeout: 2 * time.Second}
	log := zerolog.Nop()

	router := NewRouter(log, cfg, pool, rdb, sched, 12345)

	t.Cleanup(func() {
		rdb.Del(ctx, redisstream.StreamKey)
		rdb.Close()
		sched.Close()
		pool.Close()
	})

	return &testRouter{handler: router, pool: pool, rdb: rdb}
}

func TestHealthEndpoint(t *testing.T) {
	tr := setupRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	tr.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)

	status, ok := resp["status"].(map[string]any)
	if !ok {
		t.Fatal("missing 'status' in response")
	}
	if status["database"] != "up" {
		t.Errorf("expected database up, got %v", status["database"])
	}
	if status["redis"] != "up" {
		t.Errorf("expected redis up, got %v", status["redis"])
	}

	// Verify stream metrics are included
	if _, ok := resp["stream"]; !ok {
		t.Error("missing 'stream' metrics in health response")
	}
}

func TestCreateLink(t *testing.T) {
	tr := setupRouter(t)

	body := `{"url":"https://httpbin.org/get"}`
	req := httptest.NewRequest(http.MethodPost, "/links/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	tr.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp SuccessResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if !resp.Success {
		t.Error("expected success=true")
	}

	data, ok := resp.Data.(map[string]any)
	if !ok {
		t.Fatal("missing data in response")
	}
	slug, _ := data["slug"].(string)
	if slug == "" {
		t.Error("expected non-empty slug")
	}
	if data["original_url"] != "https://httpbin.org/get" {
		t.Errorf("expected original_url=https://httpbin.org/get, got %v", data["original_url"])
	}

	// Verify Redis cache was set
	cached, err := tr.rdb.Get(context.Background(), "link:"+slug).Result()
	if err != nil {
		t.Errorf("expected link cached in redis: %v", err)
	}
	if cached != "https://httpbin.org/get" {
		t.Errorf("cached URL mismatch: %s", cached)
	}

	// Cleanup
	linkID := int64(data["id"].(float64))
	t.Cleanup(func() {
		ctx := context.Background()
		tr.rdb.Del(ctx, "link:"+slug)
		tr.pool.Exec(ctx, "DELETE FROM click_logs WHERE link_id = $1", linkID)
		tr.pool.Exec(ctx, "DELETE FROM links WHERE id = $1", linkID)
	})
}

func TestCreateLink_InvalidURL(t *testing.T) {
	tr := setupRouter(t)

	body := `{"url":"not-a-valid-url"}`
	req := httptest.NewRequest(http.MethodPost, "/links/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	tr.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid URL, got %d", rec.Code)
	}
}

func TestCreateLink_MissingURL(t *testing.T) {
	tr := setupRouter(t)

	body := `{}`
	req := httptest.NewRequest(http.MethodPost, "/links/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	tr.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing URL, got %d", rec.Code)
	}
}

func TestCreateLink_InvalidJSON(t *testing.T) {
	tr := setupRouter(t)

	req := httptest.NewRequest(http.MethodPost, "/links/", strings.NewReader("{broken"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	tr.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d", rec.Code)
	}
}

func TestGetLink_Redirect(t *testing.T) {
	tr := setupRouter(t)

	// Create a link first
	body := `{"url":"https://httpbin.org/anything"}`
	createReq := httptest.NewRequest(http.MethodPost, "/links/", strings.NewReader(body))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	tr.handler.ServeHTTP(createRec, createReq)

	var createResp SuccessResponse
	json.NewDecoder(createRec.Body).Decode(&createResp)
	data := createResp.Data.(map[string]any)
	slug := data["slug"].(string)
	linkID := int64(data["id"].(float64))

	t.Cleanup(func() {
		ctx := context.Background()
		tr.rdb.Del(ctx, "link:"+slug)
		tr.pool.Exec(ctx, "DELETE FROM click_logs WHERE link_id = $1", linkID)
		tr.pool.Exec(ctx, "DELETE FROM links WHERE id = $1", linkID)
	})

	// GET /{slug} should 302 redirect
	getReq := httptest.NewRequest(http.MethodGet, "/"+slug, nil)
	getRec := httptest.NewRecorder()
	tr.handler.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusFound {
		t.Fatalf("expected 302 redirect, got %d: %s", getRec.Code, getRec.Body.String())
	}
	location := getRec.Header().Get("Location")
	if location != "https://httpbin.org/anything" {
		t.Errorf("expected redirect to https://httpbin.org/anything, got %s", location)
	}

	// Wait for goroutine to write to stream
	time.Sleep(100 * time.Millisecond)

	// Verify click was added to stream
	streamLen, _ := tr.rdb.XLen(context.Background(), redisstream.StreamKey).Result()
	if streamLen < 1 {
		t.Error("expected at least 1 entry in clicks:stream after redirect")
	}
}

func TestGetLink_NotFound(t *testing.T) {
	tr := setupRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/nonexistent1", nil)
	rec := httptest.NewRecorder()
	tr.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestDeleteLink(t *testing.T) {
	tr := setupRouter(t)

	// Create a link
	body := `{"url":"https://httpbin.org/delete"}`
	createReq := httptest.NewRequest(http.MethodPost, "/links/", strings.NewReader(body))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	tr.handler.ServeHTTP(createRec, createReq)

	var createResp SuccessResponse
	json.NewDecoder(createRec.Body).Decode(&createResp)
	data := createResp.Data.(map[string]any)
	slug := data["slug"].(string)
	linkID := int64(data["id"].(float64))

	t.Cleanup(func() {
		ctx := context.Background()
		tr.pool.Exec(ctx, "DELETE FROM click_logs WHERE link_id = $1", linkID)
		tr.pool.Exec(ctx, "DELETE FROM links WHERE id = $1", linkID)
	})

	// DELETE /links/{slug}
	delReq := httptest.NewRequest(http.MethodDelete, "/links/"+slug, nil)
	delRec := httptest.NewRecorder()
	tr.handler.ServeHTTP(delRec, delReq)

	if delRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", delRec.Code, delRec.Body.String())
	}

	// Verify Redis cache was evicted
	_, err := tr.rdb.Get(context.Background(), "link:"+slug).Result()
	if err != redis.Nil {
		t.Error("expected cache to be evicted after delete")
	}

	// Verify redirect now returns 404
	getReq := httptest.NewRequest(http.MethodGet, "/"+slug, nil)
	getRec := httptest.NewRecorder()
	tr.handler.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusNotFound {
		t.Errorf("expected 404 after delete, got %d", getRec.Code)
	}
}

func TestDeleteLink_NotFound(t *testing.T) {
	tr := setupRouter(t)

	req := httptest.NewRequest(http.MethodDelete, "/links/doesntexist", nil)
	rec := httptest.NewRecorder()
	tr.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestGetLink_CacheAside(t *testing.T) {
	tr := setupRouter(t)

	// Create a link
	body := `{"url":"https://httpbin.org/cache-test"}`
	createReq := httptest.NewRequest(http.MethodPost, "/links/", strings.NewReader(body))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	tr.handler.ServeHTTP(createRec, createReq)

	var createResp SuccessResponse
	json.NewDecoder(createRec.Body).Decode(&createResp)
	data := createResp.Data.(map[string]any)
	slug := data["slug"].(string)
	linkID := int64(data["id"].(float64))

	t.Cleanup(func() {
		ctx := context.Background()
		tr.rdb.Del(ctx, "link:"+slug)
		tr.pool.Exec(ctx, "DELETE FROM click_logs WHERE link_id = $1", linkID)
		tr.pool.Exec(ctx, "DELETE FROM links WHERE id = $1", linkID)
	})

	// Evict cache manually
	tr.rdb.Del(context.Background(), "link:"+slug)

	// GET /{slug} should still work (falls back to DB and repopulates cache)
	getReq := httptest.NewRequest(http.MethodGet, "/"+slug, nil)
	getRec := httptest.NewRecorder()
	tr.handler.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusFound {
		t.Fatalf("expected 302 on cache miss, got %d", getRec.Code)
	}

	// Verify cache was repopulated
	cached, err := tr.rdb.Get(context.Background(), "link:"+slug).Result()
	if err != nil {
		t.Error("cache should be repopulated after miss")
	}
	if cached != "https://httpbin.org/cache-test" {
		t.Errorf("repopulated cache URL mismatch: %s", cached)
	}
}
