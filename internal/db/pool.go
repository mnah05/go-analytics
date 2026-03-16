package db

import (
	"context"
	"fmt"
	"go-analytics/internal/config"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	pool *pgxpool.Pool
	mu   sync.RWMutex
)

func Open(ctx context.Context, cfg *config.Config) error {
	mu.Lock()
	defer mu.Unlock()

	if pool != nil {
		return fmt.Errorf("database pool already initialized")
	}

	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("failed to parse database config: %w", err)
	}
	poolConfig.MaxConns = 15
	poolConfig.MinConns = 2
	poolConfig.MaxConnLifetime = 30 * time.Minute
	poolConfig.MaxConnIdleTime = 5 * time.Minute
	poolConfig.HealthCheckPeriod = 1 * time.Minute

	newPool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return fmt.Errorf("failed to create pool: %w", err)
	}

	if err := newPool.Ping(ctx); err != nil {
		newPool.Close() // Clean up the pool on ping failure
		return fmt.Errorf("database unreachable: %w", err)
	}

	pool = newPool
	return nil
}

func Get() *pgxpool.Pool {
	mu.RLock()
	defer mu.RUnlock()
	return pool
}

func Close() {
	mu.Lock()
	defer mu.Unlock()

	if pool != nil {
		pool.Close()
		pool = nil
	}
}
