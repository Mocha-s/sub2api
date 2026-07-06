package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	VideoTaskProviderOpenAICompatible = "openai_compatible"
	VideoTaskPlatformOpenAIVideo      = "openai_video"
	VideoGenerationPermissionMessage  = "Video generation is not enabled for this group"
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
	case "queued", "pending", "submitted":
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
	Seconds     int
	AspectRatio string
	Images      []json.RawMessage
	Videos      []json.RawMessage
	Audios      []json.RawMessage
	Metadata    map[string]any
	RequestHash string
	PromptHash  string
	RawBody     []byte
}

type openAIVideoCreatePayload struct {
	Model       string            `json:"model"`
	Prompt      string            `json:"prompt"`
	Seconds     int               `json:"seconds"`
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

	model := strings.TrimSpace(payload.Model)
	if model == "" {
		return nil, errors.New("model is required")
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
	if _, ok := payload["id"]; ok {
		payload["id"] = rewrittenID
	}
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
	Model          string
	Prompt         string
	Status         VideoTaskStatus
	ProviderStatus string
	RequestHash    string
	PromptHash     string
	RequestBody    []byte
	ResponseBody   []byte
	Metadata       map[string]any
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
}

type VideoTaskCreateInput struct {
	PublicTaskID string
	Provider     string
	Platform     string
	UserID       int64
	APIKeyID     int64
	GroupID      int64
	AccountID    int64
	ChannelID    int64
	Model        string
	Prompt       string
	RequestHash  string
	PromptHash   string
	RequestBody  []byte
	Metadata     map[string]any
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
	AttachUpstream(ctx context.Context, publicTaskID string, update VideoTaskSubmitUpdate) error
	GetByPublicTaskID(ctx context.Context, publicTaskID string) (*VideoTask, error)
	GetByPublicTaskIDForUser(ctx context.Context, publicTaskID string, userID int64) (*VideoTask, error)
	GetByProviderTaskID(ctx context.Context, provider, providerTaskID string) (*VideoTask, error)
	GetByIdempotencyKey(ctx context.Context, apiKeyID int64, idempotencyKey string) (*VideoTask, error)
	UpdateSubmit(ctx context.Context, publicTaskID string, update VideoTaskSubmitUpdate) error
	UpdateFromProvider(ctx context.Context, publicTaskID string, update VideoTaskProviderUpdate) error
	UpdateProvider(ctx context.Context, publicTaskID string, update VideoTaskProviderUpdate) error
	ClaimDuePollTasks(ctx context.Context, now time.Time, limit int, lockOwner string, lockTTL time.Duration) ([]*VideoTask, error)
	ReleasePollLock(ctx context.Context, publicTaskID string, lockOwner string) error
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
