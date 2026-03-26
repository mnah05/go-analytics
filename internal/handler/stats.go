package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"go-analytics/internal/db"
	"go-analytics/pkg/logger"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const statsCacheTTL = 60 * time.Second

type StatsHandler struct {
	db    *db.Queries
	redis *redis.Client
}

func NewStatsHandler(pool *pgxpool.Pool, rdb *redis.Client) *StatsHandler {
	return &StatsHandler{db: db.New(pool), redis: rdb}
}

type dailyStatResponse struct {
	Date           string           `json:"date"`
	Clicks         int64            `json:"clicks"`
	UniqueVisitors int64            `json:"unique_visitors"`
	Countries      map[string]int64 `json:"countries"`
	Referers       map[string]int64 `json:"referers"`
}

type statsResponse struct {
	Slug                string              `json:"slug"`
	TotalClicks         int64               `json:"total_clicks"`
	TotalUniqueVisitors int64               `json:"total_unique_visitors"`
	Daily               []dailyStatResponse `json:"daily"`
}

func (h *StatsHandler) GetStats(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	log := logger.FromChiContext(r.Context())

	// Parse optional date range query params; default to last 30 days
	now := time.Now().UTC()
	endDate := now
	startDate := now.AddDate(0, 0, -30)

	if s := r.URL.Query().Get("start"); s != "" {
		if t, parseErr := time.Parse("2006-01-02", s); parseErr == nil {
			startDate = t
		}
	}
	if e := r.URL.Query().Get("end"); e != "" {
		if t, parseErr := time.Parse("2006-01-02", e); parseErr == nil {
			endDate = t
		}
	}

	// Try Redis cache first
	cacheKey := fmt.Sprintf("stats:%s:%s:%s",
		slug, startDate.Format("2006-01-02"), endDate.Format("2006-01-02"))

	if cached, err := h.redis.Get(r.Context(), cacheKey).Bytes(); err == nil {
		var resp statsResponse
		if json.Unmarshal(cached, &resp) == nil {
			NewSuccessResponse(w, http.StatusOK, resp, "")
			return
		}
	}

	link, err := h.db.GetLinkBySlug(r.Context(), pgtype.Text{String: slug, Valid: true})
	if err != nil {
		log.Error().Err(err).Str("slug", slug).Msg("link not found")
		NewErrorResponse(w, http.StatusNotFound, "link not found", err.Error())
		return
	}

	summary, err := h.db.GetLinkStatsSummary(r.Context(), link.ID)
	if err != nil {
		log.Error().Err(err).Str("slug", slug).Msg("failed to get stats summary")
		NewErrorResponse(w, http.StatusInternalServerError, "internal server error", err.Error())
		return
	}

	rows, err := h.db.GetDailyStatsByLink(r.Context(), db.GetDailyStatsByLinkParams{
		LinkID: link.ID,
		Date:   pgtype.Date{Time: startDate, Valid: true},
		Date_2: pgtype.Date{Time: endDate, Valid: true},
	})
	if err != nil {
		log.Error().Err(err).Str("slug", slug).Msg("failed to get daily stats")
		NewErrorResponse(w, http.StatusInternalServerError, "internal server error", err.Error())
		return
	}

	daily := make([]dailyStatResponse, 0, len(rows))
	for _, row := range rows {
		d := dailyStatResponse{
			Date:           row.Date.Time.Format("2006-01-02"),
			Clicks:         row.Clicks,
			UniqueVisitors: row.UniqueVisitors,
			Countries:      make(map[string]int64),
			Referers:       make(map[string]int64),
		}
		if len(row.Countries) > 0 {
			_ = json.Unmarshal(row.Countries, &d.Countries)
		}
		if len(row.Referers) > 0 {
			_ = json.Unmarshal(row.Referers, &d.Referers)
		}
		daily = append(daily, d)
	}

	resp := statsResponse{
		Slug:                slug,
		TotalClicks:         summary.TotalClicks,
		TotalUniqueVisitors: summary.TotalUniqueVisitors,
		Daily:               daily,
	}

	// Cache the response (best-effort, non-fatal)
	if data, err := json.Marshal(resp); err == nil {
		_ = h.redis.Set(r.Context(), cacheKey, data, statsCacheTTL).Err()
	}

	NewSuccessResponse(w, http.StatusOK, resp, "")
}
