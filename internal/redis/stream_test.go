package redisstream

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func setupTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   1, // use DB 1 for tests
	})
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("redis not available: %v", err)
	}
	// Clean test keys
	rdb.Del(ctx, StreamKey)
	t.Cleanup(func() {
		rdb.Del(ctx, StreamKey)
		rdb.Close()
	})
	return rdb
}

func TestEnsureStreamExists(t *testing.T) {
	rdb := setupTestRedis(t)
	ctx := context.Background()

	// First call should create the stream and group
	if err := EnsureStreamExists(ctx, rdb); err != nil {
		t.Fatalf("first call failed: %v", err)
	}

	// Second call should be idempotent (BUSYGROUP ignored)
	if err := EnsureStreamExists(ctx, rdb); err != nil {
		t.Fatalf("idempotent call failed: %v", err)
	}

	// Verify stream exists
	info, err := rdb.XInfoStream(ctx, StreamKey).Result()
	if err != nil {
		t.Fatalf("stream not created: %v", err)
	}
	if info.Groups != 1 {
		t.Errorf("expected 1 consumer group, got %d", info.Groups)
	}
}

func TestReadPendingClicks_EmptyStream(t *testing.T) {
	rdb := setupTestRedis(t)
	ctx := context.Background()
	EnsureStreamExists(ctx, rdb)

	msgs, err := ReadPendingClicks(ctx, rdb, "test-consumer", 100)
	if err != nil {
		t.Fatalf("unexpected error on empty stream: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
}

func TestReadPendingClicks_WithMessages(t *testing.T) {
	rdb := setupTestRedis(t)
	ctx := context.Background()
	EnsureStreamExists(ctx, rdb)

	// Add 5 messages to the stream
	for i := 0; i < 5; i++ {
		data, _ := json.Marshal(map[string]string{"slug": "test-slug", "ip": "127.0.0.1"})
		rdb.XAdd(ctx, &redis.XAddArgs{
			Stream: StreamKey,
			Values: map[string]interface{}{"data": string(data)},
		})
	}

	msgs, err := ReadPendingClicks(ctx, rdb, "test-consumer", 100)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if len(msgs) != 5 {
		t.Errorf("expected 5 messages, got %d", len(msgs))
	}

	// Verify message content
	for _, msg := range msgs {
		data, ok := msg.Values["data"].(string)
		if !ok {
			t.Error("message missing 'data' field")
			continue
		}
		var payload map[string]string
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			t.Errorf("invalid JSON in message: %v", err)
		}
	}
}

func TestReadPendingClicks_RespectsCount(t *testing.T) {
	rdb := setupTestRedis(t)
	ctx := context.Background()
	EnsureStreamExists(ctx, rdb)

	// Add 10 messages
	for i := 0; i < 10; i++ {
		rdb.XAdd(ctx, &redis.XAddArgs{
			Stream: StreamKey,
			Values: map[string]interface{}{"data": "test"},
		})
	}

	// Read only 3
	msgs, err := ReadPendingClicks(ctx, rdb, "test-consumer", 3)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if len(msgs) != 3 {
		t.Errorf("expected 3 messages (count limit), got %d", len(msgs))
	}
}

func TestReadPendingClicks_NonBlocking(t *testing.T) {
	rdb := setupTestRedis(t)
	ctx := context.Background()
	EnsureStreamExists(ctx, rdb)

	// This must return immediately, not block
	start := time.Now()
	msgs, err := ReadPendingClicks(ctx, rdb, "test-consumer", 100)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("ReadPendingClicks blocked for %v (should be <500ms)", elapsed)
	}
}

func TestAckClicks(t *testing.T) {
	rdb := setupTestRedis(t)
	ctx := context.Background()
	EnsureStreamExists(ctx, rdb)

	// Add and read messages to create pending entries
	for i := 0; i < 3; i++ {
		rdb.XAdd(ctx, &redis.XAddArgs{
			Stream: StreamKey,
			Values: map[string]interface{}{"data": "test"},
		})
	}
	msgs, _ := ReadPendingClicks(ctx, rdb, "test-consumer", 100)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}

	// Verify pending count before ack
	pending, _ := rdb.XPending(ctx, StreamKey, ConsumerGroup).Result()
	if pending.Count != 3 {
		t.Errorf("expected 3 pending, got %d", pending.Count)
	}

	// Ack all messages
	ids := make([]string, len(msgs))
	for i, msg := range msgs {
		ids[i] = msg.ID
	}
	if err := AckClicks(ctx, rdb, ids...); err != nil {
		t.Fatalf("ack failed: %v", err)
	}

	// Verify pending count is 0
	pending, _ = rdb.XPending(ctx, StreamKey, ConsumerGroup).Result()
	if pending.Count != 0 {
		t.Errorf("expected 0 pending after ack, got %d", pending.Count)
	}
}

func TestAckClicks_EmptyIDs(t *testing.T) {
	rdb := setupTestRedis(t)
	_ = EnsureStreamExists(context.Background(), rdb)

	// Should be a no-op, not an error
	if err := AckClicks(context.Background(), rdb); err != nil {
		t.Errorf("ack with no IDs should not error: %v", err)
	}
}

func TestGetStreamMetrics(t *testing.T) {
	rdb := setupTestRedis(t)
	ctx := context.Background()
	EnsureStreamExists(ctx, rdb)

	// Empty stream
	metrics, err := GetStreamMetrics(ctx, rdb)
	if err != nil {
		t.Fatalf("metrics failed: %v", err)
	}
	if metrics.StreamLength != 0 {
		t.Errorf("expected stream length 0, got %d", metrics.StreamLength)
	}
	if metrics.PendingCount != 0 {
		t.Errorf("expected pending 0, got %d", metrics.PendingCount)
	}

	// Add messages and read them (creates pending)
	for i := 0; i < 5; i++ {
		rdb.XAdd(ctx, &redis.XAddArgs{
			Stream: StreamKey,
			Values: map[string]interface{}{"data": "test"},
		})
	}
	ReadPendingClicks(ctx, rdb, "metrics-consumer", 3)

	metrics, err = GetStreamMetrics(ctx, rdb)
	if err != nil {
		t.Fatalf("metrics failed: %v", err)
	}
	if metrics.StreamLength != 5 {
		t.Errorf("expected stream length 5, got %d", metrics.StreamLength)
	}
	if metrics.PendingCount != 3 {
		t.Errorf("expected 3 pending, got %d", metrics.PendingCount)
	}
}
