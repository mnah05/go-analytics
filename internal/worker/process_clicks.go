package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"

	"go-analytics/internal/db"
	redisstream "go-analytics/internal/redis"
	"go-analytics/internal/tasks"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

const batchSize = 1000

type ClickProcessor struct {
	rdb     *redis.Client
	pool    *pgxpool.Pool
	queries *db.Queries
	logg    zerolog.Logger
}

func NewClickProcessor(rdb *redis.Client, pool *pgxpool.Pool, queries *db.Queries, logg zerolog.Logger) *ClickProcessor {
	return &ClickProcessor{
		rdb:     rdb,
		pool:    pool,
		queries: queries,
		logg:    logg,
	}
}

func (cp *ClickProcessor) ProcessPendingClicks(ctx context.Context) (err error) {
	consumerID := getConsumerID()

	messages, err := redisstream.ReadPendingClicks(ctx, cp.rdb, consumerID, batchSize)
	if err != nil {
		return fmt.Errorf("read pending clicks: %w", err)
	}

	if len(messages) == 0 {
		return nil
	}

	cp.logg.Info().Int("count", len(messages)).Str("consumer", consumerID).Msg("processing batch")

	type clickRecord struct {
		LinkID    int64
		IpAddress *netip.Addr
		Referer   pgtype.Text
		UserAgent pgtype.Text
		ClickedAt pgtype.Timestamptz
	}

	var clicks []clickRecord
	var validIDs []string

	for _, msg := range messages {
		var payload tasks.ClickTrackPayload
		data, ok := msg.Values["data"].(string)
		if !ok {
			cp.logg.Warn().Str("msg_id", msg.ID).Msg("missing data field, skipping")
			validIDs = append(validIDs, msg.ID)
			continue
		}

		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			cp.logg.Warn().Str("msg_id", msg.ID).Err(err).Msg("malformed payload, skipping")
			validIDs = append(validIDs, msg.ID)
			continue
		}

		link, err := cp.queries.GetLinkBySlug(ctx, pgtype.Text{String: payload.Slug, Valid: true})
		if err != nil {
			cp.logg.Warn().Str("slug", payload.Slug).Err(err).Msg("failed to get link, skipping")
			validIDs = append(validIDs, msg.ID)
			continue
		}

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

		clicks = append(clicks, clickRecord{
			LinkID:    link.ID,
			IpAddress: ipAddr,
			Referer:   pgtype.Text{String: payload.Referer, Valid: payload.Referer != ""},
			UserAgent: pgtype.Text{String: payload.UserAgent, Valid: payload.UserAgent != ""},
			ClickedAt: pgtype.Timestamptz{Time: payload.ClickedAt, Valid: true},
		})
		validIDs = append(validIDs, msg.ID)
	}

	if len(clicks) == 0 {
		if err := redisstream.AckClicks(ctx, cp.rdb, validIDs...); err != nil {
			cp.logg.Error().Err(err).Msg("failed to ack empty batch")
		}
		return nil
	}

	tx, err := cp.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	defer func() {
		if err != nil {
			if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
				cp.logg.Error().Err(rollbackErr).Msg("failed to rollback transaction")
			}
		}
	}()

	rows := make([][]interface{}, len(clicks))
	for i, click := range clicks {
		rows[i] = []interface{}{
			click.LinkID,
			click.IpAddress,
			click.Referer,
			pgtype.Text{}, // country — not populated from stream
			click.UserAgent,
			click.ClickedAt,
		}
	}

	_, err = tx.CopyFrom(
		ctx,
		pgx.Identifier{"click_logs"},
		[]string{"link_id", "ip_address", "referer", "country", "user_agent", "clicked_at"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return fmt.Errorf("bulk insert click logs: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	if err := redisstream.AckClicks(ctx, cp.rdb, validIDs...); err != nil {
		cp.logg.Error().Err(err).Msg("failed to ack clicks after successful insert")
	}

	cp.logg.Info().Int("processed", len(clicks)).Msg("batch processed successfully")
	return nil
}

func getConsumerID() string {
	hostname, _ := os.Hostname()
	pid := os.Getpid()
	return fmt.Sprintf("%s-%d", hostname, pid)
}
