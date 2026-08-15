package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
)

const (
	openAIVideoDefaultBaseURL   = "https://api.openai.com"
	openAIVideoMaxErrorBodySize = 4096
)

type openAICompatibleVideoProvider struct {
	client *http.Client
	openai *OpenAIGatewayService
}

func NewOpenAICompatibleVideoProvider(client *http.Client) VideoTaskProvider {
	if client == nil {
		client = http.DefaultClient
	}
	return &openAICompatibleVideoProvider{client: client}

}

func NewOpenAICompatibleVideoProviderForGateway(openai *OpenAIGatewayService) VideoTaskProvider {
	return &openAICompatibleVideoProvider{client: http.DefaultClient, openai: openai}
}

func (s *OpenAIGatewayService) ResolveVideoTaskPricing(ctx context.Context, input VideoTaskPricingResolveInput) VideoTaskPricingSelection {
	return s.resolveVideoTaskPricingAt(ctx, input, timezone.Now())
}

func (s *OpenAIGatewayService) resolveVideoTaskPricingAt(ctx context.Context, input VideoTaskPricingResolveInput, now time.Time) VideoTaskPricingSelection {
	selection := VideoTaskPricingSelection{
		BillingModel:          strings.TrimSpace(input.RequestedModel),
		BillingModelSource:    BillingModelSourceRequested,
		RateMultiplier:        1,
		AccountRateMultiplier: input.Account.BillingRateMultiplier(),
	}
	if s == nil || s.channelService == nil || input.GroupID <= 0 {
		return selection
	}

	mapping := s.channelService.ResolveChannelMapping(ctx, input.GroupID, input.RequestedModel)
	selection.ChannelID = mapping.ChannelID
	selection.BillingModelSource = mapping.BillingModelSource
	switch mapping.BillingModelSource {
	case BillingModelSourceUpstream:
		selection.BillingModel = strings.TrimSpace(input.UpstreamModel)
	case BillingModelSourceChannelMapped:
		selection.BillingModel = strings.TrimSpace(mapping.MappedModel)
	default:
		selection.BillingModel = strings.TrimSpace(input.RequestedModel)
	}
	if selection.BillingModel == "" {
		selection.BillingModel = strings.TrimSpace(input.RequestedModel)
	}
	selection.Pricing = s.channelService.GetChannelModelPricing(ctx, input.GroupID, selection.BillingModel)
	if channel, err := s.channelService.GetChannelForGroup(ctx, input.GroupID); err == nil && channel != nil {
		for i := range channel.AccountStatsPricingRules {
			rule := &channel.AccountStatsPricingRules[i]
			if !matchAccountStatsRule(rule, input.Account.ID, input.GroupID) {
				continue
			}
			if pricing := findPricingForModel(rule.Pricing, s.channelService.GetGroupPlatform(ctx, input.GroupID), strings.ToLower(selection.BillingModel)); pricing != nil {
				selection.AccountStatsPricing = pricing
				break
			}
		}
	}
	if selection.Pricing == nil || (selection.Pricing.BillingMode != BillingModeVideo && selection.Pricing.BillingMode != BillingModePerRequest) {
		selection.Pricing = nil
		return selection
	}

	groupMultiplier := 1.0
	if s.cfg != nil {
		groupMultiplier = s.cfg.Default.RateMultiplier
	}
	if input.APIKey != nil && input.APIKey.Group != nil {
		resolver := s.userGroupRateResolver
		if resolver == nil {
			resolver = newUserGroupRateResolver(nil, nil, resolveUserGroupRateCacheTTL(s.cfg), nil, "service.openai_gateway")
		}
		groupMultiplier = resolver.Resolve(ctx, input.UserID, input.GroupID, input.APIKey.Group.RateMultiplier)
	}
	baseMultiplier := effectiveRequestRateMultiplier(input.Account, groupMultiplier)
	selection.RateMultiplier = baseMultiplier
	if selection.Pricing.BillingMode == BillingModePerRequest {
		selection.RateMultiplier, _ = computePeakAwareMultipliers(input.APIKey, baseMultiplier, now)
	}
	if selection.Pricing.BillingMode == BillingModeVideo {
		selection.RateMultiplier = resolveVideoRateMultiplier(input.APIKey, baseMultiplier)
	}
	return selection
}

type OpenAIVideoUpstreamError struct {
	StatusCode int
	Body       string
}

func (e *OpenAIVideoUpstreamError) Error() string {
	if e == nil {
		return "openai video upstream error"
	}
	if strings.TrimSpace(e.Body) == "" {
		return fmt.Sprintf("openai video upstream returned status %d", e.StatusCode)
	}
	return fmt.Sprintf("openai video upstream returned status %d: %s", e.StatusCode, e.Body)
}

func (p *openAICompatibleVideoProvider) openAIVideoToken(ctx context.Context, account *Account) (string, error) {
	if account == nil {
		return "", errors.New("openai video account is required")
	}
	if p != nil && p.openai != nil {
		token, _, err := p.openai.GetAccessToken(ctx, account)
		if err != nil {
			return "", err
		}
		return token, nil
	}
	return openAIVideoToken(account)
}

func (p *openAICompatibleVideoProvider) do(req *http.Request, account *Account) (*http.Response, error) {
	if p != nil && p.openai != nil && p.openai.httpUpstream != nil && account != nil {
		proxyURL := ""
		if account.ProxyID != nil && account.Proxy != nil {
			proxyURL = account.Proxy.URL()
		}
		req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
		return p.openai.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, nil)
	}
	client := http.DefaultClient
	if p != nil && p.client != nil {
		client = p.client
	}
	return client.Do(req)
}

func (p *openAICompatibleVideoProvider) Create(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) (*VideoProviderCreateResult, error) {
	token, err := p.openAIVideoToken(ctx, account)
	if err != nil {
		return nil, err
	}
	forwardBody, err := copyValidOpenAIVideoCreateJSON(body, upstreamModel)
	if err != nil {
		return nil, err
	}
	endpoint, err := p.openAIVideoEndpoint(account, "/v1/videos")
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

	resp, err := p.do(req, account)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, openAIVideoStatusError(resp, token)
	}

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	parsed, err := parseOpenAIVideoResponse(rawBody, resp.Header.Get("X-Request-Id"))
	if err != nil {
		return nil, err
	}
	if parsed.providerTaskID == "" {
		return nil, errors.New("openai video create response missing id or task_id")
	}

	return &VideoProviderCreateResult{
		ProviderTaskID: parsed.providerTaskID,
		Status:         parsed.status,
		ProviderStatus: parsed.providerStatus,
		RawBody:        rawBody,
		Metadata:       parsed.metadata,
	}, nil
}

func (p *openAICompatibleVideoProvider) Fetch(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error) {
	token, err := p.openAIVideoToken(ctx, account)
	if err != nil {
		return nil, err
	}
	providerTaskID, err := openAIVideoProviderTaskID(task)
	if err != nil {
		return nil, err
	}

	endpoint, err := p.openAIVideoEndpoint(account, "/v1/videos/"+url.PathEscape(providerTaskID))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := p.do(req, account)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, openAIVideoStatusError(resp, token)
	}

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	parsed, err := parseOpenAIVideoResponse(rawBody, resp.Header.Get("X-Request-Id"))
	if err != nil {
		return nil, err
	}
	if parsed.providerTaskID == "" {
		parsed.providerTaskID = providerTaskID
	}

	return &VideoProviderFetchResult{
		ProviderTaskID: parsed.providerTaskID,
		Status:         parsed.status,
		ProviderStatus: parsed.providerStatus,
		RawBody:        rawBody,
		Metadata:       parsed.metadata,
	}, nil
}

func (p *openAICompatibleVideoProvider) Content(ctx context.Context, account *Account, task *VideoTask, headers http.Header) (*VideoContentStream, error) {
	token, err := p.openAIVideoToken(ctx, account)
	if err != nil {
		return nil, err
	}
	providerTaskID, err := openAIVideoProviderTaskID(task)
	if err != nil {
		return nil, err
	}

	endpoint, err := p.openAIVideoEndpoint(account, "/v1/videos/"+url.PathEscape(providerTaskID)+"/content")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, videoTaskContentMethodFromContext(ctx), endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for _, value := range headers.Values("Range") {
		req.Header.Add("Range", value)
	}

	resp, err := p.do(req, account)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		defer func() { _ = resp.Body.Close() }()
		return nil, openAIVideoStatusError(resp, token)
	}

	return &VideoContentStream{
		Body:          resp.Body,
		StatusCode:    resp.StatusCode,
		ContentType:   resp.Header.Get("Content-Type"),
		ContentLength: resp.ContentLength,
		Headers:       openAIVideoHeaders(resp.Header),
	}, nil
}

type parsedOpenAIVideoResponse struct {
	providerTaskID string
	status         VideoTaskStatus
	providerStatus string
	metadata       map[string]any
}

func parseOpenAIVideoResponse(rawBody []byte, requestID string) (*parsedOpenAIVideoResponse, error) {
	var response map[string]any
	if err := json.Unmarshal(rawBody, &response); err != nil {
		return nil, err
	}

	providerStatus := openAIVideoString(response, "status")
	metadata := map[string]any{
		"response": sanitizeOpenAIVideoMap(response),
	}
	if progress, ok := openAIVideoNumber(response["progress"]); ok {
		metadata["progress"] = progress
	}
	if requestID = strings.TrimSpace(requestID); requestID != "" {
		metadata["request_id"] = requestID
	}
	if resultURL := openAIVideoResultURL(response); resultURL != "" {
		metadata["result_url"] = resultURL
	}

	return &parsedOpenAIVideoResponse{
		providerTaskID: openAIVideoFirstString(response, "id", "task_id"),
		status:         NormalizeOpenAIVideoStatus(providerStatus),
		providerStatus: providerStatus,
		metadata:       metadata,
	}, nil
}

func (p *openAICompatibleVideoProvider) openAIVideoEndpoint(account *Account, path string) (string, error) {
	baseURL := openAIVideoBaseURL(account)
	if p != nil && p.openai != nil && p.openai.cfg != nil {
		validatedURL, err := p.openai.validateUpstreamBaseURL(baseURL)
		if err != nil {
			return "", err
		}
		baseURL = validatedURL
	}
	return buildOpenAIVideoEndpoint(baseURL, path), nil
}

func openAIVideoBaseURL(account *Account) string {
	baseURL := openAIVideoDefaultBaseURL
	if account != nil {
		if value := strings.TrimSpace(account.GetCredential("base_url")); value != "" {
			baseURL = value
		}
	}
	return baseURL
}

func buildOpenAIVideoEndpoint(baseURL string, path string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	baseURL = strings.TrimSuffix(baseURL, "/v1")
	return baseURL + path
}

func validateVideoResultURL(openai *OpenAIGatewayService, raw string) (string, error) {
	if openai != nil && openai.cfg != nil {
		return openai.validateUpstreamBaseURL(raw)
	}
	return urlvalidator.ValidateURLFormat(raw, true)
}

func withVideoResultRedirectValidator(ctx context.Context, openai *OpenAIGatewayService) context.Context {
	if openai == nil || openai.cfg == nil || !openai.cfg.Security.URLAllowlist.Enabled {
		return ctx
	}
	return WithHTTPRedirectValidator(ctx, func(target *url.URL) error {
		if target == nil {
			return errors.New("video result redirect URL is nil")
		}
		_, err := validateVideoResultURL(openai, target.String())
		return err
	})
}

func copyValidOpenAIVideoCreateJSON(body []byte, upstreamModel string) ([]byte, error) {
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, fmt.Errorf("invalid video create JSON: %w", err)
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, errors.New("video create JSON body must be an object")
	}
	upstreamModel = strings.TrimSpace(upstreamModel)
	if upstreamModel == "" {
		return append([]byte(nil), body...), nil
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("invalid video create JSON: %w", err)
	}
	encodedModel, err := json.Marshal(upstreamModel)
	if err != nil {
		return nil, err
	}
	payload["model"] = encodedModel
	forwardBody, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return forwardBody, nil
}

func openAIVideoToken(account *Account) (string, error) {
	if account == nil {
		return "", errors.New("openai video account is required")
	}
	if apiKey := strings.TrimSpace(account.GetCredential("api_key")); apiKey != "" {
		return apiKey, nil
	}
	if accessToken := strings.TrimSpace(account.GetCredential("access_token")); accessToken != "" {
		return accessToken, nil
	}
	return "", errors.New("openai video account missing api_key or access_token")
}

func openAIVideoProviderTaskID(task *VideoTask) (string, error) {
	if task == nil {
		return "", errors.New("openai video task is required")
	}
	providerTaskID := strings.TrimSpace(task.ProviderTaskID)
	if providerTaskID == "" {
		return "", errors.New("openai video task missing upstream task id")
	}
	return providerTaskID, nil
}

func openAIVideoStatusError(resp *http.Response, token string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, openAIVideoMaxErrorBodySize))
	bodyText := sanitizeOpenAIVideoErrorBody(body, token)
	return &OpenAIVideoUpstreamError{StatusCode: resp.StatusCode, Body: bodyText}
}

func sanitizeOpenAIVideoErrorBody(body []byte, token string) string {
	var parsed any
	if err := json.Unmarshal(body, &parsed); err == nil {
		if encoded, err := json.Marshal(sanitizeOpenAIVideoValue(parsed)); err == nil {
			return redactOpenAIVideoToken(string(encoded), token)
		}
	}
	return redactOpenAIVideoToken(strings.TrimSpace(string(body)), token)
}

func redactOpenAIVideoToken(value string, token string) string {
	if token = strings.TrimSpace(token); token == "" {
		return value
	}
	return strings.ReplaceAll(value, token, "[redacted]")
}

func openAIVideoFirstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := openAIVideoString(values, key); value != "" {
			return value
		}
	}
	return ""
}

func openAIVideoString(values map[string]any, key string) string {
	value, ok := values[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func openAIVideoNumber(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	default:
		return 0, false
	}
}

func openAIVideoResultURL(response map[string]any) string {
	metadata, ok := response["metadata"].(map[string]any)
	if !ok {
		return ""
	}
	value, ok := metadata["url"].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func openAIVideoHeaders(headers http.Header) map[string]string {
	result := make(map[string]string, len(headers))
	for key, values := range headers {
		if len(values) == 0 {
			continue
		}
		result[key] = strings.Join(values, ", ")
	}
	return result
}

func sanitizeOpenAIVideoMap(values map[string]any) map[string]any {
	result := make(map[string]any, len(values))
	for key, value := range values {
		if isOpenAIVideoSensitiveKey(key) {
			continue
		}
		result[key] = sanitizeOpenAIVideoValue(value)
	}
	return result
}

func sanitizeOpenAIVideoValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return sanitizeOpenAIVideoMap(v)
	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = sanitizeOpenAIVideoValue(item)
		}
		return result
	default:
		return value
	}
}

func isOpenAIVideoSensitiveKey(key string) bool {
	switch strings.ToLower(strings.ReplaceAll(key, "-", "_")) {
	case "api_key", "access_token", "refresh_token", "id_token", "authorization", "token", "client_secret", "secret", "password":
		return true
	default:
		return false
	}
}

func stampVideoAdapterMetadata(metadata map[string]any, name string) map[string]any {
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata[VideoAdapterMetadataKey] = name
	return metadata
}

type jimengOpenAIVideosAdapter struct {
	provider VideoTaskProvider
}

func (a *jimengOpenAIVideosAdapter) Name() string { return VideoAdapterJimengOpenAIVideos }

func (a *jimengOpenAIVideosAdapter) Create(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) (*VideoProviderCreateResult, error) {
	if a == nil || a.provider == nil {
		return nil, errors.New("video adapter provider is required")
	}
	if err := a.ValidateCreate(ctx, account, body, contentType, upstreamModel); err != nil {
		return nil, err
	}
	if videoTaskEndpointFromContext(ctx) == VideoTaskEndpointVideoGenerations {
		adapted, _, err := normalizeJimengOpenAIVideoGenerationsBody(body, upstreamModel)
		if err != nil {
			return nil, err
		}
		body = adapted
	}
	result, err := a.provider.Create(ctx, account, body, contentType, upstreamModel)
	if result != nil {
		result.Metadata = stampVideoResultMetadata(result.Metadata, result.RawBody, a.Name())
	}
	return result, err
}

func (a *jimengOpenAIVideosAdapter) ValidateCreate(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) error {
	if videoTaskEndpointFromContext(ctx) == VideoTaskEndpointVideoGenerations {
		adapted, legacy, err := normalizeJimengOpenAIVideoGenerationsBody(body, upstreamModel)
		if err != nil {
			return err
		}
		if err := validateVideoCompatCreateBody(body, upstreamModel); err != nil {
			return err
		}
		if err := validateJimengOpenAIVideoGenerationsFields(ctx, body, legacy); err != nil {
			return err
		}
		if !legacy {
			return nil
		}
		body = adapted
	}
	return validateOpenAIVideoCreateShape(body, videoTaskEndpointFromContext(ctx) == VideoTaskEndpointVideoGenerations)
}

func (a *jimengOpenAIVideosAdapter) Fetch(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error) {
	if a == nil || a.provider == nil {
		return nil, errors.New("video adapter provider is required")
	}
	result, err := a.provider.Fetch(ctx, account, task)
	if result != nil {
		result.Metadata = stampVideoResultMetadata(result.Metadata, result.RawBody, a.Name())
	}
	return result, err
}

func (a *jimengOpenAIVideosAdapter) Refresh(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error) {
	return a.Fetch(ctx, account, task)
}

func (a *jimengOpenAIVideosAdapter) Estimate(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) (*VideoTaskEstimateResult, error) {
	req, err := ParseVideoTaskCreateEnvelope(body)
	if err != nil {
		return nil, err
	}
	legacy := true
	if videoTaskEndpointFromContext(ctx) == VideoTaskEndpointVideoGenerations {
		adapted, normalizedLegacy, err := normalizeJimengOpenAIVideoGenerationsBody(body, upstreamModel)
		if err != nil {
			return nil, err
		}
		body = adapted
		legacy = normalizedLegacy
	}
	if legacy {
		if err := validateOpenAIVideoCreateShape(body, videoTaskEndpointFromContext(ctx) == VideoTaskEndpointVideoGenerations); err != nil {
			return nil, err
		}
	}
	return localVideoEstimateResult(a.Name(), videoTaskEndpointFromContext(ctx), req.Model, body, upstreamModel)
}

func (a *jimengOpenAIVideosAdapter) Content(ctx context.Context, account *Account, task *VideoTask, headers http.Header) (*VideoContentStream, error) {
	if a == nil || a.provider == nil {
		return nil, errors.New("video adapter provider is required")
	}
	return a.provider.Content(ctx, account, task, headers)
}

func normalizeJimengOpenAIVideoGenerationsBody(body []byte, upstreamModel string) ([]byte, bool, error) {
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, false, fmt.Errorf("invalid jimeng OpenAI video create JSON: %w", err)
	}

	_, legacyUpstreamModel := supportedOpenAIVideoModels[strings.TrimSpace(upstreamModel)]
	_, legacyRequestedModel := supportedOpenAIVideoModels[strings.TrimSpace(payload.Model)]
	if !legacyUpstreamModel && !legacyRequestedModel && (isSeedance2VideoModel(upstreamModel) || isSeedance2VideoModel(payload.Model)) {
		adapted, err := openAIDurationCreateBody(body, upstreamModel)
		if err != nil {
			return nil, false, fmt.Errorf("invalid jimeng OpenAI video create JSON: %w", err)
		}
		return adapted, false, nil
	}

	adapted, err := adaptJimengVideoGenerationsCompatBody(body, true)
	if err != nil {
		return nil, true, fmt.Errorf("invalid jimeng OpenAI video create JSON: %w", err)
	}
	return adapted, true, nil
}

func isSeedance2VideoModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, "seedance2.")
}

func validateJimengOpenAIVideoGenerationsFields(ctx context.Context, body []byte, legacy bool) error {
	if legacy {
		return validateUnifiedVideoGenerationFields(ctx, body,
			"resolution", "ratio", "aspect_ratio", "duration", "seconds", "duration_seconds", "generate_audio", "content", "quality", "size", "reference_mode",
			"image", "images", "images_base64", "imagesBase64", "image_url", "image_urls", "imageUrls", "image_reference", "reference_image", "referenceImage", "reference_images", "referenceImages", "reference_image_url", "referenceImageUrl", "reference_image_urls", "referenceImageUrls", "first_frame_image", "input_reference", "inputReference",
			"videos", "video", "video_url", "videoUrl", "video_urls", "videoUrls", "reference_video", "referenceVideo", "reference_videos", "referenceVideos", "reference_video_url", "referenceVideoUrl", "reference_video_urls", "referenceVideoUrls", "input_video", "inputVideo",
			"audios", "audio", "audios_base64", "audiosBase64", "audio_url", "audioUrl", "audio_urls", "audioUrls", "audio_reference", "audioReference", "audio_references", "audioReferences", "reference_audio", "referenceAudio", "reference_audios", "referenceAudios", "reference_audio_url", "referenceAudioUrl", "reference_audio_urls", "referenceAudioUrls",
			"start_frame", "end_frame", "style_references", "styleReferences", "element_references", "elementReferences",
		)
	}
	return validateUnifiedVideoGenerationFields(ctx, body,
		"resolution", "ratio", "aspect_ratio", "duration", "seconds", "duration_seconds", "generate_audio",
		"content", "quality", "size", "reference_mode", "image", "images", "images_base64", "imagesBase64", "image_url", "image_urls", "imageUrls", "image_reference", "reference_image", "referenceImage", "reference_images", "referenceImages", "reference_image_url", "referenceImageUrl", "reference_image_urls", "referenceImageUrls", "first_frame_image", "input_reference", "inputReference",
		"videos", "video", "video_url", "videoUrl", "video_urls", "videoUrls", "reference_video", "referenceVideo", "reference_videos", "referenceVideos", "reference_video_url", "referenceVideoUrl", "reference_video_urls", "referenceVideoUrls", "input_video", "inputVideo",
		"audios", "audio", "audios_base64", "audiosBase64", "audio_url", "audioUrl", "audio_urls", "audioUrls", "audio_reference", "audioReference", "audio_references", "audioReferences", "reference_audio", "referenceAudio", "reference_audios", "referenceAudios", "reference_audio_url", "referenceAudioUrl", "reference_audio_urls", "referenceAudioUrls",
		"start_frame", "end_frame", "style_references", "styleReferences", "element_references", "elementReferences",
	)
}

func validateOpenAIVideoCreateShape(body []byte, allowCompatFields bool) error {
	var payload openAIVideoCreatePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	if err := rejectForbiddenOpenAIVideoFieldsWithCompatFields(body, allowCompatFields); err != nil {
		return err
	}
	if strings.TrimSpace(payload.Model) == "" {
		return errors.New("model is required")
	}
	if strings.TrimSpace(payload.Prompt) == "" {
		return errors.New("prompt is required")
	}
	return nil
}

func localVideoEstimateResult(adapterName string, endpoint string, requestedModel string, body []byte, upstreamModel string) (*VideoTaskEstimateResult, error) {
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	model := strings.TrimSpace(requestedModel)
	if model == "" {
		model = strings.TrimSpace(openAIVideoString(payload, "model"))
	}
	if upstreamModel = strings.TrimSpace(upstreamModel); upstreamModel == "" {
		upstreamModel = model
	}
	metadata := map[string]any{}
	for _, key := range []string{"seconds", "duration", "duration_seconds", "aspect_ratio", "ratio", "resolution"} {
		if value, ok := payload[key]; ok {
			metadata[key] = value
		}
	}
	response := map[string]any{
		"object":         "video.estimate",
		"model":          model,
		"upstream_model": upstreamModel,
		"adapter":        adapterName,
		"endpoint":       normalizeVideoTaskEndpoint(endpoint),
		"metadata":       metadata,
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	return &VideoTaskEstimateResult{ResponseBody: encoded, Metadata: response}, nil
}

func adaptLegacyVideoGenerationsCompatBody(body []byte) ([]byte, error) {
	return adaptJimengVideoGenerationsCompatBody(body, false)
}

func adaptJimengVideoGenerationsCompatBody(body []byte, includeDurationSeconds bool) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if payload == nil {
		return nil, errors.New("jimeng OpenAI video create JSON body must be an object")
	}
	if duration, ok := payload["duration"]; ok {
		if _, hasSeconds := payload["seconds"]; !hasSeconds {
			seconds, err := jimengVideoGenerationDurationAsSeconds(duration)
			if err != nil {
				return nil, err
			}
			encodedSeconds, err := json.Marshal(seconds)
			if err != nil {
				return nil, err
			}
			payload["seconds"] = encodedSeconds
		}
		delete(payload, "duration")
	}
	if duration, ok := payload["duration_seconds"]; ok {
		if includeDurationSeconds {
			if _, hasSeconds := payload["seconds"]; !hasSeconds {
				seconds, err := jimengVideoGenerationDurationAsSeconds(duration)
				if err != nil {
					return nil, err
				}
				encodedSeconds, err := json.Marshal(seconds)
				if err != nil {
					return nil, err
				}
				payload["seconds"] = encodedSeconds
			}
		}
		delete(payload, "duration_seconds")
	}
	if ratio, ok := payload["ratio"]; ok {
		if _, hasAspectRatio := payload["aspect_ratio"]; !hasAspectRatio {
			payload["aspect_ratio"] = ratio
		}
		delete(payload, "ratio")
	}
	allowed := map[string]struct{}{
		"model": {}, "prompt": {}, "seconds": {}, "aspect_ratio": {}, "resolution": {}, "size": {}, "generate_audio": {}, "content": {}, "quality": {}, "reference_mode": {},
		"image": {}, "images": {}, "images_base64": {}, "imagesBase64": {}, "image_url": {}, "image_urls": {}, "imageUrls": {}, "image_reference": {}, "reference_image": {}, "referenceImage": {}, "reference_images": {}, "referenceImages": {}, "reference_image_url": {}, "referenceImageUrl": {}, "reference_image_urls": {}, "referenceImageUrls": {}, "first_frame_image": {}, "input_reference": {}, "inputReference": {},
		"video": {}, "videos": {}, "video_url": {}, "videoUrl": {}, "video_urls": {}, "videoUrls": {}, "reference_video": {}, "referenceVideo": {}, "reference_videos": {}, "referenceVideos": {}, "reference_video_url": {}, "referenceVideoUrl": {}, "reference_video_urls": {}, "referenceVideoUrls": {}, "input_video": {}, "inputVideo": {},
		"audio": {}, "audios": {}, "audios_base64": {}, "audiosBase64": {}, "audio_url": {}, "audioUrl": {}, "audio_urls": {}, "audioUrls": {}, "audio_reference": {}, "audioReference": {}, "audio_references": {}, "audioReferences": {}, "reference_audio": {}, "referenceAudio": {}, "reference_audios": {}, "referenceAudios": {}, "reference_audio_url": {}, "referenceAudioUrl": {}, "reference_audio_urls": {}, "referenceAudioUrls": {},
		"start_frame": {}, "end_frame": {}, "style_references": {}, "styleReferences": {}, "element_references": {}, "elementReferences": {},
	}
	for field := range payload {
		if _, ok := allowed[field]; !ok {
			delete(payload, field)
		}
	}
	adapted, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return adapted, nil
}

func jimengVideoGenerationDurationAsSeconds(raw json.RawMessage) (string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", errors.New("duration must be a number or string")
	}
	if strings.HasPrefix(trimmed, "\"") {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return "", errors.New("duration must be a number or string")
		}
		return value, nil
	}

	var number json.Number
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return "", errors.New("duration must be a number or string")
	}
	return number.String(), nil
}

func NewJimengOpenAIVideoAdapter(openai *OpenAIGatewayService) VideoTaskAdapter {
	return &jimengOpenAIVideosAdapter{provider: NewOpenAICompatibleVideoProviderForGateway(openai)}
}
