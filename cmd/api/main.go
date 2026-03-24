package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"go-analytics/internal/config"
	"go-analytics/internal/db"
	"go-analytics/internal/handler"
	"go-analytics/internal/scheduler"
	"go-analytics/pkg/logger"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

func main() {
	cfg := config.Load(logger.New())

	logg, logCleanup := logger.NewLogger(cfg, "logs/api.log")
	if logCleanup != nil {
		defer func() {
			if err := logCleanup(); err != nil {
				logg.Error().Err(err).Msg("failed to cleanup logger")
			}
		}()
	}
	ctx := context.Background()

	dbCtx, dbCancel := context.WithTimeout(ctx, 10*time.Second)
	defer dbCancel()
	if err := db.Open(dbCtx, cfg); err != nil {
		logg.Error().Err(err).Msg("failed to initialize database")
		return
	}
	logg.Info().Msg("database connected")

	pool := db.Get()
	if pool == nil {
		logg.Error().Msg("database pool is nil")
		return
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})

	if err := rdb.Ping(ctx).Err(); err != nil {
		logg.Error().Err(err).Msg("redis connection failed")
		return
	}
	logg.Info().Msg("redis connected")

	schedulerClient := scheduler.NewClient(asynq.RedisClientOpt{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	logg.Info().Msg("scheduler client initialized")

	saltStr := os.Getenv("URL_SHORTENER_SALT")
	if saltStr == "" {
		logg.Error().Msg("URL_SHORTENER_SALT is not set")
		return
	}
	idSalt, err := strconv.ParseUint(saltStr, 10, 64)
	if err != nil {
		logg.Error().Err(err).Msg("invalid URL_SHORTENER_SALT")
		return
	}
	router := handler.NewRouter(logg, cfg, pool, rdb, schedulerClient, idSalt)

	server := &http.Server{
		Addr:           ":" + cfg.AppPort,
		Handler:        router,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 1 << 20, // 1MB
	}

	serverErrors := make(chan error, 1)

	go func() {
		logg.Info().
			Str("port", cfg.AppPort).
			Msg("server starting")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErrors <- fmt.Errorf("server failed to start: %w", err)
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErrors:
		logg.Fatal().Err(err).Msg("server startup failed")
	case sig := <-sigChan:
		logg.Info().Str("signal", sig.String()).Msg("shutdown signal received")
	}

	logg.Info().Msg("shutting down server...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.APIShutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logg.Error().Err(err).Msg("server shutdown failed")
	} else {
		logg.Info().Msg("server shutdown completed gracefully")
	}

	if err := schedulerClient.Close(); err != nil {
		logg.Error().Err(err).Msg("scheduler client close failed")
	}
	logg.Info().Msg("scheduler client closed")

	if err := rdb.Close(); err != nil {
		logg.Error().Err(err).Msg("redis close failed")
	}
	logg.Info().Msg("redis disconnected")

	db.Close()
	logg.Info().Msg("database disconnected")

	logg.Info().Msg("server stopped cleanly")
}
