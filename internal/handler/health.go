package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"go-analytics/pkg/logger"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

type HealthHandler struct {
	db      *pgxpool.Pool
	redis   *redis.Client
	timeout time.Duration
}

func NewHealthHandler(db *pgxpool.Pool, redis *redis.Client, timeout time.Duration) *HealthHandler {
	return &HealthHandler{
		db:      db,
		redis:   redis,
		timeout: timeout,
	}
}

func (h *HealthHandler) Check(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	log := logger.FromChiContext(r.Context())

	status := map[string]string{
		"database": "up",
		"redis":    "up",
	}
	overall := http.StatusOK

	if err := h.db.Ping(ctx); err != nil {
		log.Error().Err(err).Msg("database health check failed")
		status["database"] = "down"
		overall = http.StatusServiceUnavailable
	}

	if err := h.redis.Ping(ctx).Err(); err != nil {
		log.Error().Err(err).Msg("redis health check failed")
		status["redis"] = "down"
		overall = http.StatusServiceUnavailable
	}

	duration := time.Since(start)

	response := map[string]any{
		"status":   status,
		"checked":  time.Now().UTC(),
		"duration": duration.Milliseconds(),
	}

	dbStatus := status["database"]
	redisStatus := status["redis"]
	log.Info().
		Dur("duration", duration).
		Str("database", dbStatus).
		Str("redis", redisStatus).
		Msg("health check completed")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(overall)
	json.NewEncoder(w).Encode(response)
}
