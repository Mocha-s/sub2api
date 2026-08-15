//go:build integration

package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageLog_UpstreamModelMismatchFilterAndPartialIndex(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)

	user := mustCreateUser(t, client, &service.User{Email: "model-audit@test.com"})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-model-audit", Name: "model-audit"})
	account := mustCreateAccount(t, client, &service.Account{Name: "model-audit-account"})
	now := time.Now().UTC()
	responseModel := "gpt-5.4"
	for _, mismatch := range []bool{true, false} {
		mismatchValue := mismatch
		_, err := repo.Create(ctx, &service.UsageLog{
			UserID: user.ID, APIKeyID: apiKey.ID, AccountID: account.ID,
			Model: "gpt-5.5", InputTokens: 1, OutputTokens: 1,
			UpstreamResponseModel: &responseModel, UpstreamModelMismatch: &mismatchValue,
			CreatedAt: now,
		})
		require.NoError(t, err)
	}

	start := now.Add(-time.Hour)
	end := now.Add(time.Hour)
	trueValue := true
	stats, err := repo.GetStatsWithFilters(ctx, usagestats.UsageLogFilters{
		UserID: user.ID, StartTime: &start, EndTime: &end, UpstreamModelMismatch: &trueValue,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), stats.TotalRequests)

	trend, err := repo.GetUsageTrendWithUsageFilters(ctx, start, end, "hour", usagestats.UsageLogFilters{
		UserID: user.ID, UpstreamModelMismatch: &trueValue,
	})
	require.NoError(t, err)
	require.Len(t, trend, 1)
	require.Equal(t, int64(1), trend[0].Requests)

	_, err = tx.ExecContext(ctx, "SET LOCAL enable_seqscan = off")
	require.NoError(t, err)
	rows, err := tx.QueryContext(ctx, `
EXPLAIN (COSTS OFF)
SELECT id
FROM usage_logs
WHERE upstream_model_mismatch IS TRUE
ORDER BY created_at DESC, id DESC
LIMIT 100
`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var planLines []string
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		planLines = append(planLines, line)
	}
	require.NoError(t, rows.Err())
	require.Contains(t, strings.Join(planLines, "\n"), usageLogsUpstreamModelMismatchIndex)
}

func TestUsageLog_GetStatsWithFilters_AggregatesAndEndpoints(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)

	user := mustCreateUser(t, client, &service.User{Email: "stats@test.com"})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-stats-1", Name: "k"})
	account := mustCreateAccount(t, client, &service.Account{Name: "acc-stats"})

	now := time.Now().UTC()
	inboundEndpoint := "/v1/messages"
	upstreamEndpoint := "/v1/responses"
	for i := 0; i < 3; i++ {
		_, err := repo.Create(ctx, &service.UsageLog{
			UserID: user.ID, APIKeyID: apiKey.ID, AccountID: account.ID,
			Model: "claude-3", InputTokens: 2, OutputTokens: 3,
			CacheCreationTokens: 4, CacheReadTokens: 5,
			TotalCost: 0.5, ActualCost: 0.4, CreatedAt: now,
			InboundEndpoint: &inboundEndpoint, UpstreamEndpoint: &upstreamEndpoint,
		})
		require.NoError(t, err)
	}

	start := now.Add(-1 * time.Hour)
	end := now.Add(1 * time.Hour)
	// 按本测试创建的 user 维度过滤:集成库为共享实例,其它用 testEntClient 的兄弟测试会留下
	// 已提交的 usage_log 行(含零 token 的失败请求),不限定 user 会把它们计入 TotalRequests。
	stats, err := repo.GetStatsWithFilters(ctx, usagestats.UsageLogFilters{UserID: user.ID, StartTime: &start, EndTime: &end})
	require.NoError(t, err)
	require.Equal(t, int64(3), stats.TotalRequests)
	require.Equal(t, int64(6), stats.TotalInputTokens)
	require.Equal(t, int64(9), stats.TotalOutputTokens)
	require.Equal(t, int64(27), stats.TotalCacheTokens)
	require.Equal(t, int64(12), stats.TotalCacheCreationTokens)
	require.Equal(t, int64(15), stats.TotalCacheReadTokens)
	require.InDelta(t, 1.2, stats.TotalActualCost, 1e-9)
	require.NotEmpty(t, stats.Endpoints)
	require.NotEmpty(t, stats.UpstreamEndpoints)
	require.NotEmpty(t, stats.EndpointPaths)
}

func TestUsageLog_GetStatsWithFilters_UsesNetRefundedCosts(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	user := mustCreateUser(t, client, &service.User{Email: "refund-stats@test.com"})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-refund-stats", Name: "k"})
	account := mustCreateAccount(t, client, &service.Account{Name: "refund-stats-account"})
	now := time.Now().UTC()
	refundedAt := now
	accountGrossA, accountGrossB := 10.0, 8.0
	for _, usage := range []*service.UsageLog{
		{UserID: user.ID, APIKeyID: apiKey.ID, AccountID: account.ID, Model: "video-a", TotalCost: 10, ActualCost: 10, AccountStatsCost: &accountGrossA, RefundedTotalCost: 4, RefundedCost: 4, RefundedAccountCost: 4, RefundedAt: &refundedAt, CreatedAt: now},
		{UserID: user.ID, APIKeyID: apiKey.ID, AccountID: account.ID, Model: "video-b", TotalCost: 8, ActualCost: 8, AccountStatsCost: &accountGrossB, RefundedTotalCost: 8, RefundedCost: 8, RefundedAccountCost: 8, RefundedAt: &refundedAt, CreatedAt: now},
	} {
		_, err := repo.Create(ctx, usage)
		require.NoError(t, err)
	}

	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	stats, err := repo.GetStatsWithFilters(ctx, usagestats.UsageLogFilters{UserID: user.ID, StartTime: &start, EndTime: &end})
	require.NoError(t, err)
	require.InDelta(t, 6, stats.TotalCost, 1e-9)
	require.InDelta(t, 6, stats.TotalActualCost, 1e-9)
	require.NotNil(t, stats.TotalAccountCost)
	require.InDelta(t, 6, *stats.TotalAccountCost, 1e-9)
	for _, endpoint := range stats.Endpoints {
		require.NotEqual(t, "video-b", endpoint.Endpoint)
	}
}

func TestUsageLog_RefundedCosts_PreaggregationReadersAndSuccessRanking(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	for _, table := range []string{
		"usage_dashboard_hourly_users", "usage_dashboard_daily_users",
		"usage_dashboard_hourly", "usage_dashboard_daily",
	} {
		_, err := tx.ExecContext(ctx, "DELETE FROM "+table)
		require.NoError(t, err)
	}

	partialUser := mustCreateUser(t, client, &service.User{Email: "refund-partial@test.com"})
	fullUser := mustCreateUser(t, client, &service.User{Email: "refund-full@test.com"})
	partialKey := mustCreateApiKey(t, client, &service.APIKey{UserID: partialUser.ID, Key: "sk-refund-partial", Name: "partial"})
	fullKey := mustCreateApiKey(t, client, &service.APIKey{UserID: fullUser.ID, Key: "sk-refund-full", Name: "full"})
	account := mustCreateAccount(t, client, &service.Account{Name: "refund-preaggregation-account"})
	dayStart := truncateToDayUTC(time.Now().UTC()).Add(-48 * time.Hour)
	hourStart := dayStart.Add(3 * time.Hour)
	accountGrossA, accountGrossB := 10.0, 8.0
	refundedAt := hourStart.Add(15 * time.Minute)
	for _, usage := range []*service.UsageLog{
		{UserID: partialUser.ID, APIKeyID: partialKey.ID, AccountID: account.ID, Model: "refund-partial", InputTokens: 1, TotalCost: 10, ActualCost: 10, AccountStatsCost: &accountGrossA, RefundedTotalCost: 4, RefundedCost: 4, RefundedAccountCost: 4, RefundedAt: &refundedAt, CreatedAt: hourStart.Add(5 * time.Minute)},
		{UserID: fullUser.ID, APIKeyID: fullKey.ID, AccountID: account.ID, Model: "refund-full", InputTokens: 1, TotalCost: 8, ActualCost: 8, AccountStatsCost: &accountGrossB, RefundedTotalCost: 8, RefundedCost: 8, RefundedAccountCost: 8, RefundedAt: &refundedAt, CreatedAt: hourStart.Add(10 * time.Minute)},
	} {
		_, err := repo.Create(ctx, usage)
		require.NoError(t, err)
	}

	aggRepo := newDashboardAggregationRepositoryWithSQL(tx)
	require.NoError(t, aggRepo.AggregateRange(ctx, hourStart, hourStart.Add(time.Hour)))
	assertPersisted := func(table, bucketColumn string, bucket any) {
		var grossTotal, refundTotal, grossActual, refundActual, grossAccount, refundAccount float64
		err := scanSingleRow(ctx, tx, `SELECT total_cost,refunded_total_cost,actual_cost,refunded_cost,account_cost,refunded_account_cost FROM `+table+` WHERE `+bucketColumn+`=$1`, []any{bucket},
			&grossTotal, &refundTotal, &grossActual, &refundActual, &grossAccount, &refundAccount)
		require.NoError(t, err)
		for _, got := range []float64{grossTotal, grossActual, grossAccount} {
			require.InDelta(t, 18, got, 1e-9)
		}
		for _, got := range []float64{refundTotal, refundActual, refundAccount} {
			require.InDelta(t, 12, got, 1e-9)
		}
	}
	assertPersisted("usage_dashboard_hourly", "bucket_start", hourStart)
	assertPersisted("usage_dashboard_daily", "bucket_date", dayStart)

	for _, granularity := range []string{"hour", "day"} {
		trend, err := repo.getUsageTrendFromAggregates(ctx, dayStart, dayStart.Add(24*time.Hour), granularity)
		require.NoError(t, err)
		require.Len(t, trend, 1)
		require.InDelta(t, 6, trend[0].Cost, 1e-9)
		require.InDelta(t, 6, trend[0].ActualCost, 1e-9)
	}
	dashboard := &DashboardStats{}
	require.NoError(t, repo.fillDashboardUsageStatsAggregated(ctx, dashboard, dayStart, hourStart))
	require.InDelta(t, 6, dashboard.TotalCost, 1e-9)
	require.InDelta(t, 6, dashboard.TotalActualCost, 1e-9)
	require.InDelta(t, 6, dashboard.TotalAccountCost, 1e-9)

	historicalUser := mustCreateUser(t, client, &service.User{Email: "refund-historical@test.com"})
	historicalKey := mustCreateApiKey(t, client, &service.APIKey{UserID: historicalUser.ID, Key: "sk-refund-historical", Name: "historical"})
	historicalAccountCost := 3.0
	historical := &service.UsageLog{UserID: historicalUser.ID, APIKeyID: historicalKey.ID, AccountID: account.ID, Model: "refund-historical", InputTokens: 1, TotalCost: 3, ActualCost: 3, AccountStatsCost: &historicalAccountCost, CreatedAt: hourStart.Add(2 * time.Hour)}
	_, err := repo.Create(ctx, historical)
	require.NoError(t, err)
	for _, column := range []string{"refunded_cost", "refunded_total_cost", "refunded_account_cost"} {
		_, err = tx.ExecContext(ctx, "ALTER TABLE usage_logs ALTER COLUMN "+column+" DROP NOT NULL")
		require.NoError(t, err)
	}
	_, err = tx.ExecContext(ctx, `UPDATE usage_logs SET refunded_cost=NULL,refunded_total_cost=NULL,refunded_account_cost=NULL WHERE id=$1`, historical.ID)
	require.NoError(t, err)
	start, end := dayStart, dayStart.Add(24*time.Hour)
	historicalStats, err := repo.GetStatsWithFilters(ctx, usagestats.UsageLogFilters{UserID: historicalUser.ID, StartTime: &start, EndTime: &end})
	require.NoError(t, err)
	require.InDelta(t, 3, historicalStats.TotalCost, 1e-9)
	require.InDelta(t, 3, historicalStats.TotalActualCost, 1e-9)
	require.InDelta(t, 3, *historicalStats.TotalAccountCost, 1e-9)

	batch, err := repo.GetBatchUserUsageStats(ctx, []int64{partialUser.ID, fullUser.ID, historicalUser.ID}, start, end)
	require.NoError(t, err)
	require.InDelta(t, 6, batch[partialUser.ID].TotalActualCost, 1e-9)
	require.Zero(t, batch[fullUser.ID].TotalActualCost)
	require.InDelta(t, 3, batch[historicalUser.ID].TotalActualCost, 1e-9)
	ranking, err := repo.GetUserSpendingRanking(ctx, start, end, 10)
	require.NoError(t, err)
	require.Len(t, ranking.Ranking, 2)
	require.Equal(t, int64(2), ranking.TotalRequests)
	for _, item := range ranking.Ranking {
		require.NotEqual(t, fullUser.ID, item.UserID)
	}
}
