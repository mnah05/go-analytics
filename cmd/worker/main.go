package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go-analytics/internal/config"
	"go-analytics/internal/db"
	"go-analytics/internal/queue"
	redisstream "go-analytics/internal/redis"
	"go-analytics/internal/tasks"
	"go-analytics/internal/worker"
	"go-analytics/pkg/logger"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

func main() {
	cfg := config.Load(logger.New())

	logg, logCleanup := logger.NewLogger(cfg, "logs/worker.log")
	if logCleanup != nil {
		defer func() {
			if err := logCleanup(); err != nil {
				logg.Error().Err(err).Msg("failed to cleanup logger")
			}
		}()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.Open(ctx, cfg); err != nil {
		logg.Error().Err(err).Msg("failed to initialize database")
		return
	}
	logg.Info().Msg("database connected")
	defer db.Close()

	pool := db.Get()
	if pool == nil {
		logg.Error().Msg("database pool is nil")
		return
	}

	redisOpt := asynq.RedisClientOpt{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})

	if err := redisstream.EnsureStreamExists(ctx, rdb); err != nil {
		logg.Error().Err(err).Msg("failed to ensure stream exists")
		return
	}
	logg.Info().Msg("redis stream initialized")

	srv := asynq.NewServer(
		redisOpt,
		asynq.Config{
			Concurrency: cfg.WorkerConcurrency,
			Queues:      queue.Priorities(),

			RetryDelayFunc: func(n int, e error, t *asynq.Task) time.Duration {
				return time.Duration(1<<uint(n)) * time.Second
			},

			ErrorHandler: asynq.ErrorHandlerFunc(func(ctx context.Context, task *asynq.Task, err error) {
				taskID := "unknown"
				if rw := task.ResultWriter(); rw != nil {
					taskID = rw.TaskID()
				}
				logg.Error().
					Err(err).
					Str("task_type", task.Type()).
					Str("task_id", taskID).
					Msg("task processing failed")
			}),
		},
	)

	mux := asynq.NewServeMux()

	mux.Use(loggingMiddleware(logg))

	mux.HandleFunc(tasks.TypeWorkerPing, func(ctx context.Context, t *asynq.Task) error {
		var payload tasks.PingTaskPayload
		logEvent := logg.Info()

		if err := json.Unmarshal(t.Payload(), &payload); err != nil {
			logEvent.Str("payload_raw", string(t.Payload()))
		} else {
			logEvent.Str("payload", payload.Message)
			if payload.RequestID != "" {
				logEvent.Str("request_id", payload.RequestID)
			}
		}

		logEvent.
			Str("task_type", t.Type()).
			Msg("worker ping task processed - worker is alive!")
		return nil
	})

	queries := db.New(pool)
	clickProcessor := worker.NewClickProcessor(rdb, pool, queries, logg)

	mux.HandleFunc(tasks.TypeProcessPendingClicks, func(ctx context.Context, t *asynq.Task) error {
		return clickProcessor.ProcessPendingClicks(ctx)
	})

	statsAggregator := worker.NewStatsAggregator(rdb, pool, queries, logg)

	mux.HandleFunc(tasks.TypeStatsAggregate, func(ctx context.Context, t *asynq.Task) error {
		var payload tasks.StatsAggregatePayload
		if len(t.Payload()) > 0 {
			if err := json.Unmarshal(t.Payload(), &payload); err != nil {
				return fmt.Errorf("unmarshal stats aggregate payload: %w", err)
			}
		}
		// If triggered by scheduler (nil payload), compute the window at runtime.
		// Window covers today 00:00 UTC → tomorrow 00:00 UTC so in-progress
		// clicks are captured. Idempotency key is per-date, so the heavy work
		// (UpsertDailyClicks, IncrementTotalClicksSince) only runs once per day.
		if payload.StartAt.IsZero() {
			now := time.Now().UTC()
			today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
			payload.StartAt = today
			payload.EndAt = today.AddDate(0, 0, 1)
			payload.RequestID = fmt.Sprintf("stats-aggregate-%s", today.Format("2006-01-02"))
		}
		return statsAggregator.Aggregate(ctx, payload)
	})

	scheduler := asynq.NewScheduler(
		redisOpt,
		&asynq.SchedulerOpts{},
	)

	pingTask := asynq.NewTask(tasks.TypeWorkerPing, []byte(`{"message":"scheduled ping"}`))
	if _, err := scheduler.Register("@every 30s", pingTask); err != nil {
		logg.Error().Err(err).Msg("failed to register ping task")
	}

	processTask := asynq.NewTask(tasks.TypeProcessPendingClicks, nil)
	if _, err := scheduler.Register("@every 5s", processTask, asynq.Queue(queue.QueueDefault), asynq.Unique(10*time.Second)); err != nil {
		logg.Error().Err(err).Msg("failed to register process clicks task")
	}

	statsTask := asynq.NewTask(tasks.TypeStatsAggregate, nil)
	if _, err := scheduler.Register(cfg.StatsAggregateCron, statsTask, asynq.Queue(queue.QueueDefault)); err != nil {
		logg.Error().Err(err).Msg("failed to register stats aggregate task")
	}

	workerErrors := make(chan error, 1)
	schedulerErrors := make(chan error, 1)

	go func() {
		logg.Info().Msg("worker starting")
		if err := srv.Run(mux); err != nil {
			workerErrors <- fmt.Errorf("worker failed to start: %w", err)
		}
	}()

	go func() {
		logg.Info().Msg("scheduler starting")
		if err := scheduler.Run(); err != nil {
			schedulerErrors <- fmt.Errorf("scheduler failed to start: %w", err)
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-workerErrors:
		logg.Fatal().Err(err).Msg("worker startup failed")
	case err := <-schedulerErrors:
		logg.Fatal().Err(err).Msg("scheduler startup failed")
	case sig := <-sigChan:
		logg.Info().Str("signal", sig.String()).Msg("shutdown signal received")
	}

	logg.Info().Msg("shutting down worker...")

	srv.Stop()
	scheduler.Shutdown()
	logg.Info().Msg("worker stopped accepting new tasks")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.WorkerShutdownTimeout)
	defer cancel()

	done := make(chan struct{})
	go func() {
		srv.Shutdown()
		close(done)
	}()

	select {
	case <-done:
		logg.Info().Msg("shutdown completed gracefully")
	case <-shutdownCtx.Done():
		logg.Warn().Msg("shutdown timed out, forcing exit")
	}

	logg.Info().Msg("worker stopped cleanly")
}

func loggingMiddleware(logg zerolog.Logger) asynq.MiddlewareFunc {
	return func(next asynq.Handler) asynq.Handler {
		return asynq.HandlerFunc(func(ctx context.Context, task *asynq.Task) error {
			start := time.Now()

			taskID := "unknown"
			if rw := task.ResultWriter(); rw != nil {
				taskID = rw.TaskID()
			}

			logg.Info().
				Str("task_type", task.Type()).
				Str("task_id", taskID).
				Msg("task started")

			err := next.ProcessTask(ctx, task)
			duration := time.Since(start)

			if err != nil {
				logg.Error().
					Err(err).
					Str("task_type", task.Type()).
					Str("task_id", taskID).
					Dur("duration", duration).
					Msg("task failed")
			} else {
				logg.Info().
					Str("task_type", task.Type()).
					Str("task_id", taskID).
					Dur("duration", duration).
					Msg("task completed")
			}

			return err
		})
	}
}
