package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
)

type openAIVideosDurationAdapter struct {
	provider *openAICompatibleVideoProvider
}

func NewOpenAIVideosDurationAdapter(openai *OpenAIGatewayService) VideoTaskAdapter {
	return &openAIVideosDurationAdapter{provider: &openAICompatibleVideoProvider{client: http.DefaultClient, openai: openai}}
}

func (a *openAIVideosDurationAdapter) Name() string { return VideoAdapterOpenAIVideosDuration }

func (a *openAIVideosDurationAdapter) ValidateCreate(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) error {
	if err := validateVideoCompatCreateBody(body, upstreamModel); err != nil {
		return err
	}
	if _, err := openAIDurationCreateBody(body, upstreamModel); err != nil {
		return err
	}
	if err := validateUnifiedVideoGenerationFields(ctx, body,
		"resolution", "ratio", "aspect_ratio", "duration", "seconds", "duration_seconds", "generate_audio",
		"content", "image", "images", "images_base64", "imagesBase64", "image_url", "image_urls", "imageUrls", "image_reference", "reference_image", "referenceImage", "reference_images", "referenceImages", "reference_image_url", "referenceImageUrl", "reference_image_urls", "referenceImageUrls", "first_frame_image", "input_reference", "inputReference",
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
	_, err = provider.openAIVideoEndpoint(account, "/v1/videos")
	return err
}

func (a *openAIVideosDurationAdapter) Create(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) (*VideoProviderCreateResult, error) {
	if err := a.ValidateCreate(ctx, account, body, contentType, upstreamModel); err != nil {
		return nil, err
	}
	provider, err := a.openAIProvider()
	if err != nil {
		return nil, err
	}
	forwardBody, err := openAIDurationCreateBody(body, upstreamModel)
	if err != nil {
		return nil, err
	}
	result, err := provider.Create(ctx, account, forwardBody, contentType, upstreamModel)
	if result != nil {
		result.Metadata = openAIDurationMetadata(result.Metadata, result.RawBody)
	}
	return result, err
}

func (a *openAIVideosDurationAdapter) Fetch(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error) {
	provider, err := a.openAIProvider()
	if err != nil {
		return nil, err
	}
	result, err := provider.Fetch(ctx, account, task)
	if result != nil {
		result.Metadata = openAIDurationMetadata(result.Metadata, result.RawBody)
	}
	return result, err
}

func (a *openAIVideosDurationAdapter) Refresh(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error) {
	return a.Fetch(ctx, account, task)
}

func (a *openAIVideosDurationAdapter) Estimate(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) (*VideoTaskEstimateResult, error) {
	req, err := ParseVideoTaskCreateEnvelope(body)
	if err != nil {
		return nil, err
	}
	forwardBody, err := openAIDurationCreateBody(body, upstreamModel)
	if err != nil {
		return nil, err
	}
	return localVideoEstimateResult(a.Name(), videoTaskEndpointFromContext(ctx), req.Model, forwardBody, upstreamModel)
}

func (a *openAIVideosDurationAdapter) Content(ctx context.Context, account *Account, task *VideoTask, headers http.Header) (*VideoContentStream, error) {
	provider, err := a.openAIProvider()
	if err != nil {
		return nil, err
	}
	stream, contentErr := provider.Content(ctx, account, task, headers)
	if contentErr == nil {
		return stream, nil
	}
	if openAIDurationCanFallbackContent(contentErr) && task != nil {
		if resultURL := videoTaskMetadataString(task.Metadata, "result_url"); resultURL != "" {
			resultURL, err = validateVideoResultURL(provider.openai, resultURL)
			if err != nil {
				return nil, err
			}
			ctx = withVideoResultRedirectValidator(ctx, provider.openai)
			return fetchVideoResultURLWithDo(ctx, resultURL, headers, func(req *http.Request) (*http.Response, error) {
				return provider.do(req, account)
			})
		}
	}
	return nil, contentErr
}

func openAIDurationCanFallbackContent(err error) bool {
	var upstreamErr *OpenAIVideoUpstreamError
	if !errors.As(err, &upstreamErr) {
		return false
	}
	switch upstreamErr.StatusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	default:
		return false
	}
}

func (a *openAIVideosDurationAdapter) openAIProvider() (*openAICompatibleVideoProvider, error) {
	if a == nil || a.provider == nil {
		return nil, errors.New("video adapter provider is required")
	}
	return a.provider, nil
}

func openAIDurationCreateBody(body []byte, upstreamModel string) ([]byte, error) {
	payload, err := openAIDurationRawPayload(body)
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	for _, key := range []string{
		"model", "prompt", "aspect_ratio", "resolution", "generate_audio", "content", "size", "quality", "reference_mode",
		"image", "images", "images_base64", "imagesBase64", "image_url", "image_urls", "imageUrls", "image_reference", "reference_image", "referenceImage", "reference_images", "referenceImages", "reference_image_url", "referenceImageUrl", "reference_image_urls", "referenceImageUrls", "first_frame_image", "input_reference", "inputReference",
		"video", "videos", "video_url", "videoUrl", "video_urls", "videoUrls", "reference_video", "referenceVideo", "reference_videos", "referenceVideos", "reference_video_url", "referenceVideoUrl", "reference_video_urls", "referenceVideoUrls", "input_video", "inputVideo",
		"audio", "audios", "audios_base64", "audiosBase64", "audio_url", "audioUrl", "audio_urls", "audioUrls", "audio_reference", "audioReference", "audio_references", "audioReferences", "reference_audio", "referenceAudio", "reference_audios", "referenceAudios", "reference_audio_url", "referenceAudioUrl", "reference_audio_urls", "referenceAudioUrls",
		"start_frame", "end_frame", "style_references", "styleReferences", "element_references", "elementReferences",
	} {
		if raw := payload[key]; len(raw) > 0 {
			out[key] = raw
		}
	}
	if upstreamModel = strings.TrimSpace(upstreamModel); upstreamModel != "" {
		encoded, err := json.Marshal(upstreamModel)
		if err != nil {
			return nil, err
		}
		out["model"] = encoded
	}
	if duration, ok, err := openAIDurationRawNumericField(payload, "duration"); err != nil {
		return nil, err
	} else if ok {
		encoded, err := json.Marshal(duration)
		if err != nil {
			return nil, err
		}
		out["duration"] = encoded
	} else if seconds, ok, err := openAIDurationRawNumericField(payload, "seconds"); err != nil {
		return nil, err
	} else if ok {
		encoded, err := json.Marshal(seconds)
		if err != nil {
			return nil, err
		}
		out["duration"] = encoded
	} else if durationSeconds, ok, err := openAIDurationRawNumericField(payload, "duration_seconds"); err != nil {
		return nil, err
	} else if ok {
		encoded, err := json.Marshal(durationSeconds)
		if err != nil {
			return nil, err
		}
		out["duration"] = encoded
	}
	if _, ok := out["aspect_ratio"]; !ok {
		if ratio := rawStringField(payload, "ratio"); ratio != "" {
			encoded, err := json.Marshal(ratio)
			if err != nil {
				return nil, err
			}
			out["aspect_ratio"] = encoded
		}
	}
	return json.Marshal(out)
}

func openAIDurationRawPayload(body []byte) (map[string]json.RawMessage, error) {
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

func openAIDurationRawNumericField(payload map[string]json.RawMessage, key string) (json.Number, bool, error) {
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

func openAIDurationMetadata(metadata map[string]any, raw []byte) map[string]any {
	return stampVideoResultMetadata(metadata, raw, VideoAdapterOpenAIVideosDuration)
}

func stampVideoResultMetadata(metadata map[string]any, raw []byte, adapterName string) map[string]any {
	metadata = stampVideoAdapterMetadata(metadata, adapterName)
	if videoTaskMetadataString(metadata, "result_url") == "" {
		if resultURL := openAIDurationResultURL(raw); resultURL != "" {
			metadata["result_url"] = resultURL
		}
	}
	return metadata
}

func openAIDurationResultURL(raw []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	if resultURL := openAIDurationDataURL(payload["data"]); resultURL != "" {
		return resultURL
	}
	if resultURL := openAIDurationURLFromMap(payload, "result_url", "resultUrl", "video_url", "videoUrl", "url", "content_url", "contentUrl"); resultURL != "" {
		return resultURL
	}
	if result, ok := payload["result"].(map[string]any); ok {
		if resultURL := openAIDurationURLFromMap(result, "video_url", "videoUrl", "url"); resultURL != "" {
			return resultURL
		}
		for _, key := range []string{"output", "outputs"} {
			if resultURL := openAIDurationURLFromValue(result[key]); resultURL != "" {
				return resultURL
			}
		}
	}
	for _, key := range []string{"output", "outputs"} {
		if resultURL := openAIDurationURLFromValue(payload[key]); resultURL != "" {
			return resultURL
		}
	}
	if metadata, ok := payload["metadata"].(map[string]any); ok {
		if resultURL := openAIDurationURLFromMap(metadata, "url", "result_url", "resultUrl", "content_url", "contentUrl"); resultURL != "" {
			return resultURL
		}
		if resultURL := openAIDurationURLFromValue(metadata["result_urls"]); resultURL != "" {
			return resultURL
		}
		if resultURL := openAIDurationURLFromValue(metadata["resultUrls"]); resultURL != "" {
			return resultURL
		}
	}
	return ""
}

func openAIDurationDataURL(value any) string {
	return openAIDurationURLFromValue(value)
}

func openAIDurationURLFromValue(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		for _, item := range v {
			if resultURL := openAIDurationURLFromValue(item); resultURL != "" {
				return resultURL
			}
		}
	case map[string]any:
		if resultURL := openAIDurationURLFromMap(v, "url", "video_url", "videoUrl", "output_url", "outputUrl", "download_url", "downloadUrl"); resultURL != "" {
			return resultURL
		}
		for _, key := range []string{"outputUrls", "output_urls", "outputs", "urls", "videos", "video_urls", "videoUrls"} {
			if resultURL := openAIDurationURLFromValue(v[key]); resultURL != "" {
				return resultURL
			}
		}
	}
	return ""
}

func openAIDurationURLFromMap(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok {
			if resultURL := strings.TrimSpace(value); resultURL != "" {
				return resultURL
			}
		}
	}
	return ""
}
