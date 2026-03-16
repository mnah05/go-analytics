package scheduler

import (
	"context"

	"github.com/hibiken/asynq"
)

type Client struct {
	client    *asynq.Client
	inspector *asynq.Inspector
}

func NewClient(redisOpt asynq.RedisClientOpt) *Client {
	return &Client{
		client:    asynq.NewClient(redisOpt),
		inspector: asynq.NewInspector(redisOpt),
	}
}

func (c *Client) Close() error {
	if err := c.inspector.Close(); err != nil {
		return err
	}
	return c.client.Close()
}

func (c *Client) GetQueues() ([]string, error) {
	return c.inspector.Queues()
}

func (c *Client) GetQueueInfo(qname string) (*asynq.QueueInfo, error) {
	return c.inspector.GetQueueInfo(qname)
}

func (c *Client) GetServers() ([]*asynq.ServerInfo, error) {
	return c.inspector.Servers()
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
