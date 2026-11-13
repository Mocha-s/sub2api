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

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, channel_id, platform, models, billing_mode, input_price, output_price, cache_write_price, cache_write_1h_price, cache_read_price, fast_multiplier, flex_multiplier, reasoning_effort_multipliers, image_input_price, image_output_price, per_request_price, time_pricing, video_price_per_second, video_reference_price_per_second, video_default_seconds, video_allowed_seconds, description, max_reasoning_effort_multiplier, created_at, updated_at, video_extra_seconds
		 FROM channel_model_pricing WHERE channel_id = $1 ORDER BY id`)).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "channel_id", "platform", "models", "billing_mode", "input_price", "output_price",
			"cache_write_price", "cache_write_1h_price", "cache_read_price", "fast_multiplier", "flex_multiplier", "reasoning_effort_multipliers", "image_input_price", "image_output_price", "per_request_price", "time_pricing",
			"video_price_per_second", "video_reference_price_per_second", "video_default_seconds", "video_allowed_seconds", "description", "max_reasoning_effort_multiplier", "created_at", "updated_at", "video_extra_seconds",
		}).AddRow(int64(11), int64(7), "openai", []byte(`["sora-2","sora-2-fast"]`), "video_reference", nil, nil, nil, nil, nil, nil, nil, []byte(`{}`), nil, nil, nil, nil, 0.03, 0.07, 10, []byte(`[5,10]`), "Fast video\nShared by aliases", nil, now, now, 2))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, pricing_id, min_tokens, max_tokens, tier_label,
		        input_price, output_price, cache_write_price, cache_write_1h_price, cache_read_price,
		        input_multiplier, output_multiplier, cache_write_multiplier, cache_read_multiplier,
		        per_request_price, video_price_per_second, video_reference_price_per_second, sort_order, created_at, updated_at
		 FROM channel_pricing_intervals
		 WHERE pricing_id = ANY($1) ORDER BY pricing_id, sort_order, id`)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "pricing_id", "min_tokens", "max_tokens", "tier_label", "input_price", "output_price",
			"cache_write_price", "cache_write_1h_price", "cache_read_price", "input_multiplier", "output_multiplier", "cache_write_multiplier", "cache_read_multiplier", "per_request_price", "video_price_per_second", "video_reference_price_per_second", "sort_order", "created_at", "updated_at",
		}).AddRow(int64(12), int64(11), 0, nil, "hd", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, 0.05, 0.09, 0, now, now))

	got, err := repo.ListModelPricing(context.Background(), 7)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, []int{5, 10}, got[0].VideoAllowedSeconds)
	require.Equal(t, "Fast video\nShared by aliases", got[0].Description)
	require.Equal(t, 0.03, *got[0].VideoPricePerSecond)
	require.Equal(t, 0.07, *got[0].VideoReferencePricePerSecond)
	require.Equal(t, 0.09, *got[0].Intervals[0].VideoReferencePricePerSecond)
	require.Equal(t, 10, *got[0].VideoDefaultSeconds)
	require.Equal(t, 2, got[0].VideoExtraSeconds)
	require.Equal(t, 0.05, *got[0].Intervals[0].VideoPricePerSecond)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBatchLoadModelPricingSelectsVideoColumnsInOrder(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &channelRepository{db: db}

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, channel_id, platform, models, billing_mode, input_price, output_price, cache_write_price, cache_write_1h_price, cache_read_price, fast_multiplier, flex_multiplier, reasoning_effort_multipliers, image_input_price, image_output_price, per_request_price, time_pricing, video_price_per_second, video_reference_price_per_second, video_default_seconds, video_allowed_seconds, description, max_reasoning_effort_multiplier, created_at, updated_at, video_extra_seconds
		 FROM channel_model_pricing WHERE channel_id = ANY($1) ORDER BY channel_id, id`)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "channel_id", "platform", "models", "billing_mode", "input_price", "output_price",
			"cache_write_price", "cache_write_1h_price", "cache_read_price", "fast_multiplier", "flex_multiplier", "reasoning_effort_multipliers", "image_input_price", "image_output_price", "per_request_price", "time_pricing",
			"video_price_per_second", "video_reference_price_per_second", "video_default_seconds", "video_allowed_seconds", "description", "max_reasoning_effort_multiplier", "created_at", "updated_at", "video_extra_seconds",
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
		VideoExtraSeconds:   2,
		Intervals: []service.PricingInterval{{
			TierLabel:           "hd",
			VideoPricePerSecond: &tierPrice,
		}},
	}

	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO channel_model_pricing (channel_id, platform, models, billing_mode, input_price, output_price, cache_write_price, cache_write_1h_price, cache_read_price, fast_multiplier, flex_multiplier, reasoning_effort_multipliers, image_input_price, image_output_price, per_request_price, time_pricing, video_price_per_second, video_reference_price_per_second, video_default_seconds, video_allowed_seconds, description, max_reasoning_effort_multiplier, video_extra_seconds)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23) RETURNING id, created_at, updated_at`)).
		WithArgs(int64(7), "openai", []byte(`["sora-2"]`), service.BillingModeVideo,
			nil, nil, nil, nil, nil, nil, nil, `{}`, nil, nil, nil, nil, price, nil, seconds, []byte(`[5,10]`), pricing.Description, nil, 2).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(int64(11), now, now))
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO channel_pricing_intervals
		 (pricing_id, min_tokens, max_tokens, tier_label, input_price, output_price, cache_write_price, cache_write_1h_price, cache_read_price, input_multiplier, output_multiplier, cache_write_multiplier, cache_read_multiplier, per_request_price, video_price_per_second, video_reference_price_per_second, sort_order)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17) RETURNING id, created_at, updated_at`)).
		WithArgs(int64(11), 0, nil, "hd", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, tierPrice, nil, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(int64(12), now, now))

	require.NoError(t, createModelPricingExec(context.Background(), db, &pricing))

	repo := &channelRepository{db: db}
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE channel_model_pricing
		 SET models = $1, billing_mode = $2, input_price = $3, output_price = $4, cache_write_price = $5, cache_write_1h_price = $6, cache_read_price = $7, fast_multiplier = $8, flex_multiplier = $9, reasoning_effort_multipliers = $10, image_input_price = $11, image_output_price = $12, per_request_price = $13, time_pricing = $14, video_price_per_second = $15, video_reference_price_per_second = $16, video_default_seconds = $17, video_allowed_seconds = $18, platform = $19, description = $20, max_reasoning_effort_multiplier = $21, video_extra_seconds = $22, updated_at = NOW()
		 WHERE id = $23`)).
		WithArgs([]byte(`["sora-2"]`), service.BillingModeVideo,
			nil, nil, nil, nil, nil, nil, nil, `{}`, nil, nil, nil, nil, price, nil, seconds, []byte(`[5,10]`), "openai", pricing.Description, nil, 2, int64(11)).
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
		        cache_write_price, cache_write_1h_price, cache_read_price, reasoning_effort_multipliers, image_input_price, image_output_price, per_request_price,
		        video_price_per_second, video_reference_price_per_second, video_default_seconds, video_allowed_seconds, created_at, updated_at, video_extra_seconds
		 FROM channel_account_stats_model_pricing WHERE rule_id = ANY($1) ORDER BY rule_id, id`)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "rule_id", "platform", "models", "billing_mode", "input_price", "output_price", "cache_write_price", "cache_write_1h_price",
			"cache_read_price", "reasoning_effort_multipliers", "image_input_price", "image_output_price", "per_request_price", "video_price_per_second", "video_reference_price_per_second", "video_default_seconds",
			"video_allowed_seconds", "created_at", "updated_at", "video_extra_seconds",
		}).AddRow(int64(21), int64(9), "openai", []byte(`["sora-2"]`), "video_reference", nil, nil, nil, nil, nil, []byte(`{}`), nil, nil, nil, 0.03, 0.07, 10, []byte(`[5,10]`), now, now, 2))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, pricing_id, min_tokens, max_tokens, tier_label,
		        input_price, output_price, cache_write_price, cache_write_1h_price, cache_read_price,
		        per_request_price, video_price_per_second, video_reference_price_per_second, sort_order, created_at, updated_at
		 FROM channel_account_stats_pricing_intervals
		 WHERE pricing_id = ANY($1) ORDER BY pricing_id, sort_order, id`)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "pricing_id", "min_tokens", "max_tokens", "tier_label", "input_price", "output_price", "cache_write_price", "cache_write_1h_price",
			"cache_read_price", "per_request_price", "video_price_per_second", "video_reference_price_per_second", "sort_order", "created_at", "updated_at",
		}).AddRow(int64(22), int64(21), 0, nil, "hd", nil, nil, nil, nil, nil, nil, 0.05, 0.09, 0, now, now))

	got, err := repo.batchLoadAccountStatsModelPricing(context.Background(), []int64{9})
	require.NoError(t, err)
	require.Equal(t, []int{5, 10}, got[9][0].VideoAllowedSeconds)
	require.Equal(t, 0.03, *got[9][0].VideoPricePerSecond)
	require.Equal(t, 2, got[9][0].VideoExtraSeconds)
	require.Equal(t, 0.07, *got[9][0].VideoReferencePricePerSecond)
	require.Equal(t, 0.09, *got[9][0].Intervals[0].VideoReferencePricePerSecond)
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
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO channel_account_stats_model_pricing (rule_id, platform, models, billing_mode, input_price, output_price, cache_write_price, cache_write_1h_price, cache_read_price, reasoning_effort_multipliers, image_input_price, image_output_price, per_request_price, video_price_per_second, video_reference_price_per_second, video_default_seconds, video_allowed_seconds, video_extra_seconds)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18) RETURNING id, created_at, updated_at`)).
		WithArgs(int64(9), "openai", []byte(`["sora-2"]`), service.BillingModeVideo, nil, nil, nil, nil, nil, `{}`, nil, nil, nil, price, nil, seconds, []byte(`[5,10]`), 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(int64(21), now, now))
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO channel_account_stats_pricing_intervals
		 (pricing_id, min_tokens, max_tokens, tier_label, input_price, output_price, cache_write_price, cache_read_price, per_request_price, video_price_per_second, video_reference_price_per_second, sort_order)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id, created_at, updated_at`)).
		WithArgs(int64(21), 0, nil, "hd", nil, nil, nil, nil, nil, tierPrice, nil, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(int64(22), now, now))

	require.NoError(t, createAccountStatsModelPricingTx(context.Background(), tx, 9, &pricing))
	require.Equal(t, "account-stat description must stay in memory only", pricing.Description)
	mock.ExpectCommit()
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}
