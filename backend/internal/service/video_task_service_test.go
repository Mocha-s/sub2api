package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

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
					"model_mapping": map[string]any{"sora-test": "sora-upstream"},
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
		Body:           []byte(`{"model":"sora-test","prompt":"make a short city clip","seconds":4,"aspect_ratio":"16:9","images":[{}]}`),
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
	require.Equal(t, "sora-upstream", provider.createUpstreamModel)
	require.Equal(t, "application/json", provider.createContentType)
	require.Equal(t, "sora-test", repo.lastCreate.Metadata["requested_model"])
	require.Equal(t, "sora-upstream", repo.lastCreate.Metadata["upstream_model"])
	require.Equal(t, "sora-test", repo.lastCreate.Metadata["billing_model"])
	require.Equal(t, "https://upstream.example", repo.lastCreate.Metadata["upstream_base_url"])
	require.Equal(t, "idem-create", repo.lastCreate.Metadata["idempotency_key"])
	require.Equal(t, 1, repo.lastCreate.Metadata["image_count"])

	var response map[string]any
	require.NoError(t, json.Unmarshal(result.ResponseBody, &response))
	require.Equal(t, result.Task.PublicTaskID, response["id"])
	require.NotEqual(t, "upstream_task_123", response["id"])
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
		Body: []byte(`{"model":"sora-test","prompt":"make a short city clip"}`),
	})

	require.Nil(t, result)
	require.ErrorIs(t, err, ErrVideoGenerationPermissionDenied)
	require.ErrorContains(t, err, VideoGenerationPermissionMessage)
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
		Body:   []byte(`{"model":"sora-test","prompt":"make a short city clip"}`),
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
	body := []byte(`{"model":"sora-test","prompt":"make a short city clip"}`)
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
		Body:           []byte(`{"model":"sora-test","prompt":"make a short city clip"}`),
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
	body := []byte(`{"model":"sora-test","prompt":"make a short city clip"}`)
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
		Body:           []byte(`{"model":"sora-test","prompt":"different prompt"}`),
		ContentType:    "application/json",
		IdempotencyKey: "idem-conflict",
	})

	require.Nil(t, result)
	require.ErrorContains(t, err, "idempotency key reused with different request body")
	require.ErrorIs(t, err, ErrVideoTaskIdempotencyConflict)
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
		Body:           []byte(`{"model":"sora-test","prompt":"make a short city clip"}`),
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
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, nil)

	result, err := svc.Create(ctx, VideoTaskCreateParams{
		APIKey:         videoTaskTestAPIKey(),
		User:           &User{ID: 7},
		Body:           []byte(`{"model":"sora-test","prompt":"make a short city clip"}`),
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
	svc := newVideoTaskServiceForTest(repo, nil, selector, provider, nil)

	result, err := svc.Create(ctx, VideoTaskCreateParams{
		APIKey:         videoTaskTestAPIKey(),
		User:           &User{ID: 7},
		Body:           []byte(`{"model":"sora-test","prompt":"make a short city clip"}`),
		ContentType:    "application/json",
		IdempotencyKey: "idem-attach-cancel",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NoError(t, repo.attachCtxErr)
}

func TestVideoTaskServiceCreateIdempotencyCreateConflictReturnsExistingSameHash(t *testing.T) {
	ctx := context.Background()
	events := &videoTaskServiceTestEvents{}
	repo := newFakeVideoTaskRepository(events)
	repo.createErr = errors.New("duplicate idempotency key")
	repo.idempotencyMisses = 1
	body := []byte(`{"model":"sora-test","prompt":"make a short city clip"}`)
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
		Body:           []byte(`{"model":"sora-test","prompt":"make a short city clip"}`),
		ContentType:    "application/json",
		IdempotencyKey: "idem-race-conflict",
	})

	require.Nil(t, result)
	require.ErrorIs(t, err, ErrVideoTaskIdempotencyConflict)
	require.Zero(t, provider.createCalls)
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
	events                *videoTaskServiceTestEvents
	tasks                 map[string]*VideoTask
	lastCreate            VideoTaskCreateInput
	createdBeforeProvider bool
	idempotencyErr        error
	idempotencyMisses     int
	createErr             error
	notFoundErr           error
	attachCtxErr          error
	updateProviderCtxErr  error
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
		ID:           int64(len(r.tasks) + 1),
		PublicTaskID: input.PublicTaskID,
		Provider:     input.Provider,
		Platform:     input.Platform,
		UserID:       input.UserID,
		APIKeyID:     input.APIKeyID,
		GroupID:      input.GroupID,
		AccountID:    input.AccountID,
		ChannelID:    input.ChannelID,
		Model:        input.Model,
		Prompt:       input.Prompt,
		Status:       VideoTaskStatusSubmitting,
		RequestHash:  input.RequestHash,
		PromptHash:   input.PromptHash,
		RequestBody:  append([]byte(nil), input.RequestBody...),
		Metadata:     cloneMap(input.Metadata),
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	r.tasks[task.PublicTaskID] = task
	r.createdBeforeProvider = true
	return cloneVideoTask(task), nil
}

func (r *fakeVideoTaskRepository) AttachUpstream(ctx context.Context, publicTaskID string, update VideoTaskSubmitUpdate) error {
	r.events.add("repo_attach")
	r.attachCtxErr = ctx.Err()
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
	return nil
}

func (r *fakeVideoTaskRepository) GetByPublicTaskID(ctx context.Context, publicTaskID string) (*VideoTask, error) {
	r.events.add("repo_get_public")
	return r.task(publicTaskID)
}

func (r *fakeVideoTaskRepository) GetByPublicTaskIDForUser(ctx context.Context, publicTaskID string, userID int64) (*VideoTask, error) {
	r.events.add("repo_get_user_public")
	task, err := r.task(publicTaskID)
	if err != nil {
		return nil, err
	}
	if task.UserID != userID {
		return nil, errors.New("task not found")
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

func (r *fakeVideoTaskRepository) UpdateSubmit(ctx context.Context, publicTaskID string, update VideoTaskSubmitUpdate) error {
	return r.AttachUpstream(ctx, publicTaskID, update)
}

func (r *fakeVideoTaskRepository) UpdateFromProvider(ctx context.Context, publicTaskID string, update VideoTaskProviderUpdate) error {
	r.events.add("repo_update_provider")
	r.updateProviderCtxErr = ctx.Err()
	task := r.tasks[publicTaskID]
	if task == nil {
		return errors.New("task not found")
	}
	task.Status = update.Status
	task.ProviderStatus = update.ProviderStatus
	task.ResponseBody = append([]byte(nil), update.ResponseBody...)
	task.Metadata = mergeMaps(task.Metadata, update.Metadata)
	task.ErrorMessage = update.ErrorMessage
	task.CompletedAt = update.CompletedAt
	task.ExpiresAt = update.ExpiresAt
	return nil
}

func (r *fakeVideoTaskRepository) UpdateProvider(ctx context.Context, publicTaskID string, update VideoTaskProviderUpdate) error {
	return r.UpdateFromProvider(ctx, publicTaskID, update)
}

func (r *fakeVideoTaskRepository) ClaimDuePollTasks(ctx context.Context, now time.Time, limit int, lockOwner string, lockTTL time.Duration) ([]*VideoTask, error) {
	return nil, nil
}

func (r *fakeVideoTaskRepository) ReleasePollLock(ctx context.Context, publicTaskID string, lockOwner string) error {
	return nil
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
}

func (s *fakeVideoTaskSelector) SelectVideoTaskAccount(ctx context.Context, groupID *int64, sessionHash string, model string) (*AccountSelectionResult, error) {
	s.selectCalls++
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
	contentResult       *VideoContentStream
	createCalls         int
	fetchCalls          int
	contentCalls        int
	createUpstreamModel string
	createContentType   string
	fetchAccountIDs     []int64
}

func (p *fakeVideoTaskProvider) Create(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) (*VideoProviderCreateResult, error) {
	p.createCalls++
	p.createUpstreamModel = upstreamModel
	p.createContentType = contentType
	p.events.add("provider_create")
	if p.createErr != nil {
		return nil, p.createErr
	}
	return p.createResult, nil
}

func (p *fakeVideoTaskProvider) Fetch(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error) {
	p.fetchCalls++
	if account != nil {
		p.fetchAccountIDs = append(p.fetchAccountIDs, account.ID)
	}
	p.events.add("provider_fetch")
	return p.fetchResult, nil
}

func (p *fakeVideoTaskProvider) Content(ctx context.Context, account *Account, task *VideoTask, headers http.Header) (*VideoContentStream, error) {
	p.contentCalls++
	p.events.add("provider_content")
	if p.contentResult != nil {
		return p.contentResult, nil
	}
	return &VideoContentStream{Body: io.NopCloser(strings.NewReader("video")), StatusCode: http.StatusOK}, nil
}

type fakeVideoTaskUsageRecorder struct {
	events            *videoTaskServiceTestEvents
	calledAfterAttach bool
}

func (r *fakeVideoTaskUsageRecorder) RecordVideoTaskSubmission(ctx context.Context, params VideoTaskCreateParams, account *Account, task *VideoTask, result *VideoProviderCreateResult, upstreamModel string) {
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
