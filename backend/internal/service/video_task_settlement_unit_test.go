//go:build unit

package service

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type settlementDashboardRecomputeStub struct{ calls [][2]time.Time }

func (s *settlementDashboardRecomputeStub) RecomputeRange(_ context.Context, start, end time.Time) error {
	s.calls = append(s.calls, [2]time.Time{start, end})
	return nil
}

type settlementDashboardCacheStub struct{ deletes int }

type settlementInvalidationCacheStub struct {
	balanceUsers   []int64
	subscriptions  [][2]int64
	rateLimitKeys  []int64
	platformQuotas []UserPlatformQuotaKey
	err            error
}

type strictAuthInvalidatorStub struct {
	err         error
	strictCalls int
}

func (s *strictAuthInvalidatorStub) InvalidateAuthCacheByKey(context.Context, string)    {}
func (s *strictAuthInvalidatorStub) InvalidateAuthCacheByUserID(context.Context, int64)  {}
func (s *strictAuthInvalidatorStub) InvalidateAuthCacheByGroupID(context.Context, int64) {}
func (s *strictAuthInvalidatorStub) InvalidateAuthCacheByUserIDStrict(context.Context, int64) error {
	s.strictCalls++
	return s.err
}

func (s *settlementInvalidationCacheStub) InvalidateUserBalance(_ context.Context, id int64) error {
	s.balanceUsers = append(s.balanceUsers, id)
	return s.err
}
func (s *settlementInvalidationCacheStub) InvalidateSubscription(_ context.Context, userID, groupID int64) error {
	s.subscriptions = append(s.subscriptions, [2]int64{userID, groupID})
	return s.err
}
func (s *settlementInvalidationCacheStub) InvalidateAPIKeyRateLimit(_ context.Context, id int64) error {
	s.rateLimitKeys = append(s.rateLimitKeys, id)
	return s.err
}
func (s *settlementInvalidationCacheStub) InvalidateUserPlatformQuota(_ context.Context, userID int64, platform string) error {
	s.platformQuotas = append(s.platformQuotas, UserPlatformQuotaKey{UserID: userID, Platform: platform})
	return s.err
}
func (s *settlementInvalidationCacheStub) HasUserPlatformQuotaLimit(context.Context, int64, string) bool {
	return false
}

func (s *settlementDashboardCacheStub) GetDashboardStats(context.Context) (string, error) {
	return "", nil
}

type orderedRefundReportingDashboard struct {
	events *[]string
	err    error
}

func (s *orderedRefundReportingDashboard) RecomputeRange(context.Context, time.Time, time.Time) error {
	*s.events = append(*s.events, "recompute")
	return s.err
}

type orderedRefundReportingCache struct {
	events *[]string
	err    error
}

func (s *orderedRefundReportingCache) GetDashboardStats(context.Context) (string, error) {
	return "", nil
}
func (s *orderedRefundReportingCache) SetDashboardStats(context.Context, string, time.Duration) error {
	return nil
}
func (s *orderedRefundReportingCache) DeleteDashboardStats(context.Context) error {
	*s.events = append(*s.events, "invalidate")
	return s.err
}
func (s *settlementDashboardCacheStub) SetDashboardStats(context.Context, string, time.Duration) error {
	return nil
}
func (s *settlementDashboardCacheStub) DeleteDashboardStats(context.Context) error {
	s.deletes++
	return nil
}

func TestNormalizeVideoTaskSettlementAmount_NUMERIC20_10Bounds(t *testing.T) {
	input := math.Nextafter(10000000000, 0)
	inside, err := NormalizeVideoTaskSettlementAmount(input)
	require.NoError(t, err)
	require.Less(t, inside, 10000000000.0)

	_, err = NormalizeVideoTaskSettlementAmount(10000000000)
	require.ErrorIs(t, err, ErrVideoTaskSettlementInvalidAmount)
}

func TestNormalizeVideoTaskSettlementAmount_AppliedFundingUsesEightDecimals(t *testing.T) {
	got, err := NormalizeVideoTaskSettlementAmount(1.1234567891)
	require.NoError(t, err)
	require.Equal(t, 1.12345679, got)
}

func TestNormalizeVideoTaskPricingAmount_PreservesTenDecimals(t *testing.T) {
	got, err := NormalizeVideoTaskPricingAmount(1.1234567891)
	require.NoError(t, err)
	require.Equal(t, 1.1234567891, got)

	_, err = NormalizeVideoTaskPricingAmount(10000000000)
	require.ErrorIs(t, err, ErrVideoTaskSettlementInvalidAmount)
}

func TestVideoTaskBillingEffectsNormalizeAccountCostToEightDecimals(t *testing.T) {
	effects, err := (VideoTaskBillingEffects{AccountStatsCost: 0.1234567892}).Normalize()
	require.NoError(t, err)
	require.Equal(t, 0.12345679, effects.AccountStatsCost)
}

func TestVideoTaskSettlementAfterCommit_LeavesRefundReportingToDurableWorker(t *testing.T) {
	recompute := &settlementDashboardRecomputeStub{}
	dashboardCache := &settlementDashboardCacheStub{}
	svc := &VideoTaskSettlementService{dashboard: recompute, dashboardCache: dashboardCache}
	createdAt := time.Date(2026, 7, 12, 10, 17, 0, 0, time.UTC)
	result := &VideoTaskSettlementResult{Applied: true, RefundUsageCreatedAt: &createdAt}

	svc.afterCommit(t.Context(), result)
	svc.afterCommit(t.Context(), &VideoTaskSettlementResult{Applied: false, RefundUsageCreatedAt: &createdAt})

	require.Empty(t, recompute.calls)
	require.Zero(t, dashboardCache.deletes)
}

func TestVideoTaskSettlementPreparePerRequestUsageOmitsVideoFields(t *testing.T) {
	apiKey := videoTaskTestAPIKey()
	quote := VideoTaskQuote{
		BillingMode: BillingModePerRequest, BillingModel: "seedance",
		Effective:    VideoTaskEffectiveParams{Seconds: 5, Resolution: "1080p", VideoCount: 1},
		UnitPriceUSD: 65, GrossCostUSD: 65, ActualCostUSD: 6.9225,
		AccountUnitPriceUSD: 65, AccountBaseCostUSD: 65, AccountCostUSD: 65,
		RateMultiplier: 0.1065, AccountRateMultiplier: 1,
	}
	svc := &VideoTaskSettlementService{repo: &videoTaskSettlementRepoStub{}, tasks: newFakeVideoTaskRepository(nil)}

	command, err := svc.Prepare(t.Context(), VideoTaskSettlementCreateInput{
		PublicTaskID: "task_per_request",
		Quote:        quote,
		Params:       VideoTaskCreateParams{APIKey: apiKey, User: &User{ID: 7}},
		Account:      &Account{ID: 99},
	})

	require.NoError(t, err)
	require.Equal(t, string(BillingModePerRequest), command.Admission.UsageMetadata["billing_mode"])
	usage := command.Admission.UsageLog
	require.NotNil(t, usage.BillingMode)
	require.Equal(t, string(BillingModePerRequest), *usage.BillingMode)
	require.Zero(t, usage.VideoCount)
	require.Nil(t, usage.VideoResolution)
	require.Nil(t, usage.VideoDurationSeconds)
}

func TestVideoTaskSettlementPrepareUsagePreservesRequestedModelAlias(t *testing.T) {
	apiKey := videoTaskTestAPIKey()
	quote := VideoTaskQuote{
		BillingMode: BillingModeVideo, BillingModel: "provider-sora",
		Effective:    VideoTaskEffectiveParams{Seconds: 5, Resolution: "720p", VideoCount: 1},
		UnitPriceUSD: 0.08, GrossCostUSD: 0.4, ActualCostUSD: 0.4,
		AccountUnitPriceUSD: 0.08, AccountBaseCostUSD: 0.4, AccountCostUSD: 0.4,
		RateMultiplier: 1, AccountRateMultiplier: 1,
	}
	svc := &VideoTaskSettlementService{repo: &videoTaskSettlementRepoStub{}, tasks: newFakeVideoTaskRepository(nil)}

	command, err := svc.Prepare(t.Context(), VideoTaskSettlementCreateInput{
		PublicTaskID:   "task_alias",
		Quote:          quote,
		Params:         VideoTaskCreateParams{APIKey: apiKey, User: &User{ID: 7}},
		Account:        &Account{ID: 99},
		RequestedModel: "video-alias",
		UpstreamModel:  "provider-sora",
	})

	require.NoError(t, err)
	usage := command.Admission.UsageLog
	require.Equal(t, "provider-sora", usage.Model)
	require.Equal(t, "video-alias", usage.RequestedModel)
	require.NotNil(t, usage.UpstreamModel)
	require.Equal(t, "provider-sora", *usage.UpstreamModel)
}

func TestVideoTaskSettlementAfterCommitInvalidatesRefundCachesFromCommittedResult(t *testing.T) {
	cache := &settlementInvalidationCacheStub{}
	svc := &VideoTaskSettlementService{cache: cache}
	result := &VideoTaskSettlementResult{
		Applied: true, UserID: 7, APIKeyID: 8, Platform: PlatformOpenAI,
		Settlement: &VideoTaskSettlementSnapshot{BillingType: BillingTypeBalance, GroupID: 9, Effects: VideoTaskBillingEffects{APIKeyRateLimitCost: 1, PlatformQuotaCost: 1}},
		PostState:  VideoTaskSettlementPostState{Platform: &VideoTaskPlatformPostState{UserID: 7, Platform: PlatformOpenAI}},
	}

	svc.afterCommit(t.Context(), result)

	require.Equal(t, []int64{7}, cache.balanceUsers)
	require.Equal(t, []int64{8}, cache.rateLimitKeys)
	require.Equal(t, []UserPlatformQuotaKey{{UserID: 7, Platform: PlatformOpenAI}}, cache.platformQuotas)
}

func TestVideoTaskSettlementProcessCacheInvalidationRetriesAnyCacheFailure(t *testing.T) {
	want := errors.New("redis unavailable")
	cache := &settlementInvalidationCacheStub{err: want}
	svc := &VideoTaskSettlementService{cache: cache}
	claim := VideoTaskCacheInvalidationClaim{UserID: 7, APIKeyID: 8, GroupID: 9, Platform: PlatformOpenAI, BillingType: BillingTypeBalance, Effects: VideoTaskBillingEffects{APIKeyRateLimitCost: 1, PlatformQuotaCost: 1}}

	err := svc.ProcessCacheInvalidation(t.Context(), claim)

	require.ErrorIs(t, err, want)
	require.Equal(t, []int64{7}, cache.balanceUsers)
	require.Equal(t, []int64{8}, cache.rateLimitKeys)
	require.Equal(t, []UserPlatformQuotaKey{{UserID: 7, Platform: PlatformOpenAI}}, cache.platformQuotas)
}

func TestVideoTaskSettlementProcessCacheInvalidationPropagatesAuthFailureThenRetries(t *testing.T) {
	want := errors.New("auth cache unavailable")
	auth := &strictAuthInvalidatorStub{err: want}
	svc := &VideoTaskSettlementService{cache: &settlementInvalidationCacheStub{}, authCache: auth}
	claim := VideoTaskCacheInvalidationClaim{UserID: 7, BillingType: BillingTypeBalance}
	require.ErrorIs(t, svc.ProcessCacheInvalidation(t.Context(), claim), want)
	auth.err = nil
	require.NoError(t, svc.ProcessCacheInvalidation(t.Context(), claim))
	require.Equal(t, 2, auth.strictCalls)
}

func TestVideoTaskSettlementProcessRefundReporting_OrdersAndPropagatesFailures(t *testing.T) {
	createdAt := time.Date(2026, 7, 12, 10, 17, 0, 0, time.UTC)
	claim := VideoTaskRefundReportingClaim{UsageCreatedAt: createdAt}

	t.Run("success recomputes before invalidating", func(t *testing.T) {
		events := []string{}
		svc := &VideoTaskSettlementService{dashboard: &orderedRefundReportingDashboard{events: &events}, dashboardCache: &orderedRefundReportingCache{events: &events}}
		require.NoError(t, svc.ProcessRefundReporting(t.Context(), claim))
		require.Equal(t, []string{"recompute", "invalidate"}, events)
	})

	t.Run("recompute failure does not invalidate", func(t *testing.T) {
		events := []string{}
		want := errors.New("recompute failed")
		svc := &VideoTaskSettlementService{dashboard: &orderedRefundReportingDashboard{events: &events, err: want}, dashboardCache: &orderedRefundReportingCache{events: &events}}
		require.ErrorIs(t, svc.ProcessRefundReporting(t.Context(), claim), want)
		require.Equal(t, []string{"recompute"}, events)
	})

	t.Run("cache failure remains retriable", func(t *testing.T) {
		events := []string{}
		want := errors.New("cache failed")
		svc := &VideoTaskSettlementService{dashboard: &orderedRefundReportingDashboard{events: &events}, dashboardCache: &orderedRefundReportingCache{events: &events, err: want}}
		require.ErrorIs(t, svc.ProcessRefundReporting(t.Context(), claim), want)
		require.Equal(t, []string{"recompute", "invalidate"}, events)
	})
}
