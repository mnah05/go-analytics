package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"go-analytics/internal/queue"
	"go-analytics/internal/scheduler"
	"go-analytics/internal/tasks"
	"go-analytics/internal/validator"
	"go-analytics/pkg/logger"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

type WorkerHandler struct {
	scheduler *scheduler.Client
	db        *pgxpool.Pool
	redis     *redis.Client
}

func NewWorkerHandler(scheduler *scheduler.Client, db *pgxpool.Pool, redis *redis.Client) *WorkerHandler {
	return &WorkerHandler{
		scheduler: scheduler,
		db:        db,
		redis:     redis,
	}
}

type PingRequest struct {
	Message string `json:"message,omitempty" validate:"omitempty,max=500"`
}

type PingResponse struct {
	Success  bool      `json:"success"`
	TaskID   string    `json:"task_id"`
	TaskType string    `json:"task_type"`
	QueuedAt time.Time `json:"queued_at"`
	Message  string    `json:"message,omitempty"`
}

func (h *WorkerHandler) Ping(w http.ResponseWriter, r *http.Request) {
	log := logger.FromChiContext(r.Context())

	requestID := middleware.GetReqID(r.Context())

	var payloadMsg string
	if r.ContentLength > 0 {
		maxSize := int64(1 << 20) // 1MB
		bodyReader := http.MaxBytesReader(w, r.Body, maxSize)
		bodyBytes, err := io.ReadAll(bodyReader)
		if err != nil {
			log.Error().Err(err).Msg("failed to read request body")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "request body too large (max 1MB)",
			})
			return
		}

		var body PingRequest
		if err := json.Unmarshal(bodyBytes, &body); err != nil {
			log.Error().Err(err).Msg("failed to decode ping request")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "invalid request body",
			})
			return
		}

		if err := validator.ValidateStruct(&body); err != nil {
			log.Error().Err(err).Msg("validation failed")
			errors := validator.GetValidationErrors(err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error":   "validation failed",
				"details": errors,
			})
			return
		}

		payloadMsg = body.Message
	}

	if payloadMsg == "" {
		payloadMsg = "ping from API"
	}

	payload := tasks.PingTaskPayload{
		Message:   payloadMsg,
		RequestID: requestID,
		QueuedAt:  time.Now().UTC(),
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		log.Error().Err(err).Msg("failed to marshal ping payload")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "failed to create task payload",
		})
		return
	}

	taskID, err := h.scheduler.EnqueueWithID(r.Context(), tasks.TypeWorkerPing, payloadBytes,
		asynq.Queue(queue.QueueDefault),
		asynq.MaxRetry(3),
		asynq.Timeout(30*time.Second),
	)
	if err != nil {
		log.Error().Err(err).Msg("failed to enqueue worker ping task")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{
			"error":   "failed to enqueue task",
			"details": err.Error(),
		})
		return
	}

	log.Info().
		Str("task_id", taskID).
		Str("task_type", tasks.TypeWorkerPing).
		Str("request_id", requestID).
		Msg("worker ping task enqueued")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(PingResponse{
		Success:  true,
		TaskID:   taskID,
		TaskType: tasks.TypeWorkerPing,
		QueuedAt: time.Now().UTC(),
		Message:  "Task queued successfully. Check worker logs to verify processing.",
	})
}

func (h *WorkerHandler) Status(w http.ResponseWriter, r *http.Request) {
	log := logger.FromChiContext(r.Context())
	requestID := middleware.GetReqID(r.Context())

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	status := map[string]any{
		"redis":    "unknown",
		"database": "unknown",
		"workers":  "unknown",
	}
	overall := http.StatusOK

	if err := h.redis.Ping(ctx).Err(); err != nil {
		log.Error().Err(err).Msg("redis health check failed")
		status["redis"] = "disconnected"
		overall = http.StatusServiceUnavailable
	} else {
		status["redis"] = "connected"
	}

	if err := h.db.Ping(ctx); err != nil {
		log.Error().Err(err).Msg("database health check failed")
		status["database"] = "disconnected"
		overall = http.StatusServiceUnavailable
	} else {
		status["database"] = "connected"
	}

	servers, err := h.scheduler.GetServers()
	if err != nil {
		log.Error().Err(err).Msg("failed to get worker servers info")
		status["workers"] = "error"
		overall = http.StatusServiceUnavailable
	} else {
		status["workers"] = map[string]any{
			"active_count": len(servers),
			"servers":      servers,
		}
		if len(servers) == 0 {
			overall = http.StatusServiceUnavailable
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(overall)
	json.NewEncoder(w).Encode(map[string]any{
		"request_id": requestID,
		"status":     status,
		"queues":     queue.Names(),
		"note":       "Use POST /worker/ping to test task processing",
	})
}

type HealthResponse struct {
	Status   map[string]string `json:"status"`
	Checked  time.Time         `json:"checked"`
	Duration int64             `json:"duration"`
}

func (h *WorkerHandler) Health(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	log := logger.FromChiContext(r.Context())

	status := map[string]string{
		"database": "up",
		"redis":    "up",
		"workers":  "up",
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

	servers, err := h.scheduler.GetServers()
	if err != nil || len(servers) == 0 {
		log.Warn().Err(err).Int("server_count", len(servers)).Msg("no active workers found")
		status["workers"] = "down"
		overall = http.StatusServiceUnavailable
	}

	duration := time.Since(start)

	response := HealthResponse{
		Status:   status,
		Checked:  time.Now().UTC(),
		Duration: duration.Milliseconds(),
	}

	log.Info().
		Dur("duration", duration).
		Str("database", status["database"]).
		Str("redis", status["redis"]).
		Str("workers", status["workers"]).
		Msg("worker health check completed")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(overall)
	json.NewEncoder(w).Encode(response)
}
