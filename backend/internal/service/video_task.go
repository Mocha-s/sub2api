package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	VideoTaskProviderOpenAICompatible = "openai_compatible"
	VideoTaskPlatformOpenAIVideo      = "openai_video"
	VideoGenerationPermissionMessage  = "Video generation is not enabled for this group"
	VideoTaskEndpointVideos           = "videos"
	VideoTaskEndpointVideoGenerations = "video_generations"
	VideoTaskEndpointMetadataKey      = "video_task_endpoint"
)

var (
	supportedOpenAIVideoModels = map[string]struct{}{
		"video-ds-2.0":      {},
		"video-ds-2.0-fast": {},
	}
	unifiedVideoGenerationSemanticFields = []string{
		"resolution", "ratio", "aspect_ratio", "duration", "seconds", "duration_seconds", "generate_audio",
		"task_mode", "priority", "return_last_frame", "web_search",
		"content", "quality", "size", "reference_mode", "image", "images", "images_base64", "imagesBase64", "image_url", "image_urls", "imageUrls", "image_reference",
		"reference_image", "referenceImage", "reference_images", "referenceImages", "reference_image_url", "referenceImageUrl", "reference_image_urls", "referenceImageUrls", "first_frame_image", "input_reference", "inputReference",
		"video", "videos", "video_url", "videoUrl", "video_urls", "videoUrls", "reference_video", "referenceVideo", "reference_videos", "referenceVideos", "reference_video_url", "referenceVideoUrl", "reference_video_urls", "referenceVideoUrls", "input_video", "inputVideo",
		"audio", "audios", "audios_base64", "audiosBase64", "audio_url", "audioUrl", "audio_urls", "audioUrls", "audio_reference", "audioReference", "audio_references", "audioReferences", "reference_audio", "referenceAudio", "reference_audios", "referenceAudios", "reference_audio_url", "referenceAudioUrl", "reference_audio_urls", "referenceAudioUrls",
		"start_frame", "end_frame", "style_references", "styleReferences", "element_references", "elementReferences", "storage_object_id",
	}

	// ErrVideoTaskActionUnsupported marks an action the selected video adapter does not implement.
	ErrVideoTaskActionUnsupported = infraerrors.New(http.StatusNotImplemented, "VIDEO_TASK_ACTION_UNSUPPORTED", "video task action is not supported")
	// ErrVideoTaskDeleteNotReady marks deletion before a task reaches a terminal state.
	ErrVideoTaskDeleteNotReady = infraerrors.Conflict("VIDEO_TASK_DELETE_NOT_READY", "video task can be deleted only after it reaches a terminal status")
)

func GroupAllowsVideoGeneration(group *Group) bool {
	return group != nil && group.AllowVideoGeneration
}

type VideoTaskStatus string

const (
	VideoTaskStatusSubmitting VideoTaskStatus = "submitting"
	VideoTaskStatusQueued     VideoTaskStatus = "queued"
	VideoTaskStatusInProgress VideoTaskStatus = "in_progress"
	VideoTaskStatusCompleted  VideoTaskStatus = "completed"
	VideoTaskStatusFailed     VideoTaskStatus = "failed"
	VideoTaskStatusCancelled  VideoTaskStatus = "cancelled"
	VideoTaskStatusExpired    VideoTaskStatus = "expired"
	VideoTaskStatusUnknown    VideoTaskStatus = "unknown"
)

func (s VideoTaskStatus) Terminal() bool {
	switch s {
	case VideoTaskStatusCompleted, VideoTaskStatusFailed, VideoTaskStatusCancelled, VideoTaskStatusExpired:
		return true
	default:
		return false
	}
}

func NormalizeOpenAIVideoStatus(status string) VideoTaskStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "queued", "pending", "submitted", "not_start":
		return VideoTaskStatusQueued
	case "processing", "running", "in_progress":
		return VideoTaskStatusInProgress
	case "completed", "complete", "success", "succeeded":
		return VideoTaskStatusCompleted
	case "failed", "failure", "error":
		return VideoTaskStatusFailed
	case "cancelled", "canceled":
		return VideoTaskStatusCancelled
	case "expired":
		return VideoTaskStatusExpired
	default:
		return VideoTaskStatusUnknown
	}
}

type OpenAIVideoCreateRequest struct {
	Model       string
	Prompt      string
	Seconds     string
	AspectRatio string
	Images      []json.RawMessage
	Videos      []json.RawMessage
	Audios      []json.RawMessage
	Metadata    map[string]any
	RequestHash string
	PromptHash  string
	RawBody     []byte
}

type VideoTaskCreateEnvelope struct {
	Model       string
	Prompt      string
	Metadata    map[string]any
	RequestHash string
	PromptHash  string
	RawBody     []byte
}

func ParseVideoTaskCreateEnvelope(body []byte) (*VideoTaskCreateEnvelope, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if payload == nil {
		return nil, errors.New("video create JSON body must be an object")
	}

	model := rawStringField(payload, "model")
	if model == "" {
		return nil, errors.New("model is required")
	}
	prompt := rawStringField(payload, "prompt")
	if prompt == "" {
		prompt = firstTextContent(payload["content"])
	}
	if prompt == "" {
		return nil, errors.New("prompt is required")
	}

	requestHash := sha256.Sum256(body)
	promptHash := sha256.Sum256([]byte(prompt))
	metadata := map[string]any{
		"requested_model": model,
		"billing_model":   model,
	}
	copyStringMetadata(payload, metadata, "duration", "seconds", "aspect_ratio", "ratio", "resolution")
	copyMediaCountMetadata(payload, metadata)

	return &VideoTaskCreateEnvelope{
		Model:       model,
		Prompt:      prompt,
		Metadata:    metadata,
		RequestHash: hex.EncodeToString(requestHash[:]),
		PromptHash:  hex.EncodeToString(promptHash[:]),
		RawBody:     append([]byte(nil), body...),
	}, nil
}

func validateUnifiedVideoGenerationFields(ctx context.Context, body []byte, supportedFields ...string) error {
	if videoTaskEndpointFromContext(ctx) != VideoTaskEndpointVideoGenerations {
		return nil
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	if payload == nil {
		return errors.New("video create JSON body must be an object")
	}
	supported := make(map[string]struct{}, len(supportedFields))
	for _, field := range supportedFields {
		supported[field] = struct{}{}
	}
	for _, field := range unifiedVideoGenerationSemanticFields {
		if _, present := payload[field]; !present {
			continue
		}
		if _, ok := supported[field]; !ok {
			return invalidVideoTaskRequest("%s is not supported by the selected video provider", field)
		}
	}
	return nil
}

func parseVideoTaskModelOnly(body []byte) (string, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	if payload == nil {
		return "", errors.New("video action JSON body must be an object")
	}
	model := rawStringField(payload, "model")
	if model == "" {
		return "", errors.New("model is required")
	}
	return model, nil
}

func rawStringField(payload map[string]json.RawMessage, key string) string {
	raw := payload[key]
	if len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return strings.TrimSpace(value)
	}
	return ""
}

func firstTextContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return ""
	}
	for _, item := range items {
		if rawStringField(item, "type") == "text" {
			if text := rawStringField(item, "text"); text != "" {
				return text
			}
		}
	}
	return ""
}

func copyStringMetadata(payload map[string]json.RawMessage, metadata map[string]any, keys ...string) {
	for _, key := range keys {
		raw := payload[key]
		if len(raw) == 0 {
			continue
		}
		if value := rawStringField(payload, key); value != "" {
			metadata[key] = value
			continue
		}
		var number json.Number
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		if err := decoder.Decode(&number); err == nil {
			metadata[key] = number.String()
		}
	}
}

func copyMediaCountMetadata(payload map[string]json.RawMessage, metadata map[string]any) {
	metadata["image_count"] = rawArrayCount(payload["images"])
	metadata["video_count"] = rawArrayCount(payload["videos"])
	metadata["audio_count"] = rawArrayCount(payload["audios"])
}

func rawArrayCount(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return 0
	}
	return len(values)
}

type openAIVideoCreatePayload struct {
	Model       string            `json:"model"`
	Prompt      string            `json:"prompt"`
	Seconds     string            `json:"seconds"`
	AspectRatio string            `json:"aspect_ratio"`
	Images      []json.RawMessage `json:"images"`
	Videos      []json.RawMessage `json:"videos"`
	Audios      []json.RawMessage `json:"audios"`
}

func ParseOpenAIVideoCreateRequest(body []byte) (*OpenAIVideoCreateRequest, error) {
	var payload openAIVideoCreatePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if err := rejectForbiddenOpenAIVideoFields(body); err != nil {
		return nil, err
	}

	model := strings.TrimSpace(payload.Model)
	if model == "" {
		return nil, errors.New("model is required")
	}
	if _, ok := supportedOpenAIVideoModels[model]; !ok {
		return nil, errors.New("model must be video-ds-2.0-fast or video-ds-2.0")
	}
	prompt := strings.TrimSpace(payload.Prompt)
	if prompt == "" {
		return nil, errors.New("prompt is required")
	}
	aspectRatio := strings.TrimSpace(payload.AspectRatio)

	requestHash := sha256.Sum256(body)
	promptHash := sha256.Sum256([]byte(prompt))
	rawBody := append([]byte(nil), body...)

	return &OpenAIVideoCreateRequest{
		Model:       model,
		Prompt:      prompt,
		Seconds:     payload.Seconds,
		AspectRatio: aspectRatio,
		Images:      append([]json.RawMessage(nil), payload.Images...),
		Videos:      append([]json.RawMessage(nil), payload.Videos...),
		Audios:      append([]json.RawMessage(nil), payload.Audios...),
		Metadata: map[string]any{
			"seconds":      payload.Seconds,
			"aspect_ratio": aspectRatio,
			"image_count":  len(payload.Images),
			"video_count":  len(payload.Videos),
			"audio_count":  len(payload.Audios),
		},
		RequestHash: hex.EncodeToString(requestHash[:]),
		PromptHash:  hex.EncodeToString(promptHash[:]),
		RawBody:     rawBody,
	}, nil
}

func rejectForbiddenOpenAIVideoFields(body []byte) error {
	return rejectForbiddenOpenAIVideoFieldsWithCompatFields(body, false)
}

func rejectForbiddenOpenAIVideoFieldsWithCompatFields(body []byte, allowCompatFields bool) error {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	for _, field := range []string{"duration", "width", "height", "size", "mode", "model_name", "req_key"} {
		if allowCompatFields && field == "size" {
			continue
		}
		if _, ok := payload[field]; ok {
			return fmt.Errorf("%s is not supported by /v1/videos", field)
		}
	}
	return nil
}

func GenerateVideoPublicTaskID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "task_" + hex.EncodeToString(b[:]), nil
}

func RewriteOpenAIVideoTaskID(body []byte, publicTaskID string) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}

	rewrittenID, err := json.Marshal(publicTaskID)
	if err != nil {
		return nil, err
	}
	payload["id"] = rewrittenID
	if _, ok := payload["task_id"]; ok {
		payload["task_id"] = rewrittenID
	}

	return json.Marshal(payload)
}

type VideoTask struct {
	ID             int64
	PublicTaskID   string
	ProviderTaskID string
	Provider       string
	Platform       string
	UserID         int64
	APIKeyID       int64
	GroupID        int64
	AccountID      int64
	ChannelID      int64
	SubscriptionID *int64
	UsageLogID     *int64
	Model          string
	Prompt         string
	Status         VideoTaskStatus
	ProviderStatus string
	RequestHash    string
	PromptHash     string
	RequestBody    []byte
	ResponseBody   []byte
	Metadata       map[string]any
	UsageMetadata  map[string]any
	BilledUSD      float64
	ErrorMessage   string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	SubmittedAt    *time.Time
	StartedAt      *time.Time
	CompletedAt    *time.Time
	ExpiresAt      *time.Time
	NextPollAt     *time.Time
	LastPolledAt   *time.Time
	LockedBy       *string
	LockedUntil    *time.Time
	PollAttempts   int
	UserDeletedAt  *time.Time
}

type VideoTaskActionParams struct {
	UserID         int64
	PublicTaskID   string
	IdempotencyKey string
}

type VideoTaskListParams struct {
	UserID int64
	Status string
	Model  string
	Limit  int
	// After limits results to tasks created after this timestamp, exclusive.
	After time.Time
	// Before limits results to tasks created before this timestamp, exclusive.
	Before time.Time
}

type VideoTaskListResult struct {
	Tasks        []*VideoTask
	ResponseBody []byte
	HasMore      bool
}

type VideoTaskEstimateParams struct {
	APIKey      *APIKey
	User        *User
	Body        []byte
	ContentType string
	Endpoint    string
}

type VideoTaskEstimateResult struct {
	ResponseBody []byte
	Metadata     map[string]any
}

type VideoTaskAssetParams struct {
	APIKey         *APIKey
	User           *User
	Body           []byte
	ContentType    string
	Endpoint       string
	IdempotencyKey string
}

type VideoTaskAssetResult struct {
	ResponseBody []byte
	Metadata     map[string]any
}

type VideoTaskCreateInput struct {
	PublicTaskID   string
	Provider       string
	Platform       string
	UserID         int64
	APIKeyID       int64
	GroupID        int64
	AccountID      int64
	ChannelID      int64
	SubscriptionID *int64
	Model          string
	Prompt         string
	RequestHash    string
	PromptHash     string
	RequestBody    []byte
	Metadata       map[string]any
}

type VideoTaskSettlementSummary struct {
	SubscriptionID  *int64
	UsageLogID      *int64
	ClearUsageLogID bool
	UsageMetadata   map[string]any
	BilledUSD       *float64
}

type VideoTaskUpstreamFallback struct {
	Snapshot VideoTaskAcceptedSnapshot
}

type VideoTaskAcceptedSnapshot struct {
	ProviderTaskID  string          `json:"provider_task_id"`
	Status          VideoTaskStatus `json:"status"`
	ProviderStatus  string          `json:"provider_status"`
	ResponseBody    string          `json:"response_body"`
	Metadata        map[string]any  `json:"metadata,omitempty"`
	SubmittedAt     *time.Time      `json:"submitted_at"`
	ExpiresAt       *time.Time      `json:"expires_at,omitempty"`
	NextPollAt      *time.Time      `json:"next_poll_at,omitempty"`
	ClearNextPollAt bool            `json:"clear_next_poll_at,omitempty"`
}

func VideoTaskAcceptedSnapshotFromSubmit(update VideoTaskSubmitUpdate) VideoTaskAcceptedSnapshot {
	return VideoTaskAcceptedSnapshot{ProviderTaskID: strings.TrimSpace(update.ProviderTaskID), Status: update.Status, ProviderStatus: update.ProviderStatus, ResponseBody: string(update.ResponseBody), Metadata: copyVideoTaskMetadata(update.Metadata), SubmittedAt: update.SubmittedAt, ExpiresAt: update.ExpiresAt, NextPollAt: update.NextPollAt, ClearNextPollAt: update.ClearNextPollAt}
}

func (s VideoTaskAcceptedSnapshot) SubmitUpdate() (VideoTaskSubmitUpdate, error) {
	if strings.TrimSpace(s.ProviderTaskID) == "" || s.SubmittedAt == nil || s.Status == "" || (!s.Status.Terminal() && s.NextPollAt == nil) {
		return VideoTaskSubmitUpdate{}, errors.New("invalid accepted video task fallback snapshot")
	}
	return VideoTaskSubmitUpdate{ProviderTaskID: strings.TrimSpace(s.ProviderTaskID), Status: s.Status, ProviderStatus: s.ProviderStatus, ResponseBody: []byte(s.ResponseBody), Metadata: copyVideoTaskMetadata(s.Metadata), SubmittedAt: s.SubmittedAt, ExpiresAt: s.ExpiresAt, NextPollAt: s.NextPollAt, ClearNextPollAt: s.ClearNextPollAt}, nil
}

type VideoTaskSubmitUpdate struct {
	ProviderTaskID  string
	Status          VideoTaskStatus
	ProviderStatus  string
	ResponseBody    []byte
	Metadata        map[string]any
	ErrorMessage    string
	SubmittedAt     *time.Time
	ExpiresAt       *time.Time
	NextPollAt      *time.Time
	ClearNextPollAt bool
}

type VideoTaskProviderUpdate struct {
	Status          VideoTaskStatus
	ProviderStatus  string
	ResponseBody    []byte
	Metadata        map[string]any
	ErrorMessage    string
	CompletedAt     *time.Time
	ExpiresAt       *time.Time
	NextPollAt      *time.Time
	ClearNextPollAt bool
}

type VideoTaskRepository interface {
	Create(ctx context.Context, input VideoTaskCreateInput) (*VideoTask, error)
	UpdateSettlementSummary(ctx context.Context, publicTaskID string, summary VideoTaskSettlementSummary) error
	PersistUpstreamFallback(ctx context.Context, publicTaskID string, fallback VideoTaskUpstreamFallback) error
	AttachUpstream(ctx context.Context, publicTaskID string, update VideoTaskSubmitUpdate) error
	GetByPublicTaskID(ctx context.Context, publicTaskID string) (*VideoTask, error)
	GetByPublicTaskIDForUser(ctx context.Context, publicTaskID string, userID int64) (*VideoTask, error)
	GetByProviderTaskID(ctx context.Context, provider, providerTaskID string) (*VideoTask, error)
	GetByIdempotencyKey(ctx context.Context, apiKeyID int64, idempotencyKey string) (*VideoTask, error)
	ListForUser(ctx context.Context, params VideoTaskListParams) ([]*VideoTask, bool, error)
	MarkUserDeleted(ctx context.Context, publicTaskID string, userID int64, deletedAt time.Time) error
	UpdateSubmit(ctx context.Context, publicTaskID string, update VideoTaskSubmitUpdate) error
	UpdateFromProvider(ctx context.Context, publicTaskID string, update VideoTaskProviderUpdate) (applied bool, err error)
	UpdateFromProviderWithPollLease(ctx context.Context, publicTaskID, leaseToken string, validAt time.Time, update VideoTaskProviderUpdate) (applied bool, err error)
	UpdateProvider(ctx context.Context, publicTaskID string, update VideoTaskProviderUpdate) error
	ClaimDuePollTasks(ctx context.Context, now time.Time, limit int, lockOwner string, lockTTL time.Duration) ([]*VideoTask, error)
	RenewPollLock(ctx context.Context, publicTaskID, leaseToken string, validAt time.Time, lockTTL time.Duration) (renewed bool, err error)
	ReleasePollLock(ctx context.Context, publicTaskID, leaseToken string) (released bool, err error)
}

type VideoProviderCreateResult struct {
	ProviderTaskID string
	Status         VideoTaskStatus
	ProviderStatus string
	RawBody        []byte
	Metadata       map[string]any
	ExpiresAt      *time.Time
}

type VideoProviderFetchResult struct {
	ProviderTaskID string
	Status         VideoTaskStatus
	ProviderStatus string
	RawBody        []byte
	Metadata       map[string]any
	ErrorMessage   string
	CompletedAt    *time.Time
	ExpiresAt      *time.Time
}

type VideoContentStream struct {
	Body          io.ReadCloser
	StatusCode    int
	ContentType   string
	ContentLength int64
	Filename      string
	Headers       map[string]string
}

type VideoTaskProvider interface {
	Create(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) (*VideoProviderCreateResult, error)
	Fetch(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error)
	Content(ctx context.Context, account *Account, task *VideoTask, headers http.Header) (*VideoContentStream, error)
}

type VideoTaskCreateValidator interface {
	ValidateCreate(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) error
}
