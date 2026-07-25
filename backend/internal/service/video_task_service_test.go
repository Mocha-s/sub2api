package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestNewVideoTaskServiceUsesAccountVideoTaskProvider(t *testing.T) {
	svc := NewVideoTaskService(newFakeVideoTaskRepository(nil), nil, nil, nil)

	_, ok := svc.provider.(*accountVideoTaskProvider)
	require.True(t, ok, "NewVideoTaskService should use account-level provider")
}

func TestVideoTaskServiceCreatePersistsUpstreamBeforeReturning(t *testing.T) {
	ctx := context.Background()
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	provider := &fakeVideoTaskProvider{
		events: events,
		createResult: &VideoProviderCreateResult{
			ProviderTaskID: "upstream_task_123",
			Status:         VideoTaskStatusQueued,
			ProviderStatus: "queued",
			RawBody:        []byte(`{"id":"upstream_task_123","status":"queued"}`),
			Metadata:       map[string]any{"request_id": "req_123"},
		},
	}
	selector := &fakeVideoTaskSelector{
		events: events,
		selection: &AccountSelectionResult{
			Account: &Account{
				ID:       99,
				Platform: PlatformOpenAI,
				Credentials: map[string]any{
					"base_url":      "https://upstream.example/v1",
					"model_mapping": map[string]any{"video-ds-2.0-fast": "video-ds-2.0-fast"},
				},
			},
			ReleaseFunc: func() { events.add("release") },
		},
	}
	usage := &fakeVideoTaskUsageRecorder{events: events}
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, usage)

	result, err := svc.Create(ctx, VideoTaskCreateParams{
		APIKey:         videoTaskTestAPIKey(),
		User:           &User{ID: 7},
		Subscription:   &UserSubscription{ID: 11},
		Body:           []byte(`{"model":"video-ds-2.0-fast","prompt":"make a short city clip","seconds":"4","aspect_ratio":"16:9","images":[{}]}`),
		ContentType:    "application/json",
		UserAgent:      "video-test/1.0",
		IPAddress:      "203.0.113.9",
		IdempotencyKey: "idem-create",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Task)
	require.Equal(t, "upstream_task_123", result.Task.ProviderTaskID)
	require.Equal(t, VideoTaskStatusQueued, result.Task.Status)
	require.Equal(t, []string{"repo_get_idempotency", "selector_select", "repo_create", "provider_create", "repo_attach", "repo_get_public", "usage_record", "release"}, events.snapshot())
	require.True(t, repo.createdBeforeProvider, "local task should exist before provider create")
	require.True(t, usage.calledAfterAttach, "usage should be recorded only after upstream id persistence")
	require.Equal(t, "video-ds-2.0-fast", provider.createUpstreamModel)
	require.Equal(t, "application/json", provider.createContentType)
	require.Equal(t, "video-ds-2.0-fast", repo.lastCreate.Metadata["requested_model"])
	require.Equal(t, "video-ds-2.0-fast", repo.lastCreate.Metadata["upstream_model"])
	require.Equal(t, "video-ds-2.0-fast", repo.lastCreate.Metadata["billing_model"])
	require.Equal(t, "https://upstream.example", repo.lastCreate.Metadata["upstream_base_url"])
	require.Equal(t, "idem-create", repo.lastCreate.Metadata["idempotency_key"])
	require.Equal(t, 1, repo.lastCreate.Metadata["image_count"])

	var response map[string]any
	require.NoError(t, json.Unmarshal(result.ResponseBody, &response))
	require.Equal(t, result.Task.PublicTaskID, response["id"])
	require.NotEqual(t, "upstream_task_123", response["id"])
}

func TestVideoTaskInboundEndpoint(t *testing.T) {
	require.Equal(t, "/v1/videos", videoTaskInboundEndpoint(""))
	require.Equal(t, "/v1/videos", videoTaskInboundEndpoint(VideoTaskEndpointVideos))
	require.Equal(t, "/v1/video/generations", videoTaskInboundEndpoint(VideoTaskEndpointVideoGenerations))
}

func TestVideoTaskCreateMetadataPreservesExistingRequestMetadataKeys(t *testing.T) {
	req := &VideoTaskCreateEnvelope{Model: "seedance", Metadata: map[string]any{
		"duration":         "60",
		"request_metadata": map[string]any{"trace_id": "trace-123", "client_tag": "batch"},
	}}

	metadata := videoTaskCreateMetadata(req, nil, "seedance", "seedance-upstream", "idem", VideoAdapterSeedanceAPIV1, VideoTaskEndpointVideos)
	requestMetadata, ok := videoTaskMetadataMap(metadata["request_metadata"])
	require.True(t, ok)
	require.Equal(t, "60", requestMetadata["duration"])
	require.Equal(t, "trace-123", requestMetadata["trace_id"])
	require.Equal(t, "batch", requestMetadata["client_tag"])
}

func TestVideoTaskForwardResultCarriesSnapshotUsageMetadataAndCost(t *testing.T) {
	quote := VideoTaskQuote{
		BillingMode: BillingModeVideo, BillingModel: "seedance-priced",
		Effective:    VideoTaskEffectiveParams{Seconds: 5, Resolution: "1080p", VideoCount: 1},
		UnitPriceUSD: 0.08, GrossCostUSD: 0.4, ActualCostUSD: 0.2,
		AccountUnitPriceUSD: 0.08, AccountBaseCostUSD: 0.4, AccountCostUSD: 0.5,
		RateMultiplier: 0.5, AccountRateMultiplier: 1.25,
	}
	task := &VideoTask{Model: "seedance", ProviderTaskID: "upstream", Metadata: map[string]any{"request_metadata": map[string]any{"video_pricing_snapshot": quote}}}

	result := videoTaskSubmissionForwardResult(task, &VideoProviderCreateResult{ProviderTaskID: "upstream", Metadata: map[string]any{"request_id": "req"}}, "seedance-upstream")

	require.Equal(t, 1, result.VideoCount)
	require.Equal(t, "1080p", result.VideoResolution)
	require.Equal(t, 5, result.VideoDurationSeconds)
	require.Equal(t, "seedance-priced", result.BillingModel)
	cost := (&OpenAIGatewayService{}).calculateOpenAIVideoCost(context.Background(), "other-model", videoTaskTestAPIKey(), result, 99)
	require.Equal(t, string(BillingModeVideo), cost.BillingMode)
	require.InDelta(t, 0.4, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.2, cost.ActualCost, 1e-12)
}

func TestCalculateOpenAIVideoCostKeepsPerRequestQuoteMode(t *testing.T) {
	quote := VideoTaskQuote{
		BillingMode: BillingModePerRequest, BillingModel: "seedance-priced",
		Effective:    VideoTaskEffectiveParams{VideoCount: 1},
		UnitPriceUSD: 65, GrossCostUSD: 65, ActualCostUSD: 6.9225,
		AccountUnitPriceUSD: 65, AccountBaseCostUSD: 65, AccountCostUSD: 65,
		RateMultiplier: 0.1065, AccountRateMultiplier: 1,
	}
	task := &VideoTask{Model: "seedance", ProviderTaskID: "upstream", Metadata: map[string]any{"request_metadata": map[string]any{"video_pricing_snapshot": quote}}}

	result := videoTaskSubmissionForwardResult(task, &VideoProviderCreateResult{ProviderTaskID: "upstream"}, "seedance-upstream")
	cost := (&OpenAIGatewayService{}).calculateOpenAIVideoCost(context.Background(), "other-model", videoTaskTestAPIKey(), result, 99)

	require.Equal(t, string(BillingModePerRequest), cost.BillingMode)
	require.InDelta(t, 65, cost.TotalCost, 1e-12)
	require.InDelta(t, 6.9225, cost.ActualCost, 1e-12)
}

func TestVideoTaskServiceCreateUsesGenericEnvelopeAndPersistsVideoAdapter(t *testing.T) {
	ctx := context.Background()
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	provider := &fakeVideoTaskProvider{
		events: events,
		createResult: &VideoProviderCreateResult{
			ProviderTaskID: "upstream_seedance",
			Status:         VideoTaskStatusQueued,
			ProviderStatus: "queued",
			RawBody:        []byte(`{"id":"upstream_seedance","status":"queued"}`),
			Metadata:       map[string]any{"request_id": "req_seedance"},
		},
	}
	selector := &fakeVideoTaskSelector{
		events: events,
		selection: &AccountSelectionResult{Account: &Account{
			ID:       99,
			Platform: PlatformOpenAI,
			Credentials: map[string]any{
				"base_url":      "https://seedance.example/api/v1",
				"video_adapter": VideoAdapterSeedanceAPIV1,
				"model_mapping": map[string]any{"seedance-2.0": "seedance-upstream-v2"},
			},
		}},
	}
	usage := &fakeVideoTaskUsageRecorder{events: events}
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, usage)

	result, err := svc.Create(ctx, VideoTaskCreateParams{
		APIKey:      videoTaskTestAPIKey(),
		User:        &User{ID: 7},
		Body:        []byte(`{"model":"seedance-2.0","prompt":"city","duration":8,"ratio":"16:9","resolution":"720p"}`),
		ContentType: "application/json",
		Endpoint:    VideoTaskEndpointVideos,
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "seedance-2.0", repo.lastCreate.Model)
	require.Equal(t, "seedance-upstream-v2", repo.lastCreate.Metadata["upstream_model"])
	require.Equal(t, VideoAdapterSeedanceAPIV1, repo.lastCreate.Metadata[VideoAdapterMetadataKey])
	require.Equal(t, "https://seedance.example/api/v1", repo.lastCreate.Metadata["upstream_base_url"])
	require.Equal(t, "8", repo.lastCreate.Metadata["duration"])
	require.Equal(t, "16:9", repo.lastCreate.Metadata["ratio"])
	require.Equal(t, "720p", repo.lastCreate.Metadata["resolution"])
	require.Equal(t, "seedance-upstream-v2", provider.createUpstreamModel)
}

func TestVideoTaskServiceCreateFailureMergesProviderErrorMetadata(t *testing.T) {
	ctx := context.Background()
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	provider := &fakeVideoTaskProvider{
		events: events,
		createErr: videoTaskProviderMetadataError{
			err: errors.New("upstream temporarily unavailable"),
			metadata: map[string]any{
				VideoAdapterMetadataKey: VideoAdapterNewAPIVideoGenerations,
			},
		},
	}
	selector := &fakeVideoTaskSelector{
		events: events,
		selection: &AccountSelectionResult{Account: &Account{
			ID:       99,
			Platform: PlatformOpenAI,
			Credentials: map[string]any{
				"base_url":      "https://seedance.example/api/v1",
				"video_adapter": VideoAdapterAutoVideoGenerations,
			},
		}},
	}
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, nil)

	result, err := svc.Create(ctx, VideoTaskCreateParams{
		APIKey:      videoTaskTestAPIKey(),
		User:        &User{ID: 7},
		Body:        []byte(`{"model":"seedance-2.0","prompt":"city","duration":5,"resolution":"720p"}`),
		ContentType: "application/json",
		Endpoint:    VideoTaskEndpointVideoGenerations,
	})

	require.Nil(t, result)
	require.ErrorContains(t, err, "upstream temporarily unavailable")
	require.Equal(t, []string{"selector_select", "repo_create", "provider_create", "repo_update_provider"}, events.snapshot())
	task := repo.tasks[repo.lastCreate.PublicTaskID]
	require.NotNil(t, task)
	require.Equal(t, VideoTaskStatusFailed, task.Status)
	require.Equal(t, VideoAdapterNewAPIVideoGenerations, task.Metadata[VideoAdapterMetadataKey])
	require.Equal(t, "upstream temporarily unavailable", task.ErrorMessage)
}

func TestVideoTaskServiceCreateVideoGenerationsAllowsDurationBodyThroughJimengAdapter(t *testing.T) {
	ctx := context.Background()
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	delegate := &fakeVideoTaskProvider{
		events: events,
		createResult: &VideoProviderCreateResult{
			ProviderTaskID: "upstream_jimeng_task",
			Status:         VideoTaskStatusQueued,
			ProviderStatus: "queued",
			RawBody:        []byte(`{"id":"upstream_jimeng_task","status":"queued"}`),
		},
	}
	selector := &fakeVideoTaskSelector{
		events: events,
		selection: &AccountSelectionResult{
			Account: &Account{
				ID:       99,
				Platform: PlatformOpenAI,
				Credentials: map[string]any{
					"model_mapping": map[string]any{"video-ds-2.0-fast": "jimeng-upstream-model"},
				},
			},
		},
	}
	svc := newVideoTaskServiceForTest(repo, nil, selector, &jimengOpenAIVideosAdapter{provider: delegate}, nil)
	body := []byte(`{"model":"video-ds-2.0-fast","prompt":"city","duration":8,"aspect_ratio":"9:16","provider_extra":{"mode":"fast"}}`)
	envelope, err := ParseVideoTaskCreateEnvelope(body)
	require.NoError(t, err)

	result, err := svc.Create(ctx, VideoTaskCreateParams{
		APIKey:      videoTaskTestAPIKey(),
		User:        &User{ID: 7},
		Body:        body,
		ContentType: "application/json",
		Endpoint:    VideoTaskEndpointVideoGenerations,
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, delegate.createCalls)
	require.JSONEq(t, `{"model":"video-ds-2.0-fast","prompt":"city","seconds":"15","aspect_ratio":"9:16"}`, string(delegate.createBody))
	require.Equal(t, "jimeng-upstream-model", delegate.createUpstreamModel)
	require.Equal(t, "video-ds-2.0-fast", repo.lastCreate.Model)
	require.Equal(t, "city", repo.lastCreate.Prompt)
	require.Equal(t, envelope.RequestHash, repo.lastCreate.RequestHash)
	require.Equal(t, envelope.PromptHash, repo.lastCreate.PromptHash)
	require.Equal(t, body, repo.lastCreate.RequestBody)
	require.Equal(t, "8", repo.lastCreate.Metadata["duration"])
	require.Equal(t, VideoAdapterJimengOpenAIVideos, result.Task.Metadata[VideoAdapterMetadataKey])
}

func TestVideoTaskServiceCreateVideoPricingStoresQuoteAndNormalizesOnlyForwardedBody(t *testing.T) {
	ctx := context.Background()
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	provider := &fakeVideoTaskProvider{events: events, createResult: &VideoProviderCreateResult{
		ProviderTaskID: "upstream_priced", Status: VideoTaskStatusQueued, ProviderStatus: "queued",
		RawBody: []byte(`{"id":"upstream_priced","status":"queued"}`),
	}}
	accountRate := 1.25
	selector := &fakeVideoTaskSelector{events: events, selection: &AccountSelectionResult{Account: &Account{
		ID: 99, Platform: PlatformOpenAI, RateMultiplier: &accountRate,
		Credentials: map[string]any{"model_mapping": map[string]any{"seedance-2.0-fast-720p": "seedance-upstream"}},
	}}}
	defaultSeconds := 10
	price := 0.08
	pricing := &fakeVideoTaskPricingResolver{events: events, selection: VideoTaskPricingSelection{
		Pricing:      &ChannelModelPricing{BillingMode: BillingModeVideo, VideoPricePerSecond: &price, VideoDefaultSeconds: &defaultSeconds, VideoAllowedSeconds: []int{5, 10}},
		BillingModel: "seedance-2.0-fast-720p", BillingModelSource: BillingModelSourceRequested, ChannelID: 17,
		RateMultiplier: 0.5, AccountRateMultiplier: accountRate,
	}}
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, nil)
	svc.pricingResolver = pricing
	svc.settlement = &fakeVideoTaskSettlementOrchestrator{events: events, state: VideoTaskSettlementCharged}
	body := []byte(`{"model":"seedance-2.0-fast-720p","prompt":"city","duration":5,"duration_seconds":10,"provider_extra":"keep"}`)
	envelope, err := ParseVideoTaskCreateEnvelope(body)
	require.NoError(t, err)

	result, err := svc.Create(ctx, VideoTaskCreateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: body, ContentType: "application/json"})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, []string{"selector_select", "pricing_resolve", "repo_create", "settlement_reserve", "provider_create", "repo_attach", "settlement_capture", "repo_get_public"}, events.snapshot())
	require.JSONEq(t, `{"model":"seedance-2.0-fast-720p","prompt":"city","seconds":"5","provider_extra":"keep"}`, string(provider.createBody))
	require.Equal(t, body, repo.lastCreate.RequestBody)
	require.Equal(t, envelope.RequestHash, repo.lastCreate.RequestHash)
	require.Equal(t, int64(17), repo.lastCreate.ChannelID)
	require.NotContains(t, repo.lastCreate.Metadata, "video_pricing_snapshot")
	requestMetadata, ok := repo.lastCreate.Metadata["request_metadata"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "5", requestMetadata["duration"])
	require.Contains(t, requestMetadata, "video_pricing_snapshot")
	snapshot, ok := videoTaskQuoteFromMetadata(repo.lastCreate.Metadata)
	require.True(t, ok)
	require.Equal(t, 5, snapshot.Effective.Seconds)
	require.Equal(t, "720p", snapshot.Effective.Resolution)
	require.InDelta(t, 0.2, snapshot.ActualCostUSD, 1e-12)
	require.InDelta(t, 0.5, snapshot.AccountCostUSD, 1e-12)
	require.Equal(t, 1, pricing.calls)
}

func TestVideoTaskServiceCreateWithoutVideoPricingKeepsLegacyDurationAndExplicitSecondsBehavior(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "duration remains fixed fifteen", body: `{"model":"video-ds-2.0-fast","prompt":"city","duration":5}`, want: `{"model":"video-ds-2.0-fast","prompt":"city","seconds":"15"}`},
		{name: "explicit seconds preserved", body: `{"model":"video-ds-2.0-fast","prompt":"city","seconds":"5"}`, want: `{"model":"video-ds-2.0-fast","prompt":"city","seconds":"5"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeVideoTaskRepository(nil)
			delegate := &fakeVideoTaskProvider{createResult: &VideoProviderCreateResult{ProviderTaskID: "upstream", Status: VideoTaskStatusQueued, RawBody: []byte(`{"id":"upstream","status":"queued"}`)}}
			selector := &fakeVideoTaskSelector{selection: &AccountSelectionResult{Account: &Account{ID: 99, Platform: PlatformOpenAI}}}
			svc := newVideoTaskServiceForTest(repo, nil, selector, &jimengOpenAIVideosAdapter{provider: delegate}, nil)
			svc.pricingResolver = &fakeVideoTaskPricingResolver{}

			_, err := svc.Create(context.Background(), VideoTaskCreateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: []byte(tt.body), Endpoint: VideoTaskEndpointVideoGenerations})

			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(delegate.createBody))
		})
	}
}

func TestVideoTaskServiceCreateIdempotencyReplayUsesStoredVideoPricingSnapshot(t *testing.T) {
	repo := newFakeVideoTaskRepository(nil)
	provider := &fakeVideoTaskProvider{createResult: &VideoProviderCreateResult{ProviderTaskID: "upstream", Status: VideoTaskStatusQueued, RawBody: []byte(`{"id":"upstream","status":"queued"}`)}}
	selector := &fakeVideoTaskSelector{selection: &AccountSelectionResult{Account: &Account{ID: 99, Platform: PlatformOpenAI}}}
	defaultSeconds := 5
	price := 0.08
	pricing := &fakeVideoTaskPricingResolver{selection: VideoTaskPricingSelection{Pricing: &ChannelModelPricing{BillingMode: BillingModeVideo, VideoPricePerSecond: &price, VideoDefaultSeconds: &defaultSeconds}, BillingModel: "seedance", RateMultiplier: 1, AccountRateMultiplier: 1}}
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, nil)
	svc.pricingResolver = pricing
	svc.settlement = &fakeVideoTaskSettlementOrchestrator{state: VideoTaskSettlementCharged}
	params := VideoTaskCreateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: []byte(`{"model":"seedance","prompt":"city","seconds":5}`), IdempotencyKey: "idem-priced"}

	first, err := svc.Create(context.Background(), params)
	require.NoError(t, err)
	encodedMetadata, err := json.Marshal(repo.tasks[first.Task.PublicTaskID].Metadata)
	require.NoError(t, err)
	var decodedMetadata map[string]any
	require.NoError(t, json.Unmarshal(encodedMetadata, &decodedMetadata))
	repo.tasks[first.Task.PublicTaskID].Metadata = decodedMetadata
	second, err := svc.Create(context.Background(), params)
	require.NoError(t, err)
	require.Equal(t, first.Task.PublicTaskID, second.Task.PublicTaskID)
	require.Equal(t, 1, provider.createCalls)
	require.Equal(t, 1, pricing.calls)
	_, ok := videoTaskQuoteFromMetadata(second.Task.Metadata)
	require.True(t, ok)
}

func TestVideoTaskServiceCreateVideoGenerationsRejectsInvalidJimengDurationBeforePersistence(t *testing.T) {
	ctx := context.Background()
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	delegate := &fakeVideoTaskProvider{
		events: events,
		createResult: &VideoProviderCreateResult{
			ProviderTaskID: "upstream_jimeng_task",
			Status:         VideoTaskStatusQueued,
			ProviderStatus: "queued",
			RawBody:        []byte(`{"id":"upstream_jimeng_task","status":"queued"}`),
		},
	}
	provider := &accountVideoTaskProvider{registry: videoAdapterRegistry{
		VideoAdapterJimengOpenAIVideos: &jimengOpenAIVideosAdapter{provider: delegate},
	}}
	selector := &fakeVideoTaskSelector{
		events: events,
		selection: &AccountSelectionResult{
			Account: &Account{ID: 99, Platform: PlatformOpenAI},
		},
	}
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, nil)

	result, err := svc.Create(ctx, VideoTaskCreateParams{
		APIKey:         videoTaskTestAPIKey(),
		User:           &User{ID: 7},
		Body:           []byte(`{"model":"video-ds-2.0-fast","prompt":"city","duration":{"bad":true}}`),
		ContentType:    "application/json",
		Endpoint:       VideoTaskEndpointVideoGenerations,
		IdempotencyKey: "idem-invalid-jimeng-duration",
	})

	require.Nil(t, result)
	require.ErrorContains(t, err, "invalid jimeng OpenAI video create JSON")
	require.Zero(t, delegate.createCalls)
	require.False(t, repo.createdBeforeProvider)
	require.Empty(t, repo.lastCreate.PublicTaskID)
}

func TestVideoTaskServiceFetchUsesStoredAccount(t *testing.T) {
	ctx := context.Background()
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	task := repo.seedTask(&VideoTask{
		PublicTaskID:   "task_local_fetch",
		ProviderTaskID: "upstream_fetch_1",
		UserID:         7,
		AccountID:      99,
		Status:         VideoTaskStatusQueued,
		ResponseBody:   []byte(`{"id":"upstream_fetch_1","status":"queued"}`),
	})
	accountStore := &fakeVideoTaskAccountStore{
		events: events,
		accounts: map[int64]*Account{
			99: {ID: 99, Platform: PlatformOpenAI},
		},
	}
	selector := &fakeVideoTaskSelector{events: events}
	provider := &fakeVideoTaskProvider{
		events: events,
		fetchResult: &VideoProviderFetchResult{
			ProviderTaskID: "upstream_fetch_1",
			Status:         VideoTaskStatusCompleted,
			ProviderStatus: "completed",
			RawBody:        []byte(`{"id":"upstream_fetch_1","status":"completed"}`),
		},
	}
	svc := newVideoTaskServiceForTest(repo, accountStore, selector, provider, nil)

	result, err := svc.Fetch(ctx, VideoTaskFetchParams{UserID: 7, PublicTaskID: task.PublicTaskID})

	require.NoError(t, err)
	require.Equal(t, []int64{99}, accountStore.getByIDCalls)
	require.Zero(t, selector.selectCalls)
	require.Equal(t, []int64{99}, provider.fetchAccountIDs)
	require.Equal(t, VideoTaskStatusCompleted, result.Task.Status)
	require.Equal(t, []string{"repo_get_user_public", "account_get", "provider_fetch", "repo_update_provider", "repo_get_public"}, events.snapshot())

	var response map[string]any
	require.NoError(t, json.Unmarshal(result.ResponseBody, &response))
	require.Equal(t, task.PublicTaskID, response["id"])
}

func TestVideoTaskServiceRefreshUsesPersistedAdapterAndUpdatesTask(t *testing.T) {
	ctx := context.Background()
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	repo.seedTask(&VideoTask{
		PublicTaskID:   "task_refresh",
		ProviderTaskID: "upstream_refresh",
		UserID:         7,
		AccountID:      99,
		Status:         VideoTaskStatusQueued,
		Metadata:       map[string]any{VideoAdapterMetadataKey: VideoAdapterJimengOpenAIVideos},
	})
	accountStore := &fakeVideoTaskAccountStore{events: events, accounts: map[int64]*Account{99: {ID: 99, Platform: PlatformOpenAI}}}
	provider := &fakeVideoTaskProvider{events: events, fetchResult: &VideoProviderFetchResult{
		ProviderTaskID: "upstream_refresh",
		Status:         VideoTaskStatusCompleted,
		ProviderStatus: "completed",
		RawBody:        []byte(`{"id":"upstream_refresh","status":"completed"}`),
	}}
	svc := newVideoTaskServiceForTest(repo, accountStore, nil, provider, nil)

	result, err := svc.Refresh(ctx, VideoTaskActionParams{UserID: 7, PublicTaskID: "task_refresh"})

	require.NoError(t, err)
	require.Equal(t, VideoTaskStatusCompleted, result.Task.Status)
	require.JSONEq(t, `{"id":"task_refresh","status":"completed"}`, string(result.ResponseBody))
}

func TestVideoTaskServiceCancelPersistsCancelledResultAfterUpstreamCancelsCaller(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	nextPollAt := time.Now().Add(time.Minute)
	repo := newFakeVideoTaskRepository(nil)
	repo.rejectCanceledProviderUpdate = true
	repo.seedTask(&VideoTask{
		PublicTaskID:   "task_cancel_after_client_disconnect",
		ProviderTaskID: "upstream_cancel_after_client_disconnect",
		UserID:         7,
		AccountID:      99,
		Status:         VideoTaskStatusQueued,
		NextPollAt:     &nextPollAt,
		Metadata:       map[string]any{VideoAdapterMetadataKey: VideoAdapterSeedanceAPIV1},
	})
	storedAdapterCanceller := &fakeVideoTaskProvider{
		cancelAfter: cancel,
		cancelResult: &VideoProviderFetchResult{
			ProviderTaskID: "upstream_cancel_after_client_disconnect",
			Status:         VideoTaskStatusCancelled,
			ProviderStatus: "cancelled",
			RawBody:        []byte(`{"id":"upstream_cancel_after_client_disconnect","status":"cancelled"}`),
		},
	}
	currentAccountAdapterCanceller := &fakeVideoTaskProvider{}
	provider := &accountVideoTaskProvider{registry: videoAdapterRegistry{
		VideoAdapterSeedanceAPIV1:        &fakeVideoTaskAdapter{name: VideoAdapterSeedanceAPIV1, fakeVideoTaskProvider: storedAdapterCanceller},
		VideoAdapterOpenAIVideosDuration: &fakeVideoTaskAdapter{name: VideoAdapterOpenAIVideosDuration, fakeVideoTaskProvider: currentAccountAdapterCanceller},
	}}
	accounts := &fakeVideoTaskAccountStore{accounts: map[int64]*Account{
		99: {ID: 99, Credentials: map[string]any{VideoAdapterMetadataKey: VideoAdapterOpenAIVideosDuration}},
	}}
	svc := newVideoTaskServiceForTest(repo, accounts, nil, provider, nil)

	result, err := svc.Cancel(ctx, VideoTaskActionParams{UserID: 7, PublicTaskID: "task_cancel_after_client_disconnect"})

	require.NoError(t, err)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.Equal(t, 1, storedAdapterCanceller.cancelCalls)
	require.Zero(t, currentAccountAdapterCanceller.cancelCalls)
	require.NoError(t, repo.updateProviderCtxErr)
	require.True(t, repo.updateProviderHasDeadline)
	require.True(t, repo.updateProviderDeadline.After(time.Now()))
	require.NoError(t, repo.reloadCtxErr)
	require.True(t, repo.reloadHasDeadline)
	require.True(t, repo.reloadDeadline.After(time.Now()))
	require.NotNil(t, result)
	require.Equal(t, VideoTaskStatusCancelled, result.Task.Status)
	require.Equal(t, "cancelled", result.Task.ProviderStatus)
	require.Nil(t, result.Task.NextPollAt)
	require.JSONEq(t, `{"id":"task_cancel_after_client_disconnect","status":"cancelled"}`, string(result.ResponseBody))

	persisted := repo.tasks["task_cancel_after_client_disconnect"]
	require.Equal(t, VideoTaskStatusCancelled, persisted.Status)
	require.Nil(t, persisted.NextPollAt)
	require.Equal(t, persisted, result.Task)
}

func TestVideoTaskServiceFetchAndRefreshPersistTerminalResultAfterProviderCancelsCaller(t *testing.T) {
	for _, tt := range []struct {
		name string
		call func(context.Context, *VideoTaskService, string) (*VideoTaskFetchResult, error)
	}{
		{
			name: "fetch",
			call: func(ctx context.Context, svc *VideoTaskService, publicTaskID string) (*VideoTaskFetchResult, error) {
				return svc.Fetch(ctx, VideoTaskFetchParams{UserID: 7, PublicTaskID: publicTaskID})
			},
		},
		{
			name: "refresh",
			call: func(ctx context.Context, svc *VideoTaskService, publicTaskID string) (*VideoTaskFetchResult, error) {
				return svc.Refresh(ctx, VideoTaskActionParams{UserID: 7, PublicTaskID: publicTaskID})
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			nextPollAt := time.Now().Add(time.Minute)
			lockedBy := "poller-lease"
			lockedUntil := time.Now().Add(time.Minute)
			publicTaskID := "task_" + tt.name + "_after_client_disconnect"
			repo := newFakeVideoTaskRepository(nil)
			repo.rejectCanceledProviderUpdate = true
			repo.seedTask(&VideoTask{
				PublicTaskID:   publicTaskID,
				ProviderTaskID: "upstream_" + tt.name + "_after_client_disconnect",
				UserID:         7,
				AccountID:      99,
				Status:         VideoTaskStatusQueued,
				NextPollAt:     &nextPollAt,
				LockedBy:       &lockedBy,
				LockedUntil:    &lockedUntil,
			})
			provider := &fakeVideoTaskProvider{
				cancelAfterFetch: cancel,
				fetchResult: &VideoProviderFetchResult{
					ProviderTaskID: "upstream_" + tt.name + "_after_client_disconnect",
					Status:         VideoTaskStatusCompleted,
					ProviderStatus: "completed",
					RawBody:        []byte(`{"id":"upstream_terminal","status":"completed"}`),
				},
			}
			svc := newVideoTaskServiceForTest(repo, &fakeVideoTaskAccountStore{accounts: map[int64]*Account{99: {ID: 99}}}, nil, provider, nil)

			result, err := tt.call(ctx, svc, publicTaskID)

			require.NoError(t, err)
			require.ErrorIs(t, ctx.Err(), context.Canceled)
			require.Same(t, ctx, provider.fetchCtx)
			if tt.name == "refresh" {
				require.Same(t, ctx, provider.refreshCtx)
			}
			require.NoError(t, repo.updateProviderCtxErr)
			require.True(t, repo.updateProviderHasDeadline)
			require.True(t, repo.updateProviderDeadline.After(time.Now()))
			require.NoError(t, repo.reloadCtxErr)
			require.True(t, repo.reloadHasDeadline)
			require.True(t, repo.reloadDeadline.After(time.Now()))
			require.NotNil(t, result)
			require.Equal(t, VideoTaskStatusCompleted, result.Task.Status)
			require.Nil(t, result.Task.NextPollAt)
			require.Nil(t, result.Task.LockedBy)
			require.Nil(t, result.Task.LockedUntil)
			require.JSONEq(t, `{"id":"`+publicTaskID+`","status":"completed"}`, string(result.ResponseBody))

			persisted := repo.tasks[publicTaskID]
			require.Equal(t, VideoTaskStatusCompleted, persisted.Status)
			require.Nil(t, persisted.NextPollAt)
			require.Nil(t, persisted.LockedBy)
			require.Nil(t, persisted.LockedUntil)
			require.Equal(t, persisted, result.Task)
		})
	}
}

func TestVideoTaskServiceForegroundActionsReturnDurableTerminalResponseAfterProviderUpdateLoss(t *testing.T) {
	for _, tt := range []struct {
		name           string
		providerResult *VideoProviderFetchResult
		call           func(*VideoTaskService) (*VideoTaskFetchResult, error)
	}{
		{
			name: "fetch",
			providerResult: &VideoProviderFetchResult{
				Status:         VideoTaskStatusInProgress,
				ProviderStatus: "processing",
				RawBody:        []byte(`{"id":"upstream_stale","status":"in_progress","source":"foreground"}`),
			},
			call: func(svc *VideoTaskService) (*VideoTaskFetchResult, error) {
				return svc.Fetch(context.Background(), VideoTaskFetchParams{UserID: 7, PublicTaskID: "task_foreground_race"})
			},
		},
		{
			name: "refresh",
			providerResult: &VideoProviderFetchResult{
				Status:         VideoTaskStatusInProgress,
				ProviderStatus: "processing",
				RawBody:        []byte(`{"id":"upstream_stale","status":"in_progress","source":"foreground"}`),
			},
			call: func(svc *VideoTaskService) (*VideoTaskFetchResult, error) {
				return svc.Refresh(context.Background(), VideoTaskActionParams{UserID: 7, PublicTaskID: "task_foreground_race"})
			},
		},
		{
			name: "cancel",
			providerResult: &VideoProviderFetchResult{
				Status:         VideoTaskStatusCancelled,
				ProviderStatus: "cancelled",
				RawBody:        []byte(`{"id":"upstream_stale","status":"cancelled","source":"foreground"}`),
			},
			call: func(svc *VideoTaskService) (*VideoTaskFetchResult, error) {
				return svc.Cancel(context.Background(), VideoTaskActionParams{UserID: 7, PublicTaskID: "task_foreground_race"})
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeVideoTaskRepository(nil)
			repo.seedTask(&VideoTask{
				PublicTaskID:   "task_foreground_race",
				ProviderTaskID: "upstream_task",
				UserID:         7,
				AccountID:      99,
				Status:         VideoTaskStatusQueued,
				ResponseBody:   []byte(`{"id":"upstream_task","status":"queued"}`),
			})
			repo.beforeProviderUpdate = func(task *VideoTask) {
				task.Status = VideoTaskStatusCompleted
				task.ProviderStatus = "completed-by-poller"
				task.ResponseBody = []byte(`{"id":"upstream_terminal","status":"completed","source":"poller"}`)
				task.Metadata = map[string]any{"source": "poller"}
			}
			provider := &fakeVideoTaskProvider{fetchResult: tt.providerResult, cancelResult: tt.providerResult}
			svc := newVideoTaskServiceForTest(repo, &fakeVideoTaskAccountStore{accounts: map[int64]*Account{99: {ID: 99}}}, nil, provider, nil)

			result, err := tt.call(svc)

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, VideoTaskStatusCompleted, result.Task.Status)
			require.Equal(t, "completed-by-poller", result.Task.ProviderStatus)
			require.JSONEq(t, `{"id":"task_foreground_race","status":"completed","source":"poller"}`, string(result.ResponseBody))
		})
	}
}

func TestVideoTaskServiceFetchAndRefreshSerializeReloadedTaskAfterSuccessfulUpdate(t *testing.T) {
	for _, tt := range []struct {
		name             string
		concurrentUpdate bool
		call             func(*VideoTaskService) (*VideoTaskFetchResult, error)
		wantResponse     string
		wantStatus       VideoTaskStatus
		wantMetadata     string
	}{
		{
			name:             "fetch terminal poller update",
			concurrentUpdate: true,
			call: func(svc *VideoTaskService) (*VideoTaskFetchResult, error) {
				return svc.Fetch(context.Background(), VideoTaskFetchParams{UserID: 7, PublicTaskID: "task_foreground_race"})
			},
			wantResponse: `{"id":"task_foreground_race","status":"completed","source":"poller"}`,
			wantStatus:   VideoTaskStatusCompleted,
			wantMetadata: "poller",
		},
		{
			name:             "refresh terminal poller update",
			concurrentUpdate: true,
			call: func(svc *VideoTaskService) (*VideoTaskFetchResult, error) {
				return svc.Refresh(context.Background(), VideoTaskActionParams{UserID: 7, PublicTaskID: "task_foreground_race"})
			},
			wantResponse: `{"id":"task_foreground_race","status":"completed","source":"poller"}`,
			wantStatus:   VideoTaskStatusCompleted,
			wantMetadata: "poller",
		},
		{
			name: "fetch nonterminal foreground update",
			call: func(svc *VideoTaskService) (*VideoTaskFetchResult, error) {
				return svc.Fetch(context.Background(), VideoTaskFetchParams{UserID: 7, PublicTaskID: "task_foreground_race"})
			},
			wantResponse: `{"id":"task_foreground_race","status":"in_progress","source":"foreground"}`,
			wantStatus:   VideoTaskStatusInProgress,
			wantMetadata: "foreground",
		},
		{
			name: "refresh nonterminal foreground update",
			call: func(svc *VideoTaskService) (*VideoTaskFetchResult, error) {
				return svc.Refresh(context.Background(), VideoTaskActionParams{UserID: 7, PublicTaskID: "task_foreground_race"})
			},
			wantResponse: `{"id":"task_foreground_race","status":"in_progress","source":"foreground"}`,
			wantStatus:   VideoTaskStatusInProgress,
			wantMetadata: "foreground",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeVideoTaskRepository(nil)
			repo.seedTask(&VideoTask{
				PublicTaskID:   "task_foreground_race",
				ProviderTaskID: "upstream_task",
				UserID:         7,
				AccountID:      99,
				Status:         VideoTaskStatusQueued,
				ResponseBody:   []byte(`{"id":"upstream_task","status":"queued"}`),
			})
			if tt.concurrentUpdate {
				repo.afterProviderUpdate = func(task *VideoTask) {
					task.Status = VideoTaskStatusCompleted
					task.ProviderStatus = "completed-by-poller"
					task.ResponseBody = []byte(`{"id":"upstream_terminal","status":"completed","source":"poller"}`)
					task.Metadata = map[string]any{"source": "poller"}
				}
			}
			providerResult := &VideoProviderFetchResult{
				Status:         VideoTaskStatusInProgress,
				ProviderStatus: "processing",
				RawBody:        []byte(`{"id":"upstream_stale","status":"in_progress","source":"foreground"}`),
				Metadata:       map[string]any{"source": "foreground"},
			}
			provider := &fakeVideoTaskProvider{fetchResult: providerResult}
			svc := newVideoTaskServiceForTest(repo, &fakeVideoTaskAccountStore{accounts: map[int64]*Account{99: {ID: 99}}}, nil, provider, nil)

			result, err := tt.call(svc)

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, tt.wantStatus, result.Task.Status)
			require.Equal(t, tt.wantMetadata, result.Task.Metadata["source"])
			require.JSONEq(t, tt.wantResponse, string(result.ResponseBody))
		})
	}
}

func TestVideoTaskServiceFetchPreservesPersistedAdapterMetadata(t *testing.T) {
	ctx := context.Background()
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	repo.seedTask(&VideoTask{
		PublicTaskID:   "task_seedance_fetch",
		ProviderTaskID: "123",
		UserID:         7,
		AccountID:      99,
		Status:         VideoTaskStatusQueued,
		Metadata:       map[string]any{VideoAdapterMetadataKey: VideoAdapterSeedanceAPIV1},
	})
	accountStore := &fakeVideoTaskAccountStore{events: events, accounts: map[int64]*Account{
		99: {ID: 99, Credentials: map[string]any{VideoAdapterMetadataKey: VideoAdapterOpenAIVideosDuration}},
	}}
	seedanceProvider := &fakeVideoTaskProvider{events: events, fetchResult: &VideoProviderFetchResult{
		ProviderTaskID: "123",
		Status:         VideoTaskStatusCompleted,
		ProviderStatus: "succeeded",
		RawBody:        []byte(`{"id":"123","status":"completed"}`),
		Metadata:       map[string]any{VideoAdapterMetadataKey: VideoAdapterSeedanceAPIV1},
	}}
	durationProvider := &fakeVideoTaskProvider{events: events}
	provider := &accountVideoTaskProvider{registry: videoAdapterRegistry{
		VideoAdapterSeedanceAPIV1:        &fakeVideoTaskAdapter{name: VideoAdapterSeedanceAPIV1, fakeVideoTaskProvider: seedanceProvider},
		VideoAdapterOpenAIVideosDuration: &fakeVideoTaskAdapter{name: VideoAdapterOpenAIVideosDuration, fakeVideoTaskProvider: durationProvider},
	}}
	svc := newVideoTaskServiceForTest(repo, accountStore, nil, provider, nil)

	result, err := svc.Fetch(ctx, VideoTaskFetchParams{UserID: 7, PublicTaskID: "task_seedance_fetch"})

	require.NoError(t, err)
	require.Equal(t, VideoTaskStatusCompleted, result.Task.Status)
	require.Equal(t, 1, seedanceProvider.fetchCalls)
	require.Zero(t, durationProvider.fetchCalls)
	require.Equal(t, VideoAdapterSeedanceAPIV1, seedanceProvider.fetchTaskMetadata[VideoAdapterMetadataKey])
	require.Equal(t, VideoAdapterSeedanceAPIV1, repo.tasks["task_seedance_fetch"].Metadata[VideoAdapterMetadataKey])
}

func TestVideoTaskServiceContentUsesPersistedAdapterMetadata(t *testing.T) {
	ctx := context.Background()
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	repo.seedTask(&VideoTask{
		PublicTaskID:   "task_seedance_content",
		ProviderTaskID: "123",
		UserID:         7,
		AccountID:      99,
		Status:         VideoTaskStatusCompleted,
		Metadata:       map[string]any{VideoAdapterMetadataKey: VideoAdapterSeedanceAPIV1},
	})
	accountStore := &fakeVideoTaskAccountStore{events: events, accounts: map[int64]*Account{
		99: {ID: 99, Credentials: map[string]any{VideoAdapterMetadataKey: VideoAdapterOpenAIVideosDuration}},
	}}
	seedanceProvider := &fakeVideoTaskProvider{events: events}
	durationProvider := &fakeVideoTaskProvider{events: events}
	provider := &accountVideoTaskProvider{registry: videoAdapterRegistry{
		VideoAdapterSeedanceAPIV1:        &fakeVideoTaskAdapter{name: VideoAdapterSeedanceAPIV1, fakeVideoTaskProvider: seedanceProvider},
		VideoAdapterOpenAIVideosDuration: &fakeVideoTaskAdapter{name: VideoAdapterOpenAIVideosDuration, fakeVideoTaskProvider: durationProvider},
	}}
	svc := newVideoTaskServiceForTest(repo, accountStore, nil, provider, nil)

	stream, err := svc.Content(ctx, VideoTaskContentParams{UserID: 7, PublicTaskID: "task_seedance_content"})
	if stream != nil && stream.Body != nil {
		defer func() { _ = stream.Body.Close() }()
	}

	require.NoError(t, err)
	require.Equal(t, 1, seedanceProvider.contentCalls)
	require.Zero(t, durationProvider.contentCalls)
	require.Equal(t, []int64{99}, seedanceProvider.contentAccountIDs)
	require.Equal(t, VideoAdapterSeedanceAPIV1, seedanceProvider.contentTaskMetadata[VideoAdapterMetadataKey])
	require.Equal(t, VideoAdapterSeedanceAPIV1, repo.tasks["task_seedance_content"].Metadata[VideoAdapterMetadataKey])
}

func TestVideoTaskServiceContentRejectsIncompleteTask(t *testing.T) {
	ctx := context.Background()
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	repo.seedTask(&VideoTask{
		PublicTaskID: "task_incomplete",
		UserID:       7,
		AccountID:    99,
		Status:       VideoTaskStatusQueued,
	})
	provider := &fakeVideoTaskProvider{events: events}
	svc := newVideoTaskServiceForTest(repo, &fakeVideoTaskAccountStore{}, nil, provider, nil)

	stream, err := svc.Content(ctx, VideoTaskContentParams{UserID: 7, PublicTaskID: "task_incomplete"})

	require.Nil(t, stream)
	require.ErrorContains(t, err, "current status: queued")
	require.ErrorIs(t, err, ErrVideoTaskNotCompleted)
	require.Zero(t, provider.contentCalls)
}

func TestVideoTaskServiceCrossUserActionsReturnNotFoundBeforeAccountOrAdapterAccess(t *testing.T) {
	const (
		ownerUserID  = int64(7)
		otherUserID  = int64(8)
		publicTaskID = "task_owned"
		adapterName  = "ownership_test"
	)

	tests := []struct {
		name   string
		status VideoTaskStatus
		call   func(context.Context, *VideoTaskService) error
	}{
		{
			name:   "Fetch",
			status: VideoTaskStatusQueued,
			call: func(ctx context.Context, svc *VideoTaskService) error {
				_, err := svc.Fetch(ctx, VideoTaskFetchParams{UserID: otherUserID, PublicTaskID: publicTaskID})
				return err
			},
		},
		{
			name:   "Content",
			status: VideoTaskStatusCompleted,
			call: func(ctx context.Context, svc *VideoTaskService) error {
				stream, err := svc.Content(ctx, VideoTaskContentParams{UserID: otherUserID, PublicTaskID: publicTaskID})
				if stream != nil && stream.Body != nil {
					_ = stream.Body.Close()
				}
				return err
			},
		},
		{
			name:   "Refresh",
			status: VideoTaskStatusQueued,
			call: func(ctx context.Context, svc *VideoTaskService) error {
				_, err := svc.Refresh(ctx, VideoTaskActionParams{UserID: otherUserID, PublicTaskID: publicTaskID})
				return err
			},
		},
		{
			name:   "Cancel",
			status: VideoTaskStatusQueued,
			call: func(ctx context.Context, svc *VideoTaskService) error {
				_, err := svc.Cancel(ctx, VideoTaskActionParams{UserID: otherUserID, PublicTaskID: publicTaskID})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := &videoTaskServiceTestEvents{}
			repo := newFakeVideoTaskRepository(events)
			repo.seedTask(&VideoTask{
				PublicTaskID:   publicTaskID,
				ProviderTaskID: "upstream_owned",
				UserID:         ownerUserID,
				AccountID:      99,
				Status:         tt.status,
				Metadata:       map[string]any{VideoAdapterMetadataKey: adapterName},
			})
			before := cloneVideoTask(repo.tasks[publicTaskID])
			accountStore := &fakeVideoTaskAccountStore{
				events:   events,
				accounts: map[int64]*Account{99: {ID: 99, Platform: PlatformOpenAI}},
			}
			adapterProvider := &fakeVideoTaskProvider{
				events: events,
				fetchResult: &VideoProviderFetchResult{
					ProviderTaskID: "upstream_owned",
					Status:         VideoTaskStatusCompleted,
					ProviderStatus: "completed",
					RawBody:        []byte(`{"id":"upstream_owned","status":"completed"}`),
				},
			}
			adapter := &ownershipVideoTaskAdapter{name: adapterName, fakeVideoTaskProvider: adapterProvider}
			provider := &accountVideoTaskProvider{registry: videoAdapterRegistry{adapterName: adapter}}
			svc := newVideoTaskServiceForTest(repo, accountStore, nil, provider, nil)

			err := tt.call(context.Background(), svc)

			require.ErrorIs(t, err, ErrVideoTaskNotFound)
			require.Empty(t, accountStore.getByIDCalls)
			require.Zero(t, adapterProvider.createCalls)
			require.Zero(t, adapterProvider.fetchCalls)
			require.Zero(t, adapterProvider.contentCalls)
			require.Zero(t, adapter.cancelCalls)
			require.Equal(t, []string{"repo_get_user_public"}, events.snapshot())
			require.Equal(t, before, repo.tasks[publicTaskID])
		})
	}
}

func TestVideoTaskServiceCreatePermissionDeniedUsesStableError(t *testing.T) {
	ctx := context.Background()
	groupID := int64(5)
	svc := newVideoTaskServiceForTest(newFakeVideoTaskRepository(nil), nil, nil, nil, nil)

	result, err := svc.Create(ctx, VideoTaskCreateParams{
		APIKey: &APIKey{
			ID:      42,
			UserID:  7,
			GroupID: &groupID,
			Group:   &Group{ID: groupID, Platform: PlatformOpenAI, AllowVideoGeneration: false},
		},
		User: &User{ID: 7},
		Body: []byte(`{"model":"video-ds-2.0","prompt":"make a short city clip"}`),
	})

	require.Nil(t, result)
	require.ErrorIs(t, err, ErrVideoGenerationPermissionDenied)
	require.ErrorContains(t, err, VideoGenerationPermissionMessage)
}

func TestVideoTaskServiceCreateAllowsResolvedCompositeOpenAI(t *testing.T) {
	ctx := WithResolvedTargetPlatform(context.Background(), PlatformOpenAI)
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	provider := &fakeVideoTaskProvider{events: events, createResult: &VideoProviderCreateResult{
		ProviderTaskID: "upstream_composite_video",
		Status:         VideoTaskStatusQueued,
		ProviderStatus: "queued",
		RawBody:        []byte(`{"id":"upstream_composite_video","status":"queued"}`),
	}}
	selector := &fakeVideoTaskSelector{events: events, selection: &AccountSelectionResult{Account: &Account{ID: 99, Platform: PlatformOpenAI}}}
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, nil)

	result, err := svc.Create(ctx, VideoTaskCreateParams{
		APIKey:      videoTaskCompositeTestAPIKey(true),
		User:        &User{ID: 7},
		Body:        []byte(`{"model":"sora-upstream","prompt":"waves"}`),
		ContentType: "application/json",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, selector.selectCalls)
	require.Equal(t, 1, provider.createCalls)
}

func TestVideoTaskServiceCreateCompositePreservesPublicAliasAndForwardsConcreteVideoModel(t *testing.T) {
	ctx := WithCompositeRouteDecision(context.Background(), CompositeRouteDecision{
		Matched:        true,
		Source:         CompositeRouteSourceExplicit,
		PublicModel:    "video-alias",
		TargetPlatform: PlatformOpenAI,
		UpstreamModel:  "sora-upstream",
		Endpoint:       CompositeRouteEndpointVideo,
	})
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	provider := &fakeVideoTaskProvider{events: events, createResult: &VideoProviderCreateResult{
		ProviderTaskID: "upstream_composite_alias",
		Status:         VideoTaskStatusQueued,
		ProviderStatus: "queued",
		RawBody:        []byte(`{"id":"upstream_composite_alias","status":"queued"}`),
	}}
	selector := &fakeVideoTaskSelector{events: events, selection: &AccountSelectionResult{Account: &Account{
		ID:       99,
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"sora-upstream": "provider-sora"},
		},
	}}}
	price, seconds := 0.08, 5
	pricing := &fakeVideoTaskPricingResolver{events: events, selection: VideoTaskPricingSelection{
		Pricing:               &ChannelModelPricing{BillingMode: BillingModeVideo, VideoPricePerSecond: &price, VideoDefaultSeconds: &seconds},
		BillingModel:          "provider-sora",
		BillingModelSource:    BillingModelSourceUpstream,
		RateMultiplier:        1,
		AccountRateMultiplier: 1,
	}}
	settlement := &fakeVideoTaskSettlementOrchestrator{events: events, state: VideoTaskSettlementCharged}
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, nil)
	svc.pricingResolver = pricing
	svc.settlement = settlement

	result, err := svc.Create(ctx, VideoTaskCreateParams{
		APIKey:      videoTaskCompositeTestAPIKey(true),
		User:        &User{ID: 7},
		Body:        []byte(`{"model":"sora-upstream","prompt":"waves","seconds":5}`),
		ContentType: "application/json",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "sora-upstream", selector.lastModel)
	require.Equal(t, "provider-sora", provider.createUpstreamModel)
	require.Equal(t, "video-alias", pricing.lastInput.RequestedModel)
	require.Equal(t, "provider-sora", pricing.lastInput.UpstreamModel)
	require.Equal(t, "video-alias", repo.lastCreate.Model)
	require.Equal(t, "video-alias", repo.lastCreate.Metadata["requested_model"])
	require.Equal(t, "provider-sora", repo.lastCreate.Metadata["upstream_model"])
	require.Equal(t, "provider-sora", repo.lastCreate.Metadata["billing_model"])
	require.Equal(t, "video-alias", settlement.reserveInput.RequestedModel)
	require.Equal(t, "video-alias", settlement.reserveUsage.RequestedModel)
	require.Equal(t, "provider-sora", settlement.reserveUsage.Model)
	require.Equal(t, "provider-sora", settlement.reserveInput.Quote.BillingModel)
}

func TestVideoTaskServiceEstimateAllowsResolvedCompositeOpenAI(t *testing.T) {
	ctx := WithResolvedTargetPlatform(context.Background(), PlatformOpenAI)
	events := &videoTaskServiceTestEvents{}
	selector := &fakeVideoTaskSelector{events: events, selection: &AccountSelectionResult{Account: &Account{ID: 99, Platform: PlatformOpenAI}}}
	provider := &fakeVideoTaskEstimator{fakeVideoTaskProvider: &fakeVideoTaskProvider{events: events}}
	svc := newVideoTaskServiceForTest(newFakeVideoTaskRepository(events), nil, selector, provider, nil)

	result, err := svc.Estimate(ctx, VideoTaskEstimateParams{
		APIKey:      videoTaskCompositeTestAPIKey(true),
		User:        &User{ID: 7},
		Body:        []byte(`{"model":"sora-upstream","prompt":"waves"}`),
		ContentType: "application/json",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, selector.selectCalls)
	require.JSONEq(t, `{"model":"sora-upstream","prompt":"waves"}`, string(provider.estimateBody))
}

func TestVideoTaskServiceCreateRejectsUnresolvedCompositeBeforeAccountSelection(t *testing.T) {
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	selector := &fakeVideoTaskSelector{events: events, selection: &AccountSelectionResult{Account: &Account{ID: 99, Platform: PlatformOpenAI}}}
	provider := &fakeVideoTaskProvider{events: events}
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, nil)

	result, err := svc.Create(context.Background(), VideoTaskCreateParams{
		APIKey: videoTaskCompositeTestAPIKey(true),
		User:   &User{ID: 7},
		Body:   []byte(`{"model":"sora-upstream","prompt":"waves"}`),
	})

	require.Nil(t, result)
	require.ErrorContains(t, err, "video generation requires OpenAI group, got composite")
	require.Zero(t, selector.selectCalls)
	require.Zero(t, provider.createCalls)
}

func TestVideoTaskServiceEstimateRejectsUnresolvedCompositeBeforeAccountSelection(t *testing.T) {
	events := &videoTaskServiceTestEvents{}
	selector := &fakeVideoTaskSelector{events: events, selection: &AccountSelectionResult{Account: &Account{ID: 99, Platform: PlatformOpenAI}}}
	provider := &fakeVideoTaskEstimator{fakeVideoTaskProvider: &fakeVideoTaskProvider{events: events}}
	svc := newVideoTaskServiceForTest(newFakeVideoTaskRepository(events), nil, selector, provider, nil)

	result, err := svc.Estimate(context.Background(), VideoTaskEstimateParams{
		APIKey: videoTaskCompositeTestAPIKey(true),
		User:   &User{ID: 7},
		Body:   []byte(`{"model":"sora-upstream","prompt":"waves"}`),
	})

	require.Nil(t, result)
	require.ErrorContains(t, err, "video generation requires OpenAI group, got composite")
	require.Zero(t, selector.selectCalls)
}

func TestVideoTaskServiceCreateResolvedCompositeStillRequiresVideoPermission(t *testing.T) {
	ctx := WithResolvedTargetPlatform(context.Background(), PlatformOpenAI)
	events := &videoTaskServiceTestEvents{}
	selector := &fakeVideoTaskSelector{events: events, selection: &AccountSelectionResult{Account: &Account{ID: 99, Platform: PlatformOpenAI}}}
	provider := &fakeVideoTaskProvider{events: events}
	svc := newVideoTaskServiceForTest(newFakeVideoTaskRepository(events), nil, selector, provider, nil)

	result, err := svc.Create(ctx, VideoTaskCreateParams{
		APIKey: videoTaskCompositeTestAPIKey(false),
		User:   &User{ID: 7},
		Body:   []byte(`{"model":"sora-upstream","prompt":"waves"}`),
	})

	require.Nil(t, result)
	require.ErrorIs(t, err, ErrVideoGenerationPermissionDenied)
	require.Zero(t, selector.selectCalls)
	require.Zero(t, provider.createCalls)
}

func TestVideoTaskServiceCreateNoAvailableAccountsUsesStableAccountUnavailableError(t *testing.T) {
	ctx := context.Background()
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	selector := &fakeVideoTaskSelector{
		events: events,
		err:    ErrNoAvailableAccounts,
	}
	provider := &fakeVideoTaskProvider{events: events}
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, nil)

	result, err := svc.Create(ctx, VideoTaskCreateParams{
		APIKey: videoTaskTestAPIKey(),
		User:   &User{ID: 7},
		Body:   []byte(`{"model":"video-ds-2.0-fast","prompt":"make a short city clip"}`),
	})

	require.Nil(t, result)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.ErrorIs(t, err, ErrVideoTaskAccountUnavailable)
	require.Zero(t, provider.createCalls)
	require.Equal(t, []string{"selector_select"}, events.snapshot())
}

func TestVideoTaskServiceFetchNotFoundUsesStableError(t *testing.T) {
	ctx := context.Background()
	repo := newFakeVideoTaskRepository(nil)
	repo.notFoundErr = infraerrors.NotFound("FAKE_NOT_FOUND", "fake task not found")
	svc := newVideoTaskServiceForTest(repo, nil, nil, nil, nil)

	result, err := svc.Fetch(ctx, VideoTaskFetchParams{UserID: 7, PublicTaskID: "task_missing"})

	require.Nil(t, result)
	require.ErrorIs(t, err, ErrVideoTaskNotFound)
}

func TestVideoTaskServiceCreateIdempotencyReturnsExistingTask(t *testing.T) {
	ctx := context.Background()
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	body := []byte(`{"model":"video-ds-2.0-fast","prompt":"make a short city clip"}`)
	req, err := ParseOpenAIVideoCreateRequest(body)
	require.NoError(t, err)
	repo.seedTask(&VideoTask{
		PublicTaskID:   "task_existing",
		UserID:         7,
		APIKeyID:       42,
		RequestHash:    req.RequestHash,
		Status:         VideoTaskStatusQueued,
		ResponseBody:   []byte(`{"id":"upstream_existing","status":"queued"}`),
		Metadata:       map[string]any{"idempotency_key": "idem-hit"},
		RequestBody:    body,
		Provider:       VideoTaskProviderOpenAICompatible,
		Platform:       VideoTaskPlatformOpenAIVideo,
		ProviderStatus: "queued",
	})
	provider := &fakeVideoTaskProvider{events: events}
	selector := &fakeVideoTaskSelector{events: events}
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, nil)

	result, err := svc.Create(ctx, VideoTaskCreateParams{
		APIKey:         videoTaskTestAPIKey(),
		User:           &User{ID: 7},
		Body:           body,
		ContentType:    "application/json",
		IdempotencyKey: "idem-hit",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "task_existing", result.Task.PublicTaskID)
	require.Zero(t, provider.createCalls)
	require.Zero(t, selector.selectCalls)

	var response map[string]any
	require.NoError(t, json.Unmarshal(result.ResponseBody, &response))
	require.Equal(t, "task_existing", response["id"])
}

func TestVideoTaskServiceCreateIdempotencyNotFoundMissProceeds(t *testing.T) {
	ctx := context.Background()
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	repo.idempotencyErr = infraerrors.NotFound("VIDEO_TASK_NOT_FOUND", "video task not found")
	provider := &fakeVideoTaskProvider{
		events: events,
		createResult: &VideoProviderCreateResult{
			ProviderTaskID: "upstream_task_miss",
			Status:         VideoTaskStatusQueued,
			ProviderStatus: "queued",
			RawBody:        []byte(`{"id":"upstream_task_miss","status":"queued"}`),
		},
	}
	selector := &fakeVideoTaskSelector{
		events: events,
		selection: &AccountSelectionResult{
			Account:     &Account{ID: 99, Platform: PlatformOpenAI},
			ReleaseFunc: func() { events.add("release") },
		},
	}
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, nil)

	result, err := svc.Create(ctx, VideoTaskCreateParams{
		APIKey:         videoTaskTestAPIKey(),
		User:           &User{ID: 7},
		Body:           []byte(`{"model":"video-ds-2.0-fast","prompt":"make a short city clip"}`),
		ContentType:    "application/json",
		IdempotencyKey: "idem-first-use",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, provider.createCalls)
	require.Equal(t, []string{"repo_get_idempotency", "selector_select", "repo_create", "provider_create", "repo_attach", "repo_get_public", "release"}, events.snapshot())
}

func TestVideoTaskServiceCreateIdempotencyExistingWithoutResponseBodyReturnsLocalState(t *testing.T) {
	ctx := context.Background()
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	body := []byte(`{"model":"video-ds-2.0","prompt":"make a short city clip"}`)
	req, err := ParseOpenAIVideoCreateRequest(body)
	require.NoError(t, err)
	repo.seedTask(&VideoTask{
		PublicTaskID: "task_submitting",
		UserID:       7,
		APIKeyID:     42,
		RequestHash:  req.RequestHash,
		Status:       VideoTaskStatusSubmitting,
		Metadata:     map[string]any{"idempotency_key": "idem-submitting"},
		RequestBody:  body,
		Provider:     VideoTaskProviderOpenAICompatible,
		Platform:     VideoTaskPlatformOpenAIVideo,
	})
	provider := &fakeVideoTaskProvider{events: events}
	selector := &fakeVideoTaskSelector{events: events}
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, nil)

	result, err := svc.Create(ctx, VideoTaskCreateParams{
		APIKey:         videoTaskTestAPIKey(),
		User:           &User{ID: 7},
		Body:           body,
		ContentType:    "application/json",
		IdempotencyKey: "idem-submitting",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Zero(t, provider.createCalls)
	require.Zero(t, selector.selectCalls)

	var response map[string]any
	require.NoError(t, json.Unmarshal(result.ResponseBody, &response))
	require.Equal(t, "task_submitting", response["id"])
	require.Equal(t, "video", response["object"])
	require.Equal(t, "submitting", response["status"])
}

func TestVideoTaskServiceCreateIdempotencyRejectsDifferentHash(t *testing.T) {
	ctx := context.Background()
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	repo.seedTask(&VideoTask{
		PublicTaskID: "task_existing",
		UserID:       7,
		APIKeyID:     42,
		RequestHash:  strings.Repeat("a", 64),
		Status:       VideoTaskStatusQueued,
		Metadata:     map[string]any{"idempotency_key": "idem-conflict"},
	})
	provider := &fakeVideoTaskProvider{events: events}
	selector := &fakeVideoTaskSelector{events: events}
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, nil)

	result, err := svc.Create(ctx, VideoTaskCreateParams{
		APIKey:         videoTaskTestAPIKey(),
		User:           &User{ID: 7},
		Body:           []byte(`{"model":"video-ds-2.0-fast","prompt":"different prompt"}`),
		ContentType:    "application/json",
		IdempotencyKey: "idem-conflict",
	})

	require.Nil(t, result)
	require.ErrorContains(t, err, "idempotency key reused with different request body")
	require.ErrorIs(t, err, ErrVideoTaskIdempotencyConflict)
	require.Zero(t, provider.createCalls)
	require.Zero(t, selector.selectCalls)
}

func TestVideoTaskServiceCreateIdempotencyRejectsSameHashDifferentEndpoint(t *testing.T) {
	ctx := context.Background()
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	body := []byte(`{"model":"video-ds-2.0-fast","prompt":"make a short city clip"}`)
	req, err := ParseVideoTaskCreateEnvelope(body)
	require.NoError(t, err)
	repo.seedTask(&VideoTask{
		PublicTaskID: "task_existing_endpoint",
		UserID:       7,
		APIKeyID:     42,
		RequestHash:  req.RequestHash,
		Status:       VideoTaskStatusQueued,
		Metadata: map[string]any{
			"idempotency_key":     "idem-endpoint-conflict",
			"video_task_endpoint": VideoTaskEndpointVideos,
		},
	})
	provider := &fakeVideoTaskProvider{events: events}
	selector := &fakeVideoTaskSelector{events: events}
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, nil)

	result, err := svc.Create(ctx, VideoTaskCreateParams{
		APIKey:         videoTaskTestAPIKey(),
		User:           &User{ID: 7},
		Body:           body,
		ContentType:    "application/json",
		IdempotencyKey: "idem-endpoint-conflict",
		Endpoint:       VideoTaskEndpointVideoGenerations,
	})

	require.Nil(t, result)
	require.ErrorIs(t, err, ErrVideoTaskIdempotencyConflict)
	require.ErrorContains(t, err, "different endpoint")
	require.Zero(t, provider.createCalls)
	require.Zero(t, selector.selectCalls)
}

func TestVideoTaskServiceCreateProviderFailureMarksLocalTaskFailed(t *testing.T) {
	ctx := context.Background()
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	providerErr := errors.New("upstream submit failed")
	provider := &fakeVideoTaskProvider{events: events, createErr: providerErr}
	selector := &fakeVideoTaskSelector{
		events: events,
		selection: &AccountSelectionResult{
			Account:     &Account{ID: 99, Platform: PlatformOpenAI},
			ReleaseFunc: func() { events.add("release") },
		},
	}
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, nil)

	result, err := svc.Create(ctx, VideoTaskCreateParams{
		APIKey:         videoTaskTestAPIKey(),
		User:           &User{ID: 7},
		Body:           []byte(`{"model":"video-ds-2.0-fast","prompt":"make a short city clip"}`),
		ContentType:    "application/json",
		IdempotencyKey: "idem-provider-fail",
	})

	require.Nil(t, result)
	require.ErrorIs(t, err, providerErr)
	require.Equal(t, []string{"repo_get_idempotency", "selector_select", "repo_create", "provider_create", "repo_update_provider", "release"}, events.snapshot())
	saved, err := repo.GetByIdempotencyKey(ctx, 42, "idem-provider-fail")
	require.NoError(t, err)
	require.Equal(t, VideoTaskStatusFailed, saved.Status)
	require.Equal(t, "failed", saved.ProviderStatus)
	require.Contains(t, saved.ErrorMessage, "upstream submit failed")
	require.NotNil(t, saved.CompletedAt)
}

func TestVideoTaskServiceCreateProviderFailurePersistsWithCancelledRequestContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	providerErr := context.Canceled
	provider := &fakeVideoTaskProvider{events: events, createErr: providerErr}
	selector := &fakeVideoTaskSelector{
		events:    events,
		selection: &AccountSelectionResult{Account: &Account{ID: 99, Platform: PlatformOpenAI}},
	}
	usage := &fakeVideoTaskUsageRecorder{events: events}
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, usage)

	result, err := svc.Create(ctx, VideoTaskCreateParams{
		APIKey:         videoTaskTestAPIKey(),
		User:           &User{ID: 7},
		Body:           []byte(`{"model":"video-ds-2.0-fast","prompt":"make a short city clip"}`),
		ContentType:    "application/json",
		IdempotencyKey: "idem-provider-cancel",
	})

	require.Nil(t, result)
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, repo.updateProviderCtxErr)
	saved, err := repo.GetByIdempotencyKey(context.Background(), 42, "idem-provider-cancel")
	require.NoError(t, err)
	require.Equal(t, VideoTaskStatusFailed, saved.Status)
}

func TestVideoTaskServiceCreateAttachUsesUncancelledPersistenceContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	provider := &fakeVideoTaskProvider{
		events: events,
		createResult: &VideoProviderCreateResult{
			ProviderTaskID: "upstream_after_cancel",
			Status:         VideoTaskStatusQueued,
			ProviderStatus: "queued",
			RawBody:        []byte(`{"id":"upstream_after_cancel","status":"queued"}`),
		},
	}
	selector := &fakeVideoTaskSelector{
		events:    events,
		selection: &AccountSelectionResult{Account: &Account{ID: 99, Platform: PlatformOpenAI}},
	}
	usage := &fakeVideoTaskUsageRecorder{events: events}
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, usage)

	result, err := svc.Create(ctx, VideoTaskCreateParams{
		APIKey:         videoTaskTestAPIKey(),
		User:           &User{ID: 7},
		Body:           []byte(`{"model":"video-ds-2.0-fast","prompt":"make a short city clip"}`),
		ContentType:    "application/json",
		IdempotencyKey: "idem-attach-cancel",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NoError(t, repo.attachCtxErr)
	require.NoError(t, repo.reloadCtxErr)
	require.True(t, repo.reloadHasDeadline)
	require.NoError(t, usage.ctxErr)
	require.True(t, usage.hasDeadline)
}

func TestVideoTaskServiceCreateAttachFailureKeepsTaskNonterminalAndPersistsFallback(t *testing.T) {
	const reconciliationMessage = "upstream task was created but could not be attached to the local task"

	for _, tt := range []struct {
		name          string
		cancelRequest bool
	}{
		{name: "active_request_context"},
		{name: "canceled_request_context", cancelRequest: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancelRequest {
				cancel()
			}
			events := &videoTaskServiceTestEvents{}
			attachErr := errors.New("attach upstream write failed")
			repo := newFakeVideoTaskRepository(events)
			repo.attachErr = attachErr
			provider := &fakeVideoTaskProvider{
				events: events,
				createResult: &VideoProviderCreateResult{
					ProviderTaskID: "upstream_attach_failed",
					Status:         VideoTaskStatusQueued,
					ProviderStatus: "queued",
					RawBody:        []byte(`{"id":"upstream_attach_failed","status":"queued"}`),
				},
			}
			selector := &fakeVideoTaskSelector{
				events: events,
				selection: &AccountSelectionResult{
					Account:     &Account{ID: 99, Platform: PlatformOpenAI},
					ReleaseFunc: func() { events.add("release") },
				},
			}
			usage := &fakeVideoTaskUsageRecorder{events: events}
			svc := newVideoTaskServiceForTest(repo, nil, selector, provider, usage)

			result, err := svc.Create(ctx, VideoTaskCreateParams{
				APIKey:      videoTaskTestAPIKey(),
				User:        &User{ID: 7},
				Body:        []byte(`{"model":"video-ds-2.0-fast","prompt":"city"}`),
				ContentType: "application/json",
				UserAgent:   "video-test/1.0",
				IPAddress:   "203.0.113.9",
			})

			require.Nil(t, result)
			require.ErrorIs(t, err, ErrVideoTaskSettlementRetriable)
			require.ErrorIs(t, err, attachErr)
			require.Equal(t, 1, provider.createCalls)
			require.Equal(t, 1, repo.attachCalls)
			require.Zero(t, usage.calls)
			require.NoError(t, repo.attachCtxErr)
			require.Equal(t, []string{"selector_select", "repo_create", "provider_create", "repo_attach", "repo_persist_upstream_fallback", "release"}, events.snapshot())

			task := repo.tasks[repo.lastCreate.PublicTaskID]
			require.NotNil(t, task)
			require.Equal(t, VideoTaskStatusSubmitting, task.Status)
			require.Empty(t, task.ProviderStatus)
			require.Empty(t, task.ErrorMessage)
			require.Equal(t, "ATTACH_UPSTREAM_FAILED", task.Metadata["reconciliation_error_code"])
			require.Equal(t, reconciliationMessage, task.Metadata["reconciliation_error_message"])
			require.Equal(t, "upstream_attach_failed", task.Metadata["reconciliation_upstream_task_id"])
			require.Equal(t, "queued", task.Metadata["reconciliation_provider_status"])
			require.Nil(t, task.CompletedAt)
			require.Nil(t, task.NextPollAt)
		})
	}
}

func TestVideoTaskServiceCreateAttachFailureReportsFallbackPersistenceError(t *testing.T) {
	events := &videoTaskServiceTestEvents{}
	attachErr := errors.New("attach upstream write failed")
	persistErr := errors.New("fallback persistence failed")
	repo := newFakeVideoTaskRepository(events)
	repo.attachErr = attachErr
	repo.fallbackErr = persistErr
	provider := &fakeVideoTaskProvider{events: events, createResult: &VideoProviderCreateResult{
		ProviderTaskID: "upstream_attach_failed",
		Status:         VideoTaskStatusQueued,
		ProviderStatus: "queued",
	}}
	selector := &fakeVideoTaskSelector{events: events, selection: &AccountSelectionResult{Account: &Account{ID: 99, Platform: PlatformOpenAI}}}
	usage := &fakeVideoTaskUsageRecorder{events: events}
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, usage)

	result, err := svc.Create(context.Background(), VideoTaskCreateParams{
		APIKey: videoTaskTestAPIKey(),
		User:   &User{ID: 7},
		Body:   []byte(`{"model":"video-ds-2.0-fast","prompt":"city"}`),
	})

	require.Nil(t, result)
	require.ErrorIs(t, err, ErrVideoTaskSettlementRetriable)
	require.ErrorIs(t, err, attachErr)
	require.ErrorIs(t, err, persistErr)
	require.Equal(t, 1, provider.createCalls)
	require.Equal(t, 1, repo.attachCalls)
	require.Zero(t, usage.calls)
	require.Equal(t, VideoTaskStatusSubmitting, repo.tasks[repo.lastCreate.PublicTaskID].Status)
}

func TestVideoTaskServiceCreateIdempotencyCreateConflictReturnsExistingSameHash(t *testing.T) {
	ctx := context.Background()
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	repo.createErr = errors.New("duplicate idempotency key")
	repo.idempotencyMisses = 1
	body := []byte(`{"model":"video-ds-2.0-fast","prompt":"make a short city clip"}`)
	req, err := ParseOpenAIVideoCreateRequest(body)
	require.NoError(t, err)
	repo.seedTask(&VideoTask{
		PublicTaskID: "task_race_existing",
		UserID:       7,
		APIKeyID:     42,
		RequestHash:  req.RequestHash,
		Status:       VideoTaskStatusQueued,
		ResponseBody: []byte(`{"id":"upstream_race","status":"queued"}`),
		Metadata:     map[string]any{"idempotency_key": "idem-race"},
	})
	provider := &fakeVideoTaskProvider{events: events}
	selector := &fakeVideoTaskSelector{events: events, selection: &AccountSelectionResult{Account: &Account{ID: 99, Platform: PlatformOpenAI}}}
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, nil)

	result, err := svc.Create(ctx, VideoTaskCreateParams{
		APIKey:         videoTaskTestAPIKey(),
		User:           &User{ID: 7},
		Body:           body,
		ContentType:    "application/json",
		IdempotencyKey: "idem-race",
	})

	require.NoError(t, err)
	require.Equal(t, "task_race_existing", result.Task.PublicTaskID)
	require.Zero(t, provider.createCalls)
	require.Equal(t, []string{"repo_get_idempotency", "selector_select", "repo_create", "repo_get_idempotency"}, events.snapshot())
}

func TestVideoTaskServiceCreateIdempotencyCreateConflictRejectsDifferentHash(t *testing.T) {
	ctx := context.Background()
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	repo.createErr = errors.New("duplicate idempotency key")
	repo.idempotencyMisses = 1
	repo.seedTask(&VideoTask{
		PublicTaskID: "task_race_conflict",
		UserID:       7,
		APIKeyID:     42,
		RequestHash:  strings.Repeat("b", 64),
		Status:       VideoTaskStatusQueued,
		Metadata:     map[string]any{"idempotency_key": "idem-race-conflict"},
	})
	provider := &fakeVideoTaskProvider{events: events}
	selector := &fakeVideoTaskSelector{events: events, selection: &AccountSelectionResult{Account: &Account{ID: 99, Platform: PlatformOpenAI}}}
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, nil)

	result, err := svc.Create(ctx, VideoTaskCreateParams{
		APIKey:         videoTaskTestAPIKey(),
		User:           &User{ID: 7},
		Body:           []byte(`{"model":"video-ds-2.0-fast","prompt":"make a short city clip"}`),
		ContentType:    "application/json",
		IdempotencyKey: "idem-race-conflict",
	})

	require.Nil(t, result)
	require.ErrorIs(t, err, ErrVideoTaskIdempotencyConflict)
	require.Zero(t, provider.createCalls)
}

func TestVideoTaskServiceFakeRepositoryListForUserMatchesRepositoryCursorsAndTieOrder(t *testing.T) {
	ctx := context.Background()
	repo := newFakeVideoTaskRepository(nil)
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	repo.seedTask(&VideoTask{ID: 1, PublicTaskID: "task_before", UserID: 7, Model: "seedance-2.0", Status: VideoTaskStatusCompleted, CreatedAt: base.Add(-time.Minute)})
	repo.seedTask(&VideoTask{ID: 2, PublicTaskID: "task_after_boundary", UserID: 7, Model: "seedance-2.0", Status: VideoTaskStatusCompleted, CreatedAt: base})
	repo.seedTask(&VideoTask{ID: 10, PublicTaskID: "task_tie_low", UserID: 7, Model: "seedance-2.0", Status: VideoTaskStatusCompleted, CreatedAt: base.Add(time.Minute)})
	repo.seedTask(&VideoTask{ID: 11, PublicTaskID: "task_tie_high", UserID: 7, Model: "seedance-2.0", Status: VideoTaskStatusCompleted, CreatedAt: base.Add(time.Minute)})
	repo.seedTask(&VideoTask{ID: 3, PublicTaskID: "task_before_boundary", UserID: 7, Model: "seedance-2.0", Status: VideoTaskStatusCompleted, CreatedAt: base.Add(2 * time.Minute)})
	repo.seedTask(&VideoTask{ID: 4, PublicTaskID: "task_after", UserID: 7, Model: "seedance-2.0", Status: VideoTaskStatusCompleted, CreatedAt: base.Add(3 * time.Minute)})

	items, hasMore, err := repo.ListForUser(ctx, VideoTaskListParams{UserID: 7, After: base, Before: base.Add(2 * time.Minute), Limit: 20})

	require.NoError(t, err)
	require.False(t, hasMore)
	require.Len(t, items, 2)
	require.Equal(t, "task_tie_high", items[0].PublicTaskID)
	require.Equal(t, "task_tie_low", items[1].PublicTaskID)
}

func TestVideoTaskServiceListReturnsLocalTasksOnlyForUser(t *testing.T) {
	ctx := context.Background()
	repo := newFakeVideoTaskRepository(nil)
	repo.seedTask(&VideoTask{PublicTaskID: "task_user", UserID: 7, Model: "seedance-2.0", Status: VideoTaskStatusCompleted, CreatedAt: time.Unix(20, 0), ResponseBody: []byte(`{"id":"upstream_user","status":"completed"}`)})
	repo.seedTask(&VideoTask{PublicTaskID: "task_other", UserID: 8, Model: "seedance-2.0", Status: VideoTaskStatusCompleted, CreatedAt: time.Unix(30, 0), ResponseBody: []byte(`{"id":"upstream_other","status":"completed"}`)})
	svc := newVideoTaskServiceForTest(repo, nil, nil, nil, nil)

	result, err := svc.List(ctx, VideoTaskListParams{UserID: 7, Limit: 20})

	require.NoError(t, err)
	require.JSONEq(t, `{"object":"list","data":[{"id":"task_user","status":"completed"}],"has_more":false}`, string(result.ResponseBody))
}

func TestVideoTaskServiceEstimateSelectsAccountButDoesNotCreateTask(t *testing.T) {
	ctx := context.Background()
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	selector := &fakeVideoTaskSelector{events: events, selection: &AccountSelectionResult{Account: &Account{ID: 99, Platform: PlatformOpenAI, Credentials: map[string]any{"model_mapping": map[string]any{"seedance-2.0": "video-ds-2.0"}}}}}
	provider := &accountVideoTaskProvider{registry: videoAdapterRegistry{VideoAdapterJimengOpenAIVideos: &jimengOpenAIVideosAdapter{provider: &fakeVideoTaskProvider{events: events}}}}
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, nil)

	result, err := svc.Estimate(ctx, VideoTaskEstimateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: []byte(`{"model":"seedance-2.0","prompt":"city","duration":5}`), Endpoint: VideoTaskEndpointVideoGenerations, ContentType: "application/json"})

	require.NoError(t, err)
	require.JSONEq(t, `{"object":"video.estimate","model":"seedance-2.0","upstream_model":"video-ds-2.0","adapter":"jimeng_openai_videos","endpoint":"video_generations","metadata":{"seconds":"15"}}`, string(result.ResponseBody))
	require.False(t, repo.createdBeforeProvider)
}

func TestVideoTaskServiceEstimateUsesSelectedChannelVideoPricing(t *testing.T) {
	defaultSeconds := 10
	defaultPrice := 0.08
	tierPrice := 0.12
	tests := []struct {
		name           string
		body           string
		allowed        []int
		wantSeconds    int
		wantResolution string
		wantUnit       float64
		wantGross      float64
	}{
		{name: "default seconds and price", body: `{"model":"seedance","prompt":"city"}`, wantSeconds: 10, wantResolution: "720p", wantUnit: 0.08, wantGross: 0.8},
		{name: "resolution tier", body: `{"model":"seedance","prompt":"city","duration":5,"resolution":"1080p"}`, wantSeconds: 5, wantResolution: "1080p", wantUnit: 0.12, wantGross: 0.6},
		{name: "allowed explicit seconds", body: `{"model":"seedance","prompt":"city","seconds":"5"}`, allowed: []int{5, 10}, wantSeconds: 5, wantResolution: "720p", wantUnit: 0.08, wantGross: 0.4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selector := &fakeVideoTaskSelector{selection: &AccountSelectionResult{Account: &Account{ID: 99, Platform: PlatformOpenAI}}}
			provider := &jimengOpenAIVideosAdapter{provider: &fakeVideoTaskProvider{}}
			pricing := &fakeVideoTaskPricingResolver{selection: VideoTaskPricingSelection{
				Pricing: &ChannelModelPricing{BillingMode: BillingModeVideo, VideoPricePerSecond: &defaultPrice, VideoDefaultSeconds: &defaultSeconds, VideoAllowedSeconds: tt.allowed,
					Intervals: []PricingInterval{{TierLabel: "1080p", VideoPricePerSecond: &tierPrice}}},
				BillingModel: "seedance", RateMultiplier: 0.5, AccountRateMultiplier: 1,
			}}
			svc := newVideoTaskServiceForTest(newFakeVideoTaskRepository(nil), nil, selector, provider, nil)
			svc.pricingResolver = pricing
			original := []byte(tt.body)

			result, err := svc.Estimate(context.Background(), VideoTaskEstimateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: original, Endpoint: VideoTaskEndpointVideoGenerations})

			require.NoError(t, err)
			require.Equal(t, []byte(tt.body), original, "estimate must not mutate the caller body")
			require.Equal(t, 1, pricing.calls)
			var response map[string]any
			require.NoError(t, json.Unmarshal(result.ResponseBody, &response))
			metadata := response["metadata"].(map[string]any)
			require.Equal(t, fmt.Sprintf("%d", tt.wantSeconds), metadata["seconds"])
			require.Equal(t, string(BillingModeVideo), response["billing_mode"])
			require.Equal(t, "seedance", response["billing_model"])
			effective := response["effective"].(map[string]any)
			require.Equal(t, float64(tt.wantSeconds), effective["seconds"])
			require.Equal(t, tt.wantResolution, effective["resolution"])
			require.InDelta(t, tt.wantUnit, response["unit_price_usd"], 1e-12)
			require.InDelta(t, tt.wantGross, response["gross_cost_usd"], 1e-12)
			require.InDelta(t, 0.5, response["rate_multiplier"], 1e-12)
			require.InDelta(t, tt.wantGross*0.5, response["actual_cost_usd"], 1e-12)
		})
	}
}

func TestVideoTaskServiceEstimatePerRequestPricingPreservesDurationFieldsAndPresentation(t *testing.T) {
	price := 65.0
	selector := &fakeVideoTaskSelector{selection: &AccountSelectionResult{Account: &Account{ID: 99, Platform: PlatformOpenAI}}}
	provider := &fakeVideoTaskEstimator{fakeVideoTaskProvider: &fakeVideoTaskProvider{}}
	pricing := &fakeVideoTaskPricingResolver{selection: VideoTaskPricingSelection{
		Pricing:               &ChannelModelPricing{BillingMode: BillingModePerRequest, PerRequestPrice: &price},
		BillingModel:          "seedance",
		RateMultiplier:        0.1065,
		AccountRateMultiplier: 1,
	}}
	svc := newVideoTaskServiceForTest(newFakeVideoTaskRepository(nil), nil, selector, provider, nil)
	svc.pricingResolver = pricing
	body := []byte(`{"model":"seedance","prompt":"city","seconds":"5","duration":10,"duration_seconds":"15"}`)

	result, err := svc.Estimate(context.Background(), VideoTaskEstimateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: body})

	require.NoError(t, err)
	require.Equal(t, body, provider.estimateBody)
	var response map[string]any
	require.NoError(t, json.Unmarshal(result.ResponseBody, &response))
	require.Equal(t, string(BillingModePerRequest), response["billing_mode"])
	require.InDelta(t, 65, response["per_request_price_usd"], 1e-12)
	require.NotContains(t, response, "unit_price_usd")
}

func TestVideoTaskServiceEstimateVideoPricingRejectsInvalidOrDisallowedDuration(t *testing.T) {
	defaultSeconds := 10
	price := 0.08
	for _, body := range []string{
		`{"model":"seedance","prompt":"city","duration":"bad"}`,
		`{"model":"seedance","prompt":"city","seconds":6}`,
	} {
		selector := &fakeVideoTaskSelector{selection: &AccountSelectionResult{Account: &Account{ID: 99, Platform: PlatformOpenAI}}}
		svc := newVideoTaskServiceForTest(newFakeVideoTaskRepository(nil), nil, selector, &jimengOpenAIVideosAdapter{provider: &fakeVideoTaskProvider{}}, nil)
		svc.pricingResolver = &fakeVideoTaskPricingResolver{selection: VideoTaskPricingSelection{Pricing: &ChannelModelPricing{BillingMode: BillingModeVideo, VideoPricePerSecond: &price, VideoDefaultSeconds: &defaultSeconds, VideoAllowedSeconds: []int{5, 10}}, BillingModel: "seedance", RateMultiplier: 1, AccountRateMultiplier: 1}}

		result, err := svc.Estimate(context.Background(), VideoTaskEstimateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: []byte(body), Endpoint: VideoTaskEndpointVideoGenerations})

		require.Nil(t, result)
		require.Error(t, err)
		require.True(t, infraerrors.IsBadRequest(err))
	}
}

func TestVideoTaskServiceEstimateWithoutVideoPricingPreservesLegacyContract(t *testing.T) {
	selector := &fakeVideoTaskSelector{selection: &AccountSelectionResult{Account: &Account{ID: 99, Platform: PlatformOpenAI, Credentials: map[string]any{"model_mapping": map[string]any{"seedance": "upstream"}}}}}
	svc := newVideoTaskServiceForTest(newFakeVideoTaskRepository(nil), nil, selector, &jimengOpenAIVideosAdapter{provider: &fakeVideoTaskProvider{}}, nil)
	svc.pricingResolver = &fakeVideoTaskPricingResolver{}
	body := []byte(`{"model":"seedance","prompt":"city","duration":5}`)

	result, err := svc.Estimate(context.Background(), VideoTaskEstimateParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: body, Endpoint: VideoTaskEndpointVideoGenerations})

	require.NoError(t, err)
	require.JSONEq(t, `{"object":"video.estimate","model":"seedance","upstream_model":"upstream","adapter":"jimeng_openai_videos","endpoint":"video_generations","metadata":{"seconds":"15"}}`, string(result.ResponseBody))
	require.Equal(t, []byte(`{"model":"seedance","prompt":"city","duration":5}`), body)
}

func TestVideoTaskServiceEstimateRequiresVideoGenerationPermissionBeforeSelectingAccount(t *testing.T) {
	ctx := context.Background()
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	selector := &fakeVideoTaskSelector{events: events, selection: &AccountSelectionResult{Account: &Account{ID: 99, Platform: PlatformOpenAI}}}
	provider := &accountVideoTaskProvider{registry: videoAdapterRegistry{VideoAdapterJimengOpenAIVideos: &jimengOpenAIVideosAdapter{provider: &fakeVideoTaskProvider{events: events}}}}
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, nil)
	groupID := int64(5)

	result, err := svc.Estimate(ctx, VideoTaskEstimateParams{
		APIKey: &APIKey{ID: 42, UserID: 7, GroupID: &groupID, Group: &Group{
			ID:                   groupID,
			Platform:             PlatformOpenAI,
			AllowVideoGeneration: false,
		}},
		User:        &User{ID: 7},
		Body:        []byte(`{"model":"seedance-2.0","prompt":"city","duration":5}`),
		Endpoint:    VideoTaskEndpointVideoGenerations,
		ContentType: "application/json",
	})

	require.Nil(t, result)
	require.ErrorIs(t, err, ErrVideoGenerationPermissionDenied)
	require.Zero(t, selector.selectCalls)
	require.False(t, repo.createdBeforeProvider)
}

func TestVideoTaskServiceReferencesAllowsModelOnlyBodyBeforeUnsupportedCapability(t *testing.T) {
	ctx := context.Background()
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	selector := &fakeVideoTaskSelector{events: events, selection: &AccountSelectionResult{Account: &Account{ID: 99, Platform: PlatformOpenAI, Credentials: map[string]any{"model_mapping": map[string]any{"seedance-2.0": "video-ds-2.0"}}}}}
	provider := &accountVideoTaskProvider{registry: videoAdapterRegistry{VideoAdapterJimengOpenAIVideos: &jimengOpenAIVideosAdapter{provider: &fakeVideoTaskProvider{events: events}}}}
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, nil)

	result, err := svc.References(ctx, VideoTaskAssetParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: []byte(`{"model":"seedance-2.0","image":"x"}`), Endpoint: VideoTaskEndpointVideos, ContentType: "application/json"})

	require.Nil(t, result)
	require.ErrorIs(t, err, ErrVideoTaskActionUnsupported)
	require.NotContains(t, err.Error(), "prompt is required")
}

func TestVideoTaskServiceMaterialAssetsAllowsModelOnlyBodyBeforeUnsupportedCapability(t *testing.T) {
	ctx := context.Background()
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	selector := &fakeVideoTaskSelector{events: events, selection: &AccountSelectionResult{Account: &Account{ID: 99, Platform: PlatformOpenAI, Credentials: map[string]any{"model_mapping": map[string]any{"seedance-2.0": "video-ds-2.0"}}}}}
	provider := &accountVideoTaskProvider{registry: videoAdapterRegistry{VideoAdapterJimengOpenAIVideos: &jimengOpenAIVideosAdapter{provider: &fakeVideoTaskProvider{events: events}}}}
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, nil)

	result, err := svc.MaterialAssets(ctx, VideoTaskAssetParams{APIKey: videoTaskTestAPIKey(), User: &User{ID: 7}, Body: []byte(`{"model":"seedance-2.0","image":"x"}`), Endpoint: VideoTaskEndpointVideos, ContentType: "application/json"})

	require.Nil(t, result)
	require.ErrorIs(t, err, ErrVideoTaskActionUnsupported)
	require.NotContains(t, err.Error(), "prompt is required")
}

func newVideoTaskServiceForTest(repo VideoTaskRepository, accountLookup videoTaskAccountLookup, selector videoTaskAccountSelector, provider VideoTaskProvider, usage videoTaskSubmissionUsageRecorder) *VideoTaskService {
	return &VideoTaskService{
		repo:          repo,
		accountLookup: accountLookup,
		selector:      selector,
		provider:      provider,
		usageRecorder: usage,
	}
}

func TestVideoTaskServiceFetchReconcilesOnlyPersistedProviderWinner(t *testing.T) {
	for _, tt := range []struct {
		name          string
		winningStatus VideoTaskStatus
		wantCalls     int
	}{
		{name: "failed CAS winner", winningStatus: VideoTaskStatusFailed, wantCalls: 1},
		{name: "stale failed loser after completed", winningStatus: VideoTaskStatusCompleted, wantCalls: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeVideoTaskRepository(nil)
			repo.seedTask(&VideoTask{PublicTaskID: "task_1", UserID: 7, AccountID: 9, Status: VideoTaskStatusQueued})
			if tt.winningStatus == VideoTaskStatusCompleted {
				repo.beforeProviderUpdate = func(task *VideoTask) { task.Status = VideoTaskStatusCompleted }
			}
			settlement := &fakeVideoTaskSettlementOrchestrator{}
			svc := newVideoTaskServiceForTest(repo, &fakeVideoTaskAccountStore{accounts: map[int64]*Account{9: {ID: 9}}}, nil, &fakeVideoTaskProvider{fetchResult: &VideoProviderFetchResult{Status: VideoTaskStatusFailed, ErrorMessage: "upstream failed"}}, nil)
			svc.settlement = settlement

			result, err := svc.Fetch(context.Background(), VideoTaskFetchParams{UserID: 7, PublicTaskID: "task_1"})
			if err != nil {
				t.Fatalf("Fetch returned error: %v", err)
			}
			if result.Task.Status != tt.winningStatus {
				t.Fatalf("persisted status = %s, want %s", result.Task.Status, tt.winningStatus)
			}
			if settlement.reconcilePersistedCalls != tt.wantCalls {
				t.Fatalf("reconcile persisted calls = %d, want %d", settlement.reconcilePersistedCalls, tt.wantCalls)
			}
		})
	}
}

func videoTaskTestAPIKey() *APIKey {
	groupID := int64(5)
	return &APIKey{
		ID:      42,
		UserID:  7,
		GroupID: &groupID,
		Group: &Group{
			ID:                   groupID,
			Platform:             PlatformOpenAI,
			AllowVideoGeneration: true,
		},
	}
}

func videoTaskCompositeTestAPIKey(allowVideo bool) *APIKey {
	groupID := int64(5)
	return &APIKey{
		ID:      42,
		UserID:  7,
		GroupID: &groupID,
		Group: &Group{
			ID:                   groupID,
			Platform:             PlatformComposite,
			AllowVideoGeneration: allowVideo,
		},
	}
}

type videoTaskServiceTestEvents struct {
	items []string
}

func (e *videoTaskServiceTestEvents) add(item string) {
	if e == nil {
		return
	}
	e.items = append(e.items, item)
}

func (e *videoTaskServiceTestEvents) snapshot() []string {
	if e == nil {
		return nil
	}
	return append([]string(nil), e.items...)
}

type fakeVideoTaskRepository struct {
	events                       *videoTaskServiceTestEvents
	tasks                        map[string]*VideoTask
	lastCreate                   VideoTaskCreateInput
	createdBeforeProvider        bool
	idempotencyErr               error
	idempotencyMisses            int
	createErr                    error
	notFoundErr                  error
	attachCtxErr                 error
	attachErr                    error
	attachCalls                  int
	updateProviderCtxErr         error
	updateProviderDeadline       time.Time
	updateProviderHasDeadline    bool
	reloadCtxErr                 error
	reloadDeadline               time.Time
	reloadHasDeadline            bool
	rejectCanceledProviderUpdate bool
	updateProviderErr            error
	fallbackErr                  error
	beforeProviderUpdate         func(*VideoTask)
	afterProviderUpdate          func(*VideoTask)
}

func newFakeVideoTaskRepository(events *videoTaskServiceTestEvents) *fakeVideoTaskRepository {
	return &fakeVideoTaskRepository{events: events, tasks: map[string]*VideoTask{}}
}

func (r *fakeVideoTaskRepository) seedTask(task *VideoTask) *VideoTask {
	copy := cloneVideoTask(task)
	r.tasks[copy.PublicTaskID] = copy
	return cloneVideoTask(copy)
}

func (r *fakeVideoTaskRepository) Create(ctx context.Context, input VideoTaskCreateInput) (*VideoTask, error) {
	r.events.add("repo_create")
	r.lastCreate = input
	if r.createErr != nil {
		return nil, r.createErr
	}
	task := &VideoTask{
		ID:             int64(len(r.tasks) + 1),
		PublicTaskID:   input.PublicTaskID,
		Provider:       input.Provider,
		Platform:       input.Platform,
		UserID:         input.UserID,
		APIKeyID:       input.APIKeyID,
		GroupID:        input.GroupID,
		AccountID:      input.AccountID,
		ChannelID:      input.ChannelID,
		SubscriptionID: input.SubscriptionID,
		Model:          input.Model,
		Prompt:         input.Prompt,
		Status:         VideoTaskStatusSubmitting,
		RequestHash:    input.RequestHash,
		PromptHash:     input.PromptHash,
		RequestBody:    append([]byte(nil), input.RequestBody...),
		Metadata:       cloneMap(input.Metadata),
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	r.tasks[task.PublicTaskID] = task
	r.createdBeforeProvider = true
	return cloneVideoTask(task), nil
}

func (r *fakeVideoTaskRepository) UpdateSettlementSummary(_ context.Context, publicTaskID string, summary VideoTaskSettlementSummary) error {
	task := r.tasks[publicTaskID]
	if task == nil {
		return errors.New("task not found")
	}
	task.SubscriptionID = summary.SubscriptionID
	task.UsageLogID = summary.UsageLogID
	if summary.ClearUsageLogID {
		task.UsageLogID = nil
	}
	task.UsageMetadata = cloneMap(summary.UsageMetadata)
	if summary.BilledUSD != nil {
		task.BilledUSD = *summary.BilledUSD
	}
	return nil
}

func (r *fakeVideoTaskRepository) PersistUpstreamFallback(_ context.Context, publicTaskID string, fallback VideoTaskUpstreamFallback) error {
	r.events.add("repo_persist_upstream_fallback")
	if r.fallbackErr != nil {
		return r.fallbackErr
	}
	task := r.tasks[publicTaskID]
	if task == nil {
		return errors.New("task not found")
	}
	task.Metadata = mergeMaps(task.Metadata, map[string]any{
		"reconciliation_error_code":        "ATTACH_UPSTREAM_FAILED",
		"reconciliation_error_message":     videoTaskAttachReconciliationErrorMessage,
		"reconciliation_upstream_task_id":  fallback.Snapshot.ProviderTaskID,
		"reconciliation_provider_status":   fallback.Snapshot.ProviderStatus,
		"reconciliation_accepted_snapshot": fallback.Snapshot,
	})
	return nil
}

func (r *fakeVideoTaskRepository) AttachUpstream(ctx context.Context, publicTaskID string, update VideoTaskSubmitUpdate) error {
	r.events.add("repo_attach")
	r.attachCalls++
	r.attachCtxErr = ctx.Err()
	if r.attachErr != nil {
		return r.attachErr
	}
	task := r.tasks[publicTaskID]
	if task == nil {
		return errors.New("task not found")
	}
	task.ProviderTaskID = update.ProviderTaskID
	task.Status = update.Status
	task.ProviderStatus = update.ProviderStatus
	task.ResponseBody = append([]byte(nil), update.ResponseBody...)
	task.Metadata = mergeMaps(task.Metadata, update.Metadata)
	task.ErrorMessage = update.ErrorMessage
	task.SubmittedAt = update.SubmittedAt
	task.ExpiresAt = update.ExpiresAt
	task.NextPollAt = update.NextPollAt
	if update.ClearNextPollAt {
		task.NextPollAt = nil
	}
	return nil
}

func (r *fakeVideoTaskRepository) GetByPublicTaskID(ctx context.Context, publicTaskID string) (*VideoTask, error) {
	r.events.add("repo_get_public")
	r.reloadCtxErr = ctx.Err()
	r.reloadDeadline, r.reloadHasDeadline = ctx.Deadline()
	return r.task(publicTaskID)
}

func (r *fakeVideoTaskRepository) GetByPublicTaskIDForUser(ctx context.Context, publicTaskID string, userID int64) (*VideoTask, error) {
	r.events.add("repo_get_user_public")
	task, err := r.task(publicTaskID)
	if err != nil {
		return nil, err
	}
	if task.UserID != userID {
		return nil, infraerrors.NotFound("VIDEO_TASK_NOT_FOUND", "video task not found")
	}
	return task, nil
}

func (r *fakeVideoTaskRepository) GetByProviderTaskID(ctx context.Context, provider, providerTaskID string) (*VideoTask, error) {
	for _, task := range r.tasks {
		if task.Provider == provider && task.ProviderTaskID == providerTaskID {
			return cloneVideoTask(task), nil
		}
	}
	return nil, errors.New("task not found")
}

func (r *fakeVideoTaskRepository) GetByIdempotencyKey(ctx context.Context, apiKeyID int64, idempotencyKey string) (*VideoTask, error) {
	r.events.add("repo_get_idempotency")
	if r.idempotencyErr != nil {
		return nil, r.idempotencyErr
	}
	if r.idempotencyMisses > 0 {
		r.idempotencyMisses--
		return nil, nil
	}
	for _, task := range r.tasks {
		if task.APIKeyID == apiKeyID && task.Metadata["idempotency_key"] == idempotencyKey {
			return cloneVideoTask(task), nil
		}
	}
	return nil, nil
}

func (r *fakeVideoTaskRepository) ListForUser(ctx context.Context, params VideoTaskListParams) ([]*VideoTask, bool, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	items := make([]*VideoTask, 0, len(r.tasks))
	for _, task := range r.tasks {
		if task.UserID != params.UserID {
			continue
		}
		if params.Status != "" && string(task.Status) != params.Status {
			continue
		}
		if params.Model != "" && task.Model != params.Model {
			continue
		}
		if !params.After.IsZero() && !task.CreatedAt.After(params.After) {
			continue
		}
		if !params.Before.IsZero() && !task.CreatedAt.Before(params.Before) {
			continue
		}
		items = append(items, cloneVideoTask(task))
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore, nil
}

func (r *fakeVideoTaskRepository) UpdateSubmit(ctx context.Context, publicTaskID string, update VideoTaskSubmitUpdate) error {
	return r.AttachUpstream(ctx, publicTaskID, update)
}

func (r *fakeVideoTaskRepository) UpdateFromProvider(ctx context.Context, publicTaskID string, update VideoTaskProviderUpdate) (bool, error) {
	r.events.add("repo_update_provider")
	r.updateProviderCtxErr = ctx.Err()
	r.updateProviderDeadline, r.updateProviderHasDeadline = ctx.Deadline()
	if r.rejectCanceledProviderUpdate && ctx.Err() != nil {
		return false, ctx.Err()
	}
	if r.updateProviderErr != nil {
		return false, r.updateProviderErr
	}
	task := r.tasks[publicTaskID]
	if task == nil {
		return false, errors.New("task not found")
	}
	if r.beforeProviderUpdate != nil {
		r.beforeProviderUpdate(task)
		r.beforeProviderUpdate = nil
	}
	if task.Status.Terminal() {
		return false, nil
	}
	task.Status = update.Status
	task.ProviderStatus = update.ProviderStatus
	task.ResponseBody = append([]byte(nil), update.ResponseBody...)
	task.Metadata = mergeMaps(task.Metadata, update.Metadata)
	task.ErrorMessage = update.ErrorMessage
	task.CompletedAt = update.CompletedAt
	task.ExpiresAt = update.ExpiresAt
	if update.ClearNextPollAt || update.Status.Terminal() {
		task.NextPollAt = nil
		if update.Status.Terminal() {
			task.LockedBy = nil
			task.LockedUntil = nil
		}
	} else if update.NextPollAt != nil {
		nextPollAt := *update.NextPollAt
		task.NextPollAt = &nextPollAt
	}
	if r.afterProviderUpdate != nil {
		r.afterProviderUpdate(task)
		r.afterProviderUpdate = nil
	}
	return true, nil
}

func (r *fakeVideoTaskRepository) UpdateFromProviderWithPollLease(ctx context.Context, publicTaskID, leaseToken string, validAt time.Time, update VideoTaskProviderUpdate) (bool, error) {
	return r.UpdateFromProvider(ctx, publicTaskID, update)
}

func (r *fakeVideoTaskRepository) UpdateProvider(ctx context.Context, publicTaskID string, update VideoTaskProviderUpdate) error {
	_, err := r.UpdateFromProvider(ctx, publicTaskID, update)
	return err
}

func (r *fakeVideoTaskRepository) ClaimDuePollTasks(ctx context.Context, now time.Time, limit int, lockOwner string, lockTTL time.Duration) ([]*VideoTask, error) {
	return nil, nil
}

func (r *fakeVideoTaskRepository) RenewPollLock(ctx context.Context, publicTaskID, leaseToken string, validAt time.Time, lockTTL time.Duration) (bool, error) {
	return false, nil
}

func (r *fakeVideoTaskRepository) ReleasePollLock(ctx context.Context, publicTaskID, leaseToken string) (bool, error) {
	return true, nil
}

func (r *fakeVideoTaskRepository) task(publicTaskID string) (*VideoTask, error) {
	task := r.tasks[publicTaskID]
	if task == nil {
		if r.notFoundErr != nil {
			return nil, r.notFoundErr
		}
		return nil, errors.New("task not found")
	}
	return cloneVideoTask(task), nil
}

type fakeVideoTaskSelector struct {
	events      *videoTaskServiceTestEvents
	selection   *AccountSelectionResult
	err         error
	selectCalls int
	lastModel   string
}

func (s *fakeVideoTaskSelector) SelectVideoTaskAccount(ctx context.Context, groupID *int64, sessionHash string, model string) (*AccountSelectionResult, error) {
	s.selectCalls++
	s.lastModel = model
	s.events.add("selector_select")
	return s.selection, s.err
}

type fakeVideoTaskAccountStore struct {
	events       *videoTaskServiceTestEvents
	accounts     map[int64]*Account
	getByIDCalls []int64
	err          error
}

func (s *fakeVideoTaskAccountStore) GetByID(ctx context.Context, id int64) (*Account, error) {
	s.events.add("account_get")
	s.getByIDCalls = append(s.getByIDCalls, id)
	if s.err != nil {
		return nil, s.err
	}
	account := s.accounts[id]
	if account == nil {
		return nil, errors.New("account not found")
	}
	copy := *account
	return &copy, nil
}

type fakeVideoTaskProvider struct {
	events              *videoTaskServiceTestEvents
	createResult        *VideoProviderCreateResult
	createErr           error
	fetchResult         *VideoProviderFetchResult
	cancelResult        *VideoProviderFetchResult
	cancelAfter         context.CancelFunc
	cancelAfterFetch    context.CancelFunc
	contentResult       *VideoContentStream
	createCalls         int
	fetchCalls          int
	cancelCalls         int
	contentCalls        int
	createUpstreamModel string
	createContentType   string
	createBody          []byte
	fetchAccountIDs     []int64
	fetchTaskMetadata   map[string]any
	fetchCtx            context.Context
	refreshCtx          context.Context
	contentAccountIDs   []int64
	contentTaskMetadata map[string]any
}

type fakeVideoTaskEstimator struct {
	*fakeVideoTaskProvider
	estimateBody []byte
}

func (p *fakeVideoTaskEstimator) Estimate(_ context.Context, _ *Account, body []byte, _ string, _ string) (*VideoTaskEstimateResult, error) {
	p.estimateBody = append([]byte(nil), body...)
	return &VideoTaskEstimateResult{ResponseBody: []byte(`{"object":"video.estimate"}`)}, nil
}

type videoTaskProviderMetadataError struct {
	err      error
	metadata map[string]any
}

func (e videoTaskProviderMetadataError) Error() string {
	if e.err == nil {
		return "video task provider error"
	}
	return e.err.Error()
}

func (e videoTaskProviderMetadataError) Unwrap() error {
	return e.err
}

func (e videoTaskProviderMetadataError) VideoTaskMetadata() map[string]any {
	return e.metadata
}

func (p *fakeVideoTaskProvider) Create(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) (*VideoProviderCreateResult, error) {
	p.createCalls++
	p.createUpstreamModel = upstreamModel
	p.createContentType = contentType
	p.createBody = append([]byte(nil), body...)
	p.events.add("provider_create")
	if p.createErr != nil {
		return nil, p.createErr
	}
	return p.createResult, nil
}

func (p *fakeVideoTaskProvider) Fetch(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error) {
	p.fetchCalls++
	p.fetchCtx = ctx
	if account != nil {
		p.fetchAccountIDs = append(p.fetchAccountIDs, account.ID)
	}
	if task != nil {
		p.fetchTaskMetadata = cloneMap(task.Metadata)
	}
	p.events.add("provider_fetch")
	if p.cancelAfterFetch != nil {
		p.cancelAfterFetch()
	}
	return p.fetchResult, nil
}

func (p *fakeVideoTaskProvider) Refresh(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error) {
	p.refreshCtx = ctx
	p.events.add("provider_refresh")
	return p.Fetch(ctx, account, task)
}

func (p *fakeVideoTaskProvider) Cancel(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error) {
	p.cancelCalls++
	p.events.add("provider_cancel")
	if p.cancelAfter != nil {
		p.cancelAfter()
	}
	if p.cancelResult != nil {
		return p.cancelResult, nil
	}
	return p.fetchResult, nil
}

func (p *fakeVideoTaskProvider) Content(ctx context.Context, account *Account, task *VideoTask, headers http.Header) (*VideoContentStream, error) {
	p.contentCalls++
	if account != nil {
		p.contentAccountIDs = append(p.contentAccountIDs, account.ID)
	}
	if task != nil {
		p.contentTaskMetadata = cloneMap(task.Metadata)
	}
	p.events.add("provider_content")
	if p.contentResult != nil {
		return p.contentResult, nil
	}
	return &VideoContentStream{Body: io.NopCloser(strings.NewReader("video")), StatusCode: http.StatusOK}, nil
}

type fakeVideoTaskAdapter struct {
	name string
	*fakeVideoTaskProvider
}

func (a *fakeVideoTaskAdapter) Name() string { return a.name }

type ownershipVideoTaskAdapter struct {
	name string
	*fakeVideoTaskProvider
	cancelCalls int
}

func (a *ownershipVideoTaskAdapter) Name() string { return a.name }

func (a *ownershipVideoTaskAdapter) Cancel(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error) {
	a.cancelCalls++
	a.events.add("provider_cancel")
	return &VideoProviderFetchResult{
		ProviderTaskID: task.ProviderTaskID,
		Status:         VideoTaskStatusCancelled,
		ProviderStatus: "cancelled",
		RawBody:        []byte(`{"id":"upstream_owned","status":"cancelled"}`),
	}, nil
}

type fakeVideoTaskUsageRecorder struct {
	events            *videoTaskServiceTestEvents
	calledAfterAttach bool
	calls             int
	ctxErr            error
	deadline          time.Time
	hasDeadline       bool
}

type fakeVideoTaskPricingResolver struct {
	events    *videoTaskServiceTestEvents
	selection VideoTaskPricingSelection
	calls     int
	lastInput VideoTaskPricingResolveInput
}

type fakeVideoTaskSettlementOrchestrator struct {
	events                  *videoTaskServiceTestEvents
	reserveInput            VideoTaskSettlementCreateInput
	reserveUsage            *UsageLog
	reserveErr              error
	reserveNotApplied       bool
	captureErr              error
	state                   VideoTaskSettlementState
	captureCalls            int
	failAndReleaseCalls     int
	reconcileResult         *VideoTaskSettlementSnapshot
	reconcileErr            error
	reconcileCalls          int
	reconcilePersistedCalls int
}

func (s *fakeVideoTaskSettlementOrchestrator) ReconcilePersistedTask(context.Context, string) error {
	s.reconcilePersistedCalls++
	return nil
}

func (s *fakeVideoTaskSettlementOrchestrator) Reserve(_ context.Context, input VideoTaskSettlementCreateInput) (*VideoTaskSubmissionClaim, error) {
	cmd, err := s.Prepare(context.Background(), input)
	if err != nil {
		return nil, err
	}
	return s.ReservePrepared(context.Background(), cmd)
}

func (s *fakeVideoTaskSettlementOrchestrator) Prepare(_ context.Context, input VideoTaskSettlementCreateInput) (*VideoTaskSettlementReserveCommand, error) {
	s.reserveInput = input
	s.reserveUsage = buildVideoTaskSettlementUsage(input, BillingTypeBalance, nil)
	return &VideoTaskSettlementReserveCommand{PublicTaskID: input.PublicTaskID, GrossCostUSD: input.Quote.GrossCostUSD, ActualCostUSD: input.Quote.ActualCostUSD}, nil
}

func (s *fakeVideoTaskSettlementOrchestrator) ReservePrepared(_ context.Context, cmd *VideoTaskSettlementReserveCommand) (*VideoTaskSubmissionClaim, error) {
	s.events.add("settlement_reserve")
	if s.reserveErr != nil {
		return nil, s.reserveErr
	}
	return &VideoTaskSubmissionClaim{Settlement: &VideoTaskSettlementSnapshot{PublicTaskID: cmd.PublicTaskID, State: VideoTaskSettlementReserved}, Claimed: !s.reserveNotApplied}, nil
}

func (s *fakeVideoTaskSettlementOrchestrator) Capture(_ context.Context, publicTaskID string) (*VideoTaskSettlementSnapshot, error) {
	s.events.add("settlement_capture")
	s.captureCalls++
	if s.captureErr != nil {
		return nil, s.captureErr
	}
	state := s.state
	if state == "" {
		state = VideoTaskSettlementCharged
	}
	return &VideoTaskSettlementSnapshot{PublicTaskID: publicTaskID, State: state}, nil
}

func (s *fakeVideoTaskSettlementOrchestrator) FailAndRelease(_ context.Context, publicTaskID string, _ error) error {
	s.events.add("settlement_fail_release")
	s.failAndReleaseCalls++
	return nil
}

func (s *fakeVideoTaskSettlementOrchestrator) Reconcile(ctx context.Context, task *VideoTask) (*VideoTaskSettlementSnapshot, error) {
	s.reconcileCalls++
	if s.reconcileErr != nil || s.reconcileResult != nil {
		return s.reconcileResult, s.reconcileErr
	}
	return s.Capture(ctx, task.PublicTaskID)
}

func (r *fakeVideoTaskPricingResolver) ResolveVideoTaskPricing(_ context.Context, input VideoTaskPricingResolveInput) VideoTaskPricingSelection {
	r.calls++
	r.lastInput = input
	r.events.add("pricing_resolve")
	return r.selection
}

func (r *fakeVideoTaskUsageRecorder) RecordVideoTaskSubmission(ctx context.Context, params VideoTaskCreateParams, account *Account, task *VideoTask, result *VideoProviderCreateResult, upstreamModel string) {
	r.calls++
	r.ctxErr = ctx.Err()
	r.deadline, r.hasDeadline = ctx.Deadline()
	r.events.add("usage_record")
	if task != nil && task.ProviderTaskID != "" {
		r.calledAfterAttach = true
	}
}

func cloneVideoTask(task *VideoTask) *VideoTask {
	if task == nil {
		return nil
	}
	copy := *task
	copy.RequestBody = append([]byte(nil), task.RequestBody...)
	copy.ResponseBody = append([]byte(nil), task.ResponseBody...)
	copy.Metadata = cloneMap(task.Metadata)
	return &copy
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func mergeMaps(base map[string]any, update map[string]any) map[string]any {
	out := cloneMap(base)
	if out == nil {
		out = map[string]any{}
	}
	for k, v := range update {
		out[k] = v
	}
	return out
}
