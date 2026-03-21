package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go-analytics/internal/config"
	"go-analytics/internal/db"
	"go-analytics/internal/queue"
	"go-analytics/internal/tasks"
	"go-analytics/pkg/logger"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/rs/zerolog"
)

func main() {
	cfg := config.Load(logger.New())

	logg, logCleanup := logger.NewLogger(cfg, "logs/worker.log")
	if logCleanup != nil {
		defer logCleanup()
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

	mux.HandleFunc(tasks.TypeClickTrack, func(ctx context.Context, t *asynq.Task) error {
		var payload tasks.ClickTrackPayload
		if err := json.Unmarshal(t.Payload(), &payload); err != nil {
			logg.Error().Err(err).Msg("failed to unmarshal click track payload")
			return fmt.Errorf("unmarshal click track payload: %w", err)
		}

		rw := t.ResultWriter()
		if rw == nil {
			logg.Error().Str("slug", payload.Slug).Msg("cannot resolve task ID, skipping to retry")
			return fmt.Errorf("task result writer is nil, cannot determine task ID")
		}
		taskID := rw.TaskID()

		log := logg.With().
			Str("request_id", payload.RequestID).
			Str("slug", payload.Slug).
			Str("task_id", taskID).
			Logger()

		// Idempotency check: skip if already processed
		processed, err := queries.IsEventProcessed(ctx, taskID)
		if err != nil {
			log.Error().Err(err).Msg("failed to check processed event")
			return fmt.Errorf("check processed event: %w", err)
		}
		if processed {
			log.Info().Msg("task already processed, skipping")
			return nil
		}

		link, err := queries.GetLinkBySlug(ctx, pgtype.Text{String: payload.Slug, Valid: true})
		if err != nil {
			log.Error().Err(err).Msg("failed to get link by slug")
			return fmt.Errorf("get link by slug: %w", err)
		}

		// Parse IP address (strip port if present)
		var ipAddr *netip.Addr
		if payload.IpAddress != "" {
			host := payload.IpAddress
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			if parsed, err := netip.ParseAddr(host); err == nil {
				ipAddr = &parsed
			}
		}

		// Transaction: insert click log + mark as processed atomically
		tx, err := pool.Begin(ctx)
		if err != nil {
			log.Error().Err(err).Msg("failed to begin transaction")
			return fmt.Errorf("begin transaction: %w", err)
		}
		defer tx.Rollback(ctx)

		qtx := queries.WithTx(tx)

		_, err = qtx.InsertClickLog(ctx, db.InsertClickLogParams{
			LinkID:    link.ID,
			IpAddress: ipAddr,
			Referer:   pgtype.Text{String: payload.Referer, Valid: payload.Referer != ""},
			UserAgent: pgtype.Text{String: payload.UserAgent, Valid: payload.UserAgent != ""},
			ClickedAt: pgtype.Timestamptz{Time: payload.ClickedAt, Valid: true},
		})
		if err != nil {
			log.Error().Err(err).Msg("failed to insert click log")
			return fmt.Errorf("insert click log: %w", err)
		}

		err = qtx.MarkEventProcessed(ctx, db.MarkEventProcessedParams{
			TaskID:  taskID,
			JobType: tasks.TypeClickTrack,
		})
		if err != nil {
			log.Error().Err(err).Msg("failed to mark event processed")
			return fmt.Errorf("mark event processed: %w", err)
		}

		if err = tx.Commit(ctx); err != nil {
			log.Error().Err(err).Msg("failed to commit transaction")
			return fmt.Errorf("commit transaction: %w", err)
		}

		log.Info().Int64("link_id", link.ID).Msg("click tracked")
		return nil
	})

	workerErrors := make(chan error, 1)

	go func() {
		logg.Info().Msg("worker starting")
		if err := srv.Run(mux); err != nil {
			workerErrors <- fmt.Errorf("worker failed to start: %w", err)
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-workerErrors:
		logg.Fatal().Err(err).Msg("worker startup failed")
	case sig := <-sigChan:
		logg.Info().Str("signal", sig.String()).Msg("shutdown signal received")
	}

	logg.Info().Msg("shutting down worker...")

	srv.Stop()
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
		logg.Info().Msg("worker shutdown completed gracefully")
	case <-shutdownCtx.Done():
		logg.Warn().Msg("worker shutdown timed out, forcing exit")
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
