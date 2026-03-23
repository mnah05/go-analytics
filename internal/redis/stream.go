package redisstream

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

const (
	StreamKey     = "clicks:stream"
	ConsumerGroup = "click-workers"
)

func EnsureStreamExists(ctx context.Context, rdb *redis.Client) error {
	err := rdb.XGroupCreateMkStream(ctx, StreamKey, ConsumerGroup, "0").Err()
	if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		return fmt.Errorf("failed to create consumer group: %w", err)
	}
	return nil
}

func ReadPendingClicks(ctx context.Context, rdb *redis.Client, consumerID string, count int64) ([]redis.XMessage, error) {
	result, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    ConsumerGroup,
		Consumer: consumerID,
		Streams:  []string{StreamKey, ">"},
		Count:    count,
		Block:    -1, // Negative value omits BLOCK arg (non-blocking); 0 means block forever
	}).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result[0].Messages, nil
}

func AckClicks(ctx context.Context, rdb *redis.Client, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	return rdb.XAck(ctx, StreamKey, ConsumerGroup, ids...).Err()
}

func GetStreamMetrics(ctx context.Context, rdb *redis.Client) (*StreamMetrics, error) {
	length, err := rdb.XLen(ctx, StreamKey).Result()
	if err != nil && err != redis.Nil {
		return nil, fmt.Errorf("xlen: %w", err)
	}

	pending, err := rdb.XPending(ctx, StreamKey, ConsumerGroup).Result()
	if err != nil && err != redis.Nil {
		return nil, fmt.Errorf("xpending: %w", err)
	}

	consumerNames := make([]string, 0)
	if pending != nil {
		for name := range pending.Consumers {
			consumerNames = append(consumerNames, name)
		}
	}

	return &StreamMetrics{
		StreamLength:  length,
		PendingCount:  pending.Count,
		ConsumerNames: consumerNames,
	}, nil
}

type StreamMetrics struct {
	StreamLength  int64
	PendingCount  int64
	ConsumerNames []string
}
