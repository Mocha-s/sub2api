package service

import (
	"context"
	"crypto/sha256"
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
	ErrVideoTaskProviderProtocol   = infraerrors.New(http.StatusBadGateway, "VIDEO_TASK_PROVIDER_PROTOCOL_ERROR", "video provider returned an invalid accepted task")
)

const (
	videoTaskAttachReconciliationErrorCode    = "ATTACH_UPSTREAM_FAILED"
	videoTaskAttachReconciliationErrorMessage = "upstream task was created but could not be attached to the local task"
	videoTaskPersistenceTimeout               = 5 * time.Second
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
	repo            VideoTaskRepository
	accountLookup   videoTaskAccountLookup
	selector        videoTaskAccountSelector
	pricingResolver videoTaskPricingResolver
	provider        VideoTaskProvider
	usageRecorder   videoTaskSubmissionUsageRecorder
	settlement      videoTaskSettlementOrchestrator
}

func NewVideoTaskService(repo VideoTaskRepository, accountRepo AccountRepository, openai *OpenAIGatewayService, settlement *VideoTaskSettlementService) *VideoTaskService {
	return &VideoTaskService{
		repo:            repo,
		accountLookup:   accountRepo,
		selector:        openai,
		pricingResolver: openai,
		provider:        NewAccountVideoTaskProvider(openai),
		usageRecorder:   openai,
		settlement:      settlement,
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
	Endpoint       string
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
	selection, _, err := s.SelectAccountWithSchedulerForCapability(ctx, groupID, "", sessionHash, requestedModel, nil, OpenAIUpstreamTransportHTTPSSE, "", false, false, false)
	return selection, err
}

func (s *OpenAIGatewayService) RecordVideoTaskSubmission(ctx context.Context, params VideoTaskCreateParams, account *Account, task *VideoTask, result *VideoProviderCreateResult, upstreamModel string) {
	if s == nil || params.APIKey == nil || params.User == nil || account == nil || task == nil || result == nil {
		return
	}

	forwardResult := videoTaskSubmissionForwardResult(task, result, upstreamModel)
	if err := s.RecordUsage(ctx, &OpenAIRecordUsageInput{
		Result:             forwardResult,
		APIKey:             params.APIKey,
		User:               params.User,
		Account:            account,
		Subscription:       params.Subscription,
		InboundEndpoint:    videoTaskInboundEndpoint(params.Endpoint),
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

func videoTaskSubmissionForwardResult(task *VideoTask, result *VideoProviderCreateResult, upstreamModel string) *OpenAIForwardResult {
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
		RequestID: requestID, ResponseID: responseID, Model: task.Model,
		BillingModel: billingModel, UpstreamModel: upstreamModel,
	}
	if quote, ok := videoTaskQuoteFromMetadata(task.Metadata); ok {
		forwardResult.BillingModel = quote.BillingModel
		forwardResult.VideoCount = quote.Effective.VideoCount
		forwardResult.VideoResolution = quote.Effective.Resolution
		forwardResult.VideoDurationSeconds = quote.Effective.Seconds
		forwardResult.videoTaskQuote = &quote
	}
	return forwardResult
}

func videoTaskInboundEndpoint(endpoint string) string {
	switch endpoint {
	case VideoTaskEndpointVideoGenerations:
		return "/v1/video/generations"
	default:
		return "/v1/videos"
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
	if !isOpenAIVideoTaskGroup(ctx, group) {
		return nil, fmt.Errorf("video generation requires OpenAI group, got %s", group.Platform)
	}
	if !GroupAllowsVideoGeneration(group) {
		return nil, ErrVideoGenerationPermissionDenied
	}

	endpoint := normalizeVideoTaskEndpoint(params.Endpoint)
	req, err := ParseVideoTaskCreateEnvelope(params.Body)
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
			return s.videoTaskReplayResult(ctx, existing, req.RequestHash, endpoint, req.RawBody, params)
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
			return nil, errors.Join(ErrVideoTaskAccountUnavailable, err)
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
	forwardBody := req.RawBody
	var quote *VideoTaskQuote
	pricingSelection := VideoTaskPricingSelection{}
	if s.pricingResolver != nil {
		pricingSelection = s.pricingResolver.ResolveVideoTaskPricing(ctx, VideoTaskPricingResolveInput{
			GroupID: groupID, UserID: params.User.ID, APIKey: params.APIKey, Account: account,
			RequestedModel: req.Model, UpstreamModel: upstreamModel,
		})
		if pricingSelection.Pricing != nil && (pricingSelection.Pricing.BillingMode == BillingModeVideo || pricingSelection.Pricing.BillingMode == BillingModePerRequest) {
			resolved, err := ResolveVideoTaskQuote(req.RawBody, pricingSelection.BillingModel, pricingSelection.Pricing, pricingSelection.RateMultiplier, pricingSelection.AccountRateMultiplier)
			if err != nil {
				return nil, err
			}
			applyVideoAccountStatsPricing(&resolved, pricingSelection.AccountStatsPricing)
			quote = &resolved
			if pricingSelection.Pricing.BillingMode == BillingModeVideo {
				forwardBody, err = applyEffectiveVideoDuration(req.RawBody, resolved.Effective.Seconds)
				if err != nil {
					return nil, err
				}
			}
		}
	}
	adapterName, err := resolveVideoAdapterName(account, nil)
	if err != nil {
		return nil, errors.Join(ErrVideoTaskAccountUnavailable, err)
	}
	if s.provider == nil {
		return nil, fmt.Errorf("%w: video task provider is required", ErrVideoTaskAccountUnavailable)
	}
	providerCtx := withVideoTaskEndpoint(ctx, endpoint)
	if validator, ok := s.provider.(VideoTaskCreateValidator); ok {
		if err := validator.ValidateCreate(providerCtx, account, forwardBody, params.ContentType, upstreamModel); err != nil {
			return nil, err
		}
	}

	publicTaskID, err := GenerateVideoPublicTaskID()
	if err != nil {
		return nil, err
	}
	metadata := videoTaskCreateMetadata(req, account, upstreamModel, idempotencyKey, adapterName, endpoint)
	metadata[VideoAdapterMetadataKey] = adapterName
	channelID := pricingSelection.ChannelID
	var reserveCommand *VideoTaskSettlementReserveCommand
	if quote != nil {
		if s.settlement == nil {
			return nil, errors.New("video task settlement service is required for video pricing")
		}
		reserveCommand, err = s.settlement.Prepare(ctx, VideoTaskSettlementCreateInput{PublicTaskID: publicTaskID, RequestID: VideoTaskChargeRequestID(publicTaskID), Quote: *quote, Params: params, Account: account, UpstreamModel: upstreamModel, ChannelID: channelID})
		if err != nil {
			return nil, err
		}
		metadata["billing_model"] = quote.BillingModel
		metadata["billing_model_source"] = pricingSelection.BillingModelSource
		requestMetadata, _ := videoTaskMetadataMap(metadata["request_metadata"])
		if requestMetadata == nil {
			requestMetadata = map[string]any{}
		}
		requestMetadata["video_pricing_snapshot"] = *quote
		requestMetadata["video_settlement_admission"] = reserveCommand
		metadata["request_metadata"] = requestMetadata
	}
	created, err := s.repo.Create(ctx, VideoTaskCreateInput{
		PublicTaskID: publicTaskID,
		Provider:     VideoTaskProviderOpenAICompatible,
		Platform:     VideoTaskPlatformOpenAIVideo,
		UserID:       params.User.ID,
		APIKeyID:     params.APIKey.ID,
		GroupID:      groupID,
		AccountID:    account.ID,
		ChannelID:    channelID,
		Model:        req.Model,
		Prompt:       req.Prompt,
		RequestHash:  req.RequestHash,
		PromptHash:   req.PromptHash,
		RequestBody:  req.RawBody,
		Metadata:     metadata,
	})
	if err != nil {
		if idempotencyKey != "" {
			if existing, idemErr := s.videoTaskIdempotencyResult(ctx, params.APIKey.ID, idempotencyKey, req.RequestHash, endpoint, req.RawBody, params); idemErr != nil {
				return nil, idemErr
			} else if existing != nil {
				return existing, nil
			}
		}
		return nil, err
	}
	if quote != nil {
		claim, err := s.settlement.ReservePrepared(ctx, reserveCommand)
		if err != nil {
			return nil, err
		}
		if claim == nil || !claim.Claimed {
			if _, reconcileErr := s.settlement.Reconcile(ctx, created); reconcileErr != nil {
				return nil, reconcileErr
			}
			reloaded, reloadErr := s.repo.GetByPublicTaskID(ctx, publicTaskID)
			if reloadErr != nil {
				return nil, errors.Join(ErrVideoTaskSettlementRetriable, reloadErr)
			}
			body, bodyErr := rewriteVideoTaskResponseBody(reloaded)
			if bodyErr != nil {
				return nil, bodyErr
			}
			return &VideoTaskCreateResult{Task: reloaded, ResponseBody: body, Header: http.Header{}}, nil
		}
	}

	return s.submitVideoTask(ctx, providerCtx, params, created, account, forwardBody, upstreamModel, quote)
}

func (s *VideoTaskService) submitVideoTask(ctx, providerCtx context.Context, params VideoTaskCreateParams, created *VideoTask, account *Account, forwardBody []byte, upstreamModel string, quote *VideoTaskQuote) (*VideoTaskCreateResult, error) {
	publicTaskID := created.PublicTaskID
	providerResult, err := s.provider.Create(providerCtx, account, forwardBody, params.ContentType, upstreamModel)
	if err != nil {
		if quote != nil {
			persistCtx, cancel := videoTaskPersistenceContext(ctx)
			defer cancel()
			if releaseErr := s.settlement.FailAndRelease(persistCtx, publicTaskID, err); releaseErr != nil {
				return nil, errors.Join(err, releaseErr)
			}
			return nil, err
		}
		s.markVideoTaskSubmitFailed(ctx, publicTaskID, err)
		return nil, err
	}
	if providerResult == nil {
		err := errors.New("video task provider returned nil create result")
		if quote != nil {
			persistCtx, cancel := videoTaskPersistenceContext(ctx)
			defer cancel()
			if releaseErr := s.settlement.FailAndRelease(persistCtx, publicTaskID, err); releaseErr != nil {
				return nil, errors.Join(err, releaseErr)
			}
			return nil, err
		}
		s.markVideoTaskSubmitFailed(ctx, publicTaskID, err)
		return nil, err
	}
	if strings.TrimSpace(providerResult.ProviderTaskID) == "" {
		err := ErrVideoTaskProviderProtocol.WithCause(errors.New("accepted provider result missing upstream task id"))
		if quote != nil {
			persistCtx, cancel := videoTaskPersistenceContext(ctx)
			defer cancel()
			if releaseErr := s.settlement.FailAndRelease(persistCtx, publicTaskID, err); releaseErr != nil {
				return nil, errors.Join(err, releaseErr)
			}
		} else {
			s.markVideoTaskSubmitFailed(ctx, publicTaskID, err)
		}
		return nil, err
	}
	if providerResult.Status == "" || providerResult.Status == VideoTaskStatusUnknown {
		providerResult.Status = NormalizeOpenAIVideoStatus(providerResult.ProviderStatus)
		if providerResult.Status == VideoTaskStatusUnknown {
			providerResult.Status = VideoTaskStatusQueued
		}
	}
	submittedAt := time.Now()
	var nextPollAt *time.Time
	clearNextPollAt := providerResult.Status.Terminal()
	if !clearNextPollAt {
		next := submittedAt.Add(nextVideoPollDelay(0))
		nextPollAt = &next
	}
	persistCtx, cancelPersist := videoTaskPersistenceContext(ctx)
	submitUpdate := VideoTaskSubmitUpdate{
		ProviderTaskID:  providerResult.ProviderTaskID,
		Status:          providerResult.Status,
		ProviderStatus:  providerResult.ProviderStatus,
		ResponseBody:    providerResult.RawBody,
		Metadata:        providerResult.Metadata,
		SubmittedAt:     &submittedAt,
		ExpiresAt:       providerResult.ExpiresAt,
		NextPollAt:      nextPollAt,
		ClearNextPollAt: clearNextPollAt,
	}
	err = s.repo.AttachUpstream(persistCtx, publicTaskID, submitUpdate)
	cancelPersist()
	if err != nil {
		if persistErr := s.persistVideoTaskAttachFailure(ctx, publicTaskID, submitUpdate); persistErr != nil {
			logger.LegacyPrintf("service.video_task", "attach fallback persistence failed: task_id=%s upstream_task_id=%s attach_err=%v fallback_err=%v", publicTaskID, providerResult.ProviderTaskID, err, persistErr)
			return nil, errors.Join(ErrVideoTaskSettlementRetriable, err, fmt.Errorf("persist video task attach fallback: %w", persistErr))
		}
		return nil, errors.Join(ErrVideoTaskSettlementRetriable, err)
	}
	if quote != nil {
		captureCtx, cancelCapture := videoTaskPersistenceContext(ctx)
		var captureErr error
		if providerResult.Status == VideoTaskStatusFailed {
			captureErr = s.settlement.ReconcilePersistedTask(captureCtx, publicTaskID)
		} else {
			_, captureErr = s.settlement.Capture(captureCtx, publicTaskID)
		}
		cancelCapture()
		if captureErr != nil {
			markerErr := s.markVideoTaskCaptureReconciliation(ctx, publicTaskID, providerResult, captureErr)
			return nil, errors.Join(ErrVideoTaskSettlementRetriable, captureErr, markerErr)
		}
	}

	reloadCtx, cancelReload := videoTaskPersistenceContext(ctx)
	task, err := s.repo.GetByPublicTaskID(reloadCtx, publicTaskID)
	cancelReload()
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
	if quote == nil && s.usageRecorder != nil {
		usageCtx, cancelUsage := videoTaskPersistenceContext(ctx)
		s.usageRecorder.RecordVideoTaskSubmission(usageCtx, params, account, task, providerResult, upstreamModel)
		cancelUsage()
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

func (s *VideoTaskService) markVideoTaskCaptureReconciliation(ctx context.Context, publicTaskID string, result *VideoProviderCreateResult, cause error) error {
	if s == nil || s.repo == nil {
		return errors.New("video task repository is required for reconciliation marker")
	}
	metadata := map[string]any{"reconciliation_error_code": "CAPTURE_FAILED", "reconciliation_error_message": cause.Error()}
	status, providerStatus := VideoTaskStatusQueued, "queued"
	if result != nil {
		status = result.Status
		providerStatus = result.ProviderStatus
	}
	persistCtx, cancel := videoTaskPersistenceContext(ctx)
	defer cancel()
	applied, err := s.repo.UpdateFromProvider(persistCtx, publicTaskID, VideoTaskProviderUpdate{Status: status, ProviderStatus: providerStatus, Metadata: metadata})
	if err != nil {
		return err
	}
	if !applied {
		return errors.New("video task capture reconciliation marker was not persisted")
	}
	return nil
}

func optionalVideoTaskSubscriptionID(subscription *UserSubscription) *int64 {
	if subscription == nil || subscription.ID <= 0 {
		return nil
	}
	id := subscription.ID
	return &id
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
	persistCtx, cancelPersist := videoTaskPersistenceContext(ctx)
	defer cancelPersist()
	return s.updateVideoTaskFromProviderResult(persistCtx, task, providerResult)
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

func (s *VideoTaskService) Refresh(ctx context.Context, params VideoTaskActionParams) (*VideoTaskFetchResult, error) {
	return s.refreshViaProvider(ctx, params, false)
}

func (s *VideoTaskService) Cancel(ctx context.Context, params VideoTaskActionParams) (*VideoTaskFetchResult, error) {
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
	if task.Status.Terminal() || task.Status == VideoTaskStatusCancelled {
		body, err := rewriteVideoTaskResponseBody(task)
		if err != nil {
			return nil, err
		}
		return &VideoTaskFetchResult{Task: task, ResponseBody: body}, nil
	}
	account, err := s.videoTaskAccount(ctx, task.AccountID)
	if err != nil {
		return nil, err
	}
	if s.provider == nil {
		return nil, fmt.Errorf("%w: video task provider is required", ErrVideoTaskAccountUnavailable)
	}
	canceller, ok := s.provider.(VideoTaskCanceller)
	if !ok {
		return nil, unsupportedVideoTaskAction("cancel")
	}
	providerResult, err := canceller.Cancel(ctx, account, task)
	if err != nil {
		return nil, err
	}
	persistCtx, cancelPersist := videoTaskPersistenceContext(ctx)
	defer cancelPersist()
	return s.updateVideoTaskFromProviderResult(persistCtx, task, providerResult)
}

func (s *VideoTaskService) List(ctx context.Context, params VideoTaskListParams) (*VideoTaskListResult, error) {
	if s == nil {
		return nil, errors.New("video task service is nil")
	}
	if s.repo == nil {
		return nil, errors.New("video task repository is required")
	}
	items, hasMore, err := s.repo.ListForUser(ctx, params)
	if err != nil {
		return nil, err
	}
	data := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		body, err := rewriteVideoTaskResponseBody(item)
		if err != nil {
			return nil, err
		}
		data = append(data, json.RawMessage(body))
	}
	body, err := json.Marshal(map[string]any{"object": "list", "data": data, "has_more": hasMore})
	if err != nil {
		return nil, err
	}
	return &VideoTaskListResult{Tasks: items, ResponseBody: body, HasMore: hasMore}, nil
}

func (s *VideoTaskService) Estimate(ctx context.Context, params VideoTaskEstimateParams) (*VideoTaskEstimateResult, error) {
	endpoint := normalizeVideoTaskEndpoint(params.Endpoint)
	req, err := ParseVideoTaskCreateEnvelope(params.Body)
	if err != nil {
		return nil, err
	}
	account, upstreamModel, release, err := s.selectVideoTaskActionAccount(ctx, params.APIKey, params.User, params.Body)
	if err != nil {
		return nil, err
	}
	if release != nil {
		defer release()
	}
	estimator, ok := s.provider.(VideoTaskEstimator)
	if !ok {
		return nil, unsupportedVideoTaskAction("estimate")
	}
	forwardBody := params.Body
	var quote *VideoTaskQuote
	if s.pricingResolver != nil {
		groupID, err := videoTaskGroupID(params.APIKey)
		if err != nil {
			return nil, err
		}
		pricing := s.pricingResolver.ResolveVideoTaskPricing(ctx, VideoTaskPricingResolveInput{
			GroupID: groupID, UserID: params.User.ID, APIKey: params.APIKey, Account: account,
			RequestedModel: req.Model, UpstreamModel: upstreamModel,
		})
		if pricing.Pricing != nil && (pricing.Pricing.BillingMode == BillingModeVideo || pricing.Pricing.BillingMode == BillingModePerRequest) {
			resolved, err := ResolveVideoTaskQuote(params.Body, pricing.BillingModel, pricing.Pricing, pricing.RateMultiplier, pricing.AccountRateMultiplier)
			if err != nil {
				return nil, err
			}
			applyVideoAccountStatsPricing(&resolved, pricing.AccountStatsPricing)
			if pricing.Pricing.BillingMode == BillingModeVideo {
				forwardBody, err = applyEffectiveVideoDuration(params.Body, resolved.Effective.Seconds)
				if err != nil {
					return nil, err
				}
			}
			quote = &resolved
		}
	}
	result, err := estimator.Estimate(withVideoTaskEndpoint(ctx, endpoint), account, forwardBody, params.ContentType, upstreamModel)
	if err != nil || quote == nil {
		return result, err
	}
	return applyVideoTaskEstimateQuote(result, *quote)
}

func applyVideoTaskEstimateQuote(result *VideoTaskEstimateResult, quote VideoTaskQuote) (*VideoTaskEstimateResult, error) {
	if result == nil {
		return nil, errors.New("video task estimator returned nil result")
	}
	var response map[string]any
	if err := json.Unmarshal(result.ResponseBody, &response); err != nil {
		return nil, err
	}
	response["billing_mode"] = quote.BillingMode
	response["billing_model"] = quote.BillingModel
	response["effective"] = quote.Effective
	switch quote.BillingMode {
	case BillingModeVideo:
		delete(response, "per_request_price_usd")
		response["unit_price_usd"] = quote.UnitPriceUSD
	case BillingModePerRequest:
		delete(response, "unit_price_usd")
		response["per_request_price_usd"] = quote.UnitPriceUSD
	}
	response["gross_cost_usd"] = quote.GrossCostUSD
	response["rate_multiplier"] = quote.RateMultiplier
	response["actual_cost_usd"] = quote.ActualCostUSD
	encoded, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	result.ResponseBody = encoded
	result.Metadata = response
	return result, nil
}

func (s *VideoTaskService) References(ctx context.Context, params VideoTaskAssetParams) (*VideoTaskAssetResult, error) {
	endpoint := normalizeVideoTaskEndpoint(params.Endpoint)
	account, upstreamModel, release, err := s.selectVideoTaskAssetActionAccount(ctx, params.APIKey, params.User, params.Body)
	if err != nil {
		return nil, err
	}
	if release != nil {
		defer release()
	}
	provider, ok := s.provider.(VideoReferenceProvider)
	if !ok {
		return nil, unsupportedVideoTaskAction("references")
	}
	return provider.References(withVideoTaskEndpoint(ctx, endpoint), account, params.Body, params.ContentType, upstreamModel)
}

func (s *VideoTaskService) MaterialAssets(ctx context.Context, params VideoTaskAssetParams) (*VideoTaskAssetResult, error) {
	endpoint := normalizeVideoTaskEndpoint(params.Endpoint)
	account, upstreamModel, release, err := s.selectVideoTaskAssetActionAccount(ctx, params.APIKey, params.User, params.Body)
	if err != nil {
		return nil, err
	}
	if release != nil {
		defer release()
	}
	provider, ok := s.provider.(VideoMaterialAssetProvider)
	if !ok {
		return nil, unsupportedVideoTaskAction("material-assets")
	}
	return provider.MaterialAssets(withVideoTaskEndpoint(ctx, endpoint), account, params.Body, params.ContentType, upstreamModel)
}

func (s *VideoTaskService) refreshViaProvider(ctx context.Context, params VideoTaskActionParams, force bool) (*VideoTaskFetchResult, error) {
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
	if task.Status.Terminal() && !force {
		body, err := rewriteVideoTaskResponseBody(task)
		if err != nil {
			return nil, err
		}
		return &VideoTaskFetchResult{Task: task, ResponseBody: body}, nil
	}
	account, err := s.videoTaskAccount(ctx, task.AccountID)
	if err != nil {
		return nil, err
	}
	if s.provider == nil {
		return nil, fmt.Errorf("%w: video task provider is required", ErrVideoTaskAccountUnavailable)
	}
	refresher, ok := s.provider.(VideoTaskRefresher)
	if !ok {
		return nil, unsupportedVideoTaskAction("refresh")
	}
	providerResult, err := refresher.Refresh(ctx, account, task)
	if err != nil {
		return nil, err
	}
	persistCtx, cancelPersist := videoTaskPersistenceContext(ctx)
	defer cancelPersist()
	return s.updateVideoTaskFromProviderResult(persistCtx, task, providerResult)
}

func (s *VideoTaskService) updateVideoTaskFromProviderResult(ctx context.Context, task *VideoTask, providerResult *VideoProviderFetchResult) (*VideoTaskFetchResult, error) {
	if providerResult == nil {
		return nil, errors.New("video task provider returned nil fetch result")
	}
	completedAt := providerResult.CompletedAt
	if completedAt == nil && providerResult.Status.Terminal() {
		now := time.Now()
		completedAt = &now
	}
	_, err := s.repo.UpdateFromProvider(ctx, task.PublicTaskID, VideoTaskProviderUpdate{
		Status:         providerResult.Status,
		ProviderStatus: providerResult.ProviderStatus,
		ResponseBody:   providerResult.RawBody,
		Metadata:       providerResult.Metadata,
		ErrorMessage:   providerResult.ErrorMessage,
		CompletedAt:    completedAt,
		ExpiresAt:      providerResult.ExpiresAt,
	})
	if err != nil {
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
	if reloaded.Status == VideoTaskStatusFailed && s.settlement != nil {
		if err := s.settlement.ReconcilePersistedTask(ctx, reloaded.PublicTaskID); err != nil {
			return nil, errors.Join(ErrVideoTaskSettlementRetriable, err)
		}
	}
	rewritten, err := rewriteVideoTaskResponseBody(reloaded)
	if err != nil {
		return nil, err
	}
	return &VideoTaskFetchResult{Task: reloaded, ResponseBody: rewritten}, nil
}

func (s *VideoTaskService) selectVideoTaskActionAccount(ctx context.Context, apiKey *APIKey, user *User, body []byte) (*Account, string, func(), error) {
	return s.selectVideoTaskAccountForAction(ctx, apiKey, user, func() (string, error) {
		req, err := ParseVideoTaskCreateEnvelope(body)
		if err != nil {
			return "", err
		}
		return req.Model, nil
	})
}

func (s *VideoTaskService) selectVideoTaskAssetActionAccount(ctx context.Context, apiKey *APIKey, user *User, body []byte) (*Account, string, func(), error) {
	return s.selectVideoTaskAccountForAction(ctx, apiKey, user, func() (string, error) {
		return parseVideoTaskModelOnly(body)
	})
}

func (s *VideoTaskService) selectVideoTaskAccountForAction(ctx context.Context, apiKey *APIKey, user *User, modelFromBody func() (string, error)) (*Account, string, func(), error) {
	if s == nil {
		return nil, "", nil, errors.New("video task service is nil")
	}
	if apiKey == nil {
		return nil, "", nil, errors.New("api key is required")
	}
	if user == nil {
		return nil, "", nil, errors.New("user is required")
	}
	group := apiKey.Group
	if group == nil {
		return nil, "", nil, errors.New("api key group is required")
	}
	if !isOpenAIVideoTaskGroup(ctx, group) {
		return nil, "", nil, fmt.Errorf("video generation requires OpenAI group, got %s", group.Platform)
	}
	if !GroupAllowsVideoGeneration(group) {
		return nil, "", nil, ErrVideoGenerationPermissionDenied
	}
	if s.selector == nil {
		return nil, "", nil, fmt.Errorf("%w: video task account selector is required", ErrVideoTaskAccountUnavailable)
	}
	if s.provider == nil {
		return nil, "", nil, fmt.Errorf("%w: video task provider is required", ErrVideoTaskAccountUnavailable)
	}
	if modelFromBody == nil {
		return nil, "", nil, errors.New("video task model parser is required")
	}
	model, err := modelFromBody()
	if err != nil {
		return nil, "", nil, err
	}
	groupID, err := videoTaskGroupID(apiKey)
	if err != nil {
		return nil, "", nil, err
	}
	selection, err := s.selector.SelectVideoTaskAccount(ctx, &groupID, "", model)
	if err != nil {
		if errors.Is(err, ErrNoAvailableAccounts) {
			return nil, "", nil, fmt.Errorf("%w: %w", ErrVideoTaskAccountUnavailable, err)
		}
		return nil, "", nil, err
	}
	if selection == nil || selection.Account == nil {
		return nil, "", nil, fmt.Errorf("%w: no available video task account", ErrVideoTaskAccountUnavailable)
	}
	account := selection.Account
	upstreamModel := strings.TrimSpace(account.GetMappedModel(model))
	if upstreamModel == "" {
		upstreamModel = model
	}
	return account, upstreamModel, selection.ReleaseFunc, nil
}

func isOpenAIVideoTaskGroup(ctx context.Context, group *Group) bool {
	if group == nil {
		return false
	}
	if group.Platform == PlatformOpenAI {
		return true
	}
	if group.Platform != PlatformComposite {
		return false
	}
	platform, ok := ResolvedTargetPlatformFromContext(ctx)
	return ok && platform == PlatformOpenAI
}

func (s *VideoTaskService) videoTaskIdempotencyResult(ctx context.Context, apiKeyID int64, idempotencyKey string, requestHash string, endpoint string, body []byte, params VideoTaskCreateParams) (*VideoTaskCreateResult, error) {
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
	return s.videoTaskReplayResult(ctx, existing, requestHash, endpoint, body, params)
}

func (s *VideoTaskService) videoTaskReplayResult(ctx context.Context, existing *VideoTask, requestHash string, endpoint string, body []byte, params VideoTaskCreateParams) (*VideoTaskCreateResult, error) {
	if err := validateVideoTaskIdempotencyReplay(existing, requestHash, endpoint, body); err != nil {
		return nil, err
	}
	_, priced := videoTaskQuoteFromMetadata(existing.Metadata)
	if videoTaskIsInterruptedAdmissionOrphan(existing) {
		if !priced {
			return nil, errors.Join(ErrVideoTaskSettlementRetriable, ErrVideoTaskSettlementIntegrity)
		}
		lookup, ok := s.settlement.(interface {
			GetPersistedSettlement(context.Context, string) (*VideoTaskSettlementSnapshot, error)
		})
		if !ok {
			return nil, ErrVideoTaskSettlementRetriable
		}
		settlement, err := lookup.GetPersistedSettlement(ctx, existing.PublicTaskID)
		if errors.Is(err, ErrVideoTaskSettlementNotFound) {
			responseBody, responseErr := rewriteVideoTaskResponseBody(existing)
			if responseErr != nil {
				return nil, responseErr
			}
			return &VideoTaskCreateResult{Task: existing, ResponseBody: responseBody, Header: http.Header{}}, nil
		}
		if err != nil {
			return nil, errors.Join(ErrVideoTaskSettlementRetriable, err)
		}
		if settlement != nil {
			return nil, errors.Join(ErrVideoTaskSettlementRetriable, ErrVideoTaskSettlementIntegrity)
		}
		return nil, errors.Join(ErrVideoTaskSettlementRetriable, ErrVideoTaskSettlementIntegrity)
	}
	if priced {
		if s.settlement == nil {
			return nil, ErrVideoTaskSettlementRetriable
		}
		if _, err := s.settlement.Reconcile(ctx, existing); err != nil {
			if errors.Is(err, ErrVideoTaskAdmissionRecovered) {
				return s.resumePristineVideoTask(ctx, existing, params)
			}
			return nil, err
		}
	}
	responseBody, err := rewriteVideoTaskResponseBody(existing)
	if err != nil {
		return nil, err
	}
	return &VideoTaskCreateResult{Task: existing, ResponseBody: responseBody, Header: http.Header{}}, nil
}

func videoTaskIsInterruptedAdmissionOrphan(task *VideoTask) bool {
	if task == nil || task.Status != VideoTaskStatusFailed || strings.TrimSpace(task.ProviderTaskID) != "" || task.SubmittedAt != nil {
		return false
	}
	if videoTaskMetadataString(task.Metadata, "reconciliation_error_code") != VideoTaskAdmissionInterruptedCode {
		return false
	}
	providerCalled, ok := task.Metadata["provider_called"].(bool)
	if !ok || providerCalled {
		return false
	}
	if _, accepted := task.Metadata["reconciliation_accepted_snapshot"]; accepted || videoTaskMetadataString(task.Metadata, "reconciliation_upstream_task_id") != "" {
		return false
	}
	return true
}

func (s *VideoTaskService) resumePristineVideoTask(ctx context.Context, task *VideoTask, params VideoTaskCreateParams) (*VideoTaskCreateResult, error) {
	if !videoTaskCanRetryAdmission(task) || len(task.RequestBody) == 0 {
		return nil, ErrVideoTaskSettlementRetriable
	}
	account, err := s.videoTaskAccount(ctx, task.AccountID)
	if err != nil {
		return nil, errors.Join(ErrVideoTaskSettlementRetriable, err)
	}
	quote, ok := videoTaskQuoteFromMetadata(task.Metadata)
	if !ok {
		return nil, errors.Join(ErrVideoTaskSettlementRetriable, errors.New("video task pricing snapshot is missing"))
	}
	forwardBody := task.RequestBody
	if quote.BillingMode == BillingModeVideo {
		forwardBody, err = applyEffectiveVideoDuration(task.RequestBody, quote.Effective.Seconds)
		if err != nil {
			return nil, errors.Join(ErrVideoTaskSettlementRetriable, err)
		}
	}
	upstreamModel := videoTaskMetadataString(task.Metadata, "upstream_model")
	if upstreamModel == "" {
		upstreamModel = task.Model
	}
	providerCtx := withVideoTaskEndpoint(ctx, normalizeVideoTaskEndpoint(params.Endpoint))
	return s.submitVideoTask(ctx, providerCtx, params, task, account, forwardBody, upstreamModel, &quote)
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
	if _, err := s.repo.UpdateFromProvider(persistCtx, publicTaskID, VideoTaskProviderUpdate{
		Status:          VideoTaskStatusFailed,
		ProviderStatus:  string(VideoTaskStatusFailed),
		ErrorMessage:    message,
		Metadata:        videoTaskMetadataFromError(cause),
		CompletedAt:     &completedAt,
		ClearNextPollAt: true,
	}); err != nil {
		logger.LegacyPrintf("service.video_task", "mark video task submit failed failed: task_id=%s err=%v", publicTaskID, err)
	}
}

func (s *VideoTaskService) persistVideoTaskAttachFailure(ctx context.Context, publicTaskID string, update VideoTaskSubmitUpdate) error {
	if strings.TrimSpace(update.ProviderTaskID) == "" {
		return errors.New("accepted provider result is missing upstream task id")
	}
	persistCtx, cancel := videoTaskPersistenceContext(ctx)
	defer cancel()
	return s.repo.PersistUpstreamFallback(persistCtx, publicTaskID, VideoTaskUpstreamFallback{Snapshot: VideoTaskAcceptedSnapshotFromSubmit(update)})
}

type videoTaskMetadataCarrier interface {
	VideoTaskMetadata() map[string]any
}

type videoTaskMetadataError struct {
	err      error
	metadata map[string]any
}

func withVideoTaskErrorMetadata(err error, metadata map[string]any) error {
	if err == nil || len(metadata) == 0 {
		return err
	}
	return videoTaskMetadataError{err: err, metadata: metadata}
}

func (e videoTaskMetadataError) Error() string {
	if e.err == nil {
		return "video task provider error"
	}
	return e.err.Error()
}

func (e videoTaskMetadataError) Unwrap() error {
	return e.err
}

func (e videoTaskMetadataError) VideoTaskMetadata() map[string]any {
	return copyVideoTaskMetadata(e.metadata)
}

func videoTaskMetadataFromError(err error) map[string]any {
	if err == nil {
		return nil
	}
	var carrier videoTaskMetadataCarrier
	if !errors.As(err, &carrier) {
		return nil
	}
	return copyVideoTaskMetadata(carrier.VideoTaskMetadata())
}

func copyVideoTaskMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		out[key] = value
	}
	return out
}

func videoTaskPersistenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), videoTaskPersistenceTimeout)
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

func videoTaskCreateMetadata(req *VideoTaskCreateEnvelope, account *Account, upstreamModel string, idempotencyKey string, adapterName string, endpoint string) map[string]any {
	metadata := map[string]any{}
	if req != nil {
		requestMetadata := make(map[string]any, len(req.Metadata))
		if existing, ok := videoTaskMetadataMap(req.Metadata["request_metadata"]); ok {
			for key, value := range existing {
				requestMetadata[key] = value
			}
		}
		for key, value := range req.Metadata {
			metadata[key] = value
			if key == "request_metadata" {
				continue
			}
			requestMetadata[key] = value
		}
		metadata["request_metadata"] = requestMetadata
		metadata["requested_model"] = req.Model
		metadata["billing_model"] = req.Model
	}
	metadata["upstream_model"] = upstreamModel
	metadata["upstream_base_url"] = videoTaskUpstreamBaseURL(account, adapterName)
	metadata["idempotency_key"] = idempotencyKey
	metadata[VideoTaskEndpointMetadataKey] = normalizeVideoTaskEndpoint(endpoint)
	return metadata
}

func validateVideoTaskIdempotencyReplay(existing *VideoTask, requestHash string, endpoint string, body []byte) error {
	endpoint = normalizeVideoTaskEndpoint(endpoint)
	existingEndpoint := videoTaskMetadataString(existing.Metadata, VideoTaskEndpointMetadataKey)
	if existingEndpoint == "" {
		if legacyVideoGenerationsRequestHashMatches(existing, endpoint, body) {
			return nil
		}
		existingEndpoint = VideoTaskEndpointVideos
	}
	if normalizeVideoTaskEndpoint(existingEndpoint) != endpoint {
		return fmt.Errorf("%w: idempotency key reused with different endpoint", ErrVideoTaskIdempotencyConflict)
	}
	if existing.RequestHash == requestHash {
		return nil
	}
	return fmt.Errorf("%w: idempotency key reused with different request body", ErrVideoTaskIdempotencyConflict)
}

func legacyVideoGenerationsRequestHashMatches(existing *VideoTask, endpoint string, body []byte) bool {
	if existing == nil || normalizeVideoTaskEndpoint(endpoint) != VideoTaskEndpointVideoGenerations || videoTaskMetadataString(existing.Metadata, VideoTaskEndpointMetadataKey) != "" {
		return false
	}
	adapted, err := adaptLegacyVideoGenerationsCompatBody(body)
	if err != nil {
		return false
	}
	hash := sha256.Sum256(adapted)
	return existing.RequestHash == fmt.Sprintf("%x", hash[:])
}

func normalizeVideoTaskEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return VideoTaskEndpointVideos
	}
	return endpoint
}

func videoTaskUpstreamBaseURL(account *Account, adapterName string) string {
	baseURL := openAIVideoDefaultBaseURL
	if account != nil {
		if value := strings.TrimSpace(account.GetCredential("base_url")); value != "" {
			baseURL = value
		}
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if adapterName != VideoAdapterSeedanceAPIV1 {
		baseURL = strings.TrimSuffix(baseURL, "/v1")
	}
	return baseURL
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
