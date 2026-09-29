package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/shopspring/decimal"
)

type VideoTaskSettlementState string

const (
	VideoTaskSettlementReserved VideoTaskSettlementState = "reserved"
	VideoTaskSettlementCharged  VideoTaskSettlementState = "charged"
	VideoTaskSettlementReleased VideoTaskSettlementState = "released"
	VideoTaskSettlementRefunded VideoTaskSettlementState = "refunded"
)

var (
	ErrVideoTaskSettlementInvalidAmount      = errors.New("video task settlement amount must be finite and nonnegative")
	ErrVideoTaskSettlementNotFound           = errors.New("video task settlement not found")
	ErrVideoTaskInsufficientBalance          = errors.New("insufficient balance for video task reservation")
	ErrVideoTaskSettlementCostExceedsReserve = errors.New("video task capture cost exceeds reservation")
	ErrVideoTaskSettlementCommandRequired    = errors.New("video task settlement command is required")
	ErrVideoTaskSettlementRelationInvalid    = errors.New("video task settlement relation is invalid")
	ErrVideoTaskSubscriptionIneligible       = errors.New("video task subscription is not eligible")
	ErrVideoTaskSettlementIntegrity          = errors.New("video task settlement integrity violation")
	ErrVideoTaskSettlementStateConflict      = errors.New("video task persisted state is not eligible for settlement operation")
	ErrVideoTaskSettlementLeaseLost          = errors.New("video task settlement reconciliation lease lost")
	ErrVideoTaskSettlementLeaseInsufficient  = errors.New("video task settlement reconciliation lease has insufficient remaining time")
	ErrVideoTaskSettlementRetriable          = infraerrors.ServiceUnavailable("VIDEO_TASK_SETTLEMENT_RETRIABLE", "video task settlement requires reconciliation")
	ErrVideoTaskAdmissionRecovered           = errors.New("video task admission recovered")
)

const (
	VideoTaskAdmissionInterruptedCode    = "VIDEO_TASK_SUBMISSION_INTERRUPTED"
	VideoTaskAdmissionInterruptedMessage = "video task submission was interrupted before provider dispatch; retry the request"
)

type VideoTaskSettlementCreateInput struct {
	PublicTaskID   string
	RequestID      string
	Quote          VideoTaskQuote
	Params         VideoTaskCreateParams
	Account        *Account
	RequestedModel string
	UpstreamModel  string
	ChannelID      int64
	SubscriptionID int64
}

type videoTaskSettlementOrchestrator interface {
	Prepare(context.Context, VideoTaskSettlementCreateInput) (*VideoTaskSettlementReserveCommand, error)
	ReservePrepared(context.Context, *VideoTaskSettlementReserveCommand) (*VideoTaskSubmissionClaim, error)
	Reserve(context.Context, VideoTaskSettlementCreateInput) (*VideoTaskSubmissionClaim, error)
	Capture(context.Context, string) (*VideoTaskSettlementSnapshot, error)
	FailAndRelease(context.Context, string, error) error
	Reconcile(context.Context, *VideoTask) (*VideoTaskSettlementSnapshot, error)
	ReconcilePersistedTask(context.Context, string) error
}

type VideoTaskSubmissionClaim struct {
	Settlement *VideoTaskSettlementSnapshot
	Claimed    bool
}

type videoTaskSettlementCache interface {
	InvalidateUserBalance(context.Context, int64) error
	InvalidateSubscription(context.Context, int64, int64) error
	InvalidateAPIKeyRateLimit(context.Context, int64) error
	InvalidateUserPlatformQuota(context.Context, int64, string) error
	HasUserPlatformQuotaLimit(context.Context, int64, string) bool
}

type dashboardRecomputeTrigger interface {
	RecomputeRange(context.Context, time.Time, time.Time) error
}

type VideoTaskSettlementService struct {
	repo           VideoTaskSettlementRepository
	tasks          VideoTaskRepository
	usageLogs      UsageLogRepository
	cache          videoTaskSettlementCache
	authCache      APIKeyAuthCacheInvalidator
	dashboard      dashboardRecomputeTrigger
	dashboardCache DashboardStatsCache
}

func NewVideoTaskSettlementService(repo VideoTaskSettlementRepository, tasks VideoTaskRepository, usageLogs UsageLogRepository, cache *BillingCacheService, authCache APIKeyAuthCacheInvalidator, dashboard *DashboardAggregationService, dashboardCache DashboardStatsCache) *VideoTaskSettlementService {
	service := &VideoTaskSettlementService{repo: repo, tasks: tasks, usageLogs: usageLogs, authCache: authCache, dashboard: dashboard, dashboardCache: dashboardCache}
	if cache != nil {
		service.cache = cache
	}
	return service
}

func VideoTaskChargeRequestID(publicTaskID string) string {
	return "video:" + strings.TrimSpace(publicTaskID) + ":charge"
}

func (s *VideoTaskSettlementService) Reserve(ctx context.Context, input VideoTaskSettlementCreateInput) (*VideoTaskSubmissionClaim, error) {
	cmd, err := s.Prepare(ctx, input)
	if err != nil {
		return nil, err
	}
	return s.ReservePrepared(ctx, cmd)
}

func (s *VideoTaskSettlementService) Prepare(ctx context.Context, input VideoTaskSettlementCreateInput) (*VideoTaskSettlementReserveCommand, error) {
	if s == nil || s.repo == nil || s.tasks == nil {
		return nil, errors.New("video task settlement service is not configured")
	}
	input.PublicTaskID = strings.TrimSpace(input.PublicTaskID)
	input.RequestID = VideoTaskChargeRequestID(input.PublicTaskID)
	billingType := BillingTypeBalance
	var subscriptionID *int64
	if input.Params.Subscription != nil && input.Params.APIKey != nil && input.Params.APIKey.Group != nil && input.Params.APIKey.Group.IsSubscriptionType() {
		billingType = BillingTypeSubscription
		id := input.Params.Subscription.ID
		subscriptionID = &id
	}
	usage := buildVideoTaskSettlementUsage(input, billingType, subscriptionID)
	metadata := map[string]any{"request_id": input.RequestID, "billing_mode": string(input.Quote.BillingMode), "quote": input.Quote}
	effects := s.videoTaskBillingEffects(ctx, input, billingType)
	pricing, err := quoteMap(input.Quote)
	if err != nil {
		return nil, err
	}
	return &VideoTaskSettlementReserveCommand{PublicTaskID: input.PublicTaskID, BillingType: billingType, GrossCostUSD: input.Quote.GrossCostUSD, ActualCostUSD: input.Quote.ActualCostUSD, AccountCostUSD: input.Quote.AccountCostUSD, PricingSnapshot: pricing, Effects: effects, Admission: &VideoTaskSettlementAdmission{SubscriptionID: subscriptionID, UsageLog: usage, UsageMetadata: metadata}}, nil
}

func (s *VideoTaskSettlementService) ReservePrepared(ctx context.Context, cmd *VideoTaskSettlementReserveCommand) (*VideoTaskSubmissionClaim, error) {
	result, err := s.repo.Reserve(ctx, cmd)
	if err != nil {
		return nil, err
	}
	s.afterCommit(ctx, result)
	return &VideoTaskSubmissionClaim{Settlement: result.Settlement, Claimed: result.Applied}, nil
}

func (s *VideoTaskSettlementService) Capture(ctx context.Context, publicTaskID string) (*VideoTaskSettlementSnapshot, error) {
	result, err := s.repo.Capture(ctx, &VideoTaskSettlementCaptureCommand{PublicTaskID: publicTaskID})
	if err != nil {
		return nil, errors.Join(ErrVideoTaskSettlementRetriable, err)
	}
	s.afterCommit(ctx, result)
	if result == nil || result.Settlement == nil || result.Settlement.State == VideoTaskSettlementReserved {
		return nil, ErrVideoTaskSettlementRetriable
	}
	return result.Settlement, nil
}

func (s *VideoTaskSettlementService) FailAndRelease(ctx context.Context, publicTaskID string, cause error) error {
	message := "video task provider submission failed"
	if cause != nil {
		message = cause.Error()
	}
	result, err := s.repo.FailSubmission(ctx, &VideoTaskSettlementFailCommand{PublicTaskID: strings.TrimSpace(publicTaskID), ErrorMessage: message, Metadata: videoTaskMetadataFromError(cause)})
	if err != nil {
		return err
	}
	s.afterCommit(ctx, result)
	return nil
}

func (s *VideoTaskSettlementService) Reconcile(ctx context.Context, task *VideoTask) (*VideoTaskSettlementSnapshot, error) {
	if task == nil {
		return nil, ErrVideoTaskNotFound
	}
	snapshot, err := s.repo.GetByPublicTaskID(ctx, task.PublicTaskID)
	if err != nil {
		if errors.Is(err, ErrVideoTaskSettlementNotFound) && videoTaskCanRetryAdmission(task) {
			cmd, commandErr := videoTaskAdmissionCommandFromMetadata(task.Metadata)
			if commandErr != nil {
				return nil, errors.Join(ErrVideoTaskSettlementRetriable, err, commandErr)
			}
			if NormalizeVideoTaskPublicID(cmd.PublicTaskID) != NormalizeVideoTaskPublicID(task.PublicTaskID) {
				return nil, errors.Join(ErrVideoTaskSettlementRetriable, err, ErrVideoTaskSettlementIntegrity)
			}
			reserveResult, reserveErr := s.repo.Reserve(ctx, &cmd)
			if reserveErr != nil {
				return nil, errors.Join(ErrVideoTaskSettlementRetriable, err, reserveErr)
			}
			if reserveResult.Applied {
				return nil, ErrVideoTaskAdmissionRecovered
			}
			reloaded, reloadErr := s.tasks.GetByPublicTaskID(ctx, task.PublicTaskID)
			if reloadErr != nil {
				return nil, errors.Join(ErrVideoTaskSettlementRetriable, reloadErr)
			}
			task = reloaded
			snapshot = reserveResult.Settlement
			if snapshot == nil || snapshot.State == VideoTaskSettlementReserved && strings.TrimSpace(task.ProviderTaskID) == "" {
				if _, ok := task.Metadata["reconciliation_accepted_snapshot"]; !ok {
					return nil, ErrVideoTaskSettlementRetriable
				}
			}
			if snapshot.State != VideoTaskSettlementReserved {
				return snapshot, nil
			}
		}
		return nil, errors.Join(ErrVideoTaskSettlementRetriable, err)
	}
	if snapshot.State != VideoTaskSettlementReserved {
		return snapshot, nil
	}
	providerTaskID := strings.TrimSpace(task.ProviderTaskID)
	if strings.TrimSpace(task.ProviderTaskID) == "" {
		snapshot, err := videoTaskAcceptedSnapshotFromMetadata(task.Metadata)
		if err != nil {
			return nil, errors.Join(ErrVideoTaskSettlementRetriable, err)
		}
		update, err := snapshot.SubmitUpdate()
		if err != nil {
			return nil, errors.Join(ErrVideoTaskSettlementRetriable, err)
		}
		providerTaskID = update.ProviderTaskID
		if err := s.tasks.AttachUpstream(ctx, task.PublicTaskID, update); err != nil {
			return nil, errors.Join(ErrVideoTaskSettlementRetriable, err)
		}
		task.ProviderTaskID = providerTaskID
	}
	return s.Capture(ctx, task.PublicTaskID)
}

func (s *VideoTaskSettlementService) GetPersistedSettlement(ctx context.Context, publicTaskID string) (*VideoTaskSettlementSnapshot, error) {
	if s == nil || s.repo == nil {
		return nil, errors.New("video task settlement service is not configured")
	}
	return s.repo.GetByPublicTaskID(ctx, strings.TrimSpace(publicTaskID))
}

func videoTaskCanRetryAdmission(task *VideoTask) bool {
	if task == nil || task.Status != VideoTaskStatusSubmitting || strings.TrimSpace(task.ProviderTaskID) != "" {
		return false
	}
	_, accepted := task.Metadata["reconciliation_accepted_snapshot"]
	return !accepted
}

func videoTaskAdmissionCommandFromMetadata(metadata map[string]any) (VideoTaskSettlementReserveCommand, error) {
	requestMetadata, ok := videoTaskMetadataMap(metadata["request_metadata"])
	if !ok {
		return VideoTaskSettlementReserveCommand{}, errors.New("video task admission metadata is missing")
	}
	value, ok := requestMetadata["video_settlement_admission"]
	if !ok {
		return VideoTaskSettlementReserveCommand{}, errors.New("video task admission command is missing")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return VideoTaskSettlementReserveCommand{}, err
	}
	var cmd VideoTaskSettlementReserveCommand
	if err := json.Unmarshal(raw, &cmd); err != nil {
		return VideoTaskSettlementReserveCommand{}, err
	}
	return cmd, nil
}

func videoTaskAcceptedSnapshotFromMetadata(metadata map[string]any) (VideoTaskAcceptedSnapshot, error) {
	value, ok := metadata["reconciliation_accepted_snapshot"]
	if !ok {
		return VideoTaskAcceptedSnapshot{}, errors.New("accepted video task fallback snapshot is missing")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return VideoTaskAcceptedSnapshot{}, err
	}
	var snapshot VideoTaskAcceptedSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return VideoTaskAcceptedSnapshot{}, err
	}
	return snapshot, nil
}

func (s *VideoTaskSettlementService) videoTaskBillingEffects(ctx context.Context, input VideoTaskSettlementCreateInput, billingType int8) VideoTaskBillingEffects {
	e := VideoTaskBillingEffects{AccountQuotaCost: input.Quote.AccountCostUSD, AccountStatsCost: input.Quote.AccountCostUSD}
	if billingType == BillingTypeSubscription {
		e.SubscriptionCost = input.Quote.ActualCostUSD
	} else {
		e.BalanceCost = input.Quote.ActualCostUSD
		if s.cache != nil && input.Params.User != nil && input.Params.APIKey != nil && s.cache.HasUserPlatformQuotaLimit(ctx, input.Params.User.ID, PlatformFromAPIKey(input.Params.APIKey)) {
			e.PlatformQuotaCost = input.Quote.ActualCostUSD
		}
	}
	if input.Params.APIKey != nil {
		if input.Params.APIKey.Quota > 0 {
			e.APIKeyQuotaCost = input.Quote.ActualCostUSD
		}
		if input.Params.APIKey.HasRateLimits() {
			e.APIKeyRateLimitCost = input.Quote.ActualCostUSD
		}
	}
	if input.Account == nil || !input.Account.IsAPIKeyOrBedrock() || !input.Account.HasAnyQuotaLimit() {
		e.AccountQuotaCost = 0
	}
	return e
}

func buildVideoTaskSettlementUsage(input VideoTaskSettlementCreateInput, billingType int8, subscriptionID *int64) *UsageLog {
	mode, inbound, upstream := string(input.Quote.BillingMode), videoTaskInboundEndpoint(input.Params.Endpoint), "/v1/videos"
	groupID := input.Params.APIKey.GroupID
	requestedModel := strings.TrimSpace(input.RequestedModel)
	if requestedModel == "" {
		requestedModel = input.Quote.BillingModel
	}
	if input.Account == nil {
		input.Account = &Account{}
	}
	var channelID *int64
	if input.ChannelID > 0 {
		channelID = &input.ChannelID
	}
	usage := &UsageLog{UserID: input.Params.User.ID, APIKeyID: input.Params.APIKey.ID, AccountID: input.Account.ID, RequestID: VideoTaskChargeRequestID(input.PublicTaskID), Model: input.Quote.BillingModel, RequestedModel: requestedModel, UpstreamModel: optionalTrimmedStringPtr(input.UpstreamModel), ChannelID: channelID, GroupID: groupID, SubscriptionID: subscriptionID, BillingMode: &mode, BillingType: billingType, RequestType: RequestTypeSync, InboundEndpoint: &inbound, UpstreamEndpoint: &upstream, TotalCost: input.Quote.GrossCostUSD, ActualCost: 0, RateMultiplier: input.Quote.RateMultiplier, AccountRateMultiplier: &input.Quote.AccountRateMultiplier, AccountStatsCost: &input.Quote.AccountCostUSD, CreatedAt: time.Now()}
	if input.Quote.BillingMode == BillingModeVideo {
		usage.VideoCount = input.Quote.Effective.VideoCount
		usage.VideoResolution = optionalTrimmedStringPtr(input.Quote.Effective.Resolution)
		usage.VideoDurationSeconds = &input.Quote.Effective.Seconds
	}
	return usage
}

func quoteMap(quote VideoTaskQuote) (map[string]any, error) {
	raw, err := json.Marshal(quote)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	err = json.Unmarshal(raw, &result)
	return result, err
}

func (s *VideoTaskSettlementService) afterCommit(ctx context.Context, result *VideoTaskSettlementResult) {
	if result == nil || !result.Applied {
		return
	}
	cacheCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if s.authCache != nil && result.UserID > 0 {
		if strict, ok := s.authCache.(APIKeyAuthCacheStrictInvalidator); ok {
			if err := strict.InvalidateAuthCacheByUserIDStrict(cacheCtx, result.UserID); err != nil {
				logger.LegacyPrintf("service.video_task_settlement", "immediate auth invalidation failed: %v", err)
			}
		} else {
			s.authCache.InvalidateAuthCacheByUserID(cacheCtx, result.UserID)
		}
	}
	if s.cache == nil || result.Settlement == nil {
		return
	}
	// Settlement and task summaries are authoritative in PostgreSQL. Cache invalidation is best effort after commit.
	if result.Settlement.BillingType == BillingTypeSubscription {
		if err := s.cache.InvalidateSubscription(cacheCtx, result.UserID, result.Settlement.GroupID); err != nil {
			logger.LegacyPrintf("service.video_task_settlement", "immediate subscription invalidation failed: %v", err)
		}
	} else {
		if err := s.cache.InvalidateUserBalance(cacheCtx, result.UserID); err != nil {
			logger.LegacyPrintf("service.video_task_settlement", "immediate balance invalidation failed: %v", err)
		}
	}
	if result.Settlement.Effects.APIKeyRateLimitCost > 0 {
		if err := s.cache.InvalidateAPIKeyRateLimit(cacheCtx, result.APIKeyID); err != nil {
			logger.LegacyPrintf("service.video_task_settlement", "immediate api key rate invalidation failed: %v", err)
		}
	}
	if result.Settlement.Effects.PlatformQuotaCost > 0 && result.PostState.Platform != nil {
		if err := s.cache.InvalidateUserPlatformQuota(cacheCtx, result.PostState.Platform.UserID, result.PostState.Platform.Platform); err != nil {
			logger.LegacyPrintf("service.video_task_settlement", "immediate platform quota invalidation failed: %v", err)
		}
	}
}

func (s *VideoTaskSettlementService) ProcessCacheInvalidation(ctx context.Context, claim VideoTaskCacheInvalidationClaim) error {
	if s == nil || s.cache == nil {
		return errors.New("video task cache invalidation is not configured")
	}
	if len(claim.Payload) > 0 {
		var payload struct {
			Version                   int
			UserID, APIKeyID, GroupID int64
			Platform                  string
			BillingType               int8
			Effects                   VideoTaskBillingEffects
		}
		if err := json.Unmarshal(claim.Payload, &payload); err != nil {
			return fmt.Errorf("decode cache invalidation payload: %w", err)
		}
		if payload.Version != 1 {
			return fmt.Errorf("unsupported cache invalidation payload version %d", payload.Version)
		}
		claim.UserID, claim.APIKeyID, claim.GroupID, claim.Platform, claim.BillingType, claim.Effects = payload.UserID, payload.APIKeyID, payload.GroupID, payload.Platform, payload.BillingType, payload.Effects
	}
	var errs []error
	if s.authCache != nil && claim.UserID > 0 {
		strict, ok := s.authCache.(APIKeyAuthCacheStrictInvalidator)
		if !ok {
			errs = append(errs, errors.New("strict auth cache invalidator is unavailable"))
		} else if err := strict.InvalidateAuthCacheByUserIDStrict(ctx, claim.UserID); err != nil {
			errs = append(errs, err)
		}
	}
	if claim.BillingType == BillingTypeSubscription {
		if err := s.cache.InvalidateSubscription(ctx, claim.UserID, claim.GroupID); err != nil {
			errs = append(errs, err)
		}
	} else if err := s.cache.InvalidateUserBalance(ctx, claim.UserID); err != nil {
		errs = append(errs, err)
	}
	if claim.Effects.APIKeyRateLimitCost > 0 {
		if err := s.cache.InvalidateAPIKeyRateLimit(ctx, claim.APIKeyID); err != nil {
			errs = append(errs, err)
		}
	}
	if claim.Effects.PlatformQuotaCost > 0 {
		if err := s.cache.InvalidateUserPlatformQuota(ctx, claim.UserID, claim.Platform); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *VideoTaskSettlementService) ProcessRefundReporting(ctx context.Context, claim VideoTaskRefundReportingClaim) error {
	if s == nil || s.dashboard == nil || s.dashboardCache == nil {
		return errors.New("video task refund reporting is not configured")
	}
	if claim.UsageCreatedAt.IsZero() {
		return errors.New("video task refund reporting usage timestamp is required")
	}
	if err := s.dashboard.RecomputeRange(ctx, claim.UsageCreatedAt, claim.UsageCreatedAt.Add(time.Nanosecond)); err != nil {
		return err
	}
	return s.dashboardCache.DeleteDashboardStats(ctx)
}

type VideoTaskBillingEffects struct {
	BalanceCost         float64           `json:"balance_cost"`
	SubscriptionCost    float64           `json:"subscription_cost"`
	APIKeyQuotaCost     float64           `json:"api_key_quota_cost"`
	APIKeyRateLimitCost float64           `json:"api_key_rate_limit_cost"`
	AccountQuotaCost    float64           `json:"account_quota_cost"`
	PlatformQuotaCost   float64           `json:"platform_quota_cost"`
	AccountStatsCost    float64           `json:"account_stats_cost"`
	ChargedAt           time.Time         `json:"charged_at"`
	WindowSnapshot      map[string]string `json:"window_snapshot,omitempty"`
}

func (e VideoTaskBillingEffects) Validate() error {
	for _, amount := range []float64{e.BalanceCost, e.SubscriptionCost, e.APIKeyQuotaCost, e.APIKeyRateLimitCost, e.AccountQuotaCost, e.PlatformQuotaCost, e.AccountStatsCost} {
		if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
			return ErrVideoTaskSettlementInvalidAmount
		}
	}
	return nil
}

func (e VideoTaskBillingEffects) Normalize() (VideoTaskBillingEffects, error) {
	values := []*float64{&e.BalanceCost, &e.SubscriptionCost, &e.APIKeyQuotaCost, &e.APIKeyRateLimitCost, &e.AccountQuotaCost, &e.PlatformQuotaCost, &e.AccountStatsCost}
	for _, value := range values {
		normalized, err := NormalizeVideoTaskSettlementAmount(*value)
		if err != nil {
			return VideoTaskBillingEffects{}, err
		}
		*value = normalized
	}
	return e, nil
}

type VideoTaskSettlementReserveCommand struct {
	PublicTaskID    string
	BillingType     int8
	GrossCostUSD    float64
	ActualCostUSD   float64
	AccountCostUSD  float64
	PricingSnapshot map[string]any
	Effects         VideoTaskBillingEffects
	Admission       *VideoTaskSettlementAdmission
}

type VideoTaskSettlementCaptureCommand struct {
	PublicTaskID  string
	ActualCostUSD float64 // Deprecated compatibility assertion; repository derives the charge from the reserve snapshot.
}

type VideoTaskSettlementAdmission struct {
	SubscriptionID *int64
	UsageLog       *UsageLog
	UsageMetadata  map[string]any
}

type VideoTaskSettlementReleaseCommand struct {
	PublicTaskID string
}

type VideoTaskSettlementFailCommand struct {
	PublicTaskID string
	ErrorMessage string
	Metadata     map[string]any
}

type VideoTaskSettlementRefundCommand struct {
	PublicTaskID string
	Reason       string
}

type VideoTaskSettlementSnapshot struct {
	ID              int64
	PublicTaskID    string
	ChargeRequestID string
	State           VideoTaskSettlementState
	BillingType     int8
	UserID          int64
	APIKeyID        int64
	GroupID         int64
	AccountID       int64
	Platform        string
	ChannelID       *int64
	SubscriptionID  *int64
	UsageLogID      *int64
	GrossCostUSD    float64
	ActualCostUSD   float64
	AccountCostUSD  float64
	RefundedCostUSD float64
	PricingSnapshot map[string]any
	Effects         VideoTaskBillingEffects
	ReservedAt      *time.Time
	ChargedAt       *time.Time
	ReleasedAt      *time.Time
	RefundedAt      *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type VideoTaskBalancePostState struct {
	UserID    int64
	Available float64
	Frozen    float64
}

type VideoTaskSubscriptionPostState struct {
	SubscriptionID int64
	DailyUsage     float64
	WeeklyUsage    float64
	MonthlyUsage   float64
	DailyPeriod    *time.Time
	WeeklyPeriod   *time.Time
	MonthlyPeriod  *time.Time
}

type VideoTaskAPIKeyPostState struct {
	APIKeyID      int64
	Status        string
	QuotaUsed     float64
	Usage5h       float64
	Usage1d       float64
	Usage7d       float64
	Window5hStart *time.Time
	Window1dStart *time.Time
	Window7dStart *time.Time
}

type VideoTaskAccountPostState struct {
	AccountID    int64
	TotalUsed    float64
	DailyUsed    float64
	WeeklyUsed   float64
	DailyPeriod  *string
	WeeklyPeriod *string
}

type VideoTaskPlatformPostState struct {
	UserID        int64
	Platform      string
	DailyUsage    float64
	WeeklyUsage   float64
	MonthlyUsage  float64
	DailyPeriod   *time.Time
	WeeklyPeriod  *time.Time
	MonthlyPeriod *time.Time
}

type VideoTaskUsageLogPostState struct {
	UsageLogID          int64
	AccountStatsCost    float64
	RefundedAccountCost float64
}

type VideoTaskSettlementPostState struct {
	Balance      *VideoTaskBalancePostState
	Subscription *VideoTaskSubscriptionPostState
	APIKey       *VideoTaskAPIKeyPostState
	Account      *VideoTaskAccountPostState
	Platform     *VideoTaskPlatformPostState
	UsageLog     *VideoTaskUsageLogPostState
}

type VideoTaskSettlementResult struct {
	Applied              bool
	Settlement           *VideoTaskSettlementSnapshot
	UserID               int64
	APIKeyID             int64
	AccountID            int64
	Platform             string
	Balance              *float64
	FrozenBalance        *float64
	APIKeyQuotaUsed      *float64
	AccountQuotaUsed     *float64
	PlatformDailyUsage   *float64
	PostState            VideoTaskSettlementPostState
	IntegrityDrift       bool
	RefundUsageCreatedAt *time.Time
}

type VideoTaskRefundReportingClaim struct {
	JobID          int64
	SettlementID   int64
	UsageLogID     int64
	UsageCreatedAt time.Time
	LeaseToken     string
	Attempts       int
}

type VideoTaskCacheInvalidationClaim struct {
	JobID, SettlementID, UserID, APIKeyID, GroupID int64
	EventType, Platform, LeaseToken                string
	BillingType                                    int8
	Effects                                        VideoTaskBillingEffects
	Attempts                                       int
	Payload                                        []byte
}

type VideoTaskAdmissionOrphanClaim struct {
	PublicTaskID string
	LeaseToken   string
}

type VideoTaskSettlementRepository interface {
	Reserve(context.Context, *VideoTaskSettlementReserveCommand) (*VideoTaskSettlementResult, error)
	Capture(context.Context, *VideoTaskSettlementCaptureCommand) (*VideoTaskSettlementResult, error)
	Release(context.Context, *VideoTaskSettlementReleaseCommand) (*VideoTaskSettlementResult, error)
	ReleaseFailed(context.Context, *VideoTaskSettlementReleaseCommand) (*VideoTaskSettlementResult, error)
	FailSubmission(context.Context, *VideoTaskSettlementFailCommand) (*VideoTaskSettlementResult, error)
	RefundFailed(context.Context, *VideoTaskSettlementRefundCommand) (*VideoTaskSettlementResult, error)
	GetByPublicTaskID(context.Context, string) (*VideoTaskSettlementSnapshot, error)
	ClaimDueReconciliation(context.Context, time.Time, int, string, time.Duration) ([]VideoTaskSettlementReconcileClaim, error)
	CompleteReconciliation(context.Context, string, string) (bool, error)
	RetryReconciliation(context.Context, string, string, string, time.Time) (bool, error)
	RenewSettlementClaim(context.Context, string, string, time.Duration) (time.Time, bool, error)
	RepairChargedUsage(context.Context, string) (*VideoTaskSettlementResult, error)
	ClaimDueRefundReporting(context.Context, time.Time, int, string, time.Duration) ([]VideoTaskRefundReportingClaim, error)
	CompleteRefundReporting(context.Context, int64, string) (bool, error)
	RetryRefundReporting(context.Context, int64, string, string, time.Time) (bool, error)
	ClaimDueCacheInvalidation(context.Context, int, string, time.Duration) ([]VideoTaskCacheInvalidationClaim, error)
	CompleteCacheInvalidation(context.Context, int64, string) (bool, error)
	RetryCacheInvalidation(context.Context, int64, string, string, time.Time) (bool, error)
	DeadLetterCacheInvalidation(context.Context, int64, string, string) (bool, error)
	ClaimDueAdmissionOrphans(context.Context, time.Time, time.Duration, int, string, time.Duration) ([]VideoTaskAdmissionOrphanClaim, error)
	FailAdmissionOrphan(context.Context, string, string, string, string) (bool, error)
}

func ValidateVideoTaskSettlementAmount(amount float64) error {
	_, err := NormalizeVideoTaskSettlementAmount(amount)
	return err
}

func NormalizeVideoTaskSettlementAmount(amount float64) (float64, error) {
	return normalizeVideoTaskAmount(amount, 8, "9999999999.99999999", true)
}

func NormalizeVideoTaskPricingAmount(amount float64) (float64, error) {
	return normalizeVideoTaskAmount(amount, 10, "9999999999.9999999999", false)
}

func normalizeVideoTaskAmount(amount float64, scale int32, maximum string, rejectRoundedZero bool) (float64, error) {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
		return 0, ErrVideoTaskSettlementInvalidAmount
	}
	d := decimal.NewFromFloat(amount).Round(scale)
	if rejectRoundedZero && amount > 0 && d.IsZero() {
		return 0, ErrVideoTaskSettlementInvalidAmount
	}
	max := decimal.RequireFromString(maximum)
	if d.Abs().GreaterThan(max) {
		return 0, ErrVideoTaskSettlementInvalidAmount
	}
	return d.InexactFloat64(), nil
}

func NormalizeVideoTaskPublicID(id string) string { return strings.TrimSpace(id) }
