package scheduler

import (
	"context"

	"github.com/hibiken/asynq"
)

type Client struct {
	client *asynq.Client
}

func NewClient(redisOpt asynq.RedisClientOpt) *Client {
	return &Client{
		client: asynq.NewClient(redisOpt),
	}
}

func (c *Client) Close() error {
	return c.client.Close()
}

func (c *Client) Enqueue(ctx context.Context, taskType string, payload []byte, opts ...asynq.Option) error {
	task := asynq.NewTask(taskType, payload)
	_, err := c.client.EnqueueContext(ctx, task, opts...)
	return err
}

func (c *Client) EnqueueWithID(ctx context.Context, taskType string, payload []byte, opts ...asynq.Option) (string, error) {
	task := asynq.NewTask(taskType, payload)
	info, err := c.client.EnqueueContext(ctx, task, opts...)
	if err != nil {
		return "", err
	}
	return info.ID, nil
}
