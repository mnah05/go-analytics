package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"go-analytics/internal/db"
	"go-analytics/internal/helper"
	redisstream "go-analytics/internal/redis"
	"go-analytics/internal/tasks"
	"go-analytics/internal/validator"
	"go-analytics/pkg/logger"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/sqids/sqids-go"
)

const (
	clickWorkerCount = 64
	clickChanSize    = 10000
)

type LinkHandler struct {
	db        *db.Queries
	pool      *pgxpool.Pool
	redis     *redis.Client
	shortener *sqids.Sqids
	clickCh   chan tasks.ClickTrackPayload
	wg        sync.WaitGroup
	logg      zerolog.Logger
}

func NewLinkHandler(pool *pgxpool.Pool, rdb *redis.Client, salt uint64, logg zerolog.Logger) *LinkHandler {
	h := &LinkHandler{
		db:        db.New(pool),
		pool:      pool,
		redis:     rdb,
		shortener: helper.NewShortener(salt),
		clickCh:   make(chan tasks.ClickTrackPayload, clickChanSize),
		logg:      logg,
	}
	h.wg.Add(clickWorkerCount)
	for range clickWorkerCount {
		go h.clickWorker()
	}
	return h
}

// Stop drains the click worker pool. Call during graceful shutdown.
func (h *LinkHandler) Stop() {
	close(h.clickCh)
	h.wg.Wait()
}

func (h *LinkHandler) clickWorker() {
	defer h.wg.Done()
	for payload := range h.clickCh {
		h.sendClickToStream(payload)
	}
}

func (h *LinkHandler) CreateLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url" validate:"required,url"`
	}
	log := logger.FromChiContext(r.Context())

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Error().Err(err).Msg("failed to decode request body")
		NewErrorResponse(w, http.StatusBadRequest, "invalid request", err.Error())
		return
	}

	if err := validator.ValidateStruct(req); err != nil {
		log.Error().Err(err).Msg("failed to validate request body")
		NewErrorResponse(w, http.StatusBadRequest, "invalid request", err.Error())
		return
	}

	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("failed to begin transaction")
		NewErrorResponse(w, http.StatusInternalServerError, "internal server error", err.Error())
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	qtx := h.db.WithTx(tx)

	link, err := qtx.CreateLink(r.Context(), req.URL)
	if err != nil {
		log.Error().Err(err).Msg("failed to create link")
		NewErrorResponse(w, http.StatusInternalServerError, "internal server error", err.Error())
		return
	}

	slug := helper.GenerateSlug(h.shortener, link.ID)
	link, err = qtx.UpdateLinkSlug(r.Context(), db.UpdateLinkSlugParams{
		Slug: pgtype.Text{String: slug, Valid: true},
		ID:   link.ID,
	})
	if err != nil {
		log.Error().Err(err).Msg("failed to update link slug")
		NewErrorResponse(w, http.StatusInternalServerError, "internal server error", err.Error())
		return
	}

	if err = tx.Commit(r.Context()); err != nil {
		log.Error().Err(err).Msg("failed to commit transaction")
		NewErrorResponse(w, http.StatusInternalServerError, "internal server error", err.Error())
		return
	}

	redisKey := fmt.Sprintf("link:%s", slug)
	if err := h.redis.Set(r.Context(), redisKey, req.URL, 30*24*time.Hour).Err(); err != nil {
		log.Error().Err(err).Msg("failed to set link in redis")
	}

	NewSuccessResponse(w, http.StatusCreated, link, "link created")
}

func (h *LinkHandler) GetLink(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	log := logger.FromChiContext(r.Context())

	var targetURL string
	redisKey := fmt.Sprintf("link:%s", slug)

	cachedURL, err := h.redis.Get(r.Context(), redisKey).Result()
	if err == nil {
		targetURL = cachedURL
	} else {
		link, err := h.db.GetLinkBySlug(r.Context(), pgtype.Text{String: slug, Valid: true})
		if err != nil {
			log.Error().Err(err).Msg("failed to get link")
			NewErrorResponse(w, http.StatusNotFound, "link not found", err.Error())
			return
		}
		targetURL = link.OriginalUrl

		if err := h.redis.Set(r.Context(), redisKey, targetURL, 30*24*time.Hour).Err(); err != nil {
			log.Error().Err(err).Msg("failed to set link in redis")
		}
	}

	// Extract request data before returning the response; worker goroutines
	// outlive the request so they must not reference *http.Request.
	payload := tasks.ClickTrackPayload{
		Slug:      slug,
		IpAddress: r.RemoteAddr,
		UserAgent: r.UserAgent(),
		Referer:   r.Referer(),
		RequestID: chimiddleware.GetReqID(r.Context()),
		ClickedAt: time.Now().UTC(),
	}

	select {
	case h.clickCh <- payload:
	default:
		log.Warn().Msg("click channel full, dropping click")
	}

	http.Redirect(w, r, targetURL, http.StatusFound)
}

func (h *LinkHandler) sendClickToStream(payload tasks.ClickTrackPayload) {
	ctx := context.Background()

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		h.logg.Error().Err(err).Msg("failed to marshal click payload")
		return
	}

	if err := h.redis.XAdd(ctx, &redis.XAddArgs{
		Stream: redisstream.StreamKey,
		MaxLen: 100000,
		Approx: true,
		Values: map[string]interface{}{
			"data": string(payloadBytes),
		},
	}).Err(); err != nil {
		h.logg.Error().Err(err).Msg("failed to add click to stream")
	}
}

func (h *LinkHandler) DeleteLink(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	log := logger.FromChiContext(r.Context())

	redisKey := fmt.Sprintf("link:%s", slug)
	if err := h.redis.Del(r.Context(), redisKey).Err(); err != nil {
		log.Error().Err(err).Msg("failed to delete link from redis")
	}

	_, err := h.db.SoftDeleteLink(r.Context(), pgtype.Text{String: slug, Valid: true})
	if err != nil {
		log.Error().Err(err).Msg("failed to delete link")
		NewErrorResponse(w, http.StatusNotFound, "link not found", err.Error())
		return
	}

	NewSuccessResponse(w, http.StatusOK, nil, "link deleted")
}
