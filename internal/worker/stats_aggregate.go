package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go-analytics/internal/db"
	"go-analytics/internal/tasks"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

const processedKeyTTL = 7 * 24 * time.Hour

type StatsAggregator struct {
	rdb     *redis.Client
	pool    *pgxpool.Pool
	queries *db.Queries
	logg    zerolog.Logger
}

func NewStatsAggregator(rdb *redis.Client, pool *pgxpool.Pool, queries *db.Queries, logg zerolog.Logger) *StatsAggregator {
	return &StatsAggregator{
		rdb:     rdb,
		pool:    pool,
		queries: queries,
		logg:    logg,
	}
}

func (sa *StatsAggregator) processedKey(requestID string) string {
	return fmt.Sprintf("stats:processed:%s", requestID)
}

func (sa *StatsAggregator) Aggregate(ctx context.Context, payload tasks.StatsAggregatePayload) (err error) {
	// idempotency check
	exists, redisErr := sa.rdb.Exists(ctx, sa.processedKey(payload.RequestID)).Result()
	if redisErr != nil {
		sa.logg.Warn().Err(redisErr).Str("request_id", payload.RequestID).Msg("failed to check idempotency, proceeding anyway")
	} else if exists > 0 {
		sa.logg.Info().Str("request_id", payload.RequestID).Msg("stats aggregate already processed, skipping")
		return nil
	}

	startAt := pgtype.Timestamptz{Time: payload.StartAt, Valid: true}
	endAt := pgtype.Timestamptz{Time: payload.EndAt, Valid: true}

	tx, err := sa.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	defer func() {
		if err != nil {
			if rbErr := tx.Rollback(ctx); rbErr != nil {
				sa.logg.Error().Err(rbErr).Msg("failed to rollback transaction")
			}
		}
	}()

	qtx := sa.queries.WithTx(tx)

	// 1. Upsert daily clicks + unique visitors
	if err = qtx.UpsertDailyClicks(ctx, db.UpsertDailyClicksParams{
		ClickedAt:   startAt,
		ClickedAt_2: endAt,
	}); err != nil {
		return fmt.Errorf("upsert daily clicks: %w", err)
	}

	// 2. Country stats — group rows by (link_id, date), build JSONB, update
	countryRows, err := qtx.GetCountryStats(ctx, db.GetCountryStatsParams{
		ClickedAt:   startAt,
		ClickedAt_2: endAt,
	})
	if err != nil {
		return fmt.Errorf("get country stats: %w", err)
	}

	type statKey struct {
		LinkID int64
		Date   pgtype.Date
	}

	countryMap := make(map[statKey]map[string]int64)
	for _, row := range countryRows {
		key := statKey{LinkID: row.LinkID, Date: row.Date}
		if countryMap[key] == nil {
			countryMap[key] = make(map[string]int64)
		}
		countryMap[key][row.Country.String] = row.Cnt
	}

	for key, counts := range countryMap {
		data, jsonErr := json.Marshal(counts)
		if jsonErr != nil {
			return fmt.Errorf("marshal country stats: %w", jsonErr)
		}
		if err = qtx.UpdateDailyCountries(ctx, db.UpdateDailyCountriesParams{
			LinkID:    key.LinkID,
			Date:      key.Date,
			Countries: data,
		}); err != nil {
			return fmt.Errorf("update daily countries: %w", err)
		}
	}

	// 3. Referer stats — same grouping pattern
	refererRows, err := qtx.GetRefererStats(ctx, db.GetRefererStatsParams{
		ClickedAt:   startAt,
		ClickedAt_2: endAt,
	})
	if err != nil {
		return fmt.Errorf("get referer stats: %w", err)
	}

	refererMap := make(map[statKey]map[string]int64)
	for _, row := range refererRows {
		key := statKey{LinkID: row.LinkID, Date: row.Date}
		if refererMap[key] == nil {
			refererMap[key] = make(map[string]int64)
		}
		refererMap[key][row.Referer.String] = row.Cnt
	}

	for key, counts := range refererMap {
		data, jsonErr := json.Marshal(counts)
		if jsonErr != nil {
			return fmt.Errorf("marshal referer stats: %w", jsonErr)
		}
		if err = qtx.UpdateDailyReferers(ctx, db.UpdateDailyReferersParams{
			LinkID:   key.LinkID,
			Date:     key.Date,
			Referers: data,
		}); err != nil {
			return fmt.Errorf("update daily referers: %w", err)
		}
	}

	// 4. Bump total_clicks on links for all clicks since startAt
	if err = qtx.IncrementTotalClicksSince(ctx, startAt); err != nil {
		return fmt.Errorf("increment total clicks: %w", err)
	}

	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	// Mark as processed in Redis after a successful commit (non-fatal if this fails)
	if redisErr := sa.rdb.Set(ctx, sa.processedKey(payload.RequestID), 1, processedKeyTTL).Err(); redisErr != nil {
		sa.logg.Warn().Err(redisErr).Str("request_id", payload.RequestID).Msg("failed to mark aggregate as processed")
	}

	sa.logg.Info().
		Str("request_id", payload.RequestID).
		Time("start_at", payload.StartAt).
		Time("end_at", payload.EndAt).
		Msg("stats aggregation completed")

	return nil
}
