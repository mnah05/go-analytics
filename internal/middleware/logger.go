package middleware

import (
	"net/http"
	"time"

	"go-analytics/pkg/logger"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"
)

func RequestLogger(base zerolog.Logger) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			reqID := middleware.GetReqID(r.Context())

			rctx := chi.RouteContext(r.Context())
			path := r.URL.Path
			if rctx != nil {
				path = rctx.RoutePattern()
			}

			reqLogger := base.With().
				Str("request_id", reqID).
				Str("method", r.Method).
				Str("path", path).
				Logger()

			ctx := logger.WithChiContext(r.Context(), reqLogger)

			rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
			next.ServeHTTP(rw, r.WithContext(ctx))

			reqLogger.Info().
				Dur("duration", time.Since(start)).
				Int("status", rw.statusCode).
				Msg("request completed")
		})
	}
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}
