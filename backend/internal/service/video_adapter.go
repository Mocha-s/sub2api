package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

const (
	VideoAdapterMetadataKey            = "video_adapter"
	VideoAdapterJimengOpenAIVideos     = "jimeng_openai_videos"
	VideoAdapterSeedanceAPIV1          = "seedance_api_v1"
	VideoAdapterOpenAIVideosDuration   = "openai_videos_duration"
	VideoAdapterNewAPIVideoGenerations = "newapi_video_generations"
	VideoAdapterAutoVideoGenerations   = "auto_video_generations"
)

type videoTaskEndpointContextKey struct{}
type videoTaskRequestIDContextKey struct{}
type videoTaskContentMethodContextKey struct{}

func withVideoTaskEndpoint(ctx context.Context, endpoint string) context.Context {
	if endpoint == "" {
		endpoint = VideoTaskEndpointVideos
	}
	return context.WithValue(ctx, videoTaskEndpointContextKey{}, endpoint)
}

func videoTaskEndpointFromContext(ctx context.Context) string {
	if ctx == nil {
		return VideoTaskEndpointVideos
	}
	if value, ok := ctx.Value(videoTaskEndpointContextKey{}).(string); ok && value != "" {
		return value
	}
	return VideoTaskEndpointVideos
}

func withVideoTaskRequestID(ctx context.Context, requestID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return ctx
	}
	return context.WithValue(ctx, videoTaskRequestIDContextKey{}, requestID)
}

func videoTaskRequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	requestID, _ := ctx.Value(videoTaskRequestIDContextKey{}).(string)
	return strings.TrimSpace(requestID)
}

func withVideoTaskContentMethod(ctx context.Context, method string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.EqualFold(method, http.MethodHead) {
		return context.WithValue(ctx, videoTaskContentMethodContextKey{}, http.MethodHead)
	}
	return context.WithValue(ctx, videoTaskContentMethodContextKey{}, http.MethodGet)
}

func videoTaskContentMethodFromContext(ctx context.Context) string {
	if ctx != nil {
		if method, ok := ctx.Value(videoTaskContentMethodContextKey{}).(string); ok && strings.EqualFold(method, http.MethodHead) {
			return http.MethodHead
		}
	}
	return http.MethodGet
}

type VideoTaskAdapter interface {
	Name() string
	Create(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) (*VideoProviderCreateResult, error)
	Fetch(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error)
	Content(ctx context.Context, account *Account, task *VideoTask, headers http.Header) (*VideoContentStream, error)
}

type VideoTaskRefresher interface {
	Refresh(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error)
}

type VideoTaskCanceller interface {
	Cancel(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error)
}

type VideoTaskEstimator interface {
	Estimate(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) (*VideoTaskEstimateResult, error)
}

type VideoReferenceProvider interface {
	References(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) (*VideoTaskAssetResult, error)
}

type VideoMaterialAssetProvider interface {
	MaterialAssets(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) (*VideoTaskAssetResult, error)
}

type videoAdapterRegistry map[string]VideoTaskAdapter

func newDefaultVideoAdapterRegistry(openai *OpenAIGatewayService) videoAdapterRegistry {
	jimeng := NewJimengOpenAIVideoAdapter(openai)
	return videoAdapterRegistry{
		jimeng.Name():                      jimeng,
		VideoAdapterSeedanceAPIV1:          NewSeedanceAPIV1VideoAdapter(openai),
		VideoAdapterOpenAIVideosDuration:   NewOpenAIVideosDurationAdapter(openai),
		VideoAdapterNewAPIVideoGenerations: NewNewAPIVideoGenerationsAdapter(openai),
		VideoAdapterAutoVideoGenerations:   NewAutoVideoGenerationsAdapter(openai),
	}
}

func (r videoAdapterRegistry) get(name string) (VideoTaskAdapter, error) {
	adapter := r[name]
	if adapter == nil {
		return nil, fmt.Errorf("unknown video_adapter %q", name)
	}
	return adapter, nil
}

func resolveVideoAdapterName(account *Account, task *VideoTask) (string, error) {
	if task != nil {
		if value := videoTaskMetadataString(task.Metadata, VideoAdapterMetadataKey); value != "" {
			return value, nil
		}
	}
	name := ""
	if account != nil {
		name = strings.TrimSpace(account.GetCredential(VideoAdapterMetadataKey))
	}
	if name == "" {
		return VideoAdapterJimengOpenAIVideos, nil
	}
	switch name {
	case VideoAdapterJimengOpenAIVideos, VideoAdapterSeedanceAPIV1, VideoAdapterOpenAIVideosDuration, VideoAdapterNewAPIVideoGenerations, VideoAdapterAutoVideoGenerations:
		return name, nil
	default:
		return "", fmt.Errorf("unknown video_adapter %q", name)
	}
}

type accountVideoTaskProvider struct {
	registry videoAdapterRegistry
}

func NewAccountVideoTaskProvider(openai *OpenAIGatewayService) VideoTaskProvider {
	return &accountVideoTaskProvider{registry: newDefaultVideoAdapterRegistry(openai)}
}

func (p *accountVideoTaskProvider) adapter(account *Account, task *VideoTask) (VideoTaskAdapter, error) {
	name, err := resolveVideoAdapterName(account, task)
	if err != nil {
		return nil, err
	}
	return p.registry.get(name)
}

func (p *accountVideoTaskProvider) Create(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) (*VideoProviderCreateResult, error) {
	adapter, err := p.adapter(account, nil)
	if err != nil {
		return nil, err
	}
	return adapter.Create(ctx, account, body, contentType, upstreamModel)
}

func (p *accountVideoTaskProvider) ValidateCreate(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) error {
	adapter, err := p.adapter(account, nil)
	if err != nil {
		return err
	}
	validator, ok := adapter.(VideoTaskCreateValidator)
	if !ok {
		return nil
	}
	return validator.ValidateCreate(ctx, account, body, contentType, upstreamModel)
}

func (p *accountVideoTaskProvider) Fetch(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error) {
	adapter, err := p.adapter(account, task)
	if err != nil {
		return nil, err
	}
	return adapter.Fetch(ctx, account, task)
}

func (p *accountVideoTaskProvider) Content(ctx context.Context, account *Account, task *VideoTask, headers http.Header) (*VideoContentStream, error) {
	adapter, err := p.adapter(account, task)
	if err != nil {
		return nil, err
	}
	return adapter.Content(ctx, account, task, headers)
}

func unsupportedVideoTaskAction(action string) error {
	message := strings.TrimSpace(action)
	if message == "" {
		message = "video task action"
	}
	return ErrVideoTaskActionUnsupported.WithMetadata(map[string]string{"action": message})
}

func (p *accountVideoTaskProvider) Refresh(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error) {
	adapter, err := p.adapter(account, task)
	if err != nil {
		return nil, err
	}
	refresher, ok := adapter.(VideoTaskRefresher)
	if !ok {
		return nil, unsupportedVideoTaskAction("refresh")
	}
	return refresher.Refresh(ctx, account, task)
}

func (p *accountVideoTaskProvider) Cancel(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error) {
	adapter, err := p.adapter(account, task)
	if err != nil {
		return nil, err
	}
	canceller, ok := adapter.(VideoTaskCanceller)
	if !ok {
		return nil, unsupportedVideoTaskAction("cancel")
	}
	return canceller.Cancel(ctx, account, task)
}

func (p *accountVideoTaskProvider) Estimate(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) (*VideoTaskEstimateResult, error) {
	adapter, err := p.adapter(account, nil)
	if err != nil {
		return nil, err
	}
	estimator, ok := adapter.(VideoTaskEstimator)
	if !ok {
		return nil, unsupportedVideoTaskAction("estimate")
	}
	if validator, ok := adapter.(VideoTaskCreateValidator); ok {
		if err := validator.ValidateCreate(ctx, account, body, contentType, upstreamModel); err != nil {
			return nil, err
		}
	}
	return estimator.Estimate(ctx, account, body, contentType, upstreamModel)
}

func (p *accountVideoTaskProvider) References(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) (*VideoTaskAssetResult, error) {
	adapter, err := p.adapter(account, nil)
	if err != nil {
		return nil, err
	}
	provider, ok := adapter.(VideoReferenceProvider)
	if !ok {
		return nil, unsupportedVideoTaskAction("references")
	}
	return provider.References(ctx, account, body, contentType, upstreamModel)
}

func (p *accountVideoTaskProvider) MaterialAssets(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) (*VideoTaskAssetResult, error) {
	adapter, err := p.adapter(account, nil)
	if err != nil {
		return nil, err
	}
	provider, ok := adapter.(VideoMaterialAssetProvider)
	if !ok {
		return nil, unsupportedVideoTaskAction("material-assets")
	}
	return provider.MaterialAssets(ctx, account, body, contentType, upstreamModel)
}
