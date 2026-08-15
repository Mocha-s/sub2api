package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type newAPIVideoGenerationsAdapter struct {
	provider *openAICompatibleVideoProvider
}

func NewNewAPIVideoGenerationsAdapter(openai *OpenAIGatewayService) VideoTaskAdapter {
	return &newAPIVideoGenerationsAdapter{provider: &openAICompatibleVideoProvider{client: http.DefaultClient, openai: openai}}
}

func (a *newAPIVideoGenerationsAdapter) Name() string { return VideoAdapterNewAPIVideoGenerations }

func (a *newAPIVideoGenerationsAdapter) ValidateCreate(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) error {
	if err := validateVideoCompatCreateBody(body, upstreamModel); err != nil {
		return err
	}
	if _, err := seedanceCreateBody(body, upstreamModel); err != nil {
		return err
	}
	if err := validateUnifiedVideoGenerationFields(ctx, body,
		"resolution", "ratio", "aspect_ratio", "duration", "seconds", "duration_seconds", "generate_audio",
		"return_last_frame", "web_search", "content", "image", "images", "images_base64", "imagesBase64", "image_url", "image_urls", "imageUrls", "image_reference", "reference_image", "referenceImage", "reference_images", "referenceImages", "reference_image_url", "referenceImageUrl", "reference_image_urls", "referenceImageUrls", "first_frame_image", "input_reference", "inputReference",
		"videos", "video", "video_url", "videoUrl", "video_urls", "videoUrls", "reference_video", "referenceVideo", "reference_videos", "referenceVideos", "reference_video_url", "referenceVideoUrl", "reference_video_urls", "referenceVideoUrls", "input_video", "inputVideo",
		"audios", "audio", "audios_base64", "audiosBase64", "audio_url", "audioUrl", "audio_urls", "audioUrls", "audio_reference", "audioReference", "audio_references", "audioReferences", "reference_audio", "referenceAudio", "reference_audios", "referenceAudios", "reference_audio_url", "referenceAudioUrl", "reference_audio_urls", "referenceAudioUrls",
		"start_frame", "end_frame", "style_references", "styleReferences", "element_references", "elementReferences", "reference_mode", "quality", "size",
	); err != nil {
		return err
	}
	provider, err := a.openAIProvider()
	if err != nil {
		return err
	}
	_, err = provider.openAIVideoEndpoint(account, "/v1/video/generations")
	return err
}

func (a *newAPIVideoGenerationsAdapter) Create(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) (*VideoProviderCreateResult, error) {
	if err := a.ValidateCreate(ctx, account, body, contentType, upstreamModel); err != nil {
		return nil, err
	}
	provider, err := a.openAIProvider()
	if err != nil {
		return nil, err
	}
	token, err := provider.openAIVideoToken(ctx, account)
	if err != nil {
		return nil, err
	}
	forwardBody, err := seedanceCreateBody(body, upstreamModel)
	if err != nil {
		return nil, err
	}
	endpoint, err := provider.openAIVideoEndpoint(account, "/v1/video/generations")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(forwardBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if requestID := videoTaskRequestIDFromContext(ctx); requestID != "" {
		req.Header.Set("X-Request-ID", requestID)
	}

	resp, err := provider.do(req, account)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, openAIVideoStatusError(resp, token)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return parseNewAPIVideoGenerationsResult(raw, resp.Header.Get("X-Request-Id"))
}

func (a *newAPIVideoGenerationsAdapter) Fetch(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error) {
	provider, err := a.openAIProvider()
	if err != nil {
		return nil, err
	}
	token, err := provider.openAIVideoToken(ctx, account)
	if err != nil {
		return nil, err
	}
	providerTaskID, err := openAIVideoProviderTaskID(task)
	if err != nil {
		return nil, err
	}
	endpoint, err := provider.openAIVideoEndpoint(account, "/v1/video/generations/"+url.PathEscape(providerTaskID))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := provider.do(req, account)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, openAIVideoStatusError(resp, token)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	result, err := parseNewAPIVideoGenerationsResultWithFallback(raw, resp.Header.Get("X-Request-Id"), providerTaskID)
	if err != nil {
		return nil, err
	}
	if result.ProviderTaskID == "" {
		result.ProviderTaskID = providerTaskID
	}
	return &VideoProviderFetchResult{
		ProviderTaskID: result.ProviderTaskID,
		Status:         result.Status,
		ProviderStatus: result.ProviderStatus,
		RawBody:        result.RawBody,
		Metadata:       result.Metadata,
		ExpiresAt:      result.ExpiresAt,
	}, nil
}

func (a *newAPIVideoGenerationsAdapter) Refresh(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error) {
	return a.Fetch(ctx, account, task)
}

func (a *newAPIVideoGenerationsAdapter) Estimate(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) (*VideoTaskEstimateResult, error) {
	req, err := ParseVideoTaskCreateEnvelope(body)
	if err != nil {
		return nil, err
	}
	forwardBody, err := seedanceCreateBody(body, upstreamModel)
	if err != nil {
		return nil, err
	}
	return localVideoEstimateResult(a.Name(), videoTaskEndpointFromContext(ctx), req.Model, forwardBody, upstreamModel)
}

func (a *newAPIVideoGenerationsAdapter) Content(ctx context.Context, account *Account, task *VideoTask, headers http.Header) (*VideoContentStream, error) {
	if task == nil {
		return nil, errors.New("newapi video task is required")
	}
	resultURL := videoTaskMetadataString(task.Metadata, "result_url")
	if resultURL == "" {
		return nil, errors.New("newapi video task missing result_url")
	}
	provider, err := a.openAIProvider()
	if err != nil {
		return nil, err
	}
	resultURL, err = validateVideoResultURL(provider.openai, resultURL)
	if err != nil {
		return nil, err
	}
	ctx = withVideoResultRedirectValidator(ctx, provider.openai)
	return fetchVideoResultURLWithDo(ctx, resultURL, headers, func(req *http.Request) (*http.Response, error) {
		return provider.do(req, account)
	})
}

func (a *newAPIVideoGenerationsAdapter) openAIProvider() (*openAICompatibleVideoProvider, error) {
	if a == nil || a.provider == nil {
		return nil, errors.New("video adapter provider is required")
	}
	return a.provider, nil
}

func parseNewAPIVideoGenerationsResult(raw []byte, requestID string) (*VideoProviderCreateResult, error) {
	return parseNewAPIVideoGenerationsResultWithFallback(raw, requestID, "")
}

func parseNewAPIVideoGenerationsResultWithFallback(raw []byte, requestID string, fallbackTaskID string) (*VideoProviderCreateResult, error) {
	parsed, err := parseOpenAIVideoResponse(raw, requestID)
	if err != nil {
		return nil, err
	}
	if parsed.providerTaskID == "" {
		parsed.providerTaskID = strings.TrimSpace(fallbackTaskID)
	}
	if parsed.providerTaskID == "" {
		return nil, errors.New("newapi video response missing id or task_id")
	}
	metadata := stampVideoAdapterMetadata(parsed.metadata, VideoAdapterNewAPIVideoGenerations)
	if videoTaskMetadataString(metadata, "result_url") == "" {
		if resultURL := openAIDurationResultURL(raw); resultURL != "" {
			metadata["result_url"] = resultURL
		}
	}
	return &VideoProviderCreateResult{
		ProviderTaskID: parsed.providerTaskID,
		Status:         parsed.status,
		ProviderStatus: parsed.providerStatus,
		RawBody:        raw,
		Metadata:       metadata,
	}, nil
}
