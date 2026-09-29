package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func pricedVideoTaskServiceFixture(events *videoTaskServiceTestEvents) (*VideoTaskService, *fakeVideoTaskRepository, *fakeVideoTaskProvider, *fakeVideoTaskSettlementOrchestrator) {
	repo := newFakeVideoTaskRepository(events)
	provider := &fakeVideoTaskProvider{events: events, createResult: &VideoProviderCreateResult{ProviderTaskID: "upstream", Status: VideoTaskStatusQueued, ProviderStatus: "queued", RawBody: []byte(`{"id":"upstream","status":"queued"}`)}}
	selector := &fakeVideoTaskSelector{events: events, selection: &AccountSelectionResult{Account: &Account{ID: 99, Platform: PlatformOpenAI}}}
	price, seconds := 0.08, 5
	pricing := &fakeVideoTaskPricingResolver{events: events, selection: VideoTaskPricingSelection{Pricing: &ChannelModelPricing{BillingMode: BillingModeVideo, VideoPricePerSecond: &price, VideoDefaultSeconds: &seconds}, BillingModel: "seedance", BillingModelSource: BillingModelSourceRequested, RateMultiplier: 0.5, AccountRateMultiplier: 1.25}}
	settlement := &fakeVideoTaskSettlementOrchestrator{events: events, state: VideoTaskSettlementCharged}
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, nil)
	svc.pricingResolver, svc.settlement = pricing, settlement
	return svc, repo, provider, settlement
}

func perRequestVideoTaskServiceFixture(events *videoTaskServiceTestEvents, usage *fakeVideoTaskUsageRecorder) (*VideoTaskService, *fakeVideoTaskRepository, *fakeVideoTaskProvider, *fakeVideoTaskSettlementOrchestrator) {
	repo := newFakeVideoTaskRepository(events)
	provider := &fakeVideoTaskProvider{events: events, createResult: &VideoProviderCreateResult{ProviderTaskID: "upstream", Status: VideoTaskStatusQueued, ProviderStatus: "queued", RawBody: []byte(`{"id":"upstream","status":"queued"}`)}}
	selector := &fakeVideoTaskSelector{events: events, selection: &AccountSelectionResult{Account: &Account{ID: 99, Platform: PlatformOpenAI}}}
	price := 65.0
	pricing := &fakeVideoTaskPricingResolver{events: events, selection: VideoTaskPricingSelection{Pricing: &ChannelModelPricing{BillingMode: BillingModePerRequest, PerRequestPrice: &price}, BillingModel: "seedance", BillingModelSource: BillingModelSourceRequested, RateMultiplier: 0.1065, AccountRateMultiplier: 1}}
	settlement := &fakeVideoTaskSettlementOrchestrator{events: events, state: VideoTaskSettlementCharged}
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, usage)
	svc.pricingResolver, svc.settlement = pricing, settlement
	return svc, repo, provider, settlement
}

func TestVideoTaskCreateImmediateProviderFailurePersistsThenReleasesWithoutCapture(t *testing.T) {
	events := &videoTaskServiceTestEvents{}
	svc, repo, provider, settlement := pricedVideoTaskServiceFixture(events)
	provider.createResult = &VideoProviderCreateResult{ProviderTaskID: "upstream_failed", Status: VideoTaskStatusFailed, ProviderStatus: "failed", RawBody: []byte(`{"id":"upstream_failed","status":"failed"}`)}

	result, err := svc.Create(context.Background(), VideoTaskCreateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: []byte(`{"model":"video-test","prompt":"test","seconds":"5"}`), ContentType: "application/json"})
	require.NoError(t, err)
	require.Equal(t, VideoTaskStatusFailed, result.Task.Status)
	require.Zero(t, settlement.captureCalls)
	require.Equal(t, 1, settlement.reconcilePersistedCalls)
	stored := repo.tasks[result.Task.PublicTaskID]
	require.Equal(t, VideoTaskStatusFailed, stored.Status)
}

func TestVideoTaskServiceCreateVideoPricingSettlesInStrictOrder(t *testing.T) {
	events := &videoTaskServiceTestEvents{}
	svc, _, provider, settlement := pricedVideoTaskServiceFixture(events)
	result, err := svc.Create(context.Background(), VideoTaskCreateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Subscription: &UserSubscription{ID: 11}, Body: []byte(`{"model":"seedance","prompt":"city","seconds":5}`)})
	require.NoError(t, err)
	require.Equal(t, []string{"selector_select", "pricing_resolve", "repo_create", "settlement_reserve", "provider_create", "repo_attach", "settlement_capture", "repo_get_public"}, events.snapshot())
	require.Equal(t, 1, provider.createCalls)
	require.Equal(t, "video:"+result.Task.PublicTaskID+":charge", settlement.reserveInput.RequestID)
	require.InDelta(t, 0.4, settlement.reserveInput.Quote.GrossCostUSD, 1e-12)
	require.InDelta(t, 0.2, settlement.reserveInput.Quote.ActualCostUSD, 1e-12)
}

func TestVideoTaskServiceCreatePerRequestPricingSettlesInStrictOrderWithoutLegacyUsage(t *testing.T) {
	events := &videoTaskServiceTestEvents{}
	usage := &fakeVideoTaskUsageRecorder{events: events}
	svc, _, provider, settlement := perRequestVideoTaskServiceFixture(events, usage)
	body := []byte(`{"model":"seedance","prompt":"city","seconds":"5","duration":10,"duration_seconds":"15"}`)

	result, err := svc.Create(context.Background(), VideoTaskCreateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: body})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, []string{"selector_select", "pricing_resolve", "repo_create", "settlement_reserve", "provider_create", "repo_attach", "settlement_capture", "repo_get_public"}, events.snapshot())
	require.Equal(t, body, provider.createBody)
	require.Equal(t, BillingModePerRequest, settlement.reserveInput.Quote.BillingMode)
	require.InDelta(t, 65, settlement.reserveInput.Quote.GrossCostUSD, 1e-12)
	require.InDelta(t, 6.9225, settlement.reserveInput.Quote.ActualCostUSD, 1e-12)
	require.Zero(t, usage.calls)
}

func TestVideoTaskServiceCreateVideoPricingReserveFailurePreventsProvider(t *testing.T) {
	events := &videoTaskServiceTestEvents{}
	svc, _, provider, settlement := pricedVideoTaskServiceFixture(events)
	settlement.reserveErr = errors.New("reserve failed")
	result, err := svc.Create(context.Background(), VideoTaskCreateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: []byte(`{"model":"seedance","prompt":"city","seconds":5}`)})
	require.Nil(t, result)
	require.ErrorIs(t, err, settlement.reserveErr)
	require.Zero(t, provider.createCalls)
	require.Equal(t, []string{"selector_select", "pricing_resolve", "repo_create", "settlement_reserve"}, events.snapshot())
}

func TestVideoTaskServiceReserveClaimLoserNeverSubmitsOrReleases(t *testing.T) {
	events := &videoTaskServiceTestEvents{}
	svc, _, provider, settlement := pricedVideoTaskServiceFixture(events)
	settlement.reserveNotApplied = true
	settlement.reconcileErr = ErrVideoTaskSettlementRetriable

	result, err := svc.Create(context.Background(), VideoTaskCreateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: []byte(`{"model":"seedance","prompt":"city","seconds":5}`)})

	require.Nil(t, result)
	require.ErrorIs(t, err, ErrVideoTaskSettlementRetriable)
	require.Zero(t, provider.createCalls)
	require.Zero(t, settlement.failAndReleaseCalls)
	require.Equal(t, 1, settlement.reconcileCalls)
}

func TestVideoTaskServiceReplayRetriesAtomicAdmissionOnlyBeforeProvider(t *testing.T) {
	events := &videoTaskServiceTestEvents{}
	svc, tasks, provider, settlement := pricedVideoTaskServiceFixture(events)
	settlement.reserveErr = errors.New("reserve transaction failed")
	params := VideoTaskCreateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: []byte(`{"model":"seedance","prompt":"city","seconds":5}`), IdempotencyKey: "reserve-retry"}
	first, err := svc.Create(context.Background(), params)
	require.Nil(t, first)
	require.ErrorIs(t, err, settlement.reserveErr)
	require.Zero(t, provider.createCalls)

	repo := &videoTaskSettlementRepoStub{getErr: ErrVideoTaskSettlementNotFound, reserveResult: &VideoTaskSettlementResult{Applied: true, Settlement: &VideoTaskSettlementSnapshot{State: VideoTaskSettlementReserved}}, captureResult: &VideoTaskSettlementResult{Applied: true, Settlement: &VideoTaskSettlementSnapshot{State: VideoTaskSettlementCharged, ActualCostUSD: 0.2}}}
	svc.settlement = &VideoTaskSettlementService{repo: repo, tasks: tasks}
	svc.accountLookup = &fakeVideoTaskAccountStore{accounts: map[int64]*Account{99: {ID: 99, Platform: PlatformOpenAI}}}
	second, err := svc.Create(context.Background(), params)

	require.NoError(t, err)
	require.NotNil(t, second)
	require.Equal(t, 1, repo.getCalls)
	require.Equal(t, 1, repo.reserveCalls)
	require.Equal(t, 1, repo.captureCalls)
	require.Equal(t, 1, provider.createCalls)
	repo.getErr = nil
	repo.snapshot = &VideoTaskSettlementSnapshot{State: VideoTaskSettlementCharged}
	third, err := svc.Create(context.Background(), params)
	require.NoError(t, err)
	require.NotNil(t, third)
	require.Equal(t, 1, provider.createCalls)
	require.Equal(t, 1, repo.captureCalls)
}

func TestVideoTaskServicePerRequestReplayReservesPersistedAdmissionWithoutDurationRewrite(t *testing.T) {
	events := &videoTaskServiceTestEvents{}
	usage := &fakeVideoTaskUsageRecorder{events: events}
	svc, tasks, provider, _ := perRequestVideoTaskServiceFixture(events, usage)
	reserveErr := errors.New("reserve transaction failed")
	repo := &videoTaskSettlementRepoStub{getErr: ErrVideoTaskSettlementNotFound, reserveErr: reserveErr}
	svc.settlement = &VideoTaskSettlementService{repo: repo, tasks: tasks}
	svc.accountLookup = &fakeVideoTaskAccountStore{accounts: map[int64]*Account{99: {ID: 99, Platform: PlatformOpenAI}}}
	body := []byte(`{"model":"seedance","prompt":"city","duration":10,"duration_seconds":"15","provider_extra":"keep"}`)
	params := VideoTaskCreateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: body, IdempotencyKey: "per-request-reserve-retry"}

	first, err := svc.Create(context.Background(), params)
	require.Nil(t, first)
	require.ErrorIs(t, err, reserveErr)
	require.Zero(t, provider.createCalls)
	require.Equal(t, 1, repo.reserveCalls)

	repo.reserveErr = nil
	repo.reserveResult = &VideoTaskSettlementResult{Applied: true, Settlement: &VideoTaskSettlementSnapshot{State: VideoTaskSettlementReserved}}
	repo.captureResult = &VideoTaskSettlementResult{Applied: true, Settlement: &VideoTaskSettlementSnapshot{State: VideoTaskSettlementCharged, ActualCostUSD: 6.9225}}
	second, err := svc.Create(context.Background(), params)

	require.NoError(t, err)
	require.NotNil(t, second)
	require.Equal(t, 2, repo.reserveCalls)
	require.Equal(t, 1, repo.captureCalls)
	require.Equal(t, 1, provider.createCalls)
	require.Equal(t, body, provider.createBody)
	require.NotContains(t, string(provider.createBody), `"seconds"`)
	require.NotNil(t, repo.reserveCommand)
	require.InDelta(t, 65, repo.reserveCommand.GrossCostUSD, 1e-12)
	require.InDelta(t, 6.9225, repo.reserveCommand.ActualCostUSD, 1e-12)
	require.Equal(t, string(BillingModePerRequest), repo.reserveCommand.PricingSnapshot["billing_mode"])
	require.NotNil(t, repo.reserveCommand.Admission)
	require.NotNil(t, repo.reserveCommand.Admission.UsageLog)
	require.Equal(t, string(BillingModePerRequest), *repo.reserveCommand.Admission.UsageLog.BillingMode)
	require.Nil(t, repo.reserveCommand.Admission.UsageLog.VideoDurationSeconds)
}

func TestVideoTaskServiceReplayRecoversPerRequestTaskWithoutSynthesizingDuration(t *testing.T) {
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	provider := &fakeVideoTaskProvider{events: events, createResult: &VideoProviderCreateResult{ProviderTaskID: "upstream_recovered", Status: VideoTaskStatusQueued, ProviderStatus: "queued", RawBody: []byte(`{"id":"upstream_recovered","status":"queued"}`)}}
	quote := VideoTaskQuote{
		BillingMode: BillingModePerRequest, BillingModel: "seedance",
		Effective:    VideoTaskEffectiveParams{VideoCount: 1},
		UnitPriceUSD: 65, GrossCostUSD: 65, ActualCostUSD: 6.9225,
		AccountUnitPriceUSD: 65, AccountBaseCostUSD: 65, AccountCostUSD: 65,
		RateMultiplier: 0.1065, AccountRateMultiplier: 1,
	}
	body := []byte(`{"model":"seedance","prompt":"city","provider_extra":"keep"}`)
	req, err := ParseVideoTaskCreateEnvelope(body)
	require.NoError(t, err)
	repo.seedTask(&VideoTask{
		PublicTaskID: "task_per_request_recovery", APIKeyID: 42, UserID: 7, AccountID: 99,
		Model: "seedance", Status: VideoTaskStatusSubmitting, RequestHash: req.RequestHash, RequestBody: body,
		Metadata: map[string]any{
			"idempotency_key":            "per-request-recovery",
			"upstream_model":             "seedance-upstream",
			VideoTaskEndpointMetadataKey: VideoTaskEndpointVideos,
			"request_metadata":           map[string]any{"video_pricing_snapshot": quote},
		},
	})
	settlement := &fakeVideoTaskSettlementOrchestrator{events: events, reconcileErr: ErrVideoTaskAdmissionRecovered}
	svc := newVideoTaskServiceForTest(repo, &fakeVideoTaskAccountStore{events: events, accounts: map[int64]*Account{99: {ID: 99, Platform: PlatformOpenAI}}}, nil, provider, nil)
	svc.settlement = settlement

	result, err := svc.Create(context.Background(), VideoTaskCreateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: body, IdempotencyKey: "per-request-recovery"})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, body, provider.createBody)
	require.NotContains(t, string(provider.createBody), `"seconds"`)
	require.Equal(t, 1, provider.createCalls)
}

func TestVideoTaskServiceRecoveredAdmissionProviderFailureReleases(t *testing.T) {
	events := &videoTaskServiceTestEvents{}
	svc, tasks, provider, settlement := pricedVideoTaskServiceFixture(events)
	settlement.reserveErr = errors.New("reserve transaction failed")
	params := VideoTaskCreateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: []byte(`{"model":"seedance","prompt":"city","seconds":5}`), IdempotencyKey: "reserve-provider-fail"}
	_, err := svc.Create(context.Background(), params)
	require.ErrorIs(t, err, settlement.reserveErr)
	require.Zero(t, provider.createCalls)

	provider.createErr = errors.New("provider unavailable")
	repo := &videoTaskSettlementRepoStub{getErr: ErrVideoTaskSettlementNotFound, reserveResult: &VideoTaskSettlementResult{Applied: true, Settlement: &VideoTaskSettlementSnapshot{State: VideoTaskSettlementReserved}}, failResult: &VideoTaskSettlementResult{Applied: true, Settlement: &VideoTaskSettlementSnapshot{State: VideoTaskSettlementReleased}}}
	svc.settlement = &VideoTaskSettlementService{repo: repo, tasks: tasks}
	svc.accountLookup = &fakeVideoTaskAccountStore{accounts: map[int64]*Account{99: {ID: 99, Platform: PlatformOpenAI}}}

	result, err := svc.Create(context.Background(), params)

	require.Nil(t, result)
	require.ErrorIs(t, err, provider.createErr)
	require.Equal(t, 1, provider.createCalls)
	require.NotNil(t, repo.failCommand)
	require.Zero(t, repo.captureCalls)
}

func TestVideoTaskServiceCreateVideoPricingProviderFailureFailsAndReleases(t *testing.T) {
	events := &videoTaskServiceTestEvents{}
	svc, _, provider, settlement := pricedVideoTaskServiceFixture(events)
	provider.createErr = errors.New("provider failed")
	result, err := svc.Create(context.Background(), VideoTaskCreateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: []byte(`{"model":"seedance","prompt":"city","seconds":5}`)})
	require.Nil(t, result)
	require.ErrorIs(t, err, provider.createErr)
	require.Equal(t, []string{"selector_select", "pricing_resolve", "repo_create", "settlement_reserve", "provider_create", "settlement_fail_release"}, events.snapshot())
	require.Equal(t, 1, settlement.failAndReleaseCalls)
}

func TestVideoTaskServiceCreatePerRequestPricingProviderFailureFailsAndReleases(t *testing.T) {
	events := &videoTaskServiceTestEvents{}
	usage := &fakeVideoTaskUsageRecorder{events: events}
	svc, _, provider, settlement := perRequestVideoTaskServiceFixture(events, usage)
	provider.createErr = errors.New("provider failed")

	result, err := svc.Create(context.Background(), VideoTaskCreateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: []byte(`{"model":"seedance","prompt":"city"}`)})

	require.Nil(t, result)
	require.ErrorIs(t, err, provider.createErr)
	require.Equal(t, 1, settlement.failAndReleaseCalls)
	require.Zero(t, settlement.captureCalls)
	require.Zero(t, usage.calls)
}

func TestVideoTaskServiceCreatePerRequestPricingImmediateFailureReconcilesPersistedTask(t *testing.T) {
	events := &videoTaskServiceTestEvents{}
	usage := &fakeVideoTaskUsageRecorder{events: events}
	svc, _, provider, settlement := perRequestVideoTaskServiceFixture(events, usage)
	provider.createResult = &VideoProviderCreateResult{ProviderTaskID: "upstream_failed", Status: VideoTaskStatusFailed, ProviderStatus: "failed", RawBody: []byte(`{"id":"upstream_failed","status":"failed"}`)}

	result, err := svc.Create(context.Background(), VideoTaskCreateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: []byte(`{"model":"seedance","prompt":"city"}`)})

	require.NoError(t, err)
	require.Equal(t, VideoTaskStatusFailed, result.Task.Status)
	require.Equal(t, 1, settlement.reconcilePersistedCalls)
	require.Zero(t, settlement.captureCalls)
	require.Zero(t, usage.calls)
}

func TestVideoTaskServiceBlankAcceptedProviderIDFailsAndReleasesWithoutCapture(t *testing.T) {
	events := &videoTaskServiceTestEvents{}
	svc, repo, provider, settlement := pricedVideoTaskServiceFixture(events)
	provider.createResult.ProviderTaskID = "   "

	result, err := svc.Create(context.Background(), VideoTaskCreateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: []byte(`{"model":"seedance","prompt":"city","seconds":5}`)})

	require.Nil(t, result)
	require.ErrorIs(t, err, ErrVideoTaskProviderProtocol)
	require.Equal(t, 1, provider.createCalls)
	require.Equal(t, 1, settlement.failAndReleaseCalls)
	require.Zero(t, settlement.captureCalls)
	require.Equal(t, VideoTaskStatusSubmitting, repo.tasks[repo.lastCreate.PublicTaskID].Status)
}

func TestVideoTaskServiceCreateVideoPricingCaptureFailureReplayNeverResubmits(t *testing.T) {
	events := &videoTaskServiceTestEvents{}
	svc, _, provider, settlement := pricedVideoTaskServiceFixture(events)
	settlement.captureErr = errors.New("capture unavailable")
	params := VideoTaskCreateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: []byte(`{"model":"seedance","prompt":"city","seconds":5}`), IdempotencyKey: "priced-replay"}
	first, err := svc.Create(context.Background(), params)
	require.Nil(t, first)
	require.ErrorIs(t, err, ErrVideoTaskSettlementRetriable)
	require.Equal(t, 1, provider.createCalls)
	settlement.captureErr = nil
	second, err := svc.Create(context.Background(), params)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.Equal(t, 1, provider.createCalls)
	require.Equal(t, 2, settlement.captureCalls)
}

func TestVideoTaskServiceAcceptedProviderAttachFailurePersistsNonterminalFallback(t *testing.T) {
	events := &videoTaskServiceTestEvents{}
	svc, repo, provider, settlement := pricedVideoTaskServiceFixture(events)
	repo.attachErr = errors.New("primary attach failed")
	params := VideoTaskCreateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: []byte(`{"model":"seedance","prompt":"city","seconds":5}`), IdempotencyKey: "attach-replay"}

	result, err := svc.Create(context.Background(), params)

	require.Nil(t, result)
	require.ErrorIs(t, err, ErrVideoTaskSettlementRetriable)
	require.Equal(t, 1, provider.createCalls)
	require.Zero(t, settlement.failAndReleaseCalls)
	stored := repo.tasks[repo.lastCreate.PublicTaskID]
	require.False(t, stored.Status.Terminal())
	require.Equal(t, "upstream", videoTaskMetadataString(stored.Metadata, "reconciliation_upstream_task_id"))
	require.Equal(t, "ATTACH_UPSTREAM_FAILED", videoTaskMetadataString(stored.Metadata, "reconciliation_error_code"))
	snapshot, ok := videoTaskMetadataMap(stored.Metadata["reconciliation_accepted_snapshot"])
	require.True(t, ok)
	require.Equal(t, "upstream", snapshot["provider_task_id"])
	require.Equal(t, "queued", snapshot["provider_status"])
	require.JSONEq(t, `{"id":"upstream","status":"queued"}`, snapshot["response_body"].(string))
	require.NotEmpty(t, snapshot["submitted_at"])
	require.NotEmpty(t, snapshot["next_poll_at"])
}

func TestVideoTaskServiceAcceptedProviderAttachFallbackFailureKeepsReservationAndReturnsTypedError(t *testing.T) {
	events := &videoTaskServiceTestEvents{}
	svc, repo, provider, settlement := pricedVideoTaskServiceFixture(events)
	attachErr := errors.New("primary attach failed")
	fallbackErr := errors.New("fallback persistence failed")
	repo.attachErr = attachErr
	repo.fallbackErr = fallbackErr

	params := VideoTaskCreateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: []byte(`{"model":"seedance","prompt":"city","seconds":5}`), IdempotencyKey: "fallback-write-failed"}
	result, err := svc.Create(context.Background(), params)

	require.Nil(t, result)
	require.ErrorIs(t, err, ErrVideoTaskSettlementRetriable)
	require.ErrorIs(t, err, attachErr)
	require.ErrorIs(t, err, fallbackErr)
	require.Equal(t, 1, provider.createCalls)
	require.Zero(t, settlement.failAndReleaseCalls)
	require.False(t, repo.tasks[repo.lastCreate.PublicTaskID].Status.Terminal())

	reconcileRepo := &videoTaskSettlementRepoStub{snapshot: &VideoTaskSettlementSnapshot{State: VideoTaskSettlementReserved}}
	svc.settlement = &VideoTaskSettlementService{repo: reconcileRepo, tasks: repo}
	result, err = svc.Create(context.Background(), params)
	require.Nil(t, result)
	require.ErrorIs(t, err, ErrVideoTaskSettlementRetriable)
	require.Equal(t, 1, provider.createCalls)
	require.Zero(t, reconcileRepo.captureCalls)
}

func TestVideoTaskServiceCaptureAndMarkerFailuresReturnBothCauses(t *testing.T) {
	events := &videoTaskServiceTestEvents{}
	svc, repo, provider, settlement := pricedVideoTaskServiceFixture(events)
	captureErr := errors.New("capture failed")
	markerErr := errors.New("marker failed")
	settlement.captureErr = captureErr
	repo.updateProviderErr = markerErr

	params := VideoTaskCreateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: []byte(`{"model":"seedance","prompt":"city","seconds":5}`), IdempotencyKey: "capture-marker-failed"}
	result, err := svc.Create(context.Background(), params)

	require.Nil(t, result)
	require.ErrorIs(t, err, ErrVideoTaskSettlementRetriable)
	require.ErrorIs(t, err, captureErr)
	require.ErrorIs(t, err, markerErr)
	require.Equal(t, 1, provider.createCalls)
	require.Zero(t, settlement.failAndReleaseCalls)
	require.Equal(t, "upstream", repo.tasks[repo.lastCreate.PublicTaskID].ProviderTaskID)

	settlement.captureErr = nil
	repo.updateProviderErr = nil
	result, err = svc.Create(context.Background(), params)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, provider.createCalls)
	require.Equal(t, 2, settlement.captureCalls)
}

func TestVideoTaskServiceAttachFallbackReplayPromotesIDAndCapturesWithoutProvider(t *testing.T) {
	events := &videoTaskServiceTestEvents{}
	svc, tasks, provider, initialSettlement := pricedVideoTaskServiceFixture(events)
	tasks.attachErr = errors.New("primary attach failed")
	params := VideoTaskCreateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: []byte(`{"model":"seedance","prompt":"city","seconds":5}`), IdempotencyKey: "fallback-replay"}
	first, err := svc.Create(context.Background(), params)
	require.Nil(t, first)
	require.ErrorIs(t, err, ErrVideoTaskSettlementRetriable)
	require.Zero(t, initialSettlement.failAndReleaseCalls)

	tasks.attachErr = nil
	settlementRepo := &videoTaskSettlementRepoStub{
		snapshot:      &VideoTaskSettlementSnapshot{State: VideoTaskSettlementReserved},
		captureResult: &VideoTaskSettlementResult{Applied: true, Settlement: &VideoTaskSettlementSnapshot{State: VideoTaskSettlementCharged, ActualCostUSD: 0.2}},
	}
	svc.settlement = &VideoTaskSettlementService{repo: settlementRepo, tasks: tasks}
	second, err := svc.Create(context.Background(), params)

	require.NoError(t, err)
	require.NotNil(t, second)
	require.Equal(t, 1, provider.createCalls)
	require.Equal(t, 1, settlementRepo.captureCalls)
	stored := tasks.tasks[tasks.lastCreate.PublicTaskID]
	require.Equal(t, "upstream", stored.ProviderTaskID)
	require.Equal(t, VideoTaskStatusQueued, stored.Status)
	require.JSONEq(t, `{"id":"upstream","status":"queued"}`, string(stored.ResponseBody))
	require.NotNil(t, stored.SubmittedAt)
	require.NotNil(t, stored.NextPollAt)
}

func TestVideoTaskSettlementReconcileTerminalStatesDoNotCapture(t *testing.T) {
	for _, state := range []VideoTaskSettlementState{VideoTaskSettlementCharged, VideoTaskSettlementReleased, VideoTaskSettlementRefunded} {
		t.Run(string(state), func(t *testing.T) {
			repo := &videoTaskSettlementRepoStub{snapshot: &VideoTaskSettlementSnapshot{State: state}}
			tasks := newFakeVideoTaskRepository(nil)
			task := tasks.seedTask(&VideoTask{PublicTaskID: "task_terminal", Status: VideoTaskStatusQueued})
			svc := &VideoTaskSettlementService{repo: repo, tasks: tasks}

			result, err := svc.Reconcile(context.Background(), task)

			require.NoError(t, err)
			require.Equal(t, state, result.State)
			require.Equal(t, 1, repo.getCalls)
			require.Zero(t, repo.captureCalls)
		})
	}
}

func TestVideoTaskSettlementReconcileIncompleteStatesReturnTyped503WithoutCapture(t *testing.T) {
	for _, tt := range []struct {
		name   string
		state  *VideoTaskSettlementSnapshot
		getErr error
	}{
		{name: "missing settlement", getErr: ErrVideoTaskSettlementNotFound},
		{name: "reserved missing link", state: &VideoTaskSettlementSnapshot{State: VideoTaskSettlementReserved}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := &videoTaskSettlementRepoStub{snapshot: tt.state, getErr: tt.getErr}
			tasks := newFakeVideoTaskRepository(nil)
			task := tasks.seedTask(&VideoTask{PublicTaskID: "task_incomplete", Status: VideoTaskStatusSubmitting})
			svc := &VideoTaskSettlementService{repo: repo, tasks: tasks}

			result, err := svc.Reconcile(context.Background(), task)

			require.Nil(t, result)
			require.ErrorIs(t, err, ErrVideoTaskSettlementRetriable)
			require.Equal(t, 1, repo.getCalls)
			require.Zero(t, repo.captureCalls)
		})
	}
}

func TestVideoTaskServiceIdempotencyCreateConflictUsesSettlementAwareReplay(t *testing.T) {
	quote := VideoTaskQuote{BillingMode: BillingModeVideo, BillingModel: "seedance", Effective: VideoTaskEffectiveParams{Seconds: 5, Resolution: "720p", VideoCount: 1}, UnitPriceUSD: 0.08, GrossCostUSD: 0.4, ActualCostUSD: 0.2, AccountUnitPriceUSD: 0.08, AccountBaseCostUSD: 0.4, AccountCostUSD: 0.4, RateMultiplier: 0.5, AccountRateMultiplier: 1}
	body := []byte(`{"model":"seedance","prompt":"city","seconds":5}`)
	req, err := ParseVideoTaskCreateEnvelope(body)
	require.NoError(t, err)

	for _, tt := range []struct {
		name            string
		settlementState VideoTaskSettlementState
		reconcileErr    error
		fallbackLinked  bool
	}{
		{name: "reserved attached", settlementState: VideoTaskSettlementCharged},
		{name: "reserved fallback linked", settlementState: VideoTaskSettlementCharged, fallbackLinked: true},
		{name: "charged", settlementState: VideoTaskSettlementCharged},
		{name: "released", settlementState: VideoTaskSettlementReleased},
		{name: "refunded", settlementState: VideoTaskSettlementRefunded},
		{name: "reserved missing link", reconcileErr: ErrVideoTaskSettlementRetriable},
		{name: "missing settlement", reconcileErr: ErrVideoTaskSettlementRetriable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			events := &videoTaskServiceTestEvents{}
			repo := newFakeVideoTaskRepository(events)
			repo.idempotencyMisses = 1
			repo.createErr = errors.New("duplicate idempotency key")
			metadata := map[string]any{"idempotency_key": "race-idem", "request_metadata": map[string]any{"video_pricing_snapshot": quote}}
			providerTaskID := "upstream_existing"
			if tt.fallbackLinked {
				providerTaskID = ""
				metadata["reconciliation_upstream_task_id"] = "upstream_fallback"
			}
			repo.seedTask(&VideoTask{PublicTaskID: "task_race", APIKeyID: 42, UserID: 7, RequestHash: req.RequestHash, RequestBody: body, Status: VideoTaskStatusQueued, ProviderTaskID: providerTaskID, ResponseBody: []byte(`{"id":"upstream_existing","status":"queued"}`), Metadata: metadata})
			provider := &fakeVideoTaskProvider{events: events}
			selector := &fakeVideoTaskSelector{events: events, selection: &AccountSelectionResult{Account: &Account{ID: 99, Platform: PlatformOpenAI}}}
			settlement := &fakeVideoTaskSettlementOrchestrator{events: events, reconcileResult: &VideoTaskSettlementSnapshot{State: tt.settlementState}, reconcileErr: tt.reconcileErr}
			svc := newVideoTaskServiceForTest(repo, nil, selector, provider, nil)
			svc.settlement = settlement

			result, err := svc.Create(context.Background(), VideoTaskCreateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: body, IdempotencyKey: "race-idem"})

			if tt.reconcileErr != nil {
				require.Nil(t, result)
				require.ErrorIs(t, err, ErrVideoTaskSettlementRetriable)
			} else {
				require.NoError(t, err)
				require.NotNil(t, result)
			}
			require.Equal(t, 1, settlement.reconcileCalls)
			require.Zero(t, provider.createCalls)
		})
	}
}

func TestVideoTaskServiceInterruptedOrphanReplayReturnsPersistedFailureWithoutResubmission(t *testing.T) {
	for _, uniqueConflict := range []bool{false, true} {
		name := "prelookup"
		if uniqueConflict {
			name = "unique conflict"
		}
		t.Run(name, func(t *testing.T) {
			events := &videoTaskServiceTestEvents{}
			svc, tasks, provider, _ := pricedVideoTaskServiceFixture(events)
			body := []byte(`{"model":"seedance","prompt":"city","seconds":5}`)
			req, err := ParseVideoTaskCreateEnvelope(body)
			require.NoError(t, err)
			quote := VideoTaskQuote{BillingMode: BillingModeVideo, BillingModel: "seedance", Effective: VideoTaskEffectiveParams{Seconds: 5, Resolution: "720p", VideoCount: 1}, UnitPriceUSD: 0.08, GrossCostUSD: 0.4, ActualCostUSD: 0.2, AccountUnitPriceUSD: 0.08, AccountBaseCostUSD: 0.4, AccountCostUSD: 0.4, RateMultiplier: 0.5, AccountRateMultiplier: 1}
			tasks.seedTask(&VideoTask{PublicTaskID: "task_interrupted", APIKeyID: 42, UserID: 7, RequestHash: req.RequestHash, RequestBody: body, Status: VideoTaskStatusFailed, ErrorMessage: VideoTaskAdmissionInterruptedMessage, Metadata: map[string]any{
				"idempotency_key": "interrupted-idem", "request_metadata": map[string]any{"video_pricing_snapshot": quote},
				"reconciliation_error_code": VideoTaskAdmissionInterruptedCode, "provider_called": false,
			}})
			if uniqueConflict {
				tasks.idempotencyMisses = 1
				tasks.createErr = errors.New("duplicate idempotency key")
			}
			settlementRepo := &videoTaskSettlementRepoStub{getErr: ErrVideoTaskSettlementNotFound}
			svc.settlement = &VideoTaskSettlementService{repo: settlementRepo, tasks: tasks}

			result, err := svc.Create(context.Background(), VideoTaskCreateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: body, IdempotencyKey: "interrupted-idem"})

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, VideoTaskStatusFailed, result.Task.Status)
			require.JSONEq(t, `{"id":"task_interrupted","object":"video","status":"failed"}`, string(result.ResponseBody))
			require.Equal(t, 1, settlementRepo.getCalls)
			require.Zero(t, settlementRepo.reserveCalls)
			require.Zero(t, provider.createCalls)
		})
	}
}

func TestVideoTaskServiceFailedMissingSettlementCorruptionRemainsTyped(t *testing.T) {
	body := []byte(`{"model":"seedance","prompt":"city","seconds":5}`)
	req, err := ParseVideoTaskCreateEnvelope(body)
	require.NoError(t, err)
	quote := VideoTaskQuote{BillingMode: BillingModeVideo, BillingModel: "seedance", Effective: VideoTaskEffectiveParams{Seconds: 5, Resolution: "720p", VideoCount: 1}, UnitPriceUSD: 0.08, GrossCostUSD: 0.4, ActualCostUSD: 0.2, AccountUnitPriceUSD: 0.08, AccountBaseCostUSD: 0.4, AccountCostUSD: 0.4, RateMultiplier: 0.5, AccountRateMultiplier: 1}
	for _, tt := range []struct {
		name       string
		marker     map[string]any
		settlement *VideoTaskSettlementSnapshot
		getErr     error
		want       error
	}{
		{name: "missing interruption marker", marker: map[string]any{}, getErr: ErrVideoTaskSettlementNotFound, want: ErrVideoTaskSettlementRetriable},
		{name: "provider may have been called", marker: map[string]any{"reconciliation_error_code": VideoTaskAdmissionInterruptedCode, "provider_called": true}, getErr: ErrVideoTaskSettlementNotFound, want: ErrVideoTaskSettlementRetriable},
		{name: "interrupted marker conflicts with settlement", marker: map[string]any{"reconciliation_error_code": VideoTaskAdmissionInterruptedCode, "provider_called": false}, settlement: &VideoTaskSettlementSnapshot{State: VideoTaskSettlementReserved}, want: ErrVideoTaskSettlementIntegrity},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tasks := newFakeVideoTaskRepository(nil)
			metadata := map[string]any{"idempotency_key": "corrupt-idem", "request_metadata": map[string]any{"video_pricing_snapshot": quote}}
			for key, value := range tt.marker {
				metadata[key] = value
			}
			tasks.seedTask(&VideoTask{PublicTaskID: "task_corrupt", APIKeyID: 42, UserID: 7, RequestHash: req.RequestHash, RequestBody: body, Status: VideoTaskStatusFailed, Metadata: metadata})
			settlementRepo := &videoTaskSettlementRepoStub{snapshot: tt.settlement, getErr: tt.getErr}
			svc := newVideoTaskServiceForTest(tasks, nil, &fakeVideoTaskSelector{}, &fakeVideoTaskProvider{}, nil)
			svc.settlement = &VideoTaskSettlementService{repo: settlementRepo, tasks: tasks}

			result, err := svc.Create(context.Background(), VideoTaskCreateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: body, IdempotencyKey: "corrupt-idem"})

			require.Nil(t, result)
			require.ErrorIs(t, err, tt.want)
			require.Zero(t, settlementRepo.reserveCalls)
		})
	}
}

func TestVideoTaskSettlementServiceFailAndReleaseUsesAtomicRepositoryCommand(t *testing.T) {
	repo := &videoTaskSettlementRepoStub{failResult: &VideoTaskSettlementResult{Applied: true, Settlement: &VideoTaskSettlementSnapshot{State: VideoTaskSettlementReleased}}}
	svc := NewVideoTaskSettlementService(repo, newFakeVideoTaskRepository(nil), nil, nil, nil, nil, nil)
	cause := errors.New("provider rejected request")

	err := svc.FailAndRelease(context.Background(), " task_123 ", cause)

	require.NoError(t, err)
	require.NotNil(t, repo.failCommand)
	require.Equal(t, "task_123", repo.failCommand.PublicTaskID)
	require.Equal(t, cause.Error(), repo.failCommand.ErrorMessage)
	require.Zero(t, repo.releaseCalls)
}

func TestVideoTaskSettlementServiceBuildsExactEffectsAndProvisionalUsage(t *testing.T) {
	cache := &videoTaskSettlementCacheStub{platformLimited: true}
	svc := &VideoTaskSettlementService{cache: cache}
	apiKey := videoTaskTestAPIKey()
	apiKey.Quota, apiKey.RateLimit5h = 10, 5
	account := &Account{ID: 99, Type: AccountTypeAPIKey, Extra: map[string]any{"quota_limit": 100.0}}
	quote := VideoTaskQuote{BillingMode: BillingModeVideo, BillingModel: "seedance", Effective: VideoTaskEffectiveParams{Seconds: 5, Resolution: "720p", VideoCount: 1}, UnitPriceUSD: 0.08, GrossCostUSD: 0.4, ActualCostUSD: 0.2, AccountUnitPriceUSD: 0.08, AccountBaseCostUSD: 0.4, AccountCostUSD: 0.5, RateMultiplier: 0.5, AccountRateMultiplier: 1.25}
	input := VideoTaskSettlementCreateInput{PublicTaskID: " task_123 ", Quote: quote, Params: VideoTaskCreateParams{APIKey: apiKey, User: &User{ID: 7}}, Account: account, UpstreamModel: "seedance-upstream", ChannelID: 17}

	effects := svc.videoTaskBillingEffects(context.Background(), input, BillingTypeBalance)
	usage := buildVideoTaskSettlementUsage(input, BillingTypeBalance, nil)

	require.Equal(t, VideoTaskBillingEffects{BalanceCost: 0.2, APIKeyQuotaCost: 0.2, APIKeyRateLimitCost: 0.2, AccountQuotaCost: 0.5, PlatformQuotaCost: 0.2, AccountStatsCost: 0.5}, effects)
	require.Equal(t, "video:task_123:charge", usage.RequestID)
	require.Zero(t, usage.ActualCost, "reserved usage must not be reported as captured revenue")
	require.NotNil(t, usage.ChannelID)
	require.Equal(t, int64(17), *usage.ChannelID)
}

type videoTaskSettlementRepoStub struct {
	failCommand    *VideoTaskSettlementFailCommand
	failResult     *VideoTaskSettlementResult
	releaseCalls   int
	snapshot       *VideoTaskSettlementSnapshot
	getErr         error
	getCalls       int
	captureResult  *VideoTaskSettlementResult
	captureErr     error
	captureCalls   int
	captureCommand *VideoTaskSettlementCaptureCommand
	reserveResult  *VideoTaskSettlementResult
	reserveErr     error
	reserveCalls   int
	reserveCommand *VideoTaskSettlementReserveCommand
}

type videoTaskSettlementCacheStub struct{ platformLimited bool }

func (s *videoTaskSettlementCacheStub) InvalidateUserBalance(context.Context, int64) error {
	return nil
}
func (s *videoTaskSettlementCacheStub) InvalidateSubscription(context.Context, int64, int64) error {
	return nil
}
func (s *videoTaskSettlementCacheStub) InvalidateAPIKeyRateLimit(context.Context, int64) error {
	return nil
}
func (s *videoTaskSettlementCacheStub) InvalidateUserPlatformQuota(context.Context, int64, string) error {
	return nil
}
func (s *videoTaskSettlementCacheStub) HasUserPlatformQuotaLimit(context.Context, int64, string) bool {
	return s.platformLimited
}

func (r *videoTaskSettlementRepoStub) Reserve(_ context.Context, command *VideoTaskSettlementReserveCommand) (*VideoTaskSettlementResult, error) {
	r.reserveCalls++
	r.reserveCommand = command
	return r.reserveResult, r.reserveErr
}
func (r *videoTaskSettlementRepoStub) Capture(_ context.Context, command *VideoTaskSettlementCaptureCommand) (*VideoTaskSettlementResult, error) {
	r.captureCalls++
	r.captureCommand = command
	return r.captureResult, r.captureErr
}
func (r *videoTaskSettlementRepoStub) Release(context.Context, *VideoTaskSettlementReleaseCommand) (*VideoTaskSettlementResult, error) {
	r.releaseCalls++
	return nil, nil
}
func (r *videoTaskSettlementRepoStub) ReleaseFailed(context.Context, *VideoTaskSettlementReleaseCommand) (*VideoTaskSettlementResult, error) {
	r.releaseCalls++
	return nil, nil
}
func (r *videoTaskSettlementRepoStub) FailSubmission(_ context.Context, command *VideoTaskSettlementFailCommand) (*VideoTaskSettlementResult, error) {
	r.failCommand = command
	return r.failResult, nil
}
func (r *videoTaskSettlementRepoStub) RefundFailed(context.Context, *VideoTaskSettlementRefundCommand) (*VideoTaskSettlementResult, error) {
	return nil, nil
}
func (r *videoTaskSettlementRepoStub) ClaimDueCacheInvalidation(context.Context, int, string, time.Duration) ([]VideoTaskCacheInvalidationClaim, error) {
	return nil, nil
}
func (r *videoTaskSettlementRepoStub) CompleteCacheInvalidation(context.Context, int64, string) (bool, error) {
	return false, nil
}
func (r *videoTaskSettlementRepoStub) RetryCacheInvalidation(context.Context, int64, string, string, time.Time) (bool, error) {
	return false, nil
}
func (r *videoTaskSettlementRepoStub) DeadLetterCacheInvalidation(context.Context, int64, string, string) (bool, error) {
	return false, nil
}
func (r *videoTaskSettlementRepoStub) ClaimDueAdmissionOrphans(context.Context, time.Time, time.Duration, int, string, time.Duration) ([]VideoTaskAdmissionOrphanClaim, error) {
	return nil, nil
}
func (r *videoTaskSettlementRepoStub) FailAdmissionOrphan(context.Context, string, string, string, string) (bool, error) {
	return false, nil
}
func (r *videoTaskSettlementRepoStub) GetByPublicTaskID(context.Context, string) (*VideoTaskSettlementSnapshot, error) {
	r.getCalls++
	return r.snapshot, r.getErr
}

func (r *videoTaskSettlementRepoStub) ClaimDueReconciliation(context.Context, time.Time, int, string, time.Duration) ([]VideoTaskSettlementReconcileClaim, error) {
	return nil, nil
}

func (r *videoTaskSettlementRepoStub) ClaimDueRefundReporting(context.Context, time.Time, int, string, time.Duration) ([]VideoTaskRefundReportingClaim, error) {
	return nil, nil
}

func (r *videoTaskSettlementRepoStub) CompleteRefundReporting(context.Context, int64, string) (bool, error) {
	return true, nil
}

func (r *videoTaskSettlementRepoStub) RetryRefundReporting(context.Context, int64, string, string, time.Time) (bool, error) {
	return true, nil
}

func (r *videoTaskSettlementRepoStub) CompleteReconciliation(context.Context, string, string) (bool, error) {
	return true, nil
}

func (r *videoTaskSettlementRepoStub) RetryReconciliation(context.Context, string, string, string, time.Time) (bool, error) {
	return true, nil
}
func (r *videoTaskSettlementRepoStub) RenewSettlementClaim(context.Context, string, string, time.Duration) (time.Time, bool, error) {
	return time.Now().Add(time.Minute), true, nil
}

func (r *videoTaskSettlementRepoStub) RepairChargedUsage(context.Context, string) (*VideoTaskSettlementResult, error) {
	return &VideoTaskSettlementResult{}, nil
}
