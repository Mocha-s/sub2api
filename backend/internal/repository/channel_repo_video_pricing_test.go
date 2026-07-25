//go:build unit

package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestListModelPricingScansVideoColumnsInOrder(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &channelRepository{db: db}
	now := time.Now()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, channel_id, platform, models, billing_mode, input_price, output_price, cache_write_price, cache_read_price, image_input_price, image_output_price, per_request_price, video_price_per_second, video_default_seconds, video_allowed_seconds, description, created_at, updated_at
		 FROM channel_model_pricing WHERE channel_id = $1 ORDER BY id`)).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "channel_id", "platform", "models", "billing_mode", "input_price", "output_price",
			"cache_write_price", "cache_read_price", "image_input_price", "image_output_price", "per_request_price",
			"video_price_per_second", "video_default_seconds", "video_allowed_seconds", "description", "created_at", "updated_at",
		}).AddRow(int64(11), int64(7), "openai", []byte(`["sora-2","sora-2-fast"]`), "video", nil, nil, nil, nil, nil, nil, nil, 0.03, 10, []byte(`[5,10]`), "Fast video\nShared by aliases", now, now))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, pricing_id, min_tokens, max_tokens, tier_label,
		        input_price, output_price, cache_write_price, cache_read_price,
		        per_request_price, video_price_per_second, sort_order, created_at, updated_at
		 FROM channel_pricing_intervals
		 WHERE pricing_id = ANY($1) ORDER BY pricing_id, sort_order, id`)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "pricing_id", "min_tokens", "max_tokens", "tier_label", "input_price", "output_price",
			"cache_write_price", "cache_read_price", "per_request_price", "video_price_per_second", "sort_order", "created_at", "updated_at",
		}).AddRow(int64(12), int64(11), 0, nil, "hd", nil, nil, nil, nil, nil, 0.05, 0, now, now))

	got, err := repo.ListModelPricing(context.Background(), 7)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, []int{5, 10}, got[0].VideoAllowedSeconds)
	require.Equal(t, "Fast video\nShared by aliases", got[0].Description)
	require.Equal(t, 0.03, *got[0].VideoPricePerSecond)
	require.Equal(t, 10, *got[0].VideoDefaultSeconds)
	require.Equal(t, 0.05, *got[0].Intervals[0].VideoPricePerSecond)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBatchLoadModelPricingSelectsVideoColumnsInOrder(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &channelRepository{db: db}

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, channel_id, platform, models, billing_mode, input_price, output_price, cache_write_price, cache_read_price, image_input_price, image_output_price, per_request_price, video_price_per_second, video_default_seconds, video_allowed_seconds, description, created_at, updated_at
		 FROM channel_model_pricing WHERE channel_id = ANY($1) ORDER BY channel_id, id`)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "channel_id", "platform", "models", "billing_mode", "input_price", "output_price",
			"cache_write_price", "cache_read_price", "image_input_price", "image_output_price", "per_request_price",
			"video_price_per_second", "video_default_seconds", "video_allowed_seconds", "description", "created_at", "updated_at",
		}))

	got, err := repo.batchLoadModelPricing(context.Background(), []int64{7})
	require.NoError(t, err)
	require.Empty(t, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMainPricingWritesVideoColumnsAndArgumentsInOrder(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now()
	price, tierPrice, seconds := 0.03, 0.05, 10
	pricing := service.ChannelModelPricing{
		ID:                  11,
		ChannelID:           7,
		Platform:            "openai",
		Models:              []string{"sora-2"},
		Description:         "Fast video\nShared by aliases",
		BillingMode:         service.BillingModeVideo,
		VideoPricePerSecond: &price,
		VideoDefaultSeconds: &seconds,
		VideoAllowedSeconds: []int{5, 10},
		Intervals: []service.PricingInterval{{
			TierLabel:           "hd",
			VideoPricePerSecond: &tierPrice,
		}},
	}

	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO channel_model_pricing (channel_id, platform, models, billing_mode, input_price, output_price, cache_write_price, cache_read_price, image_input_price, image_output_price, per_request_price, video_price_per_second, video_default_seconds, video_allowed_seconds, description)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15) RETURNING id, created_at, updated_at`)).
		WithArgs(int64(7), "openai", []byte(`["sora-2"]`), service.BillingModeVideo, nil, nil, nil, nil, nil, nil, nil, price, seconds, []byte(`[5,10]`), "Fast video\nShared by aliases").
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(int64(11), now, now))
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO channel_pricing_intervals
		 (pricing_id, min_tokens, max_tokens, tier_label, input_price, output_price, cache_write_price, cache_read_price, per_request_price, video_price_per_second, sort_order)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id, created_at, updated_at`)).
		WithArgs(int64(11), 0, nil, "hd", nil, nil, nil, nil, nil, tierPrice, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(int64(12), now, now))

	require.NoError(t, createModelPricingExec(context.Background(), db, &pricing))

	repo := &channelRepository{db: db}
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE channel_model_pricing
		 SET models = $1, billing_mode = $2, input_price = $3, output_price = $4, cache_write_price = $5, cache_read_price = $6, image_input_price = $7, image_output_price = $8, per_request_price = $9, video_price_per_second = $10, video_default_seconds = $11, video_allowed_seconds = $12, platform = $13, description = $14, updated_at = NOW()
		 WHERE id = $15`)).
		WithArgs([]byte(`["sora-2"]`), service.BillingModeVideo, nil, nil, nil, nil, nil, nil, nil, price, seconds, []byte(`[5,10]`), "openai", "Fast video\nShared by aliases", int64(11)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.UpdateModelPricing(context.Background(), &pricing))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountStatsPricingReadsVideoColumnsInOrder(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &channelRepository{db: db}
	now := time.Now()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, rule_id, platform, models, billing_mode, input_price, output_price,
		        cache_write_price, cache_read_price, image_input_price, image_output_price, per_request_price,
		        video_price_per_second, video_default_seconds, video_allowed_seconds, created_at, updated_at
		 FROM channel_account_stats_model_pricing WHERE rule_id = ANY($1) ORDER BY rule_id, id`)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "rule_id", "platform", "models", "billing_mode", "input_price", "output_price", "cache_write_price",
			"cache_read_price", "image_input_price", "image_output_price", "per_request_price", "video_price_per_second", "video_default_seconds",
			"video_allowed_seconds", "created_at", "updated_at",
		}).AddRow(int64(21), int64(9), "openai", []byte(`["sora-2"]`), "video", nil, nil, nil, nil, nil, nil, nil, 0.03, 10, []byte(`[5,10]`), now, now))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, pricing_id, min_tokens, max_tokens, tier_label,
		        input_price, output_price, cache_write_price, cache_read_price,
		        per_request_price, video_price_per_second, sort_order, created_at, updated_at
		 FROM channel_account_stats_pricing_intervals
		 WHERE pricing_id = ANY($1) ORDER BY pricing_id, sort_order, id`)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "pricing_id", "min_tokens", "max_tokens", "tier_label", "input_price", "output_price", "cache_write_price",
			"cache_read_price", "per_request_price", "video_price_per_second", "sort_order", "created_at", "updated_at",
		}).AddRow(int64(22), int64(21), 0, nil, "hd", nil, nil, nil, nil, nil, 0.05, 0, now, now))

	got, err := repo.batchLoadAccountStatsModelPricing(context.Background(), []int64{9})
	require.NoError(t, err)
	require.Equal(t, []int{5, 10}, got[9][0].VideoAllowedSeconds)
	require.Equal(t, 0.03, *got[9][0].VideoPricePerSecond)
	require.Equal(t, 0.05, *got[9][0].Intervals[0].VideoPricePerSecond)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountStatsPricingWritesVideoColumnsAndIgnoresDescription(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now()
	price, tierPrice, seconds := 0.03, 0.05, 10
	pricing := service.ChannelModelPricing{
		Platform:            "openai",
		Models:              []string{"sora-2"},
		Description:         "account-stat description must stay in memory only",
		BillingMode:         service.BillingModeVideo,
		VideoPricePerSecond: &price,
		VideoDefaultSeconds: &seconds,
		VideoAllowedSeconds: []int{5, 10},
		Intervals: []service.PricingInterval{{
			TierLabel:           "hd",
			VideoPricePerSecond: &tierPrice,
		}},
	}

	mock.ExpectBegin()
	tx, err := db.Begin()
	require.NoError(t, err)
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO channel_account_stats_model_pricing (rule_id, platform, models, billing_mode, input_price, output_price, cache_write_price, cache_read_price, image_input_price, image_output_price, per_request_price, video_price_per_second, video_default_seconds, video_allowed_seconds)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) RETURNING id, created_at, updated_at`)).
		WithArgs(int64(9), "openai", []byte(`["sora-2"]`), service.BillingModeVideo, nil, nil, nil, nil, nil, nil, nil, price, seconds, []byte(`[5,10]`)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(int64(21), now, now))
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO channel_account_stats_pricing_intervals
		 (pricing_id, min_tokens, max_tokens, tier_label, input_price, output_price, cache_write_price, cache_read_price, per_request_price, video_price_per_second, sort_order)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id, created_at, updated_at`)).
		WithArgs(int64(21), 0, nil, "hd", nil, nil, nil, nil, nil, tierPrice, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(int64(22), now, now))

	require.NoError(t, createAccountStatsModelPricingTx(context.Background(), tx, 9, &pricing))
	require.Equal(t, "account-stat description must stay in memory only", pricing.Description)
	mock.ExpectCommit()
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}
