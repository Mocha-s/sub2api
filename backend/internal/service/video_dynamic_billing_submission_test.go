package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestVideoDynamicRateFlowsIntoSubmissionReservation(t *testing.T) {
	for _, mode := range []BillingMode{BillingModePerRequest, BillingModeVideo, BillingModeVideoReference} {
		for _, route := range []string{"direct", "composite dynamic", "composite fixed"} {
			t.Run(string(mode)+"/"+route, func(t *testing.T) {
				const model = "video-billing-regression"
				targetID := int64(19)
				seconds, perRequest, perSecond, accountRate := 5, 8.0, 1.3, 0.1
				cache := newEmptyChannelCache()
				cache.loadedAt = time.Now()
				cache.channelByGroupID[targetID] = &Channel{ID: 6, Status: StatusActive, BillingModelSource: BillingModelSourceRequested}
				cache.groupPlatform[targetID] = PlatformOpenAI
				cache.pricingByGroupModel[channelModelKey{groupID: targetID, platform: PlatformOpenAI, model: model}] = &ChannelModelPricing{
					BillingMode: mode, PerRequestPrice: &perRequest, VideoPricePerSecond: &perSecond, VideoDefaultSeconds: &seconds,
				}
				channels := &ChannelService{}
				channels.cache.Store(cache)
				pricing := &OpenAIGatewayService{channelService: channels}
				account := &Account{ID: 736, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, RateMultiplier: &accountRate,
					Credentials: map[string]any{"pricing_managed_by": "api-pricing-sync", "pricing_markup_factor": 1.5}}
				ctx := context.Background()
				if route != "direct" {
					ctx = WithCompositeBillingGroup(ctx, &Group{ID: 41, Platform: PlatformComposite, Hydrated: true, Status: StatusActive,
						RateMultiplier: 1, DynamicRateBillingEnabled: route == "composite dynamic"})
				}
				key := &APIKey{ID: 215, UserID: 1, GroupID: &targetID, Group: &Group{
					ID: targetID, Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 0.15, AllowVideoGeneration: true}}
				tasks := newFakeVideoTaskRepository(nil)
				provider := &fakeVideoTaskProvider{createResult: &VideoProviderCreateResult{
					ProviderTaskID: "upstream", Status: VideoTaskStatusQueued, RawBody: []byte(`{"id":"upstream","status":"queued"}`)}}
				ledger := &videoTaskSettlementRepoStub{
					reserveResult: &VideoTaskSettlementResult{Applied: true, Settlement: &VideoTaskSettlementSnapshot{State: VideoTaskSettlementReserved}},
					captureResult: &VideoTaskSettlementResult{Applied: true, Settlement: &VideoTaskSettlementSnapshot{State: VideoTaskSettlementCharged}},
				}
				svc := newVideoTaskServiceForTest(tasks, nil, &fakeVideoTaskSelector{selection: &AccountSelectionResult{Account: account}}, provider, nil)
				svc.pricingResolver = pricing
				svc.settlement = &VideoTaskSettlementService{repo: ledger, tasks: tasks}
				result, err := svc.Create(ctx, VideoTaskCreateParams{APIKey: key, User: &User{ID: 1},
					Body: []byte(`{"model":"video-billing-regression","prompt":"test","seconds":5,"resolution":"720p"}`), ContentType: "application/json"})
				require.NoError(t, err)
				require.NotNil(t, result)
				gross, rate := 6.5, 0.15
				if mode == BillingModePerRequest {
					gross = 8
				}
				if route == "composite fixed" {
					rate = 1
				}
				require.InDelta(t, gross*rate, ledger.reserveCommand.ActualCostUSD, 1e-9)
				require.InDelta(t, gross*rate, ledger.reserveCommand.Effects.BalanceCost, 1e-9)
				require.InDelta(t, rate, ledger.reserveCommand.Admission.UsageLog.RateMultiplier, 1e-9)
				quote, ok := videoTaskQuoteFromMetadata(tasks.lastCreate.Metadata)
				require.True(t, ok)
				require.InDelta(t, gross*rate, quote.ActualCostUSD, 1e-9)
				require.InDelta(t, gross*accountRate, quote.AccountCostUSD, 1e-9)
				require.Equal(t, 1, ledger.captureCalls)
				require.Equal(t, 1, provider.createCalls)
			})
		}
	}
}
