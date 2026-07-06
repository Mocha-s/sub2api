package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

var (
	// ErrVideoTaskNotFound marks a missing video task.
	ErrVideoTaskNotFound = infraerrors.NotFound("VIDEO_TASK_NOT_FOUND", "video task not found")
	// ErrVideoGenerationPermissionDenied marks a group-level video generation denial.
	ErrVideoGenerationPermissionDenied = infraerrors.Forbidden("VIDEO_GENERATION_PERMISSION_DENIED", VideoGenerationPermissionMessage)
	// ErrVideoTaskIdempotencyConflict marks reuse of an idempotency key with a different request body.
	ErrVideoTaskIdempotencyConflict = infraerrors.Conflict("VIDEO_TASK_IDEMPOTENCY_CONFLICT", "idempotency key reused with different request body")
	// ErrVideoTaskNotCompleted marks content requests before a task is complete.
	ErrVideoTaskNotCompleted = infraerrors.Conflict("VIDEO_TASK_NOT_COMPLETED", "video task content is not available")
	// ErrVideoTaskAccountUnavailable marks missing account/provider configuration for video tasks.
	ErrVideoTaskAccountUnavailable = infraerrors.ServiceUnavailable("VIDEO_TASK_ACCOUNT_UNAVAILABLE", "video task account unavailable")
)

type videoTaskAccountLookup interface {
	GetByID(ctx context.Context, id int64) (*Account, error)
}

type videoTaskAccountSelector interface {
	SelectVideoTaskAccount(ctx context.Context, groupID *int64, sessionHash string, model string) (*AccountSelectionResult, error)
}

type videoTaskSubmissionUsageRecorder interface {
	RecordVideoTaskSubmission(ctx context.Context, params VideoTaskCreateParams, account *Account, task *VideoTask, result *VideoProviderCreateResult, upstreamModel string)
}

type VideoTaskService struct {
	repo          VideoTaskRepository
	accountLookup videoTaskAccountLookup
	selector      videoTaskAccountSelector
	provider      VideoTaskProvider
	usageRecorder videoTaskSubmissionUsageRecorder
}

func NewVideoTaskService(repo VideoTaskRepository, accountRepo AccountRepository, openai *OpenAIGatewayService) *VideoTaskService {
	return &VideoTaskService{
		repo:          repo,
		accountLookup: accountRepo,
		selector:      openai,
		provider:      NewOpenAICompatibleVideoProviderForGateway(openai),
		usageRecorder: openai,
	}
}

type VideoTaskCreateParams struct {
	APIKey         *APIKey
	User           *User
	Subscription   *UserSubscription
	Body           []byte
	ContentType    string
	UserAgent      string
	IPAddress      string
	IdempotencyKey string
}

type VideoTaskCreateResult struct {
	Task         *VideoTask
	ResponseBody []byte
	Header       http.Header
}

type VideoTaskFetchParams struct {
	UserID       int64
	PublicTaskID string
}

type VideoTaskFetchResult struct {
	Task         *VideoTask
	ResponseBody []byte
}

type VideoTaskContentParams struct {
	UserID       int64
	PublicTaskID string
	Header       http.Header
}

func (s *OpenAIGatewayService) SelectVideoTaskAccount(ctx context.Context, groupID *int64, sessionHash string, requestedModel string) (*AccountSelectionResult, error) {
	if s == nil {
		return nil, errors.New("openai gateway service is nil")
	}
	selection, _, err := s.SelectAccountWithSchedulerForCapability(ctx, groupID, "", sessionHash, requestedModel, nil, OpenAIUpstreamTransportHTTPSSE, "", false)
	return selection, err
}

func (s *OpenAIGatewayService) RecordVideoTaskSubmission(ctx context.Context, params VideoTaskCreateParams, account *Account, task *VideoTask, result *VideoProviderCreateResult, upstreamModel string) {
	if s == nil || params.APIKey == nil || params.User == nil || account == nil || task == nil || result == nil {
		return
	}

	requestID := videoTaskMetadataString(result.Metadata, "request_id")
	responseID := result.ProviderTaskID
	if responseID == "" {
		responseID = task.ProviderTaskID
	}
	billingModel := videoTaskMetadataString(task.Metadata, "billing_model")
	if billingModel == "" {
		billingModel = task.Model
	}

	forwardResult := &OpenAIForwardResult{
		RequestID:     requestID,
		ResponseID:    responseID,
		Model:         task.Model,
		BillingModel:  billingModel,
		UpstreamModel: upstreamModel,
	}
	if err := s.RecordUsage(ctx, &OpenAIRecordUsageInput{
		Result:             forwardResult,
		APIKey:             params.APIKey,
		User:               params.User,
		Account:            account,
		Subscription:       params.Subscription,
		InboundEndpoint:    "/v1/videos",
		UpstreamEndpoint:   "/v1/videos",
		UserAgent:          params.UserAgent,
		IPAddress:          params.IPAddress,
		RequestPayloadHash: task.RequestHash,
		ChannelUsageFields: ChannelUsageFields{
			ChannelID:          task.ChannelID,
			OriginalModel:      task.Model,
			ChannelMappedModel: upstreamModel,
			BillingModelSource: "requested",
		},
	}); err != nil {
		logger.LegacyPrintf("service.video_task", "video task submission usage record failed: task_id=%s err=%v", task.PublicTaskID, err)
	}
}

func (s *VideoTaskService) Create(ctx context.Context, params VideoTaskCreateParams) (*VideoTaskCreateResult, error) {
	if s == nil {
		return nil, errors.New("video task service is nil")
	}
	if s.repo == nil {
		return nil, errors.New("video task repository is required")
	}
	if params.APIKey == nil {
		return nil, errors.New("api key is required")
	}
	if params.User == nil {
		return nil, errors.New("user is required")
	}
	group := params.APIKey.Group
	if group == nil {
		return nil, errors.New("api key group is required")
	}
	if group.Platform != PlatformOpenAI {
		return nil, fmt.Errorf("video generation requires OpenAI group, got %s", group.Platform)
	}
	if !GroupAllowsVideoGeneration(group) {
		return nil, ErrVideoGenerationPermissionDenied
	}

	req, err := ParseOpenAIVideoCreateRequest(params.Body)
	if err != nil {
		return nil, err
	}
	idempotencyKey := strings.TrimSpace(params.IdempotencyKey)
	if idempotencyKey != "" {
		existing, err := s.repo.GetByIdempotencyKey(ctx, params.APIKey.ID, idempotencyKey)
		if err != nil {
			if !isVideoTaskNotFound(err) {
				return nil, err
			}
		}
		if existing != nil {
			if existing.RequestHash != req.RequestHash {
				return nil, fmt.Errorf("%w: idempotency key reused with different request body", ErrVideoTaskIdempotencyConflict)
			}
			responseBody, err := rewriteVideoTaskResponseBody(existing)
			if err != nil {
				return nil, err
			}
			return &VideoTaskCreateResult{Task: existing, ResponseBody: responseBody, Header: http.Header{}}, nil
		}
	}

	if s.selector == nil {
		return nil, fmt.Errorf("%w: video task account selector is required", ErrVideoTaskAccountUnavailable)
	}
	groupID, err := videoTaskGroupID(params.APIKey)
	if err != nil {
		return nil, err
	}
	selection, err := s.selector.SelectVideoTaskAccount(ctx, &groupID, "", req.Model)
	if err != nil {
		if errors.Is(err, ErrNoAvailableAccounts) {
			return nil, fmt.Errorf("%w: %w", ErrVideoTaskAccountUnavailable, err)
		}
		return nil, err
	}
	if selection == nil || selection.Account == nil {
		return nil, fmt.Errorf("%w: no available video task account", ErrVideoTaskAccountUnavailable)
	}
	if selection.ReleaseFunc != nil {
		defer selection.ReleaseFunc()
	}
	account := selection.Account
	upstreamModel := strings.TrimSpace(account.GetMappedModel(req.Model))
	if upstreamModel == "" {
		upstreamModel = req.Model
	}

	publicTaskID, err := GenerateVideoPublicTaskID()
	if err != nil {
		return nil, err
	}
	metadata := videoTaskCreateMetadata(req, account, upstreamModel, idempotencyKey)
	created, err := s.repo.Create(ctx, VideoTaskCreateInput{
		PublicTaskID: publicTaskID,
		Provider:     VideoTaskProviderOpenAICompatible,
		Platform:     VideoTaskPlatformOpenAIVideo,
		UserID:       params.User.ID,
		APIKeyID:     params.APIKey.ID,
		GroupID:      groupID,
		AccountID:    account.ID,
		Model:        req.Model,
		Prompt:       req.Prompt,
		RequestHash:  req.RequestHash,
		PromptHash:   req.PromptHash,
		RequestBody:  req.RawBody,
		Metadata:     metadata,
	})
	if err != nil {
		if idempotencyKey != "" {
			if existing, idemErr := s.videoTaskIdempotencyResult(ctx, params.APIKey.ID, idempotencyKey, req.RequestHash); idemErr != nil {
				return nil, idemErr
			} else if existing != nil {
				return existing, nil
			}
		}
		return nil, err
	}

	if s.provider == nil {
		return nil, fmt.Errorf("%w: video task provider is required", ErrVideoTaskAccountUnavailable)
	}
	providerResult, err := s.provider.Create(ctx, account, req.RawBody, params.ContentType, upstreamModel)
	if err != nil {
		s.markVideoTaskSubmitFailed(ctx, publicTaskID, err)
		return nil, err
	}
	if providerResult == nil {
		err := errors.New("video task provider returned nil create result")
		s.markVideoTaskSubmitFailed(ctx, publicTaskID, err)
		return nil, err
	}
	submittedAt := time.Now()
	var nextPollAt *time.Time
	clearNextPollAt := providerResult.Status.Terminal()
	if !clearNextPollAt {
		next := submittedAt.Add(nextVideoPollDelay(0))
		nextPollAt = &next
	}
	persistCtx, cancelPersist := videoTaskPersistenceContext(ctx)
	defer cancelPersist()
	if err := s.repo.AttachUpstream(persistCtx, publicTaskID, VideoTaskSubmitUpdate{
		ProviderTaskID:  providerResult.ProviderTaskID,
		Status:          providerResult.Status,
		ProviderStatus:  providerResult.ProviderStatus,
		ResponseBody:    providerResult.RawBody,
		Metadata:        providerResult.Metadata,
		SubmittedAt:     &submittedAt,
		ExpiresAt:       providerResult.ExpiresAt,
		NextPollAt:      nextPollAt,
		ClearNextPollAt: clearNextPollAt,
	}); err != nil {
		return nil, err
	}

	task, err := s.repo.GetByPublicTaskID(ctx, publicTaskID)
	if err != nil {
		if isVideoTaskNotFound(err) {
			return nil, ErrVideoTaskNotFound.WithCause(err)
		}
		return nil, err
	}
	if task == nil {
		task = created
		task.ProviderTaskID = providerResult.ProviderTaskID
		task.Status = providerResult.Status
		task.ProviderStatus = providerResult.ProviderStatus
		task.ResponseBody = append([]byte(nil), providerResult.RawBody...)
		task.SubmittedAt = &submittedAt
	}
	if s.usageRecorder != nil {
		s.usageRecorder.RecordVideoTaskSubmission(ctx, params, account, task, providerResult, upstreamModel)
	}

	responseBody := providerResult.RawBody
	if len(responseBody) == 0 {
		responseBody = task.ResponseBody
	}
	rewritten, err := RewriteOpenAIVideoTaskID(responseBody, publicTaskID)
	if err != nil {
		return nil, err
	}
	return &VideoTaskCreateResult{Task: task, ResponseBody: rewritten, Header: http.Header{}}, nil
}

func (s *VideoTaskService) Fetch(ctx context.Context, params VideoTaskFetchParams) (*VideoTaskFetchResult, error) {
	if s == nil {
		return nil, errors.New("video task service is nil")
	}
	if s.repo == nil {
		return nil, errors.New("video task repository is required")
	}
	task, err := s.repo.GetByPublicTaskIDForUser(ctx, params.PublicTaskID, params.UserID)
	if err != nil {
		if isVideoTaskNotFound(err) {
			return nil, ErrVideoTaskNotFound.WithCause(err)
		}
		return nil, err
	}
	if task == nil {
		return nil, ErrVideoTaskNotFound
	}
	if task.Status.Terminal() {
		responseBody, err := rewriteVideoTaskResponseBody(task)
		if err != nil {
			return nil, err
		}
		return &VideoTaskFetchResult{Task: task, ResponseBody: responseBody}, nil
	}

	account, err := s.videoTaskAccount(ctx, task.AccountID)
	if err != nil {
		return nil, err
	}
	if s.provider == nil {
		return nil, fmt.Errorf("%w: video task provider is required", ErrVideoTaskAccountUnavailable)
	}
	providerResult, err := s.provider.Fetch(ctx, account, task)
	if err != nil {
		return nil, err
	}
	if providerResult == nil {
		return nil, errors.New("video task provider returned nil fetch result")
	}
	completedAt := providerResult.CompletedAt
	if completedAt == nil && providerResult.Status.Terminal() {
		now := time.Now()
		completedAt = &now
	}
	if err := s.repo.UpdateFromProvider(ctx, task.PublicTaskID, VideoTaskProviderUpdate{
		Status:         providerResult.Status,
		ProviderStatus: providerResult.ProviderStatus,
		ResponseBody:   providerResult.RawBody,
		Metadata:       providerResult.Metadata,
		ErrorMessage:   providerResult.ErrorMessage,
		CompletedAt:    completedAt,
		ExpiresAt:      providerResult.ExpiresAt,
	}); err != nil {
		if isVideoTaskNotFound(err) {
			return nil, ErrVideoTaskNotFound.WithCause(err)
		}
		return nil, err
	}
	reloaded, err := s.repo.GetByPublicTaskID(ctx, task.PublicTaskID)
	if err != nil {
		if isVideoTaskNotFound(err) {
			return nil, ErrVideoTaskNotFound.WithCause(err)
		}
		return nil, err
	}
	if reloaded == nil {
		reloaded = task
		reloaded.Status = providerResult.Status
		reloaded.ProviderStatus = providerResult.ProviderStatus
		reloaded.ResponseBody = append([]byte(nil), providerResult.RawBody...)
		reloaded.CompletedAt = completedAt
	}
	responseBody := providerResult.RawBody
	if len(responseBody) == 0 {
		responseBody = reloaded.ResponseBody
	}
	rewritten, err := RewriteOpenAIVideoTaskID(responseBody, task.PublicTaskID)
	if err != nil {
		return nil, err
	}
	return &VideoTaskFetchResult{Task: reloaded, ResponseBody: rewritten}, nil
}

func (s *VideoTaskService) Content(ctx context.Context, params VideoTaskContentParams) (*VideoContentStream, error) {
	if s == nil {
		return nil, errors.New("video task service is nil")
	}
	if s.repo == nil {
		return nil, errors.New("video task repository is required")
	}
	task, err := s.repo.GetByPublicTaskIDForUser(ctx, params.PublicTaskID, params.UserID)
	if err != nil {
		if isVideoTaskNotFound(err) {
			return nil, ErrVideoTaskNotFound.WithCause(err)
		}
		return nil, err
	}
	if task == nil {
		return nil, ErrVideoTaskNotFound
	}
	if task.Status != VideoTaskStatusCompleted {
		return nil, fmt.Errorf("%w; current status: %s", ErrVideoTaskNotCompleted, task.Status)
	}
	account, err := s.videoTaskAccount(ctx, task.AccountID)
	if err != nil {
		return nil, err
	}
	if s.provider == nil {
		return nil, fmt.Errorf("%w: video task provider is required", ErrVideoTaskAccountUnavailable)
	}
	return s.provider.Content(ctx, account, task, params.Header)
}

func (s *VideoTaskService) videoTaskIdempotencyResult(ctx context.Context, apiKeyID int64, idempotencyKey string, requestHash string) (*VideoTaskCreateResult, error) {
	existing, err := s.repo.GetByIdempotencyKey(ctx, apiKeyID, idempotencyKey)
	if err != nil {
		if isVideoTaskNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if existing == nil {
		return nil, nil
	}
	if existing.RequestHash != requestHash {
		return nil, fmt.Errorf("%w: idempotency key reused with different request body", ErrVideoTaskIdempotencyConflict)
	}
	responseBody, err := rewriteVideoTaskResponseBody(existing)
	if err != nil {
		return nil, err
	}
	return &VideoTaskCreateResult{Task: existing, ResponseBody: responseBody, Header: http.Header{}}, nil
}

func (s *VideoTaskService) markVideoTaskSubmitFailed(ctx context.Context, publicTaskID string, cause error) {
	if s == nil || s.repo == nil || publicTaskID == "" {
		return
	}
	message := "video task submit failed"
	if cause != nil {
		message = cause.Error()
	}
	completedAt := time.Now()
	persistCtx, cancel := videoTaskPersistenceContext(ctx)
	defer cancel()
	if err := s.repo.UpdateFromProvider(persistCtx, publicTaskID, VideoTaskProviderUpdate{
		Status:          VideoTaskStatusFailed,
		ProviderStatus:  string(VideoTaskStatusFailed),
		ErrorMessage:    message,
		CompletedAt:     &completedAt,
		ClearNextPollAt: true,
	}); err != nil {
		logger.LegacyPrintf("service.video_task", "mark video task submit failed failed: task_id=%s err=%v", publicTaskID, err)
	}
}

func videoTaskPersistenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func (s *VideoTaskService) videoTaskAccount(ctx context.Context, accountID int64) (*Account, error) {
	if accountID <= 0 {
		return nil, fmt.Errorf("%w: video task missing account id", ErrVideoTaskAccountUnavailable)
	}
	if s.accountLookup == nil {
		return nil, fmt.Errorf("%w: video task account lookup is required", ErrVideoTaskAccountUnavailable)
	}
	account, err := s.accountLookup.GetByID(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("%w: account %d: %v", ErrVideoTaskAccountUnavailable, accountID, err)
	}
	if account == nil {
		return nil, fmt.Errorf("%w: account %d not found", ErrVideoTaskAccountUnavailable, accountID)
	}
	return account, nil
}

func videoTaskGroupID(apiKey *APIKey) (int64, error) {
	if apiKey == nil {
		return 0, errors.New("api key is required")
	}
	if apiKey.Group != nil && apiKey.Group.ID > 0 {
		return apiKey.Group.ID, nil
	}
	if apiKey.GroupID != nil && *apiKey.GroupID > 0 {
		return *apiKey.GroupID, nil
	}
	return 0, errors.New("api key group id is required")
}

func videoTaskCreateMetadata(req *OpenAIVideoCreateRequest, account *Account, upstreamModel string, idempotencyKey string) map[string]any {
	metadata := map[string]any{}
	if req != nil {
		for key, value := range req.Metadata {
			metadata[key] = value
		}
		metadata["requested_model"] = req.Model
		metadata["billing_model"] = req.Model
	}
	metadata["upstream_model"] = upstreamModel
	metadata["upstream_base_url"] = videoTaskUpstreamBaseURL(account)
	metadata["idempotency_key"] = idempotencyKey
	return metadata
}

func videoTaskUpstreamBaseURL(account *Account) string {
	baseURL := openAIVideoDefaultBaseURL
	if account != nil {
		if value := strings.TrimSpace(account.GetCredential("base_url")); value != "" {
			baseURL = value
		}
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return strings.TrimSuffix(baseURL, "/v1")
}

func rewriteVideoTaskResponseBody(task *VideoTask) ([]byte, error) {
	if task == nil {
		return nil, ErrVideoTaskNotFound
	}
	if len(task.ResponseBody) == 0 {
		return videoTaskLocalResponseBody(task)
	}
	return RewriteOpenAIVideoTaskID(task.ResponseBody, task.PublicTaskID)
}

func videoTaskLocalResponseBody(task *VideoTask) ([]byte, error) {
	if task == nil {
		return nil, ErrVideoTaskNotFound
	}
	status := string(task.Status)
	if status == "" {
		status = string(VideoTaskStatusUnknown)
	}
	return json.Marshal(map[string]any{
		"id":     task.PublicTaskID,
		"object": "video",
		"status": status,
	})
}

func isVideoTaskNotFound(err error) bool {
	return err != nil && (errors.Is(err, ErrVideoTaskNotFound) || infraerrors.IsNotFound(err))
}

func videoTaskMetadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func nextVideoPollDelay(attempts int) time.Duration {
	delays := []time.Duration{
		5 * time.Second,
		10 * time.Second,
		20 * time.Second,
		30 * time.Second,
		1 * time.Minute,
		2 * time.Minute,
	}
	if attempts < 0 {
		attempts = 0
	}
	if attempts >= len(delays) {
		return delays[len(delays)-1]
	}
	return delays[attempts]
}
