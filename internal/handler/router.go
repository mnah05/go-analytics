package handler

import (
	"net/http"
	"time"

	"go-analytics/internal/config"
	custommiddleware "go-analytics/internal/middleware"
	"go-analytics/internal/scheduler"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/go-chi/httprate"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

func NewRouter(log zerolog.Logger, cfg *config.Config, db *pgxpool.Pool, redis *redis.Client, scheduler *scheduler.Client, idSalt uint64) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "POST", "PUT", "DELETE"},
		AllowedHeaders: []string{"Accept", "Authorization", "Content-Type", "X-Request-ID"},
		ExposedHeaders: []string{"Link", "X-Request-ID"},
		MaxAge:         300,
	}))
	r.Use(custommiddleware.RequestLogger(log))
	r.Use(httprate.Limit(10, time.Second,
		httprate.WithKeyByIP(),
		httprate.WithLimitHandler(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limit exceeded","message":"too many requests"}`))
		}),
	))

	health := NewHealthHandler(db, redis, cfg.HealthCheckTimeout)
	worker := NewWorkerHandler(scheduler, db, redis)
	link := NewLinkHandler(db, redis, idSalt)
	stats := NewStatsHandler(db)

	r.Get("/health", health.Check)

	r.Route("/worker", func(r chi.Router) {
		r.Get("/health", worker.Health)
		r.Get("/status", worker.Status)
		r.Post("/ping", worker.Ping)
	})

	r.Route("/links", func(r chi.Router) {
		r.Post("/", link.CreateLink)
		r.Delete("/{slug}", link.DeleteLink)
		r.Get("/{slug}/stats", stats.GetStats)
	})

	r.Get("/{slug}", link.GetLink)

	return r
}
