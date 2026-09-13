//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type videoSettlementFixture struct {
	publicID       string
	userID         int64
	apiKeyID       int64
	groupID        int64
	accountID      int64
	subscriptionID int64
	usageLogID     int64
}

type refundReportingDashboardCache struct{ invalidations int }

func (c *refundReportingDashboardCache) GetDashboardStats(context.Context) (string, error) {
	return "", nil
}
func (c *refundReportingDashboardCache) SetDashboardStats(context.Context, string, time.Duration) error {
	return nil
}
func (c *refundReportingDashboardCache) DeleteDashboardStats(context.Context) error {
	c.invalidations++
	return nil
}

type legacyProbeDashboardRepository struct{ recomputes int }

func (r *legacyProbeDashboardRepository) AggregateRange(context.Context, time.Time, time.Time) error {
	return nil
}
func (r *legacyProbeDashboardRepository) RecomputeRange(context.Context, time.Time, time.Time) error {
	r.recomputes++
	return nil
}
func (*legacyProbeDashboardRepository) GetAggregationWatermark(context.Context) (time.Time, error) {
	return time.Time{}, nil
}
func (*legacyProbeDashboardRepository) UpdateAggregationWatermark(context.Context, time.Time) error {
	return nil
}
func (*legacyProbeDashboardRepository) CleanupAggregates(context.Context, time.Time, time.Time) error {
	return nil
}
func (*legacyProbeDashboardRepository) CleanupUsageLogs(context.Context, time.Time) error {
	return nil
}
func (*legacyProbeDashboardRepository) CleanupUsageBillingDedup(context.Context, time.Time) error {
	return nil
}
func (*legacyProbeDashboardRepository) EnsureUsageLogsPartitions(context.Context, time.Time) error {
	return nil
}

type legacyProbeAuditCandidate struct {
	taskID, usageLogID, userID, apiKeyID, accountID int64
	publicTaskID                                    string
	grossCost, customerCost, accountCost            float64
}

type legacyProbeAuditExclusion struct {
	taskID, userID, apiKeyID, accountID int64
	publicTaskID, reason                string
	usageLogID                          sql.NullInt64
}

func readLegacyProbeAuditResults(t *testing.T, ctx context.Context, conn *sql.Conn, auditSQL string) ([]legacyProbeAuditCandidate, []legacyProbeAuditExclusion) {
	t.Helper()
	rows, err := conn.QueryContext(ctx, auditSQL)
	require.NoError(t, err)
	defer rows.Close()

	var candidates []legacyProbeAuditCandidate
	var exclusions []legacyProbeAuditExclusion
	for {
		columns, columnsErr := rows.Columns()
		require.NoError(t, columnsErr)
		switch len(columns) {
		case 10:
			for rows.Next() {
				var resultSet string
				var candidate legacyProbeAuditCandidate
				require.NoError(t, rows.Scan(&resultSet, &candidate.taskID, &candidate.publicTaskID, &candidate.usageLogID, &candidate.userID, &candidate.apiKeyID, &candidate.accountID, &candidate.grossCost, &candidate.customerCost, &candidate.accountCost))
				require.Equal(t, "candidates", resultSet)
				candidates = append(candidates, candidate)
			}
		case 11:
			for rows.Next() {
				var resultSet string
				var exclusion legacyProbeAuditExclusion
				var grossCost, customerCost, accountCost sql.NullFloat64
				require.NoError(t, rows.Scan(&resultSet, &exclusion.taskID, &exclusion.publicTaskID, &exclusion.usageLogID, &exclusion.userID, &exclusion.apiKeyID, &exclusion.accountID, &grossCost, &customerCost, &accountCost, &exclusion.reason))
				require.Equal(t, "exclusions", resultSet)
				exclusions = append(exclusions, exclusion)
			}
		default:
			for rows.Next() {
			}
		}
		require.NoError(t, rows.Err())
		if !rows.NextResultSet() {
			break
		}
	}
	return candidates, exclusions
}

func newVideoSettlementFixture(t *testing.T, withSubscription, withUsageLog bool) videoSettlementFixture {
	t.Helper()
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("video-settlement-%s@example.com", uuid.NewString()), PasswordHash: "hash", Balance: 20})
	group := mustCreateGroup(t, client, &service.Group{Name: "video-settlement-" + uuid.NewString(), Platform: service.PlatformOpenAI})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, GroupID: &group.ID, Key: "sk-video-settlement-" + uuid.NewString(), Name: "video", Quota: 2})
	account := mustCreateAccount(t, client, &service.Account{Name: "video-settlement-" + uuid.NewString(), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Extra: map[string]any{"quota_limit": 100.0, "quota_daily_limit": 100.0, "quota_weekly_limit": 100.0}})

	f := videoSettlementFixture{publicID: "task_" + uuid.NewString(), userID: user.ID, apiKeyID: key.ID, groupID: group.ID, accountID: account.ID}
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO account_groups (account_id,group_id,priority,created_at) VALUES ($1,$2,1,NOW())`, account.ID, group.ID)
	require.NoError(t, err)
	var subscription any
	if withSubscription {
		s := mustCreateSubscription(t, client, &service.UserSubscription{UserID: user.ID, GroupID: group.ID})
		f.subscriptionID = s.ID
		subscription = s.ID
	}
	if withUsageLog {
		require.NoError(t, integrationDB.QueryRowContext(ctx, `
			INSERT INTO usage_logs (user_id, api_key_id, account_id, request_id, model, total_cost, actual_cost, account_stats_cost)
			VALUES ($1,$2,$3,$4,'video-test',0,0,0) RETURNING id`, user.ID, key.ID, account.ID, f.publicID).Scan(&f.usageLogID))
	}
	_, err = integrationDB.ExecContext(ctx, `
		INSERT INTO video_tasks
		(public_task_id, provider, platform, user_id, api_key_id, group_id, subscription_id, account_id,
		 requested_model, upstream_model, billing_model, status, prompt, request_hash, usage_log_id)
		VALUES ($1,'test','openai',$2,$3,$4,$5,$6,'video','video','video','submitting','prompt',$7,$8)`,
		f.publicID, f.userID, f.apiKeyID, f.groupID, subscription, f.accountID, uuid.NewString(), nullablePositiveID(f.usageLogID))
	require.NoError(t, err)
	return f
}

func nullablePositiveID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func settlementEffects(now time.Time) service.VideoTaskBillingEffects {
	return service.VideoTaskBillingEffects{
		BalanceCost:         3,
		APIKeyQuotaCost:     3,
		APIKeyRateLimitCost: 3,
		AccountQuotaCost:    3,
		PlatformQuotaCost:   3,
		AccountStatsCost:    2,
		ChargedAt:           now,
	}
}

func TestVideoTaskSettlementRepository_BalanceLifecycleExactlyOnce(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, true)
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	effects := settlementEffects(time.Now().UTC().Truncate(time.Microsecond))

	reserve := &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 3, AccountCostUSD: 1.75, PricingSnapshot: map[string]any{"unit": "second"}, Effects: effects}
	r1, err := repo.Reserve(ctx, reserve)
	require.NoError(t, err)
	require.True(t, r1.Applied)
	require.NotNil(t, r1.PostState.Balance)
	require.InDelta(t, 17, r1.PostState.Balance.Available, 1e-9)
	require.InDelta(t, 3, r1.PostState.Balance.Frozen, 1e-9)
	r2, err := repo.Reserve(ctx, reserve)
	require.NoError(t, err)
	require.False(t, r2.Applied)
	assertUserMoney(t, f.userID, 17, 3)
	var reservedEffects string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT effect_snapshot::text FROM video_task_settlements s JOIN video_tasks t ON t.id=s.video_task_id WHERE t.public_task_id=$1`, f.publicID).Scan(&reservedEffects))
	markVideoTaskProviderAccepted(t, f.publicID)

	c1, err := repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID, ActualCostUSD: 3})
	require.NoError(t, err)
	require.True(t, c1.Applied)
	require.NotNil(t, c1.Settlement.ChargedAt)
	require.NotNil(t, c1.PostState.APIKey)
	require.InDelta(t, 3, c1.PostState.APIKey.QuotaUsed, 1e-9)
	require.InDelta(t, 3, c1.PostState.APIKey.Usage5h, 1e-9)
	require.InDelta(t, 3, c1.PostState.APIKey.Usage1d, 1e-9)
	require.InDelta(t, 3, c1.PostState.APIKey.Usage7d, 1e-9)
	require.NotNil(t, c1.PostState.APIKey.Window5hStart)
	require.NotNil(t, c1.PostState.APIKey.Window1dStart)
	require.NotNil(t, c1.PostState.APIKey.Window7dStart)
	require.NotNil(t, c1.PostState.Account)
	require.InDelta(t, 3, c1.PostState.Account.TotalUsed, 1e-9)
	require.InDelta(t, 3, c1.PostState.Account.DailyUsed, 1e-9)
	require.InDelta(t, 3, c1.PostState.Account.WeeklyUsed, 1e-9)
	require.NotNil(t, c1.PostState.Account.DailyPeriod)
	require.NotNil(t, c1.PostState.Account.WeeklyPeriod)
	require.NotNil(t, c1.PostState.Platform)
	require.InDelta(t, 3, c1.PostState.Platform.DailyUsage, 1e-9)
	require.InDelta(t, 3, c1.PostState.Platform.WeeklyUsage, 1e-9)
	require.InDelta(t, 3, c1.PostState.Platform.MonthlyUsage, 1e-9)
	require.NotNil(t, c1.PostState.Platform.DailyPeriod)
	require.NotNil(t, c1.PostState.Platform.WeeklyPeriod)
	require.NotNil(t, c1.PostState.Platform.MonthlyPeriod)
	require.NotNil(t, c1.PostState.UsageLog)
	require.InDelta(t, 2, c1.PostState.UsageLog.AccountStatsCost, 1e-9)
	c2, err := repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID, ActualCostUSD: 3})
	require.NoError(t, err)
	require.False(t, c2.Applied)
	assertUserMoney(t, f.userID, 17, 0)
	var capturedEffects string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT effect_snapshot::text FROM video_task_settlements s JOIN video_tasks t ON t.id=s.video_task_id WHERE t.public_task_id=$1`, f.publicID).Scan(&capturedEffects))
	require.Equal(t, reservedEffects, capturedEffects, "capture must not mutate the reserved effect snapshot")

	var quota, rate, accountQuota, platformQuota, accountStats float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT quota_used, usage_5h FROM api_keys WHERE id=$1`, f.apiKeyID).Scan(&quota, &rate))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COALESCE((extra->>'quota_used')::numeric,0) FROM accounts WHERE id=$1`, f.accountID).Scan(&accountQuota))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM user_platform_quotas WHERE user_id=$1 AND platform='openai' AND deleted_at IS NULL`, f.userID).Scan(&platformQuota))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT account_stats_cost FROM usage_logs WHERE id=$1`, f.usageLogID).Scan(&accountStats))
	require.InDelta(t, 3, quota, 1e-9)
	require.InDelta(t, 3, rate, 1e-9)
	require.InDelta(t, 3, accountQuota, 1e-9)
	require.InDelta(t, 3, platformQuota, 1e-9)
	require.InDelta(t, 2, accountStats, 1e-9)

	_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='failed' WHERE public_task_id=$1`, f.publicID)
	require.NoError(t, err)
	refund, err := repo.RefundFailed(ctx, &service.VideoTaskSettlementRefundCommand{PublicTaskID: f.publicID, Reason: "upstream failed"})
	require.NoError(t, err)
	require.True(t, refund.Applied)
	require.NotNil(t, refund.Settlement.RefundedAt)
	require.NotNil(t, refund.PostState.Balance)
	require.InDelta(t, 20, refund.PostState.Balance.Available, 1e-9)
	require.NotNil(t, refund.PostState.Account)
	require.Zero(t, refund.PostState.Account.TotalUsed)
	refund2, err := repo.RefundFailed(ctx, &service.VideoTaskSettlementRefundCommand{PublicTaskID: f.publicID, Reason: "upstream failed"})
	require.NoError(t, err)
	require.False(t, refund2.Applied)
	assertUserMoney(t, f.userID, 20, 0)

	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT quota_used, usage_5h FROM api_keys WHERE id=$1`, f.apiKeyID).Scan(&quota, &rate))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COALESCE((extra->>'quota_used')::numeric,0) FROM accounts WHERE id=$1`, f.accountID).Scan(&accountQuota))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM user_platform_quotas WHERE user_id=$1 AND platform='openai' AND deleted_at IS NULL`, f.userID).Scan(&platformQuota))
	var refundedAccount float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT refunded_account_cost FROM usage_logs WHERE id=$1`, f.usageLogID).Scan(&refundedAccount))
	require.Zero(t, quota)
	require.Zero(t, rate)
	require.Zero(t, accountQuota)
	require.Zero(t, platformQuota)
	require.InDelta(t, 2, refundedAccount, 1e-9)
	require.Zero(t, refund.PostState.APIKey.Usage5h)
	require.Zero(t, refund.PostState.APIKey.Usage1d)
	require.Zero(t, refund.PostState.APIKey.Usage7d)
	require.Zero(t, refund.PostState.Platform.DailyUsage)
	require.Zero(t, refund.PostState.Platform.WeeklyUsage)
	require.Zero(t, refund.PostState.Platform.MonthlyUsage)
	require.Equal(t, c1.PostState.APIKey.Window5hStart, refund.PostState.APIKey.Window5hStart)
	require.Equal(t, c1.PostState.APIKey.Window1dStart, refund.PostState.APIKey.Window1dStart)
	require.Equal(t, c1.PostState.APIKey.Window7dStart, refund.PostState.APIKey.Window7dStart)
	require.Equal(t, c1.PostState.Platform.DailyPeriod, refund.PostState.Platform.DailyPeriod)
	require.Equal(t, c1.PostState.Platform.WeeklyPeriod, refund.PostState.Platform.WeeklyPeriod)
	require.Equal(t, c1.PostState.Platform.MonthlyPeriod, refund.PostState.Platform.MonthlyPeriod)
	var keyStatus string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM api_keys WHERE id=$1`, f.apiKeyID).Scan(&keyStatus))
	require.Equal(t, service.StatusAPIKeyActive, keyStatus)
	assertSettlementEventID(t, f.publicID, "reserve")
	assertSettlementEventID(t, f.publicID, "capture")
	assertSettlementEventID(t, f.publicID, "refund")
	var reportingJobs int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_task_refund_reporting_jobs j JOIN video_task_settlements s ON s.id=j.settlement_id WHERE s.charge_request_id=$1`, service.VideoTaskChargeRequestID(f.publicID)).Scan(&reportingJobs))
	require.Equal(t, 1, reportingJobs, "duplicate refund must not duplicate reporting work")

	cache := &refundReportingDashboardCache{}
	dashboard := service.NewDashboardAggregationService(NewDashboardAggregationRepository(integrationDB), nil, &config.Config{DashboardAgg: config.DashboardAggregationConfig{Enabled: true}})
	settlementService := service.NewVideoTaskSettlementService(repo, nil, nil, nil, nil, dashboard, cache)
	worker := service.NewVideoTaskSettlementReconciler(repo, settlementService)
	require.NoError(t, worker.ReconcileRefundReportingOnce(ctx, time.Now()))
	require.Equal(t, 1, cache.invalidations)
	var completedAt sql.NullTime
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT j.completed_at FROM video_task_refund_reporting_jobs j JOIN video_task_settlements s ON s.id=j.settlement_id WHERE s.charge_request_id=$1 AND s.state='refunded'`, service.VideoTaskChargeRequestID(f.publicID)).Scan(&completedAt))
	require.True(t, completedAt.Valid, "refunded reporting job must be claimable and durably completed")
}

func TestVideoTaskSettlementRepository_PerRequestCanonicalUsageSkipsReconciliation(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, false)
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	mode, inbound, upstream := string(service.BillingModePerRequest), "/v1/videos", "/v1/videos"
	quote := service.VideoTaskQuote{
		BillingMode: service.BillingModePerRequest, BillingModel: "seedance",
		Effective:    service.VideoTaskEffectiveParams{VideoCount: 1},
		UnitPriceUSD: 65, GrossCostUSD: 65, ActualCostUSD: 6.9225,
		AccountUnitPriceUSD: 65, AccountBaseCostUSD: 65, AccountCostUSD: 4.615,
		RateMultiplier: 0.1065, AccountRateMultiplier: 0.071,
	}
	pricingJSON, err := json.Marshal(quote)
	require.NoError(t, err)
	pricing := map[string]any{}
	require.NoError(t, json.Unmarshal(pricingJSON, &pricing))
	usage := &service.UsageLog{
		UserID: f.userID, APIKeyID: f.apiKeyID, AccountID: f.accountID,
		RequestID: service.VideoTaskChargeRequestID(f.publicID), Model: "seedance", RequestedModel: "seedance", GroupID: &f.groupID,
		BillingMode: &mode, BillingType: service.BillingTypeBalance, RequestType: service.RequestTypeSync,
		InboundEndpoint: &inbound, UpstreamEndpoint: &upstream, TotalCost: 65, ActualCost: 0,
		RateMultiplier: 0.1065, AccountRateMultiplier: testFloat64Ptr(0.071), AccountStatsCost: testFloat64Ptr(4.615), CreatedAt: time.Now().UTC(),
	}
	effects := service.VideoTaskBillingEffects{BalanceCost: 6.9225, APIKeyQuotaCost: 6.9225, APIKeyRateLimitCost: 6.9225, AccountQuotaCost: 4.615, PlatformQuotaCost: 6.9225, AccountStatsCost: 4.615}
	_, err = repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{
		PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance,
		GrossCostUSD: 65, ActualCostUSD: 6.9225, AccountCostUSD: 4.615, PricingSnapshot: pricing, Effects: effects,
		Admission: &service.VideoTaskSettlementAdmission{UsageLog: usage, UsageMetadata: map[string]any{"request_id": usage.RequestID}},
	})
	require.NoError(t, err)
	markVideoTaskProviderAccepted(t, f.publicID)
	_, err = repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID})
	require.NoError(t, err)

	var settlementUsageID, taskUsageID int64
	var billingMode string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT s.usage_log_id,t.usage_log_id,l.billing_mode
		FROM video_task_settlements s JOIN video_tasks t ON t.id=s.video_task_id JOIN usage_logs l ON l.id=s.usage_log_id
		WHERE t.public_task_id=$1`, f.publicID).Scan(&settlementUsageID, &taskUsageID, &billingMode))
	require.Equal(t, usage.ID, settlementUsageID)
	require.Equal(t, usage.ID, taskUsageID)
	require.Equal(t, string(service.BillingModePerRequest), billingMode)

	now := time.Now().UTC()
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_task_settlements SET next_reconcile_at=$1`, now.Add(24*time.Hour))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_task_settlements SET next_reconcile_at=$1 WHERE video_task_id=(SELECT id FROM video_tasks WHERE public_task_id=$2)`, now.Add(-time.Minute), f.publicID)
	require.NoError(t, err)
	claims, err := repo.ClaimDueReconciliation(ctx, now, 1, "per-request-canonical", time.Minute)
	require.NoError(t, err)
	require.Empty(t, claims)
}

func TestVideoTaskSettlementRepository_PerRequestBalanceLifecycleExactlyOnce(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, false)
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	mode, inbound, upstream := string(service.BillingModePerRequest), "/v1/videos", "/v1/videos"
	quote := service.VideoTaskQuote{
		BillingMode: service.BillingModePerRequest, BillingModel: "seedance",
		Effective:    service.VideoTaskEffectiveParams{VideoCount: 1},
		UnitPriceUSD: 65, GrossCostUSD: 65, ActualCostUSD: 6.9225,
		AccountUnitPriceUSD: 65, AccountBaseCostUSD: 65, AccountCostUSD: 4.615,
		RateMultiplier: 0.1065, AccountRateMultiplier: 0.071,
	}
	pricingJSON, err := json.Marshal(quote)
	require.NoError(t, err)
	pricing := map[string]any{}
	require.NoError(t, json.Unmarshal(pricingJSON, &pricing))
	usage := &service.UsageLog{
		UserID: f.userID, APIKeyID: f.apiKeyID, AccountID: f.accountID,
		RequestID: service.VideoTaskChargeRequestID(f.publicID), Model: "seedance", RequestedModel: "seedance", GroupID: &f.groupID,
		BillingMode: &mode, BillingType: service.BillingTypeBalance, RequestType: service.RequestTypeSync,
		InboundEndpoint: &inbound, UpstreamEndpoint: &upstream, TotalCost: 65, ActualCost: 0,
		RateMultiplier: 0.1065, AccountRateMultiplier: testFloat64Ptr(0.071), AccountStatsCost: testFloat64Ptr(4.615), CreatedAt: time.Now().UTC(),
	}
	effects := service.VideoTaskBillingEffects{BalanceCost: 6.9225, APIKeyQuotaCost: 6.9225, APIKeyRateLimitCost: 6.9225, AccountQuotaCost: 4.615, PlatformQuotaCost: 6.9225, AccountStatsCost: 4.615}
	_, err = repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{
		PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance,
		GrossCostUSD: 65, ActualCostUSD: 6.9225, AccountCostUSD: 4.615, PricingSnapshot: pricing, Effects: effects,
		Admission: &service.VideoTaskSettlementAdmission{UsageLog: usage, UsageMetadata: map[string]any{"request_id": usage.RequestID}},
	})
	require.NoError(t, err)
	assertUserMoney(t, f.userID, 13.0775, 6.9225)
	markVideoTaskProviderAccepted(t, f.publicID)
	captured, err := repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID})
	require.NoError(t, err)
	require.True(t, captured.Applied)
	assertUserMoney(t, f.userID, 13.0775, 0)

	var settlementUsageID, taskUsageID int64
	var billingMode string
	var actual, total, accountStats float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT s.usage_log_id,t.usage_log_id,l.billing_mode,l.actual_cost,l.total_cost,l.account_stats_cost
		FROM video_task_settlements s JOIN video_tasks t ON t.id=s.video_task_id JOIN usage_logs l ON l.id=s.usage_log_id
		WHERE t.public_task_id=$1`, f.publicID).Scan(&settlementUsageID, &taskUsageID, &billingMode, &actual, &total, &accountStats))
	require.Equal(t, usage.ID, settlementUsageID)
	require.Equal(t, usage.ID, taskUsageID)
	require.Equal(t, string(service.BillingModePerRequest), billingMode)
	require.InDelta(t, 6.9225, actual, 1e-9)
	require.InDelta(t, 65, total, 1e-9)
	require.InDelta(t, 4.615, accountStats, 1e-9)

	var quota, rate, accountQuota, platformQuota float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT quota_used,usage_5h FROM api_keys WHERE id=$1`, f.apiKeyID).Scan(&quota, &rate))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COALESCE((extra->>'quota_used')::numeric,0) FROM accounts WHERE id=$1`, f.accountID).Scan(&accountQuota))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM user_platform_quotas WHERE user_id=$1 AND platform=$2 AND deleted_at IS NULL`, f.userID, service.PlatformOpenAI).Scan(&platformQuota))
	require.InDelta(t, 6.9225, quota, 1e-9)
	require.InDelta(t, 6.9225, rate, 1e-9)
	require.InDelta(t, 4.615, accountQuota, 1e-9)
	require.InDelta(t, 6.9225, platformQuota, 1e-9)

	_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='failed' WHERE public_task_id=$1`, f.publicID)
	require.NoError(t, err)
	refunded, err := repo.RefundFailed(ctx, &service.VideoTaskSettlementRefundCommand{PublicTaskID: f.publicID, Reason: "upstream failed"})
	require.NoError(t, err)
	require.True(t, refunded.Applied)
	assertUserMoney(t, f.userID, 20, 0)

	var refundedCost, refundedTotal, refundedAccount float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT refunded_cost,refunded_total_cost,refunded_account_cost FROM usage_logs WHERE id=$1`, usage.ID).Scan(&refundedCost, &refundedTotal, &refundedAccount))
	require.InDelta(t, 6.9225, refundedCost, 1e-9)
	require.InDelta(t, 65, refundedTotal, 1e-9)
	require.InDelta(t, 4.615, refundedAccount, 1e-9)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT quota_used,usage_5h FROM api_keys WHERE id=$1`, f.apiKeyID).Scan(&quota, &rate))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COALESCE((extra->>'quota_used')::numeric,0) FROM accounts WHERE id=$1`, f.accountID).Scan(&accountQuota))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM user_platform_quotas WHERE user_id=$1 AND platform=$2 AND deleted_at IS NULL`, f.userID, service.PlatformOpenAI).Scan(&platformQuota))
	require.Zero(t, quota)
	require.Zero(t, rate)
	require.Zero(t, accountQuota)
	require.Zero(t, platformQuota)

	second, err := repo.RefundFailed(ctx, &service.VideoTaskSettlementRefundCommand{PublicTaskID: f.publicID, Reason: "upstream failed"})
	require.NoError(t, err)
	require.False(t, second.Applied)
	assertUserMoney(t, f.userID, 20, 0)
	assertSettlementEventCount(t, f.publicID, "refund", 1)
	var refundJobs int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_task_refund_reporting_jobs j JOIN video_task_settlements s ON s.id=j.settlement_id WHERE s.charge_request_id=$1`, service.VideoTaskChargeRequestID(f.publicID)).Scan(&refundJobs))
	require.Equal(t, 1, refundJobs)
}

func TestVideoTaskSettlementRepository_TransportPlatformCannotRedirectSettlementPlatform(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, false)
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)

	_, err := integrationDB.ExecContext(ctx, `UPDATE video_tasks SET platform=$1 WHERE public_task_id=$2`, service.VideoTaskPlatformOpenAIVideo, f.publicID)
	require.NoError(t, err)

	reserved, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{
		PublicTaskID: f.publicID,
		BillingType:  service.BillingTypeBalance,
		GrossCostUSD: 3,
		Effects:      settlementEffects(time.Now().UTC().Truncate(time.Microsecond)),
	})
	require.NoError(t, err)
	require.Equal(t, service.PlatformOpenAI, reserved.Settlement.Platform)
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET platform='changed_transport' WHERE public_task_id=$1`, f.publicID)
	require.NoError(t, err)
	idempotent, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 3, Effects: settlementEffects(time.Now().UTC())})
	require.NoError(t, err)
	require.False(t, idempotent.Applied)
	require.Equal(t, service.PlatformOpenAI, idempotent.Platform)
	require.Equal(t, service.PlatformOpenAI, idempotent.Settlement.Platform)

	markVideoTaskProviderAccepted(t, f.publicID)
	captured, err := repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID})
	require.NoError(t, err)
	require.Equal(t, service.PlatformOpenAI, captured.Settlement.Platform)
	var openAIUsage float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM user_platform_quotas WHERE user_id=$1 AND platform=$2 AND deleted_at IS NULL`, f.userID, service.PlatformOpenAI).Scan(&openAIUsage))
	require.InDelta(t, 3, openAIUsage, 1e-9)
	var transportQuotaRows int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_platform_quotas WHERE user_id=$1 AND platform=$2 AND deleted_at IS NULL`, f.userID, service.VideoTaskPlatformOpenAIVideo).Scan(&transportQuotaRows))
	require.Zero(t, transportQuotaRows)

	_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='failed' WHERE public_task_id=$1`, f.publicID)
	require.NoError(t, err)
	refunded, err := repo.RefundFailed(ctx, &service.VideoTaskSettlementRefundCommand{PublicTaskID: f.publicID, Reason: "upstream failed"})
	require.NoError(t, err)
	require.Equal(t, service.PlatformOpenAI, refunded.Settlement.Platform)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM user_platform_quotas WHERE user_id=$1 AND platform=$2 AND deleted_at IS NULL`, f.userID, service.PlatformOpenAI).Scan(&openAIUsage))
	require.Zero(t, openAIUsage)
}

func TestUsageLogRefundConstraintsRejectNegativeOverRefundAndInconsistentMetadata(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, true)
	_, err := integrationDB.ExecContext(ctx, `UPDATE usage_logs SET actual_cost=3,total_cost=4,account_stats_cost=2 WHERE id=$1`, f.usageLogID)
	require.NoError(t, err)

	for _, tt := range []struct {
		name  string
		query string
	}{
		{name: "negative", query: `UPDATE usage_logs SET refunded_cost=-1,refunded_at=NOW() WHERE id=$1`},
		{name: "customer overrefund", query: `UPDATE usage_logs SET refunded_cost=4,refunded_at=NOW() WHERE id=$1`},
		{name: "total overrefund", query: `UPDATE usage_logs SET refunded_total_cost=5,refunded_at=NOW() WHERE id=$1`},
		{name: "account overrefund", query: `UPDATE usage_logs SET refunded_account_cost=3,refunded_at=NOW() WHERE id=$1`},
		{name: "reason without timestamp", query: `UPDATE usage_logs SET refund_reason='invalid' WHERE id=$1`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, updateErr := integrationDB.ExecContext(ctx, tt.query, f.usageLogID)
			require.Error(t, updateErr)
			var customer, total, account float64
			var reason sql.NullString
			var refundedAt sql.NullTime
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT refunded_cost,refunded_total_cost,refunded_account_cost,refund_reason,refunded_at FROM usage_logs WHERE id=$1`, f.usageLogID).Scan(&customer, &total, &account, &reason, &refundedAt))
			require.Zero(t, customer)
			require.Zero(t, total)
			require.Zero(t, account)
			require.False(t, reason.Valid)
			require.False(t, refundedAt.Valid)
		})
	}

	_, err = integrationDB.ExecContext(ctx, `UPDATE usage_logs SET refunded_cost=3,refunded_total_cost=4,refunded_account_cost=2,refund_reason='valid',refunded_at=NOW() WHERE id=$1`, f.usageLogID)
	require.NoError(t, err)
}

func TestVideoTaskSettlementRepository_CaptureRequiresChargeablePersistedTaskState(t *testing.T) {
	for _, tt := range []struct {
		status  service.VideoTaskStatus
		allowed bool
	}{
		{status: service.VideoTaskStatusQueued, allowed: true},
		{status: service.VideoTaskStatusInProgress, allowed: true},
		{status: service.VideoTaskStatusCompleted, allowed: true},
		{status: service.VideoTaskStatusFailed},
		{status: service.VideoTaskStatusCancelled},
		{status: service.VideoTaskStatusExpired},
	} {
		t.Run(string(tt.status), func(t *testing.T) {
			ctx := context.Background()
			f := newVideoSettlementFixture(t, false, false)
			repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
			_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 2, ActualCostUSD: 2, Effects: service.VideoTaskBillingEffects{BalanceCost: 2}})
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status=$1 WHERE public_task_id=$2`, string(tt.status), f.publicID)
			require.NoError(t, err)

			result, err := repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID})
			if tt.allowed {
				require.NoError(t, err)
				require.True(t, result.Applied)
				assertSettlementEventCount(t, f.publicID, "capture", 1)
				return
			}
			require.ErrorIs(t, err, service.ErrVideoTaskSettlementStateConflict)
			assertSettlementEventCount(t, f.publicID, "capture", 0)
			assertUserMoney(t, f.userID, 18, 2)
		})
	}
}

func TestVideoTaskSettlementRepository_ReleaseFailedRequiresPersistedFailedState(t *testing.T) {
	for _, status := range []service.VideoTaskStatus{service.VideoTaskStatusQueued, service.VideoTaskStatusInProgress, service.VideoTaskStatusCompleted, service.VideoTaskStatusCancelled, service.VideoTaskStatusExpired} {
		t.Run(string(status), func(t *testing.T) {
			ctx := context.Background()
			f := newVideoSettlementFixture(t, false, false)
			repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
			_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 2, ActualCostUSD: 2, Effects: service.VideoTaskBillingEffects{BalanceCost: 2}})
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status=$1 WHERE public_task_id=$2`, string(status), f.publicID)
			require.NoError(t, err)

			result, err := repo.ReleaseFailed(ctx, &service.VideoTaskSettlementReleaseCommand{PublicTaskID: f.publicID})
			require.ErrorIs(t, err, service.ErrVideoTaskSettlementStateConflict)
			require.Nil(t, result)
			assertSettlementEventCount(t, f.publicID, "release", 0)
			assertUserMoney(t, f.userID, 18, 2)
		})
	}

	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, false)
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 2, ActualCostUSD: 2, Effects: service.VideoTaskBillingEffects{BalanceCost: 2}})
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='failed' WHERE public_task_id=$1`, f.publicID)
	require.NoError(t, err)
	result, err := repo.ReleaseFailed(ctx, &service.VideoTaskSettlementReleaseCommand{PublicTaskID: f.publicID})
	require.NoError(t, err)
	require.True(t, result.Applied)
	assertUserMoney(t, f.userID, 20, 0)
}

func TestVideoTaskSettlementRepository_ReconciliationLeaseTakeoverFencesStaleWorker(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, false)
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 1, ActualCostUSD: 1, Effects: service.VideoTaskBillingEffects{BalanceCost: 1}})
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_task_settlements SET next_reconcile_at=$1`, now.Add(24*time.Hour))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_task_settlements SET next_reconcile_at=$1 WHERE video_task_id=(SELECT id FROM video_tasks WHERE public_task_id=$2)`, now.Add(-time.Minute), f.publicID)
	require.NoError(t, err)
	claims, err := repo.ClaimDueReconciliation(ctx, now, 1, "old-token", time.Second)
	require.NoError(t, err)
	require.Len(t, claims, 1)

	takeoverAt := now.Add(2 * time.Second)
	claims, err = repo.ClaimDueReconciliation(ctx, takeoverAt, 1, "new-token", time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	_, renewed, err := repo.RenewSettlementClaim(ctx, f.publicID, "old-token", time.Minute)
	require.NoError(t, err)
	require.False(t, renewed)
	completed, err := repo.CompleteReconciliation(ctx, f.publicID, "old-token")
	require.NoError(t, err)
	require.False(t, completed)
	retried, err := repo.RetryReconciliation(ctx, f.publicID, "old-token", "stale", takeoverAt.Add(time.Minute))
	require.NoError(t, err)
	require.False(t, retried)
	_, renewed, err = repo.RenewSettlementClaim(ctx, f.publicID, "new-token", time.Minute)
	require.NoError(t, err)
	require.True(t, renewed)
}

func TestVideoTaskSettlementRepository_RenewSettlementClaimUsesDatabaseCurrentTime(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, false)
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 1, ActualCostUSD: 1, Effects: service.VideoTaskBillingEffects{BalanceCost: 1}})
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_task_settlements SET next_reconcile_at=$1 WHERE video_task_id=(SELECT id FROM video_tasks WHERE public_task_id=$2)`, now.Add(-time.Minute), f.publicID)
	require.NoError(t, err)
	claims, err := repo.ClaimDueReconciliation(ctx, now, 1, "renew-db-time", time.Second)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	var databaseBefore time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT NOW()`).Scan(&databaseBefore))
	_, err = integrationDB.ExecContext(ctx, `SELECT pg_sleep(0.05)`)
	require.NoError(t, err)
	lockedUntil, renewed, err := repo.RenewSettlementClaim(ctx, f.publicID, "renew-db-time", 2*time.Minute)
	require.NoError(t, err)
	require.True(t, renewed)
	require.True(t, lockedUntil.After(databaseBefore.Add(2*time.Minute)), "locked_until=%s database_before=%s", lockedUntil, databaseBefore)
}

func TestVideoTaskSettlementRepository_ExpiredClaimCannotCompleteOrRetryBeforeTakeover(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, false)
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 1, ActualCostUSD: 1, Effects: service.VideoTaskBillingEffects{BalanceCost: 1}})
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_task_settlements SET locked_by='expired-token',locked_until=NOW()-INTERVAL '1 second' WHERE video_task_id=(SELECT id FROM video_tasks WHERE public_task_id=$1)`, f.publicID)
	require.NoError(t, err)
	completed, err := repo.CompleteReconciliation(ctx, f.publicID, "expired-token")
	require.NoError(t, err)
	require.False(t, completed)
	retried, err := repo.RetryReconciliation(ctx, f.publicID, "expired-token", "must not persist", time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.False(t, retried)
}

func TestVideoTaskSettlementRepository_AdmissionOrphanRecoveryIsFencedAndDoesNotMutateFunding(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, false)
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	now := time.Now().UTC()
	_, err := integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='submitting',created_at=$2,request_metadata=jsonb_build_object(
		'request_metadata',jsonb_build_object('video_pricing_snapshot',jsonb_build_object('billing_mode','video'),'video_settlement_admission',jsonb_build_object('PublicTaskID',$1)))
		WHERE public_task_id=$1`, f.publicID, now.Add(-10*time.Minute))
	require.NoError(t, err)

	claims, err := repo.ClaimDueAdmissionOrphans(ctx, now, 2*time.Minute, 1, "worker-1", time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	applied, err := repo.FailAdmissionOrphan(ctx, f.publicID, "worker-1", service.VideoTaskAdmissionInterruptedCode, service.VideoTaskAdmissionInterruptedMessage)
	require.NoError(t, err)
	require.True(t, applied)

	var status, errorCode, errorMessage string
	var providerTaskID *string
	var usageLogID *int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status,error_code,error_message,upstream_task_id,usage_log_id FROM video_tasks WHERE public_task_id=$1`, f.publicID).Scan(&status, &errorCode, &errorMessage, &providerTaskID, &usageLogID))
	require.Equal(t, string(service.VideoTaskStatusFailed), status)
	require.Equal(t, service.VideoTaskAdmissionInterruptedCode, errorCode)
	require.Equal(t, service.VideoTaskAdmissionInterruptedMessage, errorMessage)
	require.Nil(t, providerTaskID)
	require.Nil(t, usageLogID)
	assertUserMoney(t, f.userID, 20, 0)
	var settlements, usages int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_task_settlements s JOIN video_tasks t ON t.id=s.video_task_id WHERE t.public_task_id=$1`, f.publicID).Scan(&settlements))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_logs WHERE request_id=$1`, service.VideoTaskChargeRequestID(f.publicID)).Scan(&usages))
	require.Zero(t, settlements)
	require.Zero(t, usages)
}

func TestVideoTaskSettlementRepository_AdmissionOrphanClaimsAreUniqueAndStaleTokensAreFenced(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, false)
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	now := time.Now().UTC()
	_, err := integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='submitting',created_at=$2,request_metadata=jsonb_build_object(
		'request_metadata',jsonb_build_object('video_pricing_snapshot',jsonb_build_object('billing_mode','video'),'video_settlement_admission',jsonb_build_object('PublicTaskID',$1)))
		WHERE public_task_id=$1`, f.publicID, now.Add(-10*time.Minute))
	require.NoError(t, err)

	var wg sync.WaitGroup
	claimCounts := make(chan int, 2)
	for _, token := range []string{"worker-a", "worker-b"} {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			claims, claimErr := repo.ClaimDueAdmissionOrphans(ctx, now, 2*time.Minute, 1, token, time.Second)
			require.NoError(t, claimErr)
			claimCounts <- len(claims)
		}(token)
	}
	wg.Wait()
	close(claimCounts)
	total := 0
	for count := range claimCounts {
		total += count
	}
	require.Equal(t, 1, total)

	claims, err := repo.ClaimDueAdmissionOrphans(ctx, now.Add(2*time.Second), 2*time.Minute, 1, "takeover", time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	for _, stale := range []string{"worker-a", "worker-b"} {
		applied, failErr := repo.FailAdmissionOrphan(ctx, f.publicID, stale, service.VideoTaskAdmissionInterruptedCode, service.VideoTaskAdmissionInterruptedMessage)
		require.NoError(t, failErr)
		require.False(t, applied)
	}
	applied, err := repo.FailAdmissionOrphan(ctx, f.publicID, "takeover", service.VideoTaskAdmissionInterruptedCode, service.VideoTaskAdmissionInterruptedMessage)
	require.NoError(t, err)
	require.True(t, applied)
}

func TestVideoTaskSettlementRepository_AdmissionOrphanClaimExcludesAcceptedAndLinkedTasks(t *testing.T) {
	ctx := context.Background()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	now := time.Now().UTC()
	for _, variant := range []string{"fresh", "accepted", "provider", "usage", "settlement"} {
		f := newVideoSettlementFixture(t, false, variant == "usage")
		metadata := `jsonb_build_object('request_metadata',jsonb_build_object('video_pricing_snapshot',jsonb_build_object('billing_mode','video'),'video_settlement_admission',jsonb_build_object('PublicTaskID',$1)))`
		if variant == "accepted" {
			metadata = metadata + ` || jsonb_build_object('reconciliation_accepted_snapshot',jsonb_build_object('provider_task_id','accepted'))`
		}
		createdAt := now.Add(-10 * time.Minute)
		if variant == "fresh" {
			createdAt = now
		}
		_, err := integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='submitting',created_at=$2,request_metadata=`+metadata+` WHERE public_task_id=$1`, f.publicID, createdAt)
		require.NoError(t, err)
		if variant == "provider" {
			_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET upstream_task_id='upstream' WHERE public_task_id=$1`, f.publicID)
			require.NoError(t, err)
		}
		if variant == "settlement" {
			_, err = repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 1, ActualCostUSD: 1, Effects: service.VideoTaskBillingEffects{BalanceCost: 1}})
			require.NoError(t, err)
		}
		claims, err := repo.ClaimDueAdmissionOrphans(ctx, now, 2*time.Minute, 10, "exclude-"+variant, time.Minute)
		require.NoError(t, err)
		for _, claim := range claims {
			require.NotEqual(t, f.publicID, claim.PublicTaskID, variant)
		}
	}
}

func TestVideoTaskSettlementRepository_ReserveRejectsNonPristineTaskWithoutEffects(t *testing.T) {
	ctx := context.Background()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	for _, variant := range []string{"failed", "cancelled", "accepted", "provider"} {
		t.Run(variant, func(t *testing.T) {
			f := newVideoSettlementFixture(t, false, false)
			status := string(service.VideoTaskStatusSubmitting)
			metadata := map[string]any{}
			var providerTaskID any
			switch variant {
			case "failed":
				status = string(service.VideoTaskStatusFailed)
			case "cancelled":
				status = string(service.VideoTaskStatusCancelled)
			case "accepted":
				metadata["reconciliation_accepted_snapshot"] = map[string]any{"provider_task_id": "accepted"}
			case "provider":
				providerTaskID = "upstream-task"
			}
			encodedMetadata, err := json.Marshal(metadata)
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status=$2,upstream_task_id=$3,request_metadata=$4 WHERE public_task_id=$1`, f.publicID, status, providerTaskID, encodedMetadata)
			require.NoError(t, err)
			mode := string(service.BillingModeVideo)
			usage := &service.UsageLog{UserID: f.userID, APIKeyID: f.apiKeyID, AccountID: f.accountID, RequestID: service.VideoTaskChargeRequestID(f.publicID), Model: "video", RequestedModel: "video", BillingMode: &mode, BillingType: service.BillingTypeBalance, TotalCost: 1, CreatedAt: time.Now()}

			result, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 1, ActualCostUSD: 1, Effects: service.VideoTaskBillingEffects{BalanceCost: 1}, Admission: &service.VideoTaskSettlementAdmission{UsageLog: usage}})

			require.Nil(t, result)
			require.ErrorIs(t, err, service.ErrVideoTaskSettlementStateConflict)
			assertUserMoney(t, f.userID, 20, 0)
			assertSettlementEventCount(t, f.publicID, "reserve", 0)
			var settlements, usages int
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_task_settlements s JOIN video_tasks t ON t.id=s.video_task_id WHERE t.public_task_id=$1`, f.publicID).Scan(&settlements))
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_logs WHERE request_id=$1`, service.VideoTaskChargeRequestID(f.publicID)).Scan(&usages))
			require.Zero(t, settlements)
			require.Zero(t, usages)
		})
	}
}

func TestVideoTaskSettlementRepository_ReserveWaitsForTerminalWinnerAndDoesNotCharge(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, false)
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	_, err := integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='submitting',request_metadata=jsonb_build_object(
		'request_metadata',jsonb_build_object('video_pricing_snapshot',jsonb_build_object('billing_mode','video'),'video_settlement_admission',jsonb_build_object('PublicTaskID',$1)))
		WHERE public_task_id=$1`, f.publicID)
	require.NoError(t, err)

	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `UPDATE video_tasks SET status='failed',provider_status='failed',error_code=$2::text,error_message=$3::text,
		result_metadata=jsonb_build_object('reconciliation_error_code',$2::text,'reconciliation_error_message',$3::text,'retriable',true,'provider_called',false),completed_at=NOW()
		WHERE public_task_id=$1`, f.publicID, service.VideoTaskAdmissionInterruptedCode, service.VideoTaskAdmissionInterruptedMessage)
	require.NoError(t, err)

	mode := string(service.BillingModeVideo)
	usage := &service.UsageLog{UserID: f.userID, APIKeyID: f.apiKeyID, AccountID: f.accountID, RequestID: service.VideoTaskChargeRequestID(f.publicID), Model: "video", RequestedModel: "video", BillingMode: &mode, BillingType: service.BillingTypeBalance, TotalCost: 1, CreatedAt: time.Now()}
	type reserveOutcome struct {
		result *service.VideoTaskSettlementResult
		err    error
	}
	done := make(chan reserveOutcome, 1)
	go func() {
		result, reserveErr := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 1, ActualCostUSD: 1, Effects: service.VideoTaskBillingEffects{BalanceCost: 1}, Admission: &service.VideoTaskSettlementAdmission{UsageLog: usage}})
		done <- reserveOutcome{result: result, err: reserveErr}
	}()
	select {
	case outcome := <-done:
		t.Fatalf("Reserve did not wait for task lock: result=%v err=%v", outcome.result, outcome.err)
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, tx.Commit())
	outcome := <-done
	require.Nil(t, outcome.result)
	require.ErrorIs(t, outcome.err, service.ErrVideoTaskSettlementStateConflict)
	assertUserMoney(t, f.userID, 20, 0)
	assertSettlementEventCount(t, f.publicID, "reserve", 0)
	var settlements, usages int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_task_settlements s JOIN video_tasks t ON t.id=s.video_task_id WHERE t.public_task_id=$1`, f.publicID).Scan(&settlements))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_logs WHERE request_id=$1`, service.VideoTaskChargeRequestID(f.publicID)).Scan(&usages))
	require.Zero(t, settlements)
	require.Zero(t, usages)
}

func TestVideoTaskSettlementRepository_FailSubmissionAtomicallyMarksFailedReleasesAndRemovesRevenue(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, true)
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 3, AccountCostUSD: 1, Effects: service.VideoTaskBillingEffects{BalanceCost: 3}})
	require.NoError(t, err)
	assertUserMoney(t, f.userID, 17, 3)

	result, err := repo.FailSubmission(ctx, &service.VideoTaskSettlementFailCommand{PublicTaskID: f.publicID, ErrorMessage: "provider rejected", Metadata: map[string]any{"provider_code": "bad_request"}})

	require.NoError(t, err)
	require.True(t, result.Applied)
	require.Equal(t, service.VideoTaskSettlementReleased, result.Settlement.State)
	assertUserMoney(t, f.userID, 20, 0)
	var status, message string
	var usageLogID *int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status,error_message,usage_log_id FROM video_tasks WHERE public_task_id=$1`, f.publicID).Scan(&status, &message, &usageLogID))
	require.Equal(t, string(service.VideoTaskStatusFailed), status)
	require.Equal(t, "provider rejected", message)
	require.Nil(t, usageLogID)
	var usageCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_logs WHERE id=$1`, f.usageLogID).Scan(&usageCount))
	require.Zero(t, usageCount)
}

func TestVideoTaskSettlementRepository_ReserveAdmissionRollbackIsAtomic(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, false)
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	requestID := service.VideoTaskChargeRequestID(f.publicID)
	mode := string(service.BillingModeVideo)
	usage := &service.UsageLog{UserID: f.userID, APIKeyID: f.apiKeyID, AccountID: f.accountID, RequestID: requestID, Model: "video", RequestedModel: "video", BillingMode: &mode, BillingType: service.BillingTypeBalance, TotalCost: 30, ActualCost: 0, CreatedAt: time.Now()}

	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 30, ActualCostUSD: 30, AccountCostUSD: 10, Effects: service.VideoTaskBillingEffects{BalanceCost: 30}, Admission: &service.VideoTaskSettlementAdmission{UsageLog: usage, UsageMetadata: map[string]any{"request_id": requestID}}})

	require.ErrorIs(t, err, service.ErrVideoTaskInsufficientBalance)
	assertUserMoney(t, f.userID, 20, 0)
	var usageCount, settlementCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_logs WHERE request_id=$1`, requestID).Scan(&usageCount))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_task_settlements s JOIN video_tasks t ON t.id=s.video_task_id WHERE t.public_task_id=$1`, f.publicID).Scan(&settlementCount))
	require.Zero(t, usageCount)
	require.Zero(t, settlementCount)
	var usageLogID *int64
	var usageMetadata sql.NullString
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT usage_log_id,usage_metadata FROM video_tasks WHERE public_task_id=$1`, f.publicID).Scan(&usageLogID, &usageMetadata))
	require.Nil(t, usageLogID)
	require.False(t, usageMetadata.Valid)
}

func TestVideoTaskSettlementRepository_ReserveRejectsConflictingUsageAdoption(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, false)
	other := newVideoSettlementFixture(t, false, false)
	requestID := service.VideoTaskChargeRequestID(f.publicID)
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO usage_logs (user_id,api_key_id,account_id,request_id,model,requested_model,total_cost,actual_cost,rate_multiplier,billing_type,request_type,video_count,video_resolution,video_duration_seconds,billing_mode,created_at) VALUES ($1,$2,$3,$4,'wrong','wrong',99,7,9,0,1,9,'4k',99,'video',NOW())`, f.userID, f.apiKeyID, other.accountID, requestID)
	require.NoError(t, err)
	mode := string(service.BillingModeVideo)
	resolution, duration := "720p", 5
	usage := &service.UsageLog{UserID: f.userID, APIKeyID: f.apiKeyID, AccountID: f.accountID, RequestID: requestID, Model: "video", RequestedModel: "video", TotalCost: 4, ActualCost: 0, RateMultiplier: 0.5, BillingType: service.BillingTypeBalance, RequestType: service.RequestTypeSync, VideoCount: 1, VideoResolution: &resolution, VideoDurationSeconds: &duration, BillingMode: &mode, CreatedAt: time.Now()}
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)

	_, err = repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 4, ActualCostUSD: 2, Effects: service.VideoTaskBillingEffects{BalanceCost: 2}, Admission: &service.VideoTaskSettlementAdmission{UsageLog: usage}})

	require.ErrorIs(t, err, service.ErrVideoTaskSettlementIntegrity)
	assertUserMoney(t, f.userID, 20, 0)
	var usageLogID *int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT usage_log_id FROM video_tasks WHERE public_task_id=$1`, f.publicID).Scan(&usageLogID))
	require.Nil(t, usageLogID)
}

func TestVideoTaskSettlementRepository_ReserveAndCaptureUsePlannedActualCost(t *testing.T) {
	for _, tt := range []struct {
		name          string
		gross, actual float64
	}{{"multiplier below one", 10, 5}, {"multiplier above one", 10, 15}} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			f := newVideoSettlementFixture(t, false, false)
			repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
			_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: tt.gross, ActualCostUSD: tt.actual, AccountCostUSD: 1, Effects: service.VideoTaskBillingEffects{BalanceCost: tt.actual}})
			require.NoError(t, err)
			assertUserMoney(t, f.userID, 20-tt.actual, tt.actual)
			markVideoTaskProviderAccepted(t, f.publicID)

			captured, err := repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID})
			require.NoError(t, err)
			require.InDelta(t, tt.actual, captured.Settlement.ActualCostUSD, 1e-9)
			assertUserMoney(t, f.userID, 20-tt.actual, 0)
		})
	}
}

func TestVideoTaskSettlementRepository_CaptureIgnoresTamperedTaskQuote(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, false)
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 4, ActualCostUSD: 2, AccountCostUSD: 1, PricingSnapshot: map[string]any{"actual_cost_usd": 2}, Effects: service.VideoTaskBillingEffects{BalanceCost: 2, APIKeyQuotaCost: 2}})
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET request_metadata=jsonb_build_object('video_pricing_snapshot',jsonb_build_object('actual_cost_usd',99)) WHERE public_task_id=$1`, f.publicID)
	require.NoError(t, err)
	markVideoTaskProviderAccepted(t, f.publicID)

	captured, err := repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID})

	require.NoError(t, err)
	require.InDelta(t, 2, captured.Settlement.ActualCostUSD, 1e-9)
	assertUserMoney(t, f.userID, 18, 0)
}

func TestVideoTaskSettlementRepository_CaptureRejectsTamperedReserveLedgerAndLeavesHoldReleasable(t *testing.T) {
	mutations := []struct {
		name string
		sql  string
	}{
		{"pricing actual", `UPDATE video_task_settlements SET pricing_snapshot=jsonb_set(pricing_snapshot,'{actual_cost_usd}','9') WHERE video_task_id=(SELECT id FROM video_tasks WHERE public_task_id=$1)`},
		{"pricing gross", `UPDATE video_task_settlements SET pricing_snapshot=jsonb_set(pricing_snapshot,'{gross_cost_usd}','9') WHERE video_task_id=(SELECT id FROM video_tasks WHERE public_task_id=$1)`},
		{"pricing account cost", `UPDATE video_task_settlements SET pricing_snapshot=jsonb_set(pricing_snapshot,'{account_cost_usd}','9') WHERE video_task_id=(SELECT id FROM video_tasks WHERE public_task_id=$1)`},
		{"effect snapshot", `UPDATE video_task_settlements SET effect_snapshot=jsonb_set(effect_snapshot,'{api_key_quota_cost}','9') WHERE video_task_id=(SELECT id FROM video_tasks WHERE public_task_id=$1)`},
		{"reserve amount", `UPDATE video_task_settlement_events SET amount_usd=9 WHERE settlement_id=(SELECT s.id FROM video_task_settlements s JOIN video_tasks t ON t.id=s.video_task_id WHERE t.public_task_id=$1) AND event_type='reserve'`},
		{"reserve table event id", `UPDATE video_task_settlement_events SET event_id='video:tampered:reserve' WHERE settlement_id=(SELECT s.id FROM video_task_settlements s JOIN video_tasks t ON t.id=s.video_task_id WHERE t.public_task_id=$1) AND event_type='reserve'`},
		{"reserve metadata event id", `UPDATE video_task_settlement_events SET metadata=jsonb_set(metadata,'{event_id}','"video:tampered:reserve"') WHERE settlement_id=(SELECT s.id FROM video_task_settlements s JOIN video_tasks t ON t.id=s.video_task_id WHERE t.public_task_id=$1) AND event_type='reserve'`},
	}
	for _, tt := range mutations {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			f := newVideoSettlementFixture(t, false, false)
			repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
			pricing := map[string]any{"billing_mode": string(service.BillingModeVideo), "billing_model": "video", "effective": map[string]any{"seconds": 5, "resolution": "720p", "video_count": 1}, "unit_price_usd": 0.8, "gross_cost_usd": 4.0, "actual_cost_usd": 2.0, "account_cost_usd": 1.0, "rate_multiplier": 0.5, "account_rate_multiplier": 0.25}
			_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 4, ActualCostUSD: 2, AccountCostUSD: 1, PricingSnapshot: pricing, Effects: service.VideoTaskBillingEffects{BalanceCost: 2, APIKeyQuotaCost: 2}})
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, tt.sql, f.publicID)
			require.NoError(t, err)
			markVideoTaskProviderAccepted(t, f.publicID)

			_, err = repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID})
			require.ErrorIs(t, err, service.ErrVideoTaskSettlementIntegrity)
			assertSettlementEventCount(t, f.publicID, "capture", 0)
			assertUserMoney(t, f.userID, 18, 2)

			released, err := repo.Release(ctx, &service.VideoTaskSettlementReleaseCommand{PublicTaskID: f.publicID})
			require.NoError(t, err)
			require.True(t, released.Applied)
			assertUserMoney(t, f.userID, 20, 0)
		})
	}
}

func TestVideoTaskSettlementRepository_QuotePrecisionContract(t *testing.T) {
	quote := map[string]any{
		"billing_mode": string(service.BillingModeVideo), "billing_model": "video",
		"effective":      map[string]any{"seconds": 3, "resolution": "720p", "video_count": 1},
		"unit_price_usd": 0.1234567891, "gross_cost_usd": 0.3703703673,
		"actual_cost_usd": 0.12345679, "rate_multiplier": 0.3333333333,
		"account_unit_price_usd": 0.1234567891, "account_base_cost_usd": 0.3703703673,
		"account_cost_usd": 0.45724736, "account_rate_multiplier": 1.234567891,
	}

	t.Run("settles and refunds applied precision", func(t *testing.T) {
		ctx := context.Background()
		f := newVideoSettlementFixture(t, false, false)
		repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
		reserved, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{
			PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance,
			GrossCostUSD: 0.3703703673, ActualCostUSD: 0.12345679, AccountCostUSD: 0.45724736,
			PricingSnapshot: quote, Effects: service.VideoTaskBillingEffects{BalanceCost: 0.12345679, AccountStatsCost: 0.45724736},
		})
		require.NoError(t, err)
		require.Equal(t, 0.3703703673, reserved.Settlement.GrossCostUSD)
		markVideoTaskProviderAccepted(t, f.publicID)
		captured, err := repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID, ActualCostUSD: 0.12345679})
		require.NoError(t, err)
		require.Equal(t, 0.12345679, captured.Settlement.ActualCostUSD)
		_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='failed' WHERE public_task_id=$1`, f.publicID)
		require.NoError(t, err)
		refunded, err := repo.RefundFailed(ctx, &service.VideoTaskSettlementRefundCommand{PublicTaskID: f.publicID})
		require.NoError(t, err)
		require.Equal(t, 0.12345679, refunded.Settlement.RefundedCostUSD)
		assertUserMoney(t, f.userID, 20, 0)
	})

	mutations := []struct {
		name, path, value string
	}{
		{name: "gross tenth digit", path: "gross_cost_usd", value: "0.3703703674"},
		{name: "actual eighth digit", path: "actual_cost_usd", value: "0.12345680"},
		{name: "account base tenth digit", path: "account_base_cost_usd", value: "0.3703703674"},
		{name: "account final eighth digit", path: "account_cost_usd", value: "0.45724737"},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			ctx := context.Background()
			f := newVideoSettlementFixture(t, false, false)
			repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
			_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{
				PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance,
				GrossCostUSD: 0.3703703673, ActualCostUSD: 0.12345679, AccountCostUSD: 0.45724736,
				PricingSnapshot: quote, Effects: service.VideoTaskBillingEffects{BalanceCost: 0.12345679},
			})
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `UPDATE video_task_settlements SET pricing_snapshot=jsonb_set(pricing_snapshot,$2::text[],$3::jsonb) WHERE video_task_id=(SELECT id FROM video_tasks WHERE public_task_id=$1)`, f.publicID, "{"+mutation.path+"}", mutation.value)
			require.NoError(t, err)
			markVideoTaskProviderAccepted(t, f.publicID)
			_, err = repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID})
			require.ErrorIs(t, err, service.ErrVideoTaskSettlementIntegrity)
			assertSettlementEventCount(t, f.publicID, "capture", 0)
		})
	}
}

func TestVideoTaskSettlementRepository_ReleaseUsesAuthoritativeHoldAfterFundingEffectTamper(t *testing.T) {
	t.Run("balance", func(t *testing.T) {
		ctx := context.Background()
		f := newVideoSettlementFixture(t, false, false)
		repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
		_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 4, ActualCostUSD: 2, Effects: service.VideoTaskBillingEffects{BalanceCost: 2}})
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, `UPDATE video_task_settlements SET effect_snapshot=jsonb_set(effect_snapshot,'{balance_cost}','9') WHERE video_task_id=(SELECT id FROM video_tasks WHERE public_task_id=$1)`, f.publicID)
		require.NoError(t, err)
		markVideoTaskProviderAccepted(t, f.publicID)
		_, err = repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID})
		require.ErrorIs(t, err, service.ErrVideoTaskSettlementIntegrity)

		released, err := repo.Release(ctx, &service.VideoTaskSettlementReleaseCommand{PublicTaskID: f.publicID})
		require.NoError(t, err)
		require.True(t, released.Applied)
		require.True(t, released.IntegrityDrift)
		assertUserMoney(t, f.userID, 20, 0)
	})

	t.Run("subscription", func(t *testing.T) {
		ctx := context.Background()
		f := newVideoSettlementFixture(t, true, false)
		repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
		_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeSubscription, GrossCostUSD: 4, ActualCostUSD: 1.5, Effects: service.VideoTaskBillingEffects{SubscriptionCost: 1.5}})
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET daily_usage_usd=daily_usage_usd+5,weekly_usage_usd=weekly_usage_usd+5,monthly_usage_usd=monthly_usage_usd+5 WHERE id=$1`, f.subscriptionID)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, `UPDATE video_task_settlements SET effect_snapshot=jsonb_set(effect_snapshot,'{subscription_cost}','9') WHERE video_task_id=(SELECT id FROM video_tasks WHERE public_task_id=$1)`, f.publicID)
		require.NoError(t, err)
		markVideoTaskProviderAccepted(t, f.publicID)
		_, err = repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID})
		require.ErrorIs(t, err, service.ErrVideoTaskSettlementIntegrity)

		released, err := repo.Release(ctx, &service.VideoTaskSettlementReleaseCommand{PublicTaskID: f.publicID})
		require.NoError(t, err)
		require.True(t, released.Applied)
		require.True(t, released.IntegrityDrift)
		assertSubscriptionUsage(t, f.subscriptionID, 5)
	})
}

func TestVideoTaskSettlementRepository_GetByPublicTaskIDFullRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, true)
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	chargedAt := time.Now().UTC().Truncate(time.Microsecond)
	pricing := map[string]any{"model": "video-test", "seconds": float64(8)}
	effects := settlementEffects(chargedAt)
	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{
		PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance,
		GrossCostUSD: 3, AccountCostUSD: 1.75, PricingSnapshot: pricing, Effects: effects,
	})
	require.NoError(t, err)

	snapshot, err := repo.GetByPublicTaskID(ctx, f.publicID)
	require.NoError(t, err)
	require.Equal(t, "video:"+f.publicID+":charge", snapshot.ChargeRequestID)
	require.InDelta(t, 1.75, snapshot.AccountCostUSD, 1e-9)
	require.Equal(t, "video-test", snapshot.PricingSnapshot["model"])
	require.InDelta(t, 3, snapshot.Effects.APIKeyQuotaCost, 1e-9)
	require.NotNil(t, snapshot.ReservedAt)
	require.Nil(t, snapshot.ChargedAt)
	require.Nil(t, snapshot.ReleasedAt)
	require.Nil(t, snapshot.RefundedAt)
	require.False(t, snapshot.CreatedAt.IsZero())
	require.False(t, snapshot.UpdatedAt.IsZero())
	markVideoTaskProviderAccepted(t, f.publicID)

	_, err = repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID, ActualCostUSD: 3})
	require.NoError(t, err)
	snapshot, err = repo.GetByPublicTaskID(ctx, f.publicID)
	require.NoError(t, err)
	require.NotNil(t, snapshot.ChargedAt)
	require.True(t, snapshot.Effects.ChargedAt.IsZero(), "reserve snapshot remains a quote; applied charge time is stored on capture event")
}

func TestVideoTaskSettlementRepository_RepairChargedUsageRecreatesMissingRowAndLinks(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, false)
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	mode, inbound, upstream := string(service.BillingModeVideo), "/v1/videos", "/v1/videos"
	resolution, duration := "720p", 8
	usage := &service.UsageLog{
		UserID: f.userID, APIKeyID: f.apiKeyID, AccountID: f.accountID,
		RequestID: service.VideoTaskChargeRequestID(f.publicID), Model: "video", RequestedModel: "video", GroupID: &f.groupID,
		BillingMode: &mode, BillingType: service.BillingTypeBalance, RequestType: service.RequestTypeSync,
		InboundEndpoint: &inbound, UpstreamEndpoint: &upstream, VideoCount: 1, VideoResolution: &resolution,
		VideoDurationSeconds: &duration, TotalCost: 3, ActualCost: 0, RateMultiplier: 1,
		AccountRateMultiplier: testFloat64Ptr(1), AccountStatsCost: testFloat64Ptr(2), CreatedAt: time.Now().UTC(),
	}
	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{
		PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 3, ActualCostUSD: 3, AccountCostUSD: 2,
		Effects:   service.VideoTaskBillingEffects{BalanceCost: 3, AccountStatsCost: 2},
		Admission: &service.VideoTaskSettlementAdmission{UsageLog: usage, UsageMetadata: map[string]any{"request_id": usage.RequestID}},
	})
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='queued' WHERE public_task_id=$1`, f.publicID)
	require.NoError(t, err)
	_, err = repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID})
	require.NoError(t, err)
	originalID := usage.ID
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET usage_log_id=NULL,billed_usd=0,platform='changed_transport' WHERE public_task_id=$1`, f.publicID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_task_settlements SET usage_log_id=NULL WHERE video_task_id=(SELECT id FROM video_tasks WHERE public_task_id=$1)`, f.publicID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `DELETE FROM usage_logs WHERE id=$1`, originalID)
	require.NoError(t, err)

	result, err := repo.RepairChargedUsage(ctx, f.publicID)
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.Equal(t, service.PlatformOpenAI, result.Platform)
	var settlementUsageID, taskUsageID int64
	var billed float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT s.usage_log_id,t.usage_log_id,t.billed_usd FROM video_task_settlements s JOIN video_tasks t ON t.id=s.video_task_id WHERE t.public_task_id=$1`, f.publicID).Scan(&settlementUsageID, &taskUsageID, &billed))
	require.Equal(t, settlementUsageID, taskUsageID)
	require.NotZero(t, taskUsageID)
	require.InDelta(t, 3, billed, 1e-9)
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_logs WHERE request_id=$1 AND api_key_id=$2`, usage.RequestID, f.apiKeyID).Scan(&count))
	require.Equal(t, 1, count)
}

func testFloat64Ptr(value float64) *float64 { return &value }

type chargedUsageRepairFixture struct {
	videoSettlementFixture
	repo    service.VideoTaskSettlementRepository
	usage   *service.UsageLog
	usageID int64
}

func newChargedUsageRepairFixture(t *testing.T, multipliers ...float64) chargedUsageRepairFixture {
	t.Helper()
	rateMultiplier, accountRateMultiplier := 1.0, 1.0
	if len(multipliers) != 0 {
		if len(multipliers) != 2 {
			t.Fatalf("newChargedUsageRepairFixture requires both rate multipliers")
		}
		rateMultiplier, accountRateMultiplier = multipliers[0], multipliers[1]
	}
	return newChargedUsageRepairFixtureWithRatesAndPricing(t, rateMultiplier, accountRateMultiplier, nil)
}

func newChargedUsageRepairFixtureWithPricing(t *testing.T, pricing map[string]any) chargedUsageRepairFixture {
	t.Helper()
	return newChargedUsageRepairFixtureWithRatesAndPricing(t, 1, 1, pricing)
}

func newChargedUsageRepairFixtureWithRatesAndPricing(t *testing.T, rateMultiplier, accountRateMultiplier float64, pricing map[string]any) chargedUsageRepairFixture {
	t.Helper()
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, false)
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	mode, inbound, upstream := string(service.BillingModeVideo), "/v1/videos", "/v1/videos"
	resolution, duration := "720p", 8
	usage := &service.UsageLog{UserID: f.userID, APIKeyID: f.apiKeyID, AccountID: f.accountID, RequestID: service.VideoTaskChargeRequestID(f.publicID), Model: "video", RequestedModel: "video", GroupID: &f.groupID, BillingMode: &mode, BillingType: service.BillingTypeBalance, RequestType: service.RequestTypeSync, InboundEndpoint: &inbound, UpstreamEndpoint: &upstream, VideoCount: 1, VideoResolution: &resolution, VideoDurationSeconds: &duration, TotalCost: 3, RateMultiplier: rateMultiplier, AccountRateMultiplier: &accountRateMultiplier, AccountStatsCost: testFloat64Ptr(2), CreatedAt: time.Now().UTC()}
	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 3, ActualCostUSD: 3, AccountCostUSD: 2, PricingSnapshot: pricing, Effects: service.VideoTaskBillingEffects{BalanceCost: 3, AccountStatsCost: 2}, Admission: &service.VideoTaskSettlementAdmission{UsageLog: usage, UsageMetadata: map[string]any{"request_id": usage.RequestID}}})
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='queued' WHERE public_task_id=$1`, f.publicID)
	require.NoError(t, err)
	_, err = repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID})
	require.NoError(t, err)
	return chargedUsageRepairFixture{videoSettlementFixture: f, repo: repo, usage: usage, usageID: usage.ID}
}

func (f chargedUsageRepairFixture) clearTaskLink(t *testing.T) {
	t.Helper()
	_, err := integrationDB.ExecContext(context.Background(), `UPDATE video_tasks SET usage_log_id=NULL,billed_usd=0 WHERE public_task_id=$1`, f.publicID)
	require.NoError(t, err)
}

func (f chargedUsageRepairFixture) clearSettlementLink(t *testing.T) {
	t.Helper()
	_, err := integrationDB.ExecContext(context.Background(), `UPDATE video_task_settlements SET usage_log_id=NULL WHERE video_task_id=(SELECT id FROM video_tasks WHERE public_task_id=$1)`, f.publicID)
	require.NoError(t, err)
}

func assertChargedUsageLinks(t *testing.T, publicID string, wantID int64) {
	t.Helper()
	var settlementID, taskID int64
	var billed float64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT s.usage_log_id,t.usage_log_id,t.billed_usd FROM video_task_settlements s JOIN video_tasks t ON t.id=s.video_task_id WHERE t.public_task_id=$1`, publicID).Scan(&settlementID, &taskID, &billed))
	require.Equal(t, wantID, settlementID)
	require.Equal(t, wantID, taskID)
	require.InDelta(t, 3, billed, 1e-9)
}

func setOnlyVideoTaskSettlementDue(t *testing.T, publicID string, now time.Time) {
	t.Helper()
	ctx := context.Background()
	_, err := integrationDB.ExecContext(ctx, `UPDATE video_task_settlements SET next_reconcile_at=$1`, now.Add(24*time.Hour))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_task_settlements SET next_reconcile_at=$1 WHERE video_task_id=(SELECT id FROM video_tasks WHERE public_task_id=$2)`, now.Add(-time.Minute), publicID)
	require.NoError(t, err)
}

func TestVideoTaskSettlementRepository_RepairChargedUsageAdoptsValidDeterministicRow(t *testing.T) {
	f := newChargedUsageRepairFixture(t)
	f.clearTaskLink(t)
	f.clearSettlementLink(t)
	result, err := f.repo.RepairChargedUsage(context.Background(), f.publicID)
	require.NoError(t, err)
	require.True(t, result.Applied)
	assertChargedUsageLinks(t, f.publicID, f.usageID)
}

func TestVideoTaskSettlementRepository_RepairChargedUsageRejectsMismatchedDeterministicRow(t *testing.T) {
	f := newChargedUsageRepairFixture(t)
	f.clearTaskLink(t)
	f.clearSettlementLink(t)
	_, err := integrationDB.ExecContext(context.Background(), `UPDATE usage_logs SET model='unrelated' WHERE id=$1`, f.usageID)
	require.NoError(t, err)
	_, err = f.repo.RepairChargedUsage(context.Background(), f.publicID)
	require.ErrorIs(t, err, service.ErrVideoTaskSettlementIntegrity)
	var settlementID, taskID sql.NullInt64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT s.usage_log_id,t.usage_log_id FROM video_task_settlements s JOIN video_tasks t ON t.id=s.video_task_id WHERE t.public_task_id=$1`, f.publicID).Scan(&settlementID, &taskID))
	require.False(t, settlementID.Valid)
	require.False(t, taskID.Valid)
}

func TestVideoTaskSettlementRepository_RepairChargedUsageRejectsEconomicOrRoutingMismatch(t *testing.T) {
	for _, tt := range []struct {
		name   string
		column string
		value  any
	}{
		{name: "account stats cost", column: "account_stats_cost", value: 99},
		{name: "inbound endpoint", column: "inbound_endpoint", value: "/unrelated"},
		{name: "upstream endpoint", column: "upstream_endpoint", value: "/unrelated"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newChargedUsageRepairFixture(t)
			f.clearTaskLink(t)
			f.clearSettlementLink(t)
			_, err := integrationDB.ExecContext(context.Background(), fmt.Sprintf(`UPDATE usage_logs SET %s=$1 WHERE id=$2`, tt.column), tt.value, f.usageID)
			require.NoError(t, err)
			_, err = f.repo.RepairChargedUsage(context.Background(), f.publicID)
			require.ErrorIs(t, err, service.ErrVideoTaskSettlementIntegrity)
		})
	}
}

func TestVideoTaskSettlementRepository_ClaimDueReconciliationDetectsCorruptLinkedUsage(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*testing.T, chargedUsageRepairFixture)
	}{
		{name: "identity", mutate: func(t *testing.T, f chargedUsageRepairFixture) {
			other := newVideoSettlementFixture(t, false, false)
			_, err := integrationDB.ExecContext(context.Background(), `UPDATE usage_logs SET account_id=$1 WHERE id=$2`, other.accountID, f.usageID)
			require.NoError(t, err)
		}},
		{name: "economics", mutate: func(t *testing.T, f chargedUsageRepairFixture) {
			_, err := integrationDB.ExecContext(context.Background(), `UPDATE usage_logs SET actual_cost=99 WHERE id=$1`, f.usageID)
			require.NoError(t, err)
		}},
		{name: "routing", mutate: func(t *testing.T, f chargedUsageRepairFixture) {
			_, err := integrationDB.ExecContext(context.Background(), `UPDATE usage_logs SET upstream_endpoint='/wrong' WHERE id=$1`, f.usageID)
			require.NoError(t, err)
		}},
		{name: "refund", mutate: func(t *testing.T, f chargedUsageRepairFixture) {
			_, err := integrationDB.ExecContext(context.Background(), `UPDATE usage_logs SET refunded_cost=1,refunded_total_cost=1,refunded_account_cost=1,refunded_at=NOW() WHERE id=$1`, f.usageID)
			require.NoError(t, err)
		}},
		{name: "request type", mutate: func(t *testing.T, f chargedUsageRepairFixture) {
			_, err := integrationDB.ExecContext(context.Background(), `UPDATE usage_logs SET request_type=2 WHERE id=$1`, f.usageID)
			require.NoError(t, err)
		}},
		{name: "rate multiplier", mutate: func(t *testing.T, f chargedUsageRepairFixture) {
			_, err := integrationDB.ExecContext(context.Background(), `UPDATE usage_logs SET rate_multiplier=9 WHERE id=$1`, f.usageID)
			require.NoError(t, err)
		}},
		{name: "account rate multiplier", mutate: func(t *testing.T, f chargedUsageRepairFixture) {
			_, err := integrationDB.ExecContext(context.Background(), `UPDATE usage_logs SET account_rate_multiplier=9 WHERE id=$1`, f.usageID)
			require.NoError(t, err)
		}},
		{name: "refund reason", mutate: func(t *testing.T, f chargedUsageRepairFixture) {
			_, err := integrationDB.ExecContext(context.Background(), `UPDATE usage_logs SET refund_reason='corrupt',refunded_at=NOW() WHERE id=$1`, f.usageID)
			require.NoError(t, err)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newChargedUsageRepairFixture(t)
			tt.mutate(t, f)
			now := time.Now().UTC()
			_, err := integrationDB.ExecContext(context.Background(), `UPDATE video_task_settlements SET next_reconcile_at=$1`, now.Add(24*time.Hour))
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(context.Background(), `UPDATE video_task_settlements SET next_reconcile_at=$1 WHERE video_task_id=(SELECT id FROM video_tasks WHERE public_task_id=$2)`, now.Add(-time.Minute), f.publicID)
			require.NoError(t, err)
			claims, err := f.repo.ClaimDueReconciliation(context.Background(), now, 1, "corruption-token", time.Minute)
			require.NoError(t, err)
			require.Len(t, claims, 1)
			require.Equal(t, f.publicID, claims[0].PublicTaskID)
			result, err := f.repo.RepairChargedUsage(context.Background(), f.publicID)
			require.NoError(t, err)
			require.True(t, result.Applied)
		})
	}
}

func TestVideoTaskSettlementRepository_ClaimDueReconciliationSkipsCanonicalChargedUsage(t *testing.T) {
	f := newChargedUsageRepairFixture(t)
	now := time.Now().UTC()
	_, err := integrationDB.ExecContext(context.Background(), `UPDATE video_task_settlements SET next_reconcile_at=$1`, now.Add(24*time.Hour))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(context.Background(), `UPDATE video_task_settlements SET next_reconcile_at=$1 WHERE video_task_id=(SELECT id FROM video_tasks WHERE public_task_id=$2)`, now.Add(-time.Minute), f.publicID)
	require.NoError(t, err)
	claims, err := f.repo.ClaimDueReconciliation(context.Background(), now, 1, "canonical-token", time.Minute)
	require.NoError(t, err)
	require.Empty(t, claims)
}

func TestVideoTaskSettlementRepository_ClaimDueReconciliationSkipsStorageRoundedMultipliers(t *testing.T) {
	f := newChargedUsageRepairFixture(t, 0.15150000000000002, 0.10100000000000001)
	ctx := context.Background()
	var rateMultiplier, accountRateMultiplier float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT rate_multiplier,account_rate_multiplier FROM usage_logs WHERE id=$1`, f.usageID).Scan(&rateMultiplier, &accountRateMultiplier))
	require.Equal(t, 0.1515, rateMultiplier)
	require.Equal(t, 0.101, accountRateMultiplier)

	now := time.Now().UTC()
	setOnlyVideoTaskSettlementDue(t, f.publicID, now)
	claims, err := f.repo.ClaimDueReconciliation(ctx, now, 1, "rounded-multiplier", time.Minute)
	require.NoError(t, err)
	require.Empty(t, claims)
}

func TestVideoTaskSettlementRepository_RepairChargedUsageAcceptsStorageRoundedMultipliers(t *testing.T) {
	f := newChargedUsageRepairFixture(t, 0.15150000000000002, 0.10100000000000001)
	f.clearTaskLink(t)
	f.clearSettlementLink(t)

	result, err := f.repo.RepairChargedUsage(context.Background(), f.publicID)
	require.NoError(t, err)
	require.True(t, result.Applied)
	assertChargedUsageLinks(t, f.publicID, f.usageID)
}

func TestVideoTaskSettlementRepository_ClaimDueReconciliationClearsHistoricalErrorOnce(t *testing.T) {
	f := newChargedUsageRepairFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	setOnlyVideoTaskSettlementDue(t, f.publicID, now)
	_, err := integrationDB.ExecContext(ctx, `UPDATE video_task_settlements SET last_error='historical repair failure',reconcile_attempts=3 WHERE video_task_id=(SELECT id FROM video_tasks WHERE public_task_id=$1)`, f.publicID)
	require.NoError(t, err)

	claims, err := f.repo.ClaimDueReconciliation(ctx, now, 1, "historical-error", time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Equal(t, f.publicID, claims[0].PublicTaskID)
	repaired, err := f.repo.RepairChargedUsage(ctx, f.publicID)
	require.NoError(t, err)
	require.False(t, repaired.Applied)
	completed, err := f.repo.CompleteReconciliation(ctx, f.publicID, "historical-error")
	require.NoError(t, err)
	require.True(t, completed)

	var lastError, lockedBy sql.NullString
	var nextReconcileAt, lockedUntil sql.NullTime
	var attempts int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT last_error,next_reconcile_at,reconcile_attempts,locked_by,locked_until FROM video_task_settlements WHERE video_task_id=(SELECT id FROM video_tasks WHERE public_task_id=$1)`, f.publicID).Scan(&lastError, &nextReconcileAt, &attempts, &lockedBy, &lockedUntil))
	require.False(t, lastError.Valid)
	require.False(t, nextReconcileAt.Valid)
	require.Zero(t, attempts)
	require.False(t, lockedBy.Valid)
	require.False(t, lockedUntil.Valid)

	claims, err = f.repo.ClaimDueReconciliation(ctx, now.Add(time.Second), 1, "historical-error-second", time.Minute)
	require.NoError(t, err)
	require.Empty(t, claims)
}

func TestVideoTaskSettlementRepository_ClaimDueReconciliationLegacyBillingModeFallback(t *testing.T) {
	for _, tt := range []struct {
		name    string
		pricing map[string]any
	}{
		{name: "absent"},
		{name: "json null", pricing: map[string]any{"billing_mode": nil}},
		{name: "empty", pricing: map[string]any{"billing_mode": ""}},
		{name: "whitespace", pricing: map[string]any{"billing_mode": " \t "}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newChargedUsageRepairFixtureWithPricing(t, tt.pricing)
			now := time.Now().UTC()
			_, err := integrationDB.ExecContext(context.Background(), `UPDATE video_task_settlements SET next_reconcile_at=$1`, now.Add(24*time.Hour))
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(context.Background(), `UPDATE video_task_settlements SET next_reconcile_at=$1 WHERE video_task_id=(SELECT id FROM video_tasks WHERE public_task_id=$2)`, now.Add(-time.Minute), f.publicID)
			require.NoError(t, err)

			claims, err := f.repo.ClaimDueReconciliation(context.Background(), now, 1, "legacy-billing-mode", time.Minute)
			require.NoError(t, err)
			require.Empty(t, claims)
		})
	}
}

func TestVideoTaskSettlementRepository_RepairChargedUsageCorrectsAuthoritativelyLinkedCorruption(t *testing.T) {
	f := newChargedUsageRepairFixture(t)
	_, err := integrationDB.ExecContext(context.Background(), `UPDATE usage_logs SET actual_cost=99,account_id=$1,upstream_endpoint='/wrong',refunded_cost=1,refunded_total_cost=2,refunded_account_cost=1,refund_reason='wrong',refunded_at=NOW() WHERE id=$2`, f.accountID, f.usageID)
	require.NoError(t, err)
	result, err := f.repo.RepairChargedUsage(context.Background(), f.publicID)
	require.NoError(t, err)
	require.True(t, result.Applied)
	var actual, refunded, refundedTotal, refundedAccount float64
	var endpoint string
	var refundedAt sql.NullTime
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT actual_cost,upstream_endpoint,refunded_cost,refunded_total_cost,refunded_account_cost,refunded_at FROM usage_logs WHERE id=$1`, f.usageID).Scan(&actual, &endpoint, &refunded, &refundedTotal, &refundedAccount, &refundedAt))
	require.InDelta(t, 3, actual, 1e-9)
	require.Equal(t, "/v1/videos", endpoint)
	require.Zero(t, refunded)
	require.Zero(t, refundedTotal)
	require.Zero(t, refundedAccount)
	require.False(t, refundedAt.Valid)
}

func TestVideoTaskSettlementRepository_RepairChargedUsageRepairsOneSidedLinks(t *testing.T) {
	for _, tt := range []struct {
		name  string
		clear func(chargedUsageRepairFixture, *testing.T)
	}{
		{name: "task missing", clear: func(f chargedUsageRepairFixture, t *testing.T) { f.clearTaskLink(t) }},
		{name: "settlement missing", clear: func(f chargedUsageRepairFixture, t *testing.T) { f.clearSettlementLink(t) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newChargedUsageRepairFixture(t)
			tt.clear(f, t)
			result, err := f.repo.RepairChargedUsage(context.Background(), f.publicID)
			require.NoError(t, err)
			require.True(t, result.Applied)
			assertChargedUsageLinks(t, f.publicID, f.usageID)
		})
	}
}

func TestVideoTaskSettlementRepository_RepairChargedUsageRepairsMissingTaskSummary(t *testing.T) {
	f := newChargedUsageRepairFixture(t)
	_, err := integrationDB.ExecContext(context.Background(), `UPDATE video_tasks SET usage_metadata='{}'::jsonb WHERE public_task_id=$1`, f.publicID)
	require.NoError(t, err)
	result, err := f.repo.RepairChargedUsage(context.Background(), f.publicID)
	require.NoError(t, err)
	require.True(t, result.Applied)
	assertChargedUsageLinks(t, f.publicID, f.usageID)
	var requestID string
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT usage_metadata->>'request_id' FROM video_tasks WHERE public_task_id=$1`, f.publicID).Scan(&requestID))
	require.Equal(t, f.usage.RequestID, requestID)
}

func TestVideoTaskSettlementRepository_RepairChargedUsageConcurrentCreatesOneRow(t *testing.T) {
	f := newChargedUsageRepairFixture(t)
	f.clearTaskLink(t)
	f.clearSettlementLink(t)
	_, err := integrationDB.ExecContext(context.Background(), `DELETE FROM usage_logs WHERE id=$1`, f.usageID)
	require.NoError(t, err)
	var wg sync.WaitGroup
	results := make(chan *service.VideoTaskSettlementResult, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, repairErr := f.repo.RepairChargedUsage(context.Background(), f.publicID)
			results <- result
			errs <- repairErr
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for repairErr := range errs {
		require.NoError(t, repairErr)
	}
	applied := 0
	for result := range results {
		if result != nil && result.Applied {
			applied++
		}
	}
	require.Equal(t, 1, applied)
	var count int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM usage_logs WHERE request_id=$1 AND api_key_id=$2`, f.usage.RequestID, f.apiKeyID).Scan(&count))
	require.Equal(t, 1, count)
}

func TestVideoTaskSettlementRepository_CaptureUsesCurrentTimeNotReserveTime(t *testing.T) {
	ctx := context.Background()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	f := newVideoSettlementFixture(t, false, false)
	staleQuoteTime := time.Now().UTC().Add(-24 * time.Hour)
	reserved, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{
		PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 1,
		Effects: service.VideoTaskBillingEffects{BalanceCost: 1, APIKeyRateLimitCost: 1, ChargedAt: staleQuoteTime},
	})
	require.NoError(t, err)
	require.Nil(t, reserved.Settlement.ChargedAt)
	require.True(t, reserved.Settlement.Effects.ChargedAt.IsZero())
	markVideoTaskProviderAccepted(t, f.publicID)

	beforeCapture := time.Now().UTC()
	captured, err := repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID, ActualCostUSD: 1})
	require.NoError(t, err)
	require.NotNil(t, captured.Settlement.ChargedAt)
	require.False(t, captured.Settlement.ChargedAt.Before(beforeCapture))
	require.NotNil(t, captured.PostState.APIKey.Window5hStart)
	require.False(t, captured.PostState.APIKey.Window5hStart.Before(beforeCapture))
	var appliedChargedAt time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT (e.metadata->'applied_effects'->>'charged_at')::timestamptz FROM video_task_settlement_events e JOIN video_task_settlements s ON s.id=e.settlement_id JOIN video_tasks t ON t.id=s.video_task_id WHERE t.public_task_id=$1 AND e.event_type='capture'`, f.publicID).Scan(&appliedChargedAt))
	require.False(t, appliedChargedAt.Before(beforeCapture))
}

func TestVideoTaskSettlementRepository_BalanceReleaseRestoresOnlyReservation(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, false)
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{
		PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 3,
		Effects: service.VideoTaskBillingEffects{BalanceCost: 3, ChargedAt: time.Now().UTC()},
	})
	require.NoError(t, err)
	result, err := repo.Release(ctx, &service.VideoTaskSettlementReleaseCommand{PublicTaskID: f.publicID})
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.NotNil(t, result.Settlement.ReleasedAt)
	assertUserMoney(t, f.userID, 20, 0)
	var state string
	var refunded float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT state,refunded_cost_usd FROM video_task_settlements s JOIN video_tasks t ON t.id=s.video_task_id WHERE t.public_task_id=$1`, f.publicID).Scan(&state, &refunded))
	require.Equal(t, string(service.VideoTaskSettlementReleased), state)
	require.Zero(t, refunded)
}

func TestVideoTaskSettlementRepository_SubscriptionReserveAndRelease(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, true, false)
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	effects := service.VideoTaskBillingEffects{SubscriptionCost: 2.5, ChargedAt: time.Now().UTC()}
	cmd := &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeSubscription, GrossCostUSD: 2.5, Effects: effects}

	first, err := repo.Reserve(ctx, cmd)
	require.NoError(t, err)
	require.True(t, first.Applied)
	require.NotNil(t, first.PostState.Subscription)
	require.NotNil(t, first.PostState.Subscription.DailyPeriod)
	require.NotNil(t, first.PostState.Subscription.WeeklyPeriod)
	require.NotNil(t, first.PostState.Subscription.MonthlyPeriod)
	second, err := repo.Reserve(ctx, cmd)
	require.NoError(t, err)
	require.False(t, second.Applied)
	assertSubscriptionUsage(t, f.subscriptionID, 2.5)

	released, err := repo.Release(ctx, &service.VideoTaskSettlementReleaseCommand{PublicTaskID: f.publicID})
	require.NoError(t, err)
	require.True(t, released.Applied)
	assertSubscriptionUsage(t, f.subscriptionID, 0)
	var state string
	var refunded float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT state, refunded_cost_usd FROM video_task_settlements s JOIN video_tasks t ON t.id=s.video_task_id WHERE t.public_task_id=$1`, f.publicID).Scan(&state, &refunded))
	require.Equal(t, "released", state)
	require.Zero(t, refunded)
}

func TestVideoTaskSettlementRepository_SubscriptionReservesPlannedActualCost(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, true, false)
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)

	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeSubscription, GrossCostUSD: 4, ActualCostUSD: 1.5, Effects: service.VideoTaskBillingEffects{SubscriptionCost: 1.5}})
	require.NoError(t, err)
	assertSubscriptionUsage(t, f.subscriptionID, 1.5)

	released, err := repo.Release(ctx, &service.VideoTaskSettlementReleaseCommand{PublicTaskID: f.publicID})
	require.NoError(t, err)
	require.True(t, released.Applied)
	assertSubscriptionUsage(t, f.subscriptionID, 0)
}

func TestVideoTaskSettlementRepository_RejectsInvalidAccountCostAtomically(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, false)
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{
		PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 3,
		AccountCostUSD: math.Inf(1), Effects: service.VideoTaskBillingEffects{BalanceCost: 3},
	})
	require.ErrorIs(t, err, service.ErrVideoTaskSettlementInvalidAmount)
	assertUserMoney(t, f.userID, 20, 0)
	assertSettlementEventCount(t, f.publicID, "reserve", 0)
}

func TestVideoTaskSettlementRepository_NormalizesFixedPointAndLeavesNoHold(t *testing.T) {
	ctx := context.Background()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)

	t.Run("positive below half unit is rejected", func(t *testing.T) {
		f := newVideoSettlementFixture(t, false, false)
		_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 0.000000004, Effects: service.VideoTaskBillingEffects{BalanceCost: 0.000000004}})
		require.ErrorIs(t, err, service.ErrVideoTaskSettlementInvalidAmount)
		assertUserMoney(t, f.userID, 20, 0)
	})

	t.Run("half unit rounds once through refund", func(t *testing.T) {
		f := newVideoSettlementFixture(t, false, false)
		gross := 1.2345678951
		applied := 1.234567895
		wantApplied := 1.23456790
		reserved, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: gross, ActualCostUSD: applied, AccountCostUSD: applied, Effects: service.VideoTaskBillingEffects{BalanceCost: applied, APIKeyQuotaCost: applied}})
		require.NoError(t, err)
		require.Equal(t, gross, reserved.Settlement.GrossCostUSD)
		require.Equal(t, wantApplied, reserved.Settlement.AccountCostUSD)
		require.Equal(t, wantApplied, reserved.Settlement.Effects.BalanceCost)
		markVideoTaskProviderAccepted(t, f.publicID)
		captured, err := repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID, ActualCostUSD: applied})
		require.NoError(t, err)
		require.Equal(t, wantApplied, captured.Settlement.ActualCostUSD)
		_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='failed' WHERE public_task_id=$1`, f.publicID)
		require.NoError(t, err)
		refunded, err := repo.RefundFailed(ctx, &service.VideoTaskSettlementRefundCommand{PublicTaskID: f.publicID})
		require.NoError(t, err)
		require.Equal(t, wantApplied, refunded.Settlement.RefundedCostUSD)
		assertUserMoney(t, f.userID, 20, 0)
	})

	t.Run("rounding boundary release clears exact hold", func(t *testing.T) {
		f := newVideoSettlementFixture(t, false, false)
		input := 0.000000005
		reserved, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: input, Effects: service.VideoTaskBillingEffects{BalanceCost: input}})
		require.NoError(t, err)
		require.Equal(t, input, reserved.Settlement.GrossCostUSD)
		require.Equal(t, 0.00000001, reserved.Settlement.ActualCostUSD)
		require.Equal(t, 0.00000001, reserved.Settlement.Effects.BalanceCost)
		_, err = repo.Release(ctx, &service.VideoTaskSettlementReleaseCommand{PublicTaskID: f.publicID})
		require.NoError(t, err)
		assertUserMoney(t, f.userID, 20, 0)
	})
}

func TestVideoTaskSettlementRepository_NilCommandsReturnError(t *testing.T) {
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	ctx := context.Background()
	_, err := repo.Reserve(ctx, nil)
	require.ErrorIs(t, err, service.ErrVideoTaskSettlementCommandRequired)
	_, err = repo.Capture(ctx, nil)
	require.ErrorIs(t, err, service.ErrVideoTaskSettlementCommandRequired)
	_, err = repo.Release(ctx, nil)
	require.ErrorIs(t, err, service.ErrVideoTaskSettlementCommandRequired)
	_, err = repo.RefundFailed(ctx, nil)
	require.ErrorIs(t, err, service.ErrVideoTaskSettlementCommandRequired)
}

func TestVideoTaskSettlementRepository_RefundPreservesResetWindowsAndGuardsStatus(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, false)
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	now := time.Now().UTC().Truncate(time.Microsecond)
	effects := settlementEffects(now)
	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 3, Effects: effects})
	require.NoError(t, err)
	markVideoTaskProviderAccepted(t, f.publicID)
	_, err = repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID, ActualCostUSD: 3})
	require.NoError(t, err)

	for _, status := range []string{"cancelled", "expired"} {
		_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status=$1 WHERE public_task_id=$2`, status, f.publicID)
		require.NoError(t, err)
		result, refundErr := repo.RefundFailed(ctx, &service.VideoTaskSettlementRefundCommand{PublicTaskID: f.publicID})
		require.NoError(t, refundErr)
		require.False(t, result.Applied)
	}

	newWindow := now.Add(24 * time.Hour)
	_, err = integrationDB.ExecContext(ctx, `UPDATE api_keys SET usage_5h=7, usage_1d=7, usage_7d=7, window_5h_start=$1, window_1d_start=$1, window_7d_start=$1 WHERE id=$2`, newWindow, f.apiKeyID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE user_platform_quotas SET daily_usage_usd=7, weekly_usage_usd=7, monthly_usage_usd=7, daily_window_start=$1, weekly_window_start=$1, monthly_window_start=$1 WHERE user_id=$2 AND platform='openai'`, newWindow, f.userID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='failed' WHERE public_task_id=$1`, f.publicID)
	require.NoError(t, err)
	result, err := repo.RefundFailed(ctx, &service.VideoTaskSettlementRefundCommand{PublicTaskID: f.publicID})
	require.NoError(t, err)
	require.True(t, result.Applied)
	var key5h, key1d, key7d float64
	var key5hStart, key1dStart, key7dStart time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT usage_5h,usage_1d,usage_7d,window_5h_start,window_1d_start,window_7d_start FROM api_keys WHERE id=$1`, f.apiKeyID).Scan(&key5h, &key1d, &key7d, &key5hStart, &key1dStart, &key7dStart))
	var platformDaily, platformWeekly, platformMonthly float64
	var platformDailyStart, platformWeeklyStart, platformMonthlyStart time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd,daily_window_start,weekly_window_start,monthly_window_start FROM user_platform_quotas WHERE user_id=$1 AND platform='openai'`, f.userID).Scan(&platformDaily, &platformWeekly, &platformMonthly, &platformDailyStart, &platformWeeklyStart, &platformMonthlyStart))
	require.Equal(t, 7.0, key5h)
	require.Equal(t, 7.0, key1d)
	require.Equal(t, 7.0, key7d)
	require.WithinDuration(t, newWindow, key5hStart, time.Microsecond)
	require.WithinDuration(t, newWindow, key1dStart, time.Microsecond)
	require.WithinDuration(t, newWindow, key7dStart, time.Microsecond)
	require.Equal(t, 7.0, platformDaily)
	require.Equal(t, 7.0, platformWeekly)
	require.Equal(t, 7.0, platformMonthly)
	require.WithinDuration(t, newWindow, platformDailyStart, time.Microsecond)
	require.WithinDuration(t, newWindow, platformWeeklyStart, time.Microsecond)
	require.WithinDuration(t, newWindow, platformMonthlyStart, time.Microsecond)
}

func TestVideoTaskSettlementRepository_RefundAPIKeyStatusRestorationRules(t *testing.T) {
	ctx := context.Background()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	past := time.Now().UTC().Add(-time.Hour)
	future := time.Now().UTC().Add(time.Hour)
	atQuotaAfterRefund := 5.0
	tests := []struct {
		name       string
		status     string
		expiresAt  *time.Time
		quotaUsed  *float64
		wantStatus string
	}{
		{name: "quota exhausted below quota becomes active", status: service.StatusAPIKeyQuotaExhausted, wantStatus: service.StatusAPIKeyActive},
		{name: "quota exhausted below quota before future expiry becomes active", status: service.StatusAPIKeyQuotaExhausted, expiresAt: &future, wantStatus: service.StatusAPIKeyActive},
		{name: "quota exhausted still at quota remains exhausted", status: service.StatusAPIKeyQuotaExhausted, quotaUsed: &atQuotaAfterRefund, wantStatus: service.StatusAPIKeyQuotaExhausted},
		{name: "quota exhausted past expiry remains exhausted", status: service.StatusAPIKeyQuotaExhausted, expiresAt: &past, wantStatus: service.StatusAPIKeyQuotaExhausted},
		{name: "expired remains expired", status: service.StatusAPIKeyExpired, expiresAt: &past, wantStatus: service.StatusAPIKeyExpired},
		{name: "disabled remains disabled", status: service.StatusAPIKeyDisabled, wantStatus: service.StatusAPIKeyDisabled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newVideoSettlementFixture(t, false, false)
			effects := service.VideoTaskBillingEffects{BalanceCost: 3, APIKeyQuotaCost: 3, ChargedAt: time.Now().UTC()}
			_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 3, Effects: effects})
			require.NoError(t, err)
			markVideoTaskProviderAccepted(t, f.publicID)
			_, err = repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID, ActualCostUSD: 3})
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `UPDATE api_keys SET status=$1,expires_at=$2,quota_used=COALESCE($3,quota_used) WHERE id=$4`, tt.status, tt.expiresAt, tt.quotaUsed, f.apiKeyID)
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='failed' WHERE public_task_id=$1`, f.publicID)
			require.NoError(t, err)
			result, err := repo.RefundFailed(ctx, &service.VideoTaskSettlementRefundCommand{PublicTaskID: f.publicID})
			require.NoError(t, err)
			require.True(t, result.Applied)
			require.Equal(t, tt.wantStatus, result.PostState.APIKey.Status)
			if tt.quotaUsed == nil {
				require.Zero(t, result.PostState.APIKey.QuotaUsed)
			} else {
				require.Equal(t, 2.0, result.PostState.APIKey.QuotaUsed)
			}
		})
	}
}

func TestVideoTaskSettlementRepository_PhantomAndConcurrentRefundGuards(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	phantom := newVideoSettlementFixture(t, false, false)
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	release, err := repo.Release(ctx, &service.VideoTaskSettlementReleaseCommand{PublicTaskID: phantom.publicID})
	require.NoError(t, err)
	require.False(t, release.Applied)
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='failed' WHERE public_task_id=$1`, phantom.publicID)
	require.NoError(t, err)
	refund, err := repo.RefundFailed(ctx, &service.VideoTaskSettlementRefundCommand{PublicTaskID: phantom.publicID})
	require.NoError(t, err)
	require.False(t, refund.Applied)
	assertUserMoney(t, phantom.userID, 20, 0)

	f := newVideoSettlementFixture(t, false, false)
	effects := settlementEffects(time.Now().UTC())
	_, err = repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 3, Effects: effects})
	require.NoError(t, err)
	markVideoTaskProviderAccepted(t, f.publicID)
	_, err = repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID, ActualCostUSD: 3})
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='failed' WHERE public_task_id=$1`, f.publicID)
	require.NoError(t, err)

	var wg sync.WaitGroup
	applied := make(chan bool, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := repo.RefundFailed(ctx, &service.VideoTaskSettlementRefundCommand{PublicTaskID: f.publicID})
			if e == nil {
				applied <- r.Applied
			}
			errs <- e
		}()
	}
	wg.Wait()
	close(applied)
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	count := 0
	for a := range applied {
		if a {
			count++
		}
	}
	require.Equal(t, 1, count)
	assertUserMoney(t, f.userID, 20, 0)
}

func TestVideoTaskSettlementRepository_ConcurrentReserveAndInsufficientBalance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	f := newVideoSettlementFixture(t, false, false)
	cmd := &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 3, Effects: service.VideoTaskBillingEffects{BalanceCost: 3, ChargedAt: time.Now().UTC()}}
	results := runConcurrentSettlementCalls(t, func() (bool, error) {
		result, err := repo.Reserve(ctx, cmd)
		if err != nil {
			return false, err
		}
		return result.Applied, nil
	})
	require.ElementsMatch(t, []bool{true, false}, results)
	assertUserMoney(t, f.userID, 17, 3)
	assertSettlementEventCount(t, f.publicID, "reserve", 1)

	insufficient := newVideoSettlementFixture(t, false, false)
	_, err := integrationDB.ExecContext(ctx, `UPDATE users SET balance=1 WHERE id=$1`, insufficient.userID)
	require.NoError(t, err)
	_, err = repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: insufficient.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 3, Effects: service.VideoTaskBillingEffects{BalanceCost: 3}})
	require.ErrorIs(t, err, service.ErrVideoTaskInsufficientBalance)
	assertUserMoney(t, insufficient.userID, 1, 0)
	var settlements, events int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_task_settlements s JOIN video_tasks t ON t.id=s.video_task_id WHERE t.public_task_id=$1`, insufficient.publicID).Scan(&settlements))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_task_settlement_events e JOIN video_task_settlements s ON s.id=e.settlement_id JOIN video_tasks t ON t.id=s.video_task_id WHERE t.public_task_id=$1`, insufficient.publicID).Scan(&events))
	require.Zero(t, settlements)
	require.Zero(t, events)
}

func TestVideoTaskSettlementRepository_ReserveLocksFundingBeforeAccount(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	f := newVideoSettlementFixture(t, false, false)

	accountBlocker, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = accountBlocker.Rollback() }()
	var accountID int64
	require.NoError(t, accountBlocker.QueryRowContext(ctx, `SELECT id FROM accounts WHERE id=$1 FOR UPDATE`, f.accountID).Scan(&accountID))

	type reserveOutcome struct {
		result *service.VideoTaskSettlementResult
		err    error
	}
	done := make(chan reserveOutcome, 1)
	go func() {
		result, reserveErr := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 1, Effects: service.VideoTaskBillingEffects{BalanceCost: 1}})
		done <- reserveOutcome{result: result, err: reserveErr}
	}()
	select {
	case outcome := <-done:
		t.Fatalf("Reserve did not wait for the account lock: result=%v err=%v", outcome.result, outcome.err)
	case <-time.After(100 * time.Millisecond):
	}

	fundingProbe, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	var userID int64
	err = fundingProbe.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE NOWAIT`, f.userID).Scan(&userID)
	require.Error(t, err, "Reserve must hold the funding lock while waiting for the account lock")
	require.NoError(t, fundingProbe.Rollback())
	require.NoError(t, accountBlocker.Rollback())

	outcome := <-done
	require.NoError(t, outcome.err)
	require.True(t, outcome.result.Applied)
}

func TestVideoTaskSettlementRepository_ConcurrentCapturePreservesReserve(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	f := newVideoSettlementFixture(t, false, false)
	effects := settlementEffects(time.Now().UTC().Truncate(time.Microsecond))
	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 3, Effects: effects})
	require.NoError(t, err)
	markVideoTaskProviderAccepted(t, f.publicID)
	before, err := repo.GetByPublicTaskID(ctx, f.publicID)
	require.NoError(t, err)

	_, err = repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID, ActualCostUSD: 4})
	require.ErrorIs(t, err, service.ErrVideoTaskSettlementIntegrity)
	assertUserMoney(t, f.userID, 17, 3)
	assertSettlementEventCount(t, f.publicID, "capture", 0)

	results := runConcurrentSettlementCalls(t, func() (bool, error) {
		result, callErr := repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID})
		if callErr != nil {
			return false, callErr
		}
		return result.Applied, nil
	})
	require.ElementsMatch(t, []bool{true, false}, results)
	var appliedSnapshotBefore, appliedSnapshotAfter string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT applied_snapshot::text FROM video_task_settlements s JOIN video_tasks t ON t.id=s.video_task_id WHERE t.public_task_id=$1`, f.publicID).Scan(&appliedSnapshotBefore))
	require.NotEmpty(t, appliedSnapshotBefore)
	_, err = repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID, ActualCostUSD: 1})
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT applied_snapshot::text FROM video_task_settlements s JOIN video_tasks t ON t.id=s.video_task_id WHERE t.public_task_id=$1`, f.publicID).Scan(&appliedSnapshotAfter))
	require.Equal(t, appliedSnapshotBefore, appliedSnapshotAfter)
	assertUserMoney(t, f.userID, 17, 0)
	after, err := repo.GetByPublicTaskID(ctx, f.publicID)
	require.NoError(t, err)
	require.Equal(t, before.GrossCostUSD, after.GrossCostUSD)
	require.Equal(t, before.Effects, after.Effects)
	require.InDelta(t, 3, after.ActualCostUSD, 1e-9)
	assertSettlementEventCount(t, f.publicID, "capture", 1)
}

func TestVideoTaskSettlementRepository_ConcurrentReleaseIsNotRefund(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	f := newVideoSettlementFixture(t, false, false)
	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 3, Effects: service.VideoTaskBillingEffects{BalanceCost: 3}})
	require.NoError(t, err)
	results := runConcurrentSettlementCalls(t, func() (bool, error) {
		result, callErr := repo.Release(ctx, &service.VideoTaskSettlementReleaseCommand{PublicTaskID: f.publicID})
		if callErr != nil {
			return false, callErr
		}
		return result.Applied, nil
	})
	require.ElementsMatch(t, []bool{true, false}, results)
	assertUserMoney(t, f.userID, 20, 0)
	snapshot, err := repo.GetByPublicTaskID(ctx, f.publicID)
	require.NoError(t, err)
	require.Equal(t, service.VideoTaskSettlementReleased, snapshot.State)
	require.Zero(t, snapshot.RefundedCostUSD)
	require.Nil(t, snapshot.RefundedAt)
	require.NotNil(t, snapshot.ReleasedAt)
	assertSettlementEventCount(t, f.publicID, "release", 1)
	assertSettlementEventCount(t, f.publicID, "refund", 0)
	assertSettlementEventID(t, f.publicID, "reserve")
	assertSettlementEventID(t, f.publicID, "release")
}

func TestVideoTaskSettlementRepository_SubscriptionRefundHonorsPeriodReset(t *testing.T) {
	ctx := context.Background()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	f := newVideoSettlementFixture(t, true, false)
	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeSubscription, GrossCostUSD: 2.5, Effects: service.VideoTaskBillingEffects{SubscriptionCost: 2.5, ChargedAt: time.Now().UTC()}})
	require.NoError(t, err)
	markVideoTaskProviderAccepted(t, f.publicID)
	captured, err := repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID, ActualCostUSD: 2.5})
	require.NoError(t, err)
	require.True(t, captured.Applied)
	require.NotNil(t, captured.PostState.Subscription)
	require.InDelta(t, 2.5, captured.PostState.Subscription.MonthlyUsage, 1e-9)

	newPeriod := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Microsecond)
	_, err = integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET daily_usage_usd=7,weekly_usage_usd=7,monthly_usage_usd=7,daily_window_start=$1,weekly_window_start=$1,monthly_window_start=$1 WHERE id=$2`, newPeriod, f.subscriptionID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='failed' WHERE public_task_id=$1`, f.publicID)
	require.NoError(t, err)
	refunded, err := repo.RefundFailed(ctx, &service.VideoTaskSettlementRefundCommand{PublicTaskID: f.publicID})
	require.NoError(t, err)
	require.True(t, refunded.Applied)
	require.NotNil(t, refunded.PostState.Subscription)
	require.Equal(t, 7.0, refunded.PostState.Subscription.DailyUsage)
	require.Equal(t, 7.0, refunded.PostState.Subscription.WeeklyUsage)
	require.Equal(t, 7.0, refunded.PostState.Subscription.MonthlyUsage)
}

func TestVideoTaskSettlementRepository_SubscriptionExpiryAfterCaptureDoesNotBlockRefund(t *testing.T) {
	ctx := context.Background()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	f := newVideoSettlementFixture(t, true, false)
	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeSubscription, GrossCostUSD: 2, Effects: service.VideoTaskBillingEffects{SubscriptionCost: 2}})
	require.NoError(t, err)
	markVideoTaskProviderAccepted(t, f.publicID)
	_, err = repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID, ActualCostUSD: 2})
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET expires_at=NOW()-interval '1 minute' WHERE id=$1`, f.subscriptionID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='failed' WHERE public_task_id=$1`, f.publicID)
	require.NoError(t, err)
	result, err := repo.RefundFailed(ctx, &service.VideoTaskSettlementRefundCommand{PublicTaskID: f.publicID})
	require.NoError(t, err)
	require.True(t, result.Applied)
	assertSubscriptionUsage(t, f.subscriptionID, 0)
}

func TestVideoTaskSettlementRepository_SubscriptionReserveEligibility(t *testing.T) {
	ctx := context.Background()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	tests := []struct {
		name   string
		mutate func(t *testing.T, f videoSettlementFixture)
	}{
		{name: "expired", mutate: func(t *testing.T, f videoSettlementFixture) {
			_, err := integrationDB.Exec(`UPDATE user_subscriptions SET expires_at=NOW()-interval '1 minute' WHERE id=$1`, f.subscriptionID)
			require.NoError(t, err)
		}},
		{name: "inactive", mutate: func(t *testing.T, f videoSettlementFixture) {
			_, err := integrationDB.Exec(`UPDATE user_subscriptions SET status='suspended' WHERE id=$1`, f.subscriptionID)
			require.NoError(t, err)
		}},
		{name: "unrelated user", mutate: func(t *testing.T, f videoSettlementFixture) {
			user := mustCreateUser(t, testEntClient(t), &service.User{Email: "unrelated-sub-" + uuid.NewString() + "@example.com", PasswordHash: "hash"})
			_, err := integrationDB.Exec(`UPDATE user_subscriptions SET user_id=$1 WHERE id=$2`, user.ID, f.subscriptionID)
			require.NoError(t, err)
		}},
		{name: "unrelated group", mutate: func(t *testing.T, f videoSettlementFixture) {
			group := mustCreateGroup(t, testEntClient(t), &service.Group{Name: "unrelated-sub-" + uuid.NewString(), Platform: service.PlatformOpenAI})
			_, err := integrationDB.Exec(`UPDATE user_subscriptions SET group_id=$1 WHERE id=$2`, group.ID, f.subscriptionID)
			require.NoError(t, err)
		}},
		{name: "insufficient daily quota", mutate: func(t *testing.T, f videoSettlementFixture) {
			_, err := integrationDB.Exec(`UPDATE groups SET daily_limit_usd=2 WHERE id=$1`, f.groupID)
			require.NoError(t, err)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newVideoSettlementFixture(t, true, false)
			tt.mutate(t, f)
			_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeSubscription, GrossCostUSD: 2.5, Effects: service.VideoTaskBillingEffects{SubscriptionCost: 2.5}})
			require.Error(t, err)
			assertSettlementEventCount(t, f.publicID, "reserve", 0)
		})
	}
}

func TestVideoTaskSettlementRepository_SubscriptionReserveResetsStaleWindows(t *testing.T) {
	ctx := context.Background()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	f := newVideoSettlementFixture(t, true, false)
	old := time.Now().UTC().Add(-31 * 24 * time.Hour)
	_, err := integrationDB.ExecContext(ctx, `UPDATE groups SET daily_limit_usd=3,weekly_limit_usd=3,monthly_limit_usd=3 WHERE id=$1`, f.groupID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET daily_usage_usd=3,weekly_usage_usd=3,monthly_usage_usd=3,daily_window_start=$1,weekly_window_start=$1,monthly_window_start=$1 WHERE id=$2`, old, f.subscriptionID)
	require.NoError(t, err)
	result, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeSubscription, GrossCostUSD: 2.5, Effects: service.VideoTaskBillingEffects{SubscriptionCost: 2.5}})
	require.NoError(t, err)
	require.Equal(t, 2.5, result.PostState.Subscription.DailyUsage)
	require.Equal(t, 2.5, result.PostState.Subscription.WeeklyUsage)
	require.Equal(t, 2.5, result.PostState.Subscription.MonthlyUsage)
	require.True(t, result.PostState.Subscription.DailyPeriod.After(old))
	require.True(t, result.PostState.Subscription.WeeklyPeriod.After(old))
	require.True(t, result.PostState.Subscription.MonthlyPeriod.After(old))
}

func TestVideoTaskSettlementRepository_ConcurrentSubscriptionLimitAcrossTasks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	f := newVideoSettlementFixture(t, true, false)
	_, err := integrationDB.ExecContext(ctx, `UPDATE groups SET daily_limit_usd=3 WHERE id=$1`, f.groupID)
	require.NoError(t, err)
	secondID := "task_" + uuid.NewString()
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO video_tasks (public_task_id,provider,platform,user_id,api_key_id,group_id,subscription_id,account_id,requested_model,upstream_model,billing_model,status,prompt,request_hash) SELECT $1,provider,platform,user_id,api_key_id,group_id,subscription_id,account_id,requested_model,upstream_model,billing_model,status,prompt,$2 FROM video_tasks WHERE public_task_id=$3`, secondID, uuid.NewString(), f.publicID)
	require.NoError(t, err)
	ids := []string{f.publicID, secondID}
	var wg sync.WaitGroup
	type reserveOutcome struct {
		applied bool
		err     error
	}
	results := make(chan reserveOutcome, 2)
	for _, id := range ids {
		wg.Add(1)
		go func(publicID string) {
			defer wg.Done()
			result, callErr := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: publicID, BillingType: service.BillingTypeSubscription, GrossCostUSD: 2, Effects: service.VideoTaskBillingEffects{SubscriptionCost: 2}})
			results <- reserveOutcome{applied: callErr == nil && result.Applied, err: callErr}
		}(id)
	}
	wg.Wait()
	close(results)
	applied, rejected := 0, 0
	for outcome := range results {
		if outcome.applied {
			applied++
		}
		if errors.Is(outcome.err, service.ErrVideoTaskSubscriptionIneligible) {
			rejected++
		}
	}
	require.Equal(t, 1, applied)
	require.Equal(t, 1, rejected)
	require.NoError(t, ctx.Err())
	assertSubscriptionUsage(t, f.subscriptionID, 2)
}

func TestVideoTaskSettlementRepository_RejectsUnrelatedTaskReferences(t *testing.T) {
	ctx := context.Background()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)

	t.Run("api key owner", func(t *testing.T) {
		f := newVideoSettlementFixture(t, false, false)
		other := newVideoSettlementFixture(t, false, false)
		_, err := integrationDB.ExecContext(ctx, `UPDATE video_tasks SET api_key_id=$1 WHERE public_task_id=$2`, other.apiKeyID, f.publicID)
		require.NoError(t, err)
		_, err = repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 1, Effects: service.VideoTaskBillingEffects{BalanceCost: 1}})
		require.Error(t, err)
		assertUserMoney(t, f.userID, 20, 0)
	})

	t.Run("account group membership", func(t *testing.T) {
		f := newVideoSettlementFixture(t, false, false)
		_, err := integrationDB.ExecContext(ctx, `DELETE FROM account_groups WHERE account_id=$1 AND group_id=$2`, f.accountID, f.groupID)
		require.NoError(t, err)
		_, err = repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 1, Effects: service.VideoTaskBillingEffects{BalanceCost: 1}})
		require.Error(t, err)
		assertUserMoney(t, f.userID, 20, 0)
	})

	t.Run("account and group platform mismatch", func(t *testing.T) {
		f := newVideoSettlementFixture(t, false, false)
		_, err := integrationDB.ExecContext(ctx, `UPDATE groups SET platform=$1 WHERE id=$2`, service.PlatformAnthropic, f.groupID)
		require.NoError(t, err)
		_, err = repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 1, Effects: service.VideoTaskBillingEffects{BalanceCost: 1}})
		require.ErrorIs(t, err, service.ErrVideoTaskSettlementRelationInvalid)
		assertUserMoney(t, f.userID, 20, 0)
		assertSettlementEventCount(t, f.publicID, "reserve", 0)
	})

	t.Run("channel existence", func(t *testing.T) {
		f := newVideoSettlementFixture(t, false, false)
		_, err := integrationDB.ExecContext(ctx, `UPDATE video_tasks SET channel_id=9223372036854775807 WHERE public_task_id=$1`, f.publicID)
		require.NoError(t, err)
		_, err = repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 1, Effects: service.VideoTaskBillingEffects{BalanceCost: 1}})
		require.ErrorIs(t, err, service.ErrVideoTaskSettlementRelationInvalid)
	})

	t.Run("usage log identity", func(t *testing.T) {
		mutations := []struct{ name, sql string }{
			{name: "user", sql: `UPDATE usage_logs SET user_id=$1 WHERE id=$2`},
			{name: "api key", sql: `UPDATE usage_logs SET api_key_id=$1 WHERE id=$2`},
			{name: "account", sql: `UPDATE usage_logs SET account_id=$1 WHERE id=$2`},
			{name: "request", sql: `UPDATE usage_logs SET request_id=$1::text WHERE id=$2`},
		}
		for _, mutation := range mutations {
			t.Run(mutation.name, func(t *testing.T) {
				f := newVideoSettlementFixture(t, false, true)
				other := newVideoSettlementFixture(t, false, false)
				_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 1, Effects: service.VideoTaskBillingEffects{BalanceCost: 1, AccountStatsCost: 1}})
				require.NoError(t, err)
				var unrelated any
				switch mutation.name {
				case "user":
					unrelated = other.userID
				case "api key":
					unrelated = other.apiKeyID
				case "account":
					unrelated = other.accountID
				default:
					unrelated = "other-request"
				}
				_, err = integrationDB.ExecContext(ctx, mutation.sql, unrelated, f.usageLogID)
				require.NoError(t, err)
				markVideoTaskProviderAccepted(t, f.publicID)
				_, err = repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID, ActualCostUSD: 1})
				require.ErrorIs(t, err, service.ErrVideoTaskSettlementRelationInvalid)
				assertUserMoney(t, f.userID, 19, 1)
				assertSettlementEventCount(t, f.publicID, "capture", 0)
			})
		}
	})

	t.Run("refund usage log identity", func(t *testing.T) {
		f := newVideoSettlementFixture(t, false, true)
		_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 1, Effects: service.VideoTaskBillingEffects{BalanceCost: 1, AccountStatsCost: 1}})
		require.NoError(t, err)
		markVideoTaskProviderAccepted(t, f.publicID)
		_, err = repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID, ActualCostUSD: 1})
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, `UPDATE usage_logs SET request_id='other-request' WHERE id=$1`, f.usageLogID)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='failed' WHERE public_task_id=$1`, f.publicID)
		require.NoError(t, err)
		_, err = repo.RefundFailed(ctx, &service.VideoTaskSettlementRefundCommand{PublicTaskID: f.publicID})
		require.ErrorIs(t, err, service.ErrVideoTaskSettlementRelationInvalid)
		assertUserMoney(t, f.userID, 19, 0)
		assertSettlementEventCount(t, f.publicID, "refund", 0)
	})
}

func TestVideoTaskSettlementRepository_TaskIdentityDriftCaptureRejectsReleaseReconciles(t *testing.T) {
	ctx := context.Background()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	original := newVideoSettlementFixture(t, false, false)
	other := newVideoSettlementFixture(t, true, false)
	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: original.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 1, Effects: service.VideoTaskBillingEffects{BalanceCost: 1, APIKeyQuotaCost: 1}})
	require.NoError(t, err)
	markVideoTaskProviderAccepted(t, original.publicID)
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET user_id=$1,api_key_id=$2,group_id=$3,account_id=$4,subscription_id=$5,channel_id=NULL,platform='anthropic' WHERE public_task_id=$6`, other.userID, other.apiKeyID, other.groupID, other.accountID, other.subscriptionID, original.publicID)
	require.NoError(t, err)

	_, err = repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: original.publicID, ActualCostUSD: 1})
	require.ErrorIs(t, err, service.ErrVideoTaskSettlementIntegrity)
	assertUserMoney(t, original.userID, 19, 1)
	assertUserMoney(t, other.userID, 20, 0)
	assertSettlementEventCount(t, original.publicID, "capture", 0)

	released, err := repo.Release(ctx, &service.VideoTaskSettlementReleaseCommand{PublicTaskID: original.publicID})
	require.NoError(t, err)
	require.True(t, released.Applied)
	require.True(t, released.IntegrityDrift)
	assertUserMoney(t, original.userID, 20, 0)
	assertUserMoney(t, other.userID, 20, 0)
}

func TestVideoTaskSettlementRepository_MissingTaskAccountCaptureRejectsThenReleaseUsesSettlement(t *testing.T) {
	ctx := context.Background()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	f := newVideoSettlementFixture(t, false, false)
	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 1, Effects: service.VideoTaskBillingEffects{BalanceCost: 1}})
	require.NoError(t, err)
	markVideoTaskProviderAccepted(t, f.publicID)
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET account_id=9223372036854775807 WHERE public_task_id=$1`, f.publicID)
	require.NoError(t, err)

	_, err = repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID, ActualCostUSD: 1})
	require.ErrorIs(t, err, service.ErrVideoTaskSettlementIntegrity)
	assertUserMoney(t, f.userID, 19, 1)
	assertSettlementEventCount(t, f.publicID, "capture", 0)

	released, err := repo.Release(ctx, &service.VideoTaskSettlementReleaseCommand{PublicTaskID: f.publicID})
	require.NoError(t, err)
	require.True(t, released.Applied)
	require.True(t, released.IntegrityDrift)
	require.Equal(t, f.accountID, released.AccountID)
	require.Equal(t, service.PlatformOpenAI, released.Platform)
	assertUserMoney(t, f.userID, 20, 0)
}

func TestVideoTaskSettlementRepository_MissingTaskAccountRefundUsesChargedSettlement(t *testing.T) {
	ctx := context.Background()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	f := newVideoSettlementFixture(t, false, false)
	effects := service.VideoTaskBillingEffects{BalanceCost: 1, AccountQuotaCost: 1, PlatformQuotaCost: 1}
	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 1, Effects: effects})
	require.NoError(t, err)
	markVideoTaskProviderAccepted(t, f.publicID)
	_, err = repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID, ActualCostUSD: 1})
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET account_id=9223372036854775807,status='failed' WHERE public_task_id=$1`, f.publicID)
	require.NoError(t, err)

	refunded, err := repo.RefundFailed(ctx, &service.VideoTaskSettlementRefundCommand{PublicTaskID: f.publicID, Reason: "upstream failed"})
	require.NoError(t, err)
	require.True(t, refunded.Applied)
	require.True(t, refunded.IntegrityDrift)
	require.Equal(t, f.accountID, refunded.AccountID)
	require.Equal(t, service.PlatformOpenAI, refunded.Platform)
	assertUserMoney(t, f.userID, 20, 0)
	var accountQuota, platformQuota float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COALESCE((extra->>'quota_used')::numeric,0) FROM accounts WHERE id=$1`, f.accountID).Scan(&accountQuota))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM user_platform_quotas WHERE user_id=$1 AND platform=$2 AND deleted_at IS NULL`, f.userID, service.PlatformOpenAI).Scan(&platformQuota))
	require.Zero(t, accountQuota)
	require.Zero(t, platformQuota)
}

func TestVideoTaskSettlementRepository_RouteDriftAfterCaptureRefundsOriginalRows(t *testing.T) {
	ctx := context.Background()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	original := newVideoSettlementFixture(t, false, false)
	other := newVideoSettlementFixture(t, true, false)
	effects := service.VideoTaskBillingEffects{BalanceCost: 1, APIKeyQuotaCost: 1, AccountQuotaCost: 1, PlatformQuotaCost: 1}
	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: original.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 1, Effects: effects})
	require.NoError(t, err)
	markVideoTaskProviderAccepted(t, original.publicID)
	_, err = repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: original.publicID, ActualCostUSD: 1})
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET user_id=$1,api_key_id=$2,group_id=$3,account_id=$4,subscription_id=$5,platform='anthropic',status='failed' WHERE public_task_id=$6`, other.userID, other.apiKeyID, other.groupID, other.accountID, other.subscriptionID, original.publicID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `DELETE FROM account_groups WHERE account_id=$1 AND group_id=$2`, original.accountID, original.groupID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE api_keys SET deleted_at=NOW() WHERE id=$1`, original.apiKeyID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE groups SET platform='anthropic' WHERE id=$1`, original.groupID)
	require.NoError(t, err)

	refunded, err := repo.RefundFailed(ctx, &service.VideoTaskSettlementRefundCommand{PublicTaskID: original.publicID})
	require.NoError(t, err)
	require.True(t, refunded.Applied)
	require.True(t, refunded.IntegrityDrift)
	assertUserMoney(t, original.userID, 20, 0)
	assertUserMoney(t, other.userID, 20, 0)
	var originalQuota, originalAccountQuota float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT quota_used FROM api_keys WHERE id=$1`, original.apiKeyID).Scan(&originalQuota))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COALESCE((extra->>'quota_used')::numeric,0) FROM accounts WHERE id=$1`, original.accountID).Scan(&originalAccountQuota))
	require.Zero(t, originalQuota)
	require.Zero(t, originalAccountQuota)
}

func TestVideoTaskSettlementRepository_TamperedCaptureEffectsCannotOverRefund(t *testing.T) {
	ctx := context.Background()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	f := newVideoSettlementFixture(t, false, false)
	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 1, Effects: service.VideoTaskBillingEffects{BalanceCost: 1, APIKeyQuotaCost: 1}})
	require.NoError(t, err)
	markVideoTaskProviderAccepted(t, f.publicID)
	_, err = repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID, ActualCostUSD: 1})
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_task_settlement_events e SET metadata=jsonb_set(metadata,'{applied_effects,balance_cost}','100'::jsonb) FROM video_task_settlements s,video_tasks t WHERE e.settlement_id=s.id AND s.video_task_id=t.id AND t.public_task_id=$1 AND e.event_type='capture'`, f.publicID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='failed' WHERE public_task_id=$1`, f.publicID)
	require.NoError(t, err)
	_, err = repo.RefundFailed(ctx, &service.VideoTaskSettlementRefundCommand{PublicTaskID: f.publicID})
	require.ErrorIs(t, err, service.ErrVideoTaskSettlementIntegrity)
	assertUserMoney(t, f.userID, 19, 0)
	assertSettlementEventCount(t, f.publicID, "refund", 0)
}

func TestVideoTaskSettlementRepository_TamperedCaptureIdentityOrWindowsCannotRefund(t *testing.T) {
	ctx := context.Background()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	tests := []struct{ name, path, value string }{
		{name: "identity", path: "{identity,user_id}", value: "0"},
		{name: "window", path: "{applied_effects,window_snapshot,api_key_5h}", value: `"not-a-time"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newVideoSettlementFixture(t, false, false)
			_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 1, Effects: service.VideoTaskBillingEffects{BalanceCost: 1, APIKeyRateLimitCost: 1}})
			require.NoError(t, err)
			markVideoTaskProviderAccepted(t, f.publicID)
			_, err = repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID, ActualCostUSD: 1})
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `UPDATE video_task_settlement_events e SET metadata=jsonb_set(metadata,$1::text[],$2::jsonb) FROM video_task_settlements s,video_tasks t WHERE e.settlement_id=s.id AND s.video_task_id=t.id AND t.public_task_id=$3 AND e.event_type='capture'`, tt.path, tt.value, f.publicID)
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='failed' WHERE public_task_id=$1`, f.publicID)
			require.NoError(t, err)
			_, err = repo.RefundFailed(ctx, &service.VideoTaskSettlementRefundCommand{PublicTaskID: f.publicID})
			require.ErrorIs(t, err, service.ErrVideoTaskSettlementIntegrity)
			assertUserMoney(t, f.userID, 19, 0)
			assertSettlementEventCount(t, f.publicID, "refund", 0)
		})
	}
}

func TestVideoTaskSettlementRepository_EventCopyMustMatchAuthoritativeAppliedSnapshot(t *testing.T) {
	ctx := context.Background()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	tests := []struct {
		name   string
		mutate string
	}{
		{name: "delete account daily", mutate: `metadata #- '{applied_effects,window_snapshot,account_daily}'`},
		{name: "replace account weekly with valid timestamp", mutate: `jsonb_set(metadata,'{applied_effects,window_snapshot,account_weekly}','"2030-01-01T00:00:00Z"'::jsonb)`},
		{name: "replace charged at with valid timestamp", mutate: `jsonb_set(metadata,'{applied_effects,charged_at}','"2030-01-01T00:00:00Z"'::jsonb)`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newVideoSettlementFixture(t, false, false)
			effects := service.VideoTaskBillingEffects{BalanceCost: 1, AccountQuotaCost: 1}
			_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 1, Effects: effects})
			require.NoError(t, err)
			markVideoTaskProviderAccepted(t, f.publicID)
			_, err = repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID, ActualCostUSD: 1})
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `UPDATE video_task_settlement_events e SET metadata=`+tt.mutate+` FROM video_task_settlements s,video_tasks t WHERE e.settlement_id=s.id AND s.video_task_id=t.id AND t.public_task_id=$1 AND e.event_type='capture'`, f.publicID)
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='failed' WHERE public_task_id=$1`, f.publicID)
			require.NoError(t, err)
			_, err = repo.RefundFailed(ctx, &service.VideoTaskSettlementRefundCommand{PublicTaskID: f.publicID})
			require.ErrorIs(t, err, service.ErrVideoTaskSettlementIntegrity)
			assertUserMoney(t, f.userID, 19, 0)
			assertSettlementEventCount(t, f.publicID, "refund", 0)
		})
	}
}

func TestVideoTaskSettlementRepository_OptionalAccountWindowsAreAuthoritative(t *testing.T) {
	ctx := context.Background()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	tests := []struct {
		name                  string
		removeKeys            []string
		wantDaily, wantWeekly bool
	}{
		{name: "total only", removeKeys: []string{"quota_daily_limit", "quota_weekly_limit"}},
		{name: "daily only", removeKeys: []string{"quota_weekly_limit"}, wantDaily: true},
		{name: "weekly only", removeKeys: []string{"quota_daily_limit"}, wantWeekly: true},
		{name: "daily and weekly", wantDaily: true, wantWeekly: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newVideoSettlementFixture(t, false, false)
			for _, key := range tt.removeKeys {
				_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=extra-$1::text WHERE id=$2`, key, f.accountID)
				require.NoError(t, err)
			}
			_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 1, Effects: service.VideoTaskBillingEffects{BalanceCost: 1, AccountQuotaCost: 1}})
			require.NoError(t, err)
			markVideoTaskProviderAccepted(t, f.publicID)
			captured, err := repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID, ActualCostUSD: 1})
			require.NoError(t, err)
			require.Equal(t, 1.0, captured.PostState.Account.TotalUsed)
			if tt.wantDaily {
				require.Equal(t, 1.0, captured.PostState.Account.DailyUsed)
			} else {
				require.Zero(t, captured.PostState.Account.DailyUsed)
			}
			if tt.wantWeekly {
				require.Equal(t, 1.0, captured.PostState.Account.WeeklyUsed)
			} else {
				require.Zero(t, captured.PostState.Account.WeeklyUsed)
			}
			var appliedDaily, appliedWeekly, copiesEqual bool
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COALESCE((s.applied_snapshot->'dimensions'->>'account_daily')::boolean,false),COALESCE((s.applied_snapshot->'dimensions'->>'account_weekly')::boolean,false),e.metadata->'applied_snapshot'=s.applied_snapshot FROM video_task_settlements s JOIN video_tasks t ON t.id=s.video_task_id JOIN video_task_settlement_events e ON e.settlement_id=s.id AND e.event_type='capture' WHERE t.public_task_id=$1`, f.publicID).Scan(&appliedDaily, &appliedWeekly, &copiesEqual))
			require.Equal(t, tt.wantDaily, appliedDaily)
			require.Equal(t, tt.wantWeekly, appliedWeekly)
			require.True(t, copiesEqual)
			_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='failed' WHERE public_task_id=$1`, f.publicID)
			require.NoError(t, err)
			refunded, err := repo.RefundFailed(ctx, &service.VideoTaskSettlementRefundCommand{PublicTaskID: f.publicID})
			require.NoError(t, err)
			require.True(t, refunded.Applied)
			require.Zero(t, refunded.PostState.Account.TotalUsed)
			require.Zero(t, refunded.PostState.Account.DailyUsed)
			require.Zero(t, refunded.PostState.Account.WeeklyUsed)
		})
	}
}

func TestVideoTaskSettlementRepository_DisabledAccountLimitsIgnoreStaleWindows(t *testing.T) {
	ctx := context.Background()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	tests := []struct {
		name                        string
		dailyEnabled, weeklyEnabled bool
	}{
		{name: "both disabled"},
		{name: "daily enabled weekly disabled", dailyEnabled: true},
		{name: "daily disabled weekly enabled", weeklyEnabled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newVideoSettlementFixture(t, false, false)
			period := time.Now().UTC().Format(time.RFC3339Nano)
			extra := map[string]any{
				"quota_limit": 100.0, "quota_used": 0.0,
				"quota_daily_used": 7.0, "quota_daily_start": period,
				"quota_weekly_used": 9.0, "quota_weekly_start": period,
			}
			if tt.dailyEnabled {
				extra["quota_daily_limit"] = 100.0
			}
			if tt.weeklyEnabled {
				extra["quota_weekly_limit"] = 100.0
			}
			extraJSON, err := json.Marshal(extra)
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=$1::jsonb WHERE id=$2`, extraJSON, f.accountID)
			require.NoError(t, err)
			_, err = repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 1, Effects: service.VideoTaskBillingEffects{BalanceCost: 1, AccountQuotaCost: 1}})
			require.NoError(t, err)
			markVideoTaskProviderAccepted(t, f.publicID)
			captured, err := repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID, ActualCostUSD: 1})
			require.NoError(t, err)
			require.Equal(t, 1.0, captured.PostState.Account.TotalUsed)
			if tt.dailyEnabled {
				require.Equal(t, 8.0, captured.PostState.Account.DailyUsed)
			} else {
				require.Equal(t, 7.0, captured.PostState.Account.DailyUsed)
			}
			if tt.weeklyEnabled {
				require.Equal(t, 10.0, captured.PostState.Account.WeeklyUsed)
			} else {
				require.Equal(t, 9.0, captured.PostState.Account.WeeklyUsed)
			}
			var appliedDaily, appliedWeekly bool
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT (applied_snapshot->'dimensions'->>'account_daily')::boolean,(applied_snapshot->'dimensions'->>'account_weekly')::boolean FROM video_task_settlements s JOIN video_tasks t ON t.id=s.video_task_id WHERE t.public_task_id=$1`, f.publicID).Scan(&appliedDaily, &appliedWeekly))
			require.Equal(t, tt.dailyEnabled, appliedDaily)
			require.Equal(t, tt.weeklyEnabled, appliedWeekly)
			_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='failed' WHERE public_task_id=$1`, f.publicID)
			require.NoError(t, err)
			refunded, err := repo.RefundFailed(ctx, &service.VideoTaskSettlementRefundCommand{PublicTaskID: f.publicID})
			require.NoError(t, err)
			require.Zero(t, refunded.PostState.Account.TotalUsed)
			require.Equal(t, 7.0, refunded.PostState.Account.DailyUsed)
			require.Equal(t, 9.0, refunded.PostState.Account.WeeklyUsed)
		})
	}
}

func TestVideoTaskSettlementRepository_CaptureAuditTamperBlocksRefund(t *testing.T) {
	ctx := context.Background()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	tests := []struct{ name, mutate string }{
		{name: "amount", mutate: `UPDATE video_task_settlement_events e SET amount_usd=2 FROM video_task_settlements s,video_tasks t WHERE e.settlement_id=s.id AND s.video_task_id=t.id AND t.public_task_id=$1 AND e.event_type='capture'`},
		{name: "table event id", mutate: `UPDATE video_task_settlement_events e SET event_id=event_id||':tampered' FROM video_task_settlements s,video_tasks t WHERE e.settlement_id=s.id AND s.video_task_id=t.id AND t.public_task_id=$1 AND e.event_type='capture'`},
		{name: "metadata event id", mutate: `UPDATE video_task_settlement_events e SET metadata=jsonb_set(metadata,'{event_id}','"tampered"'::jsonb) FROM video_task_settlements s,video_tasks t WHERE e.settlement_id=s.id AND s.video_task_id=t.id AND t.public_task_id=$1 AND e.event_type='capture'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newVideoSettlementFixture(t, false, false)
			_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 1, Effects: service.VideoTaskBillingEffects{BalanceCost: 1}})
			require.NoError(t, err)
			markVideoTaskProviderAccepted(t, f.publicID)
			_, err = repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID, ActualCostUSD: 1})
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, tt.mutate, f.publicID)
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='failed' WHERE public_task_id=$1`, f.publicID)
			require.NoError(t, err)
			_, err = repo.RefundFailed(ctx, &service.VideoTaskSettlementRefundCommand{PublicTaskID: f.publicID})
			require.ErrorIs(t, err, service.ErrVideoTaskSettlementIntegrity)
			assertUserMoney(t, f.userID, 19, 0)
			assertSettlementEventCount(t, f.publicID, "refund", 0)
		})
	}
}

func TestVideoTaskSettlementRepository_SoftDeletedSubscriptionReleaseUsesOriginalRow(t *testing.T) {
	ctx := context.Background()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	f := newVideoSettlementFixture(t, true, false)
	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeSubscription, GrossCostUSD: 2, Effects: service.VideoTaskBillingEffects{SubscriptionCost: 2}})
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET deleted_at=NOW() WHERE id=$1`, f.subscriptionID)
	require.NoError(t, err)
	result, err := repo.Release(ctx, &service.VideoTaskSettlementReleaseCommand{PublicTaskID: f.publicID})
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.NotNil(t, result.PostState.Subscription)
	require.Zero(t, result.PostState.Subscription.DailyUsage)
	assertSettlementEventCount(t, f.publicID, "release", 1)
}

func TestVideoTaskSettlementRepository_SoftDeletedSubscriptionAndPlatformRefundOriginalRows(t *testing.T) {
	ctx := context.Background()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	f := newVideoSettlementFixture(t, true, false)
	effects := service.VideoTaskBillingEffects{SubscriptionCost: 2, PlatformQuotaCost: 2}
	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeSubscription, GrossCostUSD: 2, Effects: effects})
	require.NoError(t, err)
	markVideoTaskProviderAccepted(t, f.publicID)
	_, err = repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID, ActualCostUSD: 2})
	require.NoError(t, err)
	var platformQuotaID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT id FROM user_platform_quotas WHERE user_id=$1 AND platform='openai' AND deleted_at IS NULL`, f.userID).Scan(&platformQuotaID))
	_, err = integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET deleted_at=NOW() WHERE id=$1`, f.subscriptionID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE user_platform_quotas SET deleted_at=NOW() WHERE id=$1`, platformQuotaID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='failed' WHERE public_task_id=$1`, f.publicID)
	require.NoError(t, err)
	result, err := repo.RefundFailed(ctx, &service.VideoTaskSettlementRefundCommand{PublicTaskID: f.publicID})
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.NotNil(t, result.PostState.Subscription)
	require.NotNil(t, result.PostState.Platform)
	require.Zero(t, result.PostState.Subscription.DailyUsage)
	require.Zero(t, result.PostState.Platform.DailyUsage)
	var subscriptionUsage, platformUsage float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM user_subscriptions WHERE id=$1`, f.subscriptionID).Scan(&subscriptionUsage))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM user_platform_quotas WHERE id=$1`, platformQuotaID).Scan(&platformUsage))
	require.Zero(t, subscriptionUsage)
	require.Zero(t, platformUsage)
	assertSettlementEventCount(t, f.publicID, "refund", 1)
}

func TestVideoTaskSettlementRepository_AccountResetGuardAndRefundOutbox(t *testing.T) {
	ctx := context.Background()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	f := newVideoSettlementFixture(t, false, false)
	effects := service.VideoTaskBillingEffects{BalanceCost: 3, AccountQuotaCost: 3, ChargedAt: time.Now().UTC()}
	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 3, Effects: effects})
	require.NoError(t, err)
	markVideoTaskProviderAccepted(t, f.publicID)
	_, err = repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID, ActualCostUSD: 3})
	require.NoError(t, err)
	newPeriod := time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339Nano)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=extra||jsonb_build_object('quota_daily_used',7,'quota_weekly_used',7,'quota_daily_start',$1::text,'quota_weekly_start',$1::text) WHERE id=$2`, newPeriod, f.accountID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='failed' WHERE public_task_id=$1`, f.publicID)
	require.NoError(t, err)
	first, err := repo.RefundFailed(ctx, &service.VideoTaskSettlementRefundCommand{PublicTaskID: f.publicID})
	require.NoError(t, err)
	require.True(t, first.Applied)
	require.Equal(t, 7.0, first.PostState.Account.DailyUsed)
	require.Equal(t, 7.0, first.PostState.Account.WeeklyUsed)
	require.Zero(t, first.PostState.Account.TotalUsed)
	second, err := repo.RefundFailed(ctx, &service.VideoTaskSettlementRefundCommand{PublicTaskID: f.publicID})
	require.NoError(t, err)
	require.False(t, second.Applied)
	var outboxCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM scheduler_outbox WHERE event_type=$1 AND account_id=$2`, service.SchedulerOutboxEventAccountChanged, f.accountID).Scan(&outboxCount))
	require.Equal(t, 1, outboxCount)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_task_cache_invalidation_jobs j JOIN video_task_settlements s ON s.id=j.settlement_id JOIN video_tasks t ON t.id=s.video_task_id WHERE t.public_task_id=$1 AND j.event_type='refund'`, f.publicID).Scan(&outboxCount))
	require.Equal(t, 1, outboxCount)
	claims, err := repo.ClaimDueCacheInvalidation(ctx, 10000, "cache-worker", time.Minute)
	require.NoError(t, err)
	var refundClaim *service.VideoTaskCacheInvalidationClaim
	for i := range claims {
		if claims[i].EventType == "refund" && claims[i].UserID == f.userID {
			refundClaim = &claims[i]
			break
		}
	}
	require.NotNil(t, refundClaim)
	require.Equal(t, f.apiKeyID, refundClaim.APIKeyID)
	completed, err := repo.CompleteCacheInvalidation(ctx, refundClaim.JobID, refundClaim.LeaseToken)
	require.NoError(t, err)
	require.True(t, completed)
}

func TestVideoTaskSettlementRepository_ClaimCacheInvalidationDoesNotFailOnMalformedPayload(t *testing.T) {
	ctx := context.Background()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	f := newVideoSettlementFixture(t, false, false)
	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{
		PublicTaskID: f.publicID,
		BillingType:  service.BillingTypeBalance,
		GrossCostUSD: 1,
		Effects:      service.VideoTaskBillingEffects{BalanceCost: 1},
	})
	require.NoError(t, err)

	var settlementID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT s.id FROM video_task_settlements s JOIN video_tasks t ON t.id=s.video_task_id WHERE t.public_task_id=$1`, f.publicID).Scan(&settlementID))
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO video_task_cache_invalidation_jobs(settlement_id,event_type,payload,created_at,updated_at) VALUES
		($1,'malformed-test','{"Version":"bad","UserID":7}'::jsonb,NOW()-interval '1 minute',NOW()),
		($1,'valid-test','{"Version":1,"UserID":8,"APIKeyID":9,"BillingType":0,"Effects":{}}'::jsonb,NOW(),NOW())`, settlementID)
	require.NoError(t, err)

	claims, err := repo.ClaimDueCacheInvalidation(ctx, 10000, "malformed-worker", time.Minute)
	require.NoError(t, err)
	var malformed, valid *service.VideoTaskCacheInvalidationClaim
	for i := range claims {
		switch claims[i].EventType {
		case "malformed-test":
			malformed = &claims[i]
		case "valid-test":
			valid = &claims[i]
		}
	}
	require.NotNil(t, malformed)
	require.NotEmpty(t, malformed.Payload)
	require.Zero(t, malformed.UserID)
	require.NotNil(t, valid)
	require.Equal(t, int64(8), valid.UserID)
	require.Equal(t, int64(9), valid.APIKeyID)
}

func TestVideoTaskLegacyProbeCompensation(t *testing.T) {
	ctx := context.Background()
	compensationSQL, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "ops", "2026-07-13-compensate-per-request-video-probe.sql"))
	require.NoError(t, err)

	conn, err := integrationDB.Conn(ctx)
	require.NoError(t, err)
	schema := fmt.Sprintf("legacy_probe_compensation_%d", time.Now().UnixNano())
	_, err = conn.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA %s", schema))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		_ = conn.Close()
		_, cleanupErr := integrationDB.ExecContext(context.Background(), fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema))
		require.NoError(t, cleanupErr)
	})
	for _, table := range []string{
		"groups", "users", "accounts", "api_keys", "usage_logs", "video_tasks", "user_subscriptions", "user_platform_quotas",
		"video_task_settlements", "video_task_settlement_events", "video_task_refund_reporting_jobs", "video_task_cache_invalidation_jobs",
	} {
		_, err = conn.ExecContext(ctx, fmt.Sprintf("CREATE TABLE %s.%s (LIKE public.%s INCLUDING ALL)", schema, table, table))
		require.NoError(t, err, table)
	}
	_, err = conn.ExecContext(ctx, fmt.Sprintf("SET search_path TO %s, public", schema))
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `
		INSERT INTO groups (id,name,platform) VALUES (3001,'legacy-probe-group','openai');
		INSERT INTO users (id,email,password_hash,balance,frozen_balance) VALUES (24,'legacy-probe@example.com','hash',100,3);
		INSERT INTO accounts (id,name,platform,type,credentials,extra) VALUES
			(221,'legacy-probe-account','openai','apikey','{}'::jsonb,'{"quota_used":12.5,"quota_daily_used":7,"quota_weekly_used":8}'::jsonb);
		INSERT INTO api_keys (id,user_id,key,name,group_id,quota,quota_used,usage_5h,usage_1d,usage_7d) VALUES
			(60,24,'sk-legacy-probe','legacy-probe',3001,100,12.5,7,8,9);
		INSERT INTO user_platform_quotas (user_id,platform,daily_usage_usd,weekly_usage_usd,monthly_usage_usd) VALUES
			(24,'openai',12.5,13.5,14.5);
		INSERT INTO user_subscriptions (id,user_id,group_id,starts_at,expires_at,daily_usage_usd,weekly_usage_usd,monthly_usage_usd) VALUES
			(4001,24,3001,NOW()-INTERVAL '1 day',NOW()+INTERVAL '30 days',15.5,16.5,17.5);
		INSERT INTO usage_logs
			(id,user_id,api_key_id,account_id,request_id,model,total_cost,actual_cost,account_stats_cost,account_rate_multiplier,billing_type,billing_mode,created_at)
		VALUES
			(70697,24,60,221,'legacy-probe-request','video-ds-2.0-fast',65,6.9225,4.615,0.071,0,'per_request',NOW()),
			(70698,25,60,221,'legacy-probe-corrupt-request','video-ds-2.0-fast',65,6.9225,4.615,0.071,0,'per_request',NOW());
		INSERT INTO video_tasks
			(public_task_id,provider,platform,user_id,api_key_id,group_id,account_id,requested_model,upstream_model,billing_model,status,prompt,request_hash,result_metadata,usage_log_id)
		VALUES
			('task_32d91ebd2001e678fca0ad8310067765','test','openai_video',24,60,3001,221,'video-ds-2.0-fast','video-ds-2.0-fast','video-ds-2.0-fast','failed','probe','legacy-probe-hash',jsonb_build_object('request_id','legacy-probe-request-mismatch'),NULL),
			('task_legacy_probe_corrupt_direct','test','openai_video',25,60,3001,221,'video-ds-2.0-fast','video-ds-2.0-fast','video-ds-2.0-fast','failed','probe','legacy-probe-corrupt-hash',jsonb_build_object('request_id','legacy-probe-corrupt-request'),70697)`)
	require.NoError(t, err)

	var balanceBefore, apiQuotaBefore, apiUsage5hBefore, apiUsage1dBefore, apiUsage7dBefore float64
	var accountQuotaBefore, accountDailyBefore, accountWeeklyBefore float64
	var platformDailyBefore, platformWeeklyBefore, platformMonthlyBefore float64
	var subscriptionDailyBefore, subscriptionWeeklyBefore, subscriptionMonthlyBefore float64
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=24`).Scan(&balanceBefore))
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT quota_used,usage_5h,usage_1d,usage_7d FROM api_keys WHERE id=60`).Scan(&apiQuotaBefore, &apiUsage5hBefore, &apiUsage1dBefore, &apiUsage7dBefore))
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT (extra->>'quota_used')::numeric,(extra->>'quota_daily_used')::numeric,(extra->>'quota_weekly_used')::numeric FROM accounts WHERE id=221`).Scan(&accountQuotaBefore, &accountDailyBefore, &accountWeeklyBefore))
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_platform_quotas WHERE user_id=24 AND platform='openai'`).Scan(&platformDailyBefore, &platformWeeklyBefore, &platformMonthlyBefore))
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE id=4001`).Scan(&subscriptionDailyBefore, &subscriptionWeeklyBefore, &subscriptionMonthlyBefore))

	assertCompensationAbort := func() {
		_, execErr := conn.ExecContext(ctx, string(compensationSQL))
		require.Error(t, execErr)
		_, rollbackErr := conn.ExecContext(ctx, "ROLLBACK")
		require.NoError(t, rollbackErr)
		var balanceAfterAbort float64
		var settlementsAfterAbort, eventsAfterAbort, reportingAfterAbort, cacheAfterAbort int
		require.NoError(t, conn.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=24`).Scan(&balanceAfterAbort))
		require.NoError(t, conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_task_settlements`).Scan(&settlementsAfterAbort))
		require.NoError(t, conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_task_settlement_events`).Scan(&eventsAfterAbort))
		require.NoError(t, conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_task_refund_reporting_jobs`).Scan(&reportingAfterAbort))
		require.NoError(t, conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_task_cache_invalidation_jobs`).Scan(&cacheAfterAbort))
		require.Equal(t, balanceBefore, balanceAfterAbort)
		require.Zero(t, settlementsAfterAbort)
		require.Zero(t, eventsAfterAbort)
		require.Zero(t, reportingAfterAbort)
		require.Zero(t, cacheAfterAbort)
	}
	assertCompensationAbort()

	_, err = conn.ExecContext(ctx, `UPDATE video_tasks SET result_metadata=jsonb_build_object('request_id','legacy-probe-request') WHERE public_task_id='task_32d91ebd2001e678fca0ad8310067765'`)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `UPDATE usage_logs SET billing_mode=NULL WHERE id=70697`)
	require.NoError(t, err)
	assertCompensationAbort()
	_, err = conn.ExecContext(ctx, `UPDATE usage_logs SET billing_mode='per_request' WHERE id=70697`)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `UPDATE video_tasks SET group_id=3002 WHERE public_task_id='task_32d91ebd2001e678fca0ad8310067765'`)
	require.NoError(t, err)
	assertCompensationAbort()
	_, err = conn.ExecContext(ctx, `UPDATE video_tasks SET group_id=3001 WHERE public_task_id='task_32d91ebd2001e678fca0ad8310067765'`)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `UPDATE video_tasks SET platform='openai' WHERE public_task_id='task_32d91ebd2001e678fca0ad8310067765'`)
	require.NoError(t, err)
	assertCompensationAbort()
	_, err = conn.ExecContext(ctx, `UPDATE video_tasks SET platform='openai_video' WHERE public_task_id='task_32d91ebd2001e678fca0ad8310067765'`)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `UPDATE video_tasks SET subscription_id=4001 WHERE public_task_id='task_32d91ebd2001e678fca0ad8310067765'`)
	require.NoError(t, err)
	assertCompensationAbort()
	_, err = conn.ExecContext(ctx, `UPDATE video_tasks SET subscription_id=NULL WHERE public_task_id='task_32d91ebd2001e678fca0ad8310067765'`)
	require.NoError(t, err)
	auditSQL, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "ops", "2026-07-13-audit-failed-per-request-video.sql"))
	require.NoError(t, err)
	candidates, exclusions := readLegacyProbeAuditResults(t, ctx, conn, string(auditSQL))
	require.Len(t, candidates, 1)
	require.Equal(t, "task_32d91ebd2001e678fca0ad8310067765", candidates[0].publicTaskID)
	require.Equal(t, int64(70697), candidates[0].usageLogID)
	require.Equal(t, int64(24), candidates[0].userID)
	require.Equal(t, int64(60), candidates[0].apiKeyID)
	require.Equal(t, int64(221), candidates[0].accountID)
	require.InDelta(t, 65, candidates[0].grossCost, 1e-9)
	require.InDelta(t, 6.9225, candidates[0].customerCost, 1e-9)
	require.InDelta(t, 4.615, candidates[0].accountCost, 1e-9)
	require.Len(t, exclusions, 1)
	require.Equal(t, "task_legacy_probe_corrupt_direct", exclusions[0].publicTaskID)
	require.Equal(t, "no_reconstructable_usage_link", exclusions[0].reason)
	require.False(t, exclusions[0].usageLogID.Valid)

	_, err = conn.ExecContext(ctx, string(compensationSQL))
	require.NoError(t, err)

	var balanceAfter, apiQuotaAfter, apiUsage5hAfter, apiUsage1dAfter, apiUsage7dAfter float64
	var accountQuotaAfter, accountDailyAfter, accountWeeklyAfter float64
	var platformDailyAfter, platformWeeklyAfter, platformMonthlyAfter float64
	var subscriptionDailyAfter, subscriptionWeeklyAfter, subscriptionMonthlyAfter float64
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=24`).Scan(&balanceAfter))
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT quota_used,usage_5h,usage_1d,usage_7d FROM api_keys WHERE id=60`).Scan(&apiQuotaAfter, &apiUsage5hAfter, &apiUsage1dAfter, &apiUsage7dAfter))
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT (extra->>'quota_used')::numeric,(extra->>'quota_daily_used')::numeric,(extra->>'quota_weekly_used')::numeric FROM accounts WHERE id=221`).Scan(&accountQuotaAfter, &accountDailyAfter, &accountWeeklyAfter))
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_platform_quotas WHERE user_id=24 AND platform='openai'`).Scan(&platformDailyAfter, &platformWeeklyAfter, &platformMonthlyAfter))
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE id=4001`).Scan(&subscriptionDailyAfter, &subscriptionWeeklyAfter, &subscriptionMonthlyAfter))
	require.InDelta(t, balanceBefore+6.9225, balanceAfter, 1e-9)
	require.Equal(t, apiQuotaBefore, apiQuotaAfter)
	require.Equal(t, apiUsage5hBefore, apiUsage5hAfter)
	require.Equal(t, apiUsage1dBefore, apiUsage1dAfter)
	require.Equal(t, apiUsage7dBefore, apiUsage7dAfter)
	require.Equal(t, accountQuotaBefore, accountQuotaAfter)
	require.Equal(t, accountDailyBefore, accountDailyAfter)
	require.Equal(t, accountWeeklyBefore, accountWeeklyAfter)
	require.Equal(t, platformDailyBefore, platformDailyAfter)
	require.Equal(t, platformWeeklyBefore, platformWeeklyAfter)
	require.Equal(t, platformMonthlyBefore, platformMonthlyAfter)
	require.Equal(t, subscriptionDailyBefore, subscriptionDailyAfter)
	require.Equal(t, subscriptionWeeklyBefore, subscriptionWeeklyAfter)
	require.Equal(t, subscriptionMonthlyBefore, subscriptionMonthlyAfter)

	var refundedCost, refundedTotal, refundedAccount float64
	var refundReason sql.NullString
	var refundedAt sql.NullTime
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT refunded_cost,refunded_total_cost,refunded_account_cost,refund_reason,refunded_at FROM usage_logs WHERE id=70697`).Scan(&refundedCost, &refundedTotal, &refundedAccount, &refundReason, &refundedAt))
	require.InDelta(t, 6.9225, refundedCost, 1e-9)
	require.InDelta(t, 65, refundedTotal, 1e-9)
	require.InDelta(t, 4.615, refundedAccount, 1e-9)
	require.True(t, refundReason.Valid)
	require.Contains(t, refundReason.String, "Legacy per-request video failure compensation")
	require.True(t, refundedAt.Valid)

	var taskUsageLogID, taskID, settlementID int64
	var settlementState, chargeRequestID, pricingSnapshot, effectSnapshot string
	var grossCost, actualCost, accountCost, settlementRefund float64
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT t.id,t.usage_log_id,s.id,s.state,s.charge_request_id,s.gross_cost_usd,s.actual_cost_usd,s.account_cost_usd,s.refunded_cost_usd,s.pricing_snapshot::text,s.effect_snapshot::text
		FROM video_tasks t JOIN video_task_settlements s ON s.video_task_id=t.id WHERE t.public_task_id='task_32d91ebd2001e678fca0ad8310067765'`).Scan(
		&taskID, &taskUsageLogID, &settlementID, &settlementState, &chargeRequestID, &grossCost, &actualCost, &accountCost, &settlementRefund, &pricingSnapshot, &effectSnapshot,
	))
	require.Equal(t, int64(70697), taskUsageLogID)
	require.Equal(t, "refunded", settlementState)
	require.Equal(t, "legacy_per_request_video_probe_compensation:task_32d91ebd2001e678fca0ad8310067765", chargeRequestID)
	require.InDelta(t, 65, grossCost, 1e-9)
	require.InDelta(t, 6.9225, actualCost, 1e-9)
	require.InDelta(t, 4.615, accountCost, 1e-9)
	require.InDelta(t, 6.9225, settlementRefund, 1e-9)
	require.JSONEq(t, `{"billing_mode":"per_request","compensation_kind":"legacy_per_request_compensation","source_usage_log_id":70697}`, pricingSnapshot)
	require.JSONEq(t, `{"balance_cost":0,"subscription_cost":0,"api_key_quota_cost":0,"api_key_rate_limit_cost":0,"account_quota_cost":0,"platform_quota_cost":0,"account_stats_cost":0}`, effectSnapshot)

	var events, reportingJobs, cacheJobs int
	var eventType, eventMetadata, cachePayload string
	var eventAmount float64
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_task_settlement_events WHERE settlement_id=$1 AND event_type='legacy_refund'`, settlementID).Scan(&events))
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT event_type,amount_usd,metadata::text FROM video_task_settlement_events WHERE settlement_id=$1`, settlementID).Scan(&eventType, &eventAmount, &eventMetadata))
	require.Equal(t, 1, events)
	require.Equal(t, "legacy_refund", eventType)
	require.InDelta(t, 6.9225, eventAmount, 1e-9)
	require.JSONEq(t, fmt.Sprintf(`{"event_id":"legacy_per_request_video_probe_refund:task_32d91ebd2001e678fca0ad8310067765","guard_version":1,"omitted_effects":["api_key_quota","api_key_rate_windows","account_quota","subscription_quota","platform_quota"],"target_ids":{"account_id":221,"api_key_id":60,"usage_log_id":70697,"user_id":24,"video_task_id":%d}}`, taskID), eventMetadata)
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_task_refund_reporting_jobs WHERE settlement_id=$1 AND usage_log_id=70697`, settlementID).Scan(&reportingJobs))
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_task_cache_invalidation_jobs WHERE settlement_id=$1 AND event_type='legacy_refund'`, settlementID).Scan(&cacheJobs))
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT payload::text FROM video_task_cache_invalidation_jobs WHERE settlement_id=$1 AND event_type='legacy_refund'`, settlementID).Scan(&cachePayload))
	require.Equal(t, 1, reportingJobs)
	require.Equal(t, 1, cacheJobs)
	require.JSONEq(t, `{"Version":1,"UserID":24,"APIKeyID":60,"GroupID":3001,"Platform":"openai_video","BillingType":0,"Effects":{"balance_cost":0,"subscription_cost":0,"api_key_quota_cost":0,"api_key_rate_limit_cost":0,"account_quota_cost":0,"platform_quota_cost":0,"account_stats_cost":0}}`, cachePayload)

	workerDB, err := sql.Open("postgres", integrationDSN)
	require.NoError(t, err)
	workerDB.SetMaxOpenConns(1)
	workerDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = workerDB.Close() })
	_, err = workerDB.ExecContext(ctx, fmt.Sprintf("SET search_path TO %s, public", schema))
	require.NoError(t, err)
	workerRepo := NewVideoTaskSettlementRepository(nil, workerDB)
	billingPort := NewBillingCache(testRedis(t))
	billingService := service.NewBillingCacheService(billingPort, nil, nil, nil, nil, nil, &config.Config{}, nil)
	t.Cleanup(billingService.Stop)
	require.NoError(t, billingPort.SetUserBalance(ctx, 24, balanceAfter))
	require.NoError(t, billingPort.SetAPIKeyRateLimit(ctx, 60, &service.APIKeyRateLimitCacheData{Usage5h: 7, Usage1d: 8, Usage7d: 9}))
	dashboardRepo := &legacyProbeDashboardRepository{}
	dashboardCache := &refundReportingDashboardCache{}
	settlementService := service.NewVideoTaskSettlementService(workerRepo, nil, nil, billingService, nil, service.NewDashboardAggregationService(dashboardRepo, nil, &config.Config{}), dashboardCache)
	worker := service.NewVideoTaskSettlementReconciler(workerRepo, settlementService)
	require.NoError(t, worker.ReconcileCacheInvalidationOnce(ctx, time.Now()))
	_, cacheReadErr := billingPort.GetUserBalance(ctx, 24)
	require.ErrorIs(t, cacheReadErr, redis.Nil)
	_, rateReadErr := billingPort.GetAPIKeyRateLimit(ctx, 60)
	require.NoError(t, rateReadErr, "zero effects must not invalidate the API key rate cache")
	var cacheCompleted sql.NullTime
	var cacheAttempts int
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT completed_at,attempts FROM video_task_cache_invalidation_jobs WHERE settlement_id=$1 AND event_type='legacy_refund'`, settlementID).Scan(&cacheCompleted, &cacheAttempts))
	require.True(t, cacheCompleted.Valid)
	require.Equal(t, 1, cacheAttempts)

	require.NoError(t, worker.ReconcileRefundReportingOnce(ctx, time.Now()))
	require.Equal(t, 1, dashboardRepo.recomputes)
	require.Equal(t, 1, dashboardCache.invalidations)
	var reportingCompleted sql.NullTime
	var reportingAttempts int
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT completed_at,attempts FROM video_task_refund_reporting_jobs WHERE settlement_id=$1 AND usage_log_id=70697`, settlementID).Scan(&reportingCompleted, &reportingAttempts))
	require.True(t, reportingCompleted.Valid)
	require.Equal(t, 1, reportingAttempts)

	_, err = conn.ExecContext(ctx, string(compensationSQL))
	require.Error(t, err)
	_, rollbackErr := conn.ExecContext(ctx, "ROLLBACK")
	require.NoError(t, rollbackErr)
	var balanceAfterRetry float64
	var settlementsAfterRetry, eventsAfterRetry, reportingAfterRetry, cacheAfterRetry int
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=24`).Scan(&balanceAfterRetry))
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_task_settlements`).Scan(&settlementsAfterRetry))
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_task_settlement_events WHERE event_type='legacy_refund'`).Scan(&eventsAfterRetry))
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_task_refund_reporting_jobs`).Scan(&reportingAfterRetry))
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_task_cache_invalidation_jobs WHERE event_type='legacy_refund'`).Scan(&cacheAfterRetry))
	require.Equal(t, balanceAfter, balanceAfterRetry)
	require.Equal(t, 1, settlementsAfterRetry)
	require.Equal(t, 1, eventsAfterRetry)
	require.Equal(t, 1, reportingAfterRetry)
	require.Equal(t, 1, cacheAfterRetry)
}

func TestVideoTaskSettlementRepository_DeterministicEventIDs(t *testing.T) {
	ctx := context.Background()
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	f := newVideoSettlementFixture(t, false, false)
	_, err := repo.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 3, Effects: service.VideoTaskBillingEffects{BalanceCost: 3}})
	require.NoError(t, err)
	markVideoTaskProviderAccepted(t, f.publicID)
	_, err = repo.Capture(ctx, &service.VideoTaskSettlementCaptureCommand{PublicTaskID: f.publicID, ActualCostUSD: 3})
	require.NoError(t, err)
	rows, err := integrationDB.QueryContext(ctx, `SELECT event_type,metadata->>'event_id' FROM video_task_settlement_events e JOIN video_task_settlements s ON s.id=e.settlement_id JOIN video_tasks t ON t.id=s.video_task_id WHERE t.public_task_id=$1 ORDER BY event_type`, f.publicID)
	require.NoError(t, err)
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var action, id string
		require.NoError(t, rows.Scan(&action, &id))
		got[action] = id
	}
	require.NoError(t, rows.Err())
	require.Equal(t, "video:"+f.publicID+":reserve", got["reserve"])
	require.Equal(t, "video:"+f.publicID+":capture", got["capture"])
}

func markVideoTaskProviderAccepted(t *testing.T, publicID string) {
	t.Helper()
	_, err := integrationDB.ExecContext(context.Background(), `UPDATE video_tasks SET status='queued',upstream_task_id=COALESCE(upstream_task_id,'test-upstream'),provider_status='queued',submitted_at=COALESCE(submitted_at,NOW()) WHERE public_task_id=$1`, publicID)
	require.NoError(t, err)
}

func runConcurrentSettlementCalls(t *testing.T, call func() (bool, error)) []bool {
	t.Helper()
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); applied, err := call(); results <- applied; errs <- err }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var values []bool
	for applied := range results {
		values = append(values, applied)
	}
	return values
}

func assertSettlementEventCount(t *testing.T, publicID, action string, want int) {
	t.Helper()
	var count int
	require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM video_task_settlement_events e JOIN video_task_settlements s ON s.id=e.settlement_id JOIN video_tasks v ON v.id=s.video_task_id WHERE v.public_task_id=$1 AND e.event_type=$2`, publicID, action).Scan(&count))
	require.Equal(t, want, count)
}

func assertSettlementEventID(t *testing.T, publicID, action string) {
	t.Helper()
	var id string
	require.NoError(t, integrationDB.QueryRow(`SELECT e.metadata->>'event_id' FROM video_task_settlement_events e JOIN video_task_settlements s ON s.id=e.settlement_id JOIN video_tasks v ON v.id=s.video_task_id WHERE v.public_task_id=$1 AND e.event_type=$2`, publicID, action).Scan(&id))
	require.Equal(t, "video:"+publicID+":"+action, id)
	assertSettlementEventCount(t, publicID, action, 1)
}

func assertUserMoney(t *testing.T, userID int64, balance, frozen float64) {
	t.Helper()
	var gotBalance, gotFrozen float64
	require.NoError(t, integrationDB.QueryRow(`SELECT balance, frozen_balance FROM users WHERE id=$1`, userID).Scan(&gotBalance, &gotFrozen))
	require.InDelta(t, balance, gotBalance, 1e-9)
	require.InDelta(t, frozen, gotFrozen, 1e-9)
}

func assertSubscriptionUsage(t *testing.T, subscriptionID int64, want float64) {
	t.Helper()
	var daily, weekly, monthly float64
	require.NoError(t, integrationDB.QueryRow(`SELECT daily_usage_usd, weekly_usage_usd, monthly_usage_usd FROM user_subscriptions WHERE id=$1`, subscriptionID).Scan(&daily, &weekly, &monthly))
	require.InDelta(t, want, daily, 1e-9)
	require.InDelta(t, want, weekly, 1e-9)
	require.InDelta(t, want, monthly, 1e-9)
}
