package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type seedanceAPIV1VideoAdapter struct {
	client *http.Client
	openai *OpenAIGatewayService
}

func NewSeedanceAPIV1VideoAdapter(openai *OpenAIGatewayService) VideoTaskAdapter {
	return &seedanceAPIV1VideoAdapter{client: http.DefaultClient, openai: openai}
}

func (a *seedanceAPIV1VideoAdapter) Name() string { return VideoAdapterSeedanceAPIV1 }

func (a *seedanceAPIV1VideoAdapter) ValidateCreate(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) error {
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
	_, err := a.seedanceEndpoint(account, "/video-generations")
	return err
}

func (a *seedanceAPIV1VideoAdapter) Create(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) (*VideoProviderCreateResult, error) {
	if err := a.ValidateCreate(ctx, account, body, contentType, upstreamModel); err != nil {
		return nil, err
	}
	token, err := a.openAIVideoToken(ctx, account)
	if err != nil {
		return nil, err
	}
	forwardBody, err := seedanceCreateBody(body, upstreamModel)
	if err != nil {
		return nil, err
	}
	endpoint, err := a.seedanceEndpoint(account, "/video-generations")
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

	resp, err := a.do(req, account)
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
	return parseSeedanceCreateResponse(raw, token)
}

func (a *seedanceAPIV1VideoAdapter) Fetch(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error) {
	token, err := a.openAIVideoToken(ctx, account)
	if err != nil {
		return nil, err
	}
	providerTaskID, err := openAIVideoProviderTaskID(task)
	if err != nil {
		return nil, err
	}
	endpoint, err := a.seedanceEndpoint(account, "/video-generations/"+url.PathEscape(providerTaskID))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := a.do(req, account)
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
	parsed, err := parseSeedanceTaskResponseWithFallback(raw, token, providerTaskID)
	if err != nil {
		return nil, err
	}
	if parsed.ProviderTaskID == "" {
		parsed.ProviderTaskID = providerTaskID
	}
	return &VideoProviderFetchResult{
		ProviderTaskID: parsed.ProviderTaskID,
		Status:         parsed.Status,
		ProviderStatus: parsed.ProviderStatus,
		RawBody:        parsed.RawBody,
		Metadata:       parsed.Metadata,
		ExpiresAt:      parsed.ExpiresAt,
	}, nil
}

func (a *seedanceAPIV1VideoAdapter) Refresh(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error) {
	return a.Fetch(ctx, account, task)
}

func (a *seedanceAPIV1VideoAdapter) Estimate(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) (*VideoTaskEstimateResult, error) {
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

func (a *seedanceAPIV1VideoAdapter) Content(ctx context.Context, account *Account, task *VideoTask, headers http.Header) (*VideoContentStream, error) {
	if task == nil {
		return nil, errors.New("seedance video task is required")
	}
	resultURL := videoTaskMetadataString(task.Metadata, "result_url")
	if resultURL == "" {
		return nil, errors.New("seedance video task missing result_url")
	}
	resultURL, err := validateVideoResultURL(a.openai, resultURL)
	if err != nil {
		return nil, err
	}
	ctx = withVideoResultRedirectValidator(ctx, a.openai)
	return fetchVideoResultURLWithDo(ctx, resultURL, headers, func(req *http.Request) (*http.Response, error) {
		return a.do(req, account)
	})
}

func (a *seedanceAPIV1VideoAdapter) openAIVideoToken(ctx context.Context, account *Account) (string, error) {
	provider := &openAICompatibleVideoProvider{openai: nil}
	if a != nil {
		provider.openai = a.openai
	}
	return provider.openAIVideoToken(ctx, account)
}

func (a *seedanceAPIV1VideoAdapter) seedanceEndpoint(account *Account, path string) (string, error) {
	baseURL, err := seedanceBaseURL(account)
	if err != nil {
		return "", err
	}
	if a != nil && a.openai != nil && a.openai.cfg != nil {
		validatedURL, err := a.openai.validateUpstreamBaseURL(baseURL)
		if err != nil {
			return "", err
		}
		baseURL = validatedURL
	}
	return buildSeedanceEndpoint(baseURL, path)
}

func seedanceBaseURL(account *Account) (string, error) {
	if account == nil {
		return "", errors.New("seedance base_url is required")
	}
	baseURL := strings.TrimSpace(account.GetCredential("base_url"))
	if baseURL == "" {
		return "", errors.New("seedance base_url is required")
	}
	return baseURL, nil
}

func buildSeedanceEndpoint(baseURL string, path string) (string, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return "", errors.New("seedance base_url is required")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return baseURL + path, nil
}

func (a *seedanceAPIV1VideoAdapter) do(req *http.Request, account *Account) (*http.Response, error) {
	if a != nil && a.openai != nil && a.openai.httpUpstream != nil && account != nil {
		proxyURL := ""
		if account.ProxyID != nil && account.Proxy != nil {
			proxyURL = account.Proxy.URL()
		}
		req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
		return a.openai.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, nil)
	}
	return a.httpClient().Do(req)
}

func (a *seedanceAPIV1VideoAdapter) httpClient() *http.Client {
	if a != nil && a.client != nil {
		return a.client
	}
	return http.DefaultClient
}

func seedanceCreateBody(body []byte, upstreamModel string) ([]byte, error) {
	payload, err := seedanceRawPayload(body)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if upstreamModel = strings.TrimSpace(upstreamModel); upstreamModel != "" {
		out["model"] = upstreamModel
	} else if model := rawStringField(payload, "model"); model != "" {
		out["model"] = model
	}
	if prompt := rawStringField(payload, "prompt"); prompt != "" {
		out["prompt"] = prompt
	}
	if duration, ok, err := seedanceRawNumericField(payload, "duration"); err != nil {
		return nil, err
	} else if ok {
		out["duration"] = duration
	} else if seconds, ok, err := seedanceRawNumericField(payload, "seconds"); err != nil {
		return nil, err
	} else if ok {
		out["duration"] = seconds
	} else if durationSeconds, ok, err := seedanceRawNumericField(payload, "duration_seconds"); err != nil {
		return nil, err
	} else if ok {
		out["duration"] = durationSeconds
	}
	if ratio := rawStringField(payload, "ratio"); ratio != "" {
		out["ratio"] = ratio
	} else if ratio := rawStringField(payload, "aspect_ratio"); ratio != "" {
		out["ratio"] = ratio
	}
	for _, key := range []string{
		"resolution", "size", "generate_audio", "return_last_frame", "web_search", "content", "quality", "reference_mode",
		"image", "images", "images_base64", "imagesBase64", "image_url", "image_urls", "imageUrls", "image_reference", "reference_image", "referenceImage", "reference_images", "referenceImages", "reference_image_url", "referenceImageUrl", "reference_image_urls", "referenceImageUrls", "first_frame_image", "input_reference", "inputReference",
		"video", "videos", "video_url", "videoUrl", "video_urls", "videoUrls", "reference_video", "referenceVideo", "reference_videos", "referenceVideos", "reference_video_url", "referenceVideoUrl", "reference_video_urls", "referenceVideoUrls", "input_video", "inputVideo",
		"audio", "audios", "audios_base64", "audiosBase64", "audio_url", "audioUrl", "audio_urls", "audioUrls", "audio_reference", "audioReference", "audio_references", "audioReferences", "reference_audio", "referenceAudio", "reference_audios", "referenceAudios", "reference_audio_url", "referenceAudioUrl", "reference_audio_urls", "referenceAudioUrls",
		"start_frame", "end_frame", "style_references", "styleReferences", "element_references", "elementReferences",
	} {
		if raw := payload[key]; len(raw) > 0 {
			value, err := seedanceRawValue(raw)
			if err != nil {
				return nil, err
			}
			out[key] = value
		}
	}
	return json.Marshal(out)
}

func seedanceRawPayload(body []byte) (map[string]json.RawMessage, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("invalid video create JSON: %w", err)
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, errors.New("video create JSON body must be an object")
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("invalid video create JSON: %w", err)
	}
	return payload, nil
}

func seedanceRawValue(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func seedanceRawNumericField(payload map[string]json.RawMessage, key string) (json.Number, bool, error) {
	raw := payload[key]
	if len(raw) == 0 {
		return "", false, nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "\"") {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", false, err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return "", false, fmt.Errorf("%s must be a number or numeric string", key)
		}
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return "", false, fmt.Errorf("%s must be a number or numeric string", key)
		}
		return json.Number(strconv.FormatFloat(parsed, 'f', -1, 64)), true, nil
	}

	var number json.Number
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return "", false, fmt.Errorf("%s must be a number or numeric string", key)
	}
	return number, true, nil
}

func parseSeedanceCreateResponse(raw []byte, token string) (*VideoProviderCreateResult, error) {
	return parseSeedanceTaskResponse(raw, token)
}

func parseSeedanceTaskResponse(raw []byte, token string) (*VideoProviderCreateResult, error) {
	return parseSeedanceTaskResponseWithFallback(raw, token, "")
}

func parseSeedanceTaskResponseWithFallback(raw []byte, token string, fallbackTaskID string) (*VideoProviderCreateResult, error) {
	var envelope struct {
		Code    int            `json:"code"`
		Message string         `json:"message"`
		Data    map[string]any `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&envelope); err != nil {
		return nil, err
	}
	if envelope.Code != 0 {
		return nil, fmt.Errorf("seedance upstream error code %d: %s", envelope.Code, redactOpenAIVideoToken(envelope.Message, token))
	}
	if envelope.Data == nil {
		return nil, errors.New("seedance response missing data")
	}

	providerTaskID := seedanceAnyString(envelope.Data["id"])
	if providerTaskID == "" {
		providerTaskID = seedanceAnyString(envelope.Data["task_id"])
	}
	if providerTaskID == "" {
		providerTaskID = strings.TrimSpace(fallbackTaskID)
	}
	if providerTaskID == "" {
		return nil, errors.New("seedance response missing id or task_id")
	}
	providerStatus := strings.TrimSpace(seedanceAnyString(envelope.Data["status"]))
	status := seedanceStatus(providerStatus)
	progress, ok := seedanceAnyFloat(envelope.Data["progress"])
	if !ok {
		progress = 0
	}
	resultURL := seedanceResultURL(envelope.Data)

	canonical := map[string]any{
		"id":       providerTaskID,
		"task_id":  providerTaskID,
		"object":   "video",
		"status":   string(status),
		"progress": progress,
	}
	for _, key := range []string{"model", "duration", "ratio", "resolution"} {
		if value, ok := envelope.Data[key]; ok {
			canonical[key] = value
		}
	}
	if result, ok := envelope.Data["result"]; ok {
		canonical["result"] = result
	}
	if resultURL != "" {
		canonical["video_url"] = resultURL
	}
	canonicalBody, err := json.Marshal(canonical)
	if err != nil {
		return nil, err
	}
	metadata := map[string]any{
		"progress": progress,
		"response": sanitizeOpenAIVideoValue(envelope.Data),
	}
	if resultURL != "" {
		metadata["result_url"] = resultURL
	}
	metadata = stampVideoAdapterMetadata(metadata, VideoAdapterSeedanceAPIV1)

	return &VideoProviderCreateResult{
		ProviderTaskID: providerTaskID,
		Status:         status,
		ProviderStatus: providerStatus,
		RawBody:        canonicalBody,
		Metadata:       metadata,
	}, nil
}

func seedanceStatus(status string) VideoTaskStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "queued", "pending", "submitted", "created":
		return VideoTaskStatusQueued
	case "running", "processing", "in_progress", "generating":
		return VideoTaskStatusInProgress
	case "succeeded", "success", "completed", "complete", "done":
		return VideoTaskStatusCompleted
	case "failed", "failure", "error":
		return VideoTaskStatusFailed
	case "canceled", "cancelled":
		return VideoTaskStatusCancelled
	case "expired":
		return VideoTaskStatusExpired
	default:
		return VideoTaskStatusUnknown
	}
}

func seedanceAnyString(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return ""
	}
}

func seedanceAnyFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case json.Number:
		out, err := v.Float64()
		return out, err == nil
	case string:
		out, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return out, err == nil
	default:
		return 0, false
	}
}

func seedanceResultURL(data map[string]any) string {
	if resultURL := seedanceURLFromValue(data["result"]); resultURL != "" {
		return resultURL
	}
	return seedanceURLFromMap(data)
}

func seedanceURLFromValue(value any) string {
	switch v := value.(type) {
	case map[string]any:
		return seedanceURLFromMap(v)
	case []any:
		for _, item := range v {
			if resultURL := seedanceURLFromValue(item); resultURL != "" {
				return resultURL
			}
		}
	case string:
		return strings.TrimSpace(v)
	}
	return ""
}

func seedanceURLFromMap(values map[string]any) string {
	for _, key := range []string{"video_url", "videoUrl", "url", "output_url", "outputUrl", "download_url", "downloadUrl", "file_url", "fileUrl", "last_frame_url", "lastFrameUrl"} {
		if value := seedanceAnyString(values[key]); value != "" {
			return value
		}
	}
	for _, key := range []string{"videos", "video_urls", "videoUrls", "urls"} {
		if value := seedanceURLFromValue(values[key]); value != "" {
			return value
		}
	}
	return ""
}

func fetchVideoResultURLWithDo(ctx context.Context, resultURL string, headers http.Header, do func(*http.Request) (*http.Response, error)) (*VideoContentStream, error) {
	resultURL = strings.TrimSpace(resultURL)
	if resultURL == "" {
		return nil, errors.New("video result_url is required")
	}
	req, err := http.NewRequestWithContext(ctx, videoTaskContentMethodFromContext(ctx), resultURL, nil)
	if err != nil {
		return nil, err
	}
	for _, value := range headers.Values("Range") {
		req.Header.Add("Range", value)
	}
	if do == nil {
		do = http.DefaultClient.Do
	}
	resp, err := do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		defer func() { _ = resp.Body.Close() }()
		return nil, openAIVideoStatusError(resp, "")
	}
	return &VideoContentStream{
		Body:          resp.Body,
		StatusCode:    resp.StatusCode,
		ContentType:   resp.Header.Get("Content-Type"),
		ContentLength: resp.ContentLength,
		Headers:       openAIVideoHeaders(resp.Header),
	}, nil
}
