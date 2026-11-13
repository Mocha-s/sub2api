package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// VideoAdapterMegaAi handles MegaAi-compatible upstream APIs.
// Endpoints: POST /v1/videos (create), GET /v1/videos/{id} (fetch), GET /v1/videos/{id}/content (download).
// Requires an integer duration and resolution/ratio rather than OpenAI's size field.
const VideoAdapterMegaAi = "megaai"

type megaAiVideoAdapter struct {
	provider *openAICompatibleVideoProvider
}

func NewMegaAiVideoAdapter(openai *OpenAIGatewayService) VideoTaskAdapter {
	return &megaAiVideoAdapter{provider: &openAICompatibleVideoProvider{client: http.DefaultClient, openai: openai}}
}

func (a *megaAiVideoAdapter) Name() string { return VideoAdapterMegaAi }

func (a *megaAiVideoAdapter) ValidateCreate(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) error {
	if _, err := megaAiCreateBody(body, upstreamModel); err != nil {
		return err
	}
	if err := validateUnifiedVideoGenerationFields(ctx, body,
		"size", "resolution", "ratio", "aspect_ratio", "duration", "seconds", "duration_seconds",
		"seed", "referenceImages", "referenceVideos", "referenceAudios",
		"generate_audio", "content", "images", "videos", "audios",
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

func (a *megaAiVideoAdapter) Create(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) (*VideoProviderCreateResult, error) {
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
	forwardBody, err := megaAiCreateBody(body, upstreamModel)
	if err != nil {
		return nil, err
	}
	endpoint, err := provider.openAIVideoEndpoint(account, "/v1/videos")
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
	parsed, err := parseOpenAIVideoResponse(raw, resp.Header.Get("X-Request-Id"))
	if err != nil {
		return nil, err
	}
	if parsed.providerTaskID == "" {
		return nil, errors.New("megaai video create response missing id or task_id")
	}
	return &VideoProviderCreateResult{
		ProviderTaskID: parsed.providerTaskID,
		Status:         parsed.status,
		ProviderStatus: parsed.providerStatus,
		RawBody:        raw,
		Metadata:       stampVideoAdapterMetadata(parsed.metadata, VideoAdapterMegaAi),
	}, nil
}

func (a *megaAiVideoAdapter) Fetch(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error) {
	provider, err := a.openAIProvider()
	if err != nil {
		return nil, err
	}
	result, err := provider.Fetch(ctx, account, task)
	if result != nil {
		result.Metadata = stampVideoAdapterMetadata(result.Metadata, VideoAdapterMegaAi)
	}
	return result, err
}

func (a *megaAiVideoAdapter) Refresh(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error) {
	return a.Fetch(ctx, account, task)
}

func (a *megaAiVideoAdapter) Content(ctx context.Context, account *Account, task *VideoTask, headers http.Header) (*VideoContentStream, error) {
	provider, err := a.openAIProvider()
	if err != nil {
		return nil, err
	}
	return provider.Content(ctx, account, task, headers)
}

func (a *megaAiVideoAdapter) Estimate(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) (*VideoTaskEstimateResult, error) {
	req, err := ParseVideoTaskCreateEnvelope(body)
	if err != nil {
		return nil, err
	}
	forwardBody, err := megaAiCreateBody(body, upstreamModel)
	if err != nil {
		return nil, err
	}
	return localVideoEstimateResult(a.Name(), videoTaskEndpointFromContext(ctx), req.Model, forwardBody, upstreamModel)
}

func (a *megaAiVideoAdapter) openAIProvider() (*openAICompatibleVideoProvider, error) {
	if a == nil || a.provider == nil {
		return nil, errors.New("video adapter provider is required")
	}
	return a.provider, nil
}

// megaAiCreateBody normalises the request for MegaAi:
// duration/seconds/duration_seconds → integer duration; ratio/aspect_ratio → ratio;
// resolution, seed, reference* fields passed through unchanged.
func megaAiCreateBody(body []byte, upstreamModel string) ([]byte, error) {
	payload, err := seedanceRawPayload(body)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if upstreamModel != "" {
		out["model"] = upstreamModel
	} else if model := rawStringField(payload, "model"); model != "" {
		out["model"] = model
	}
	if prompt := rawStringField(payload, "prompt"); prompt != "" {
		out["prompt"] = prompt
	}
	// Match billing precedence and emit a JSON integer even for integral decimal/exponent inputs.
	for _, key := range []string{"seconds", "duration", "duration_seconds"} {
		if raw, exists := payload[key]; exists {
			value, err := seedanceRawValue(raw)
			if err != nil {
				return nil, err
			}
			seconds, err := parseVideoTaskSeconds(value)
			if err != nil {
				return nil, invalidVideoTaskRequest("%s must be an integer between 1 and 3600", key)
			}
			out["duration"] = seconds
			break
		}
	}
	// ratio: accept ratio or aspect_ratio
	ratio := rawStringField(payload, "ratio")
	if ratio == "" {
		ratio = rawStringField(payload, "aspect_ratio")
	}
	if ratio != "" {
		out["ratio"] = ratio
	}
	if raw, exists := payload["size"]; exists {
		var size string
		if err := json.Unmarshal(raw, &size); err != nil || strings.TrimSpace(size) == "" {
			return nil, invalidVideoTaskRequest("size must be a resolution or pixel dimensions")
		}
		size = strings.ToLower(strings.TrimSpace(size))
		resolution := NormalizeVideoResolutionTier(size)
		switch resolution {
		case "480p", "720p", "1080p", "4k":
		default:
			return nil, invalidVideoTaskRequest("unsupported MegaAi size %q; use resolution and ratio", size)
		}
		out["resolution"] = resolution
		if ratio == "" {
			if sizeRatio := videoAspectRatioFromSize(size); sizeRatio != "" {
				if size == "854x480" {
					sizeRatio = "16:9"
				}
				if size == "480x854" {
					sizeRatio = "9:16"
				}
				out["ratio"] = sizeRatio
			}
		}
	}
	for _, key := range []string{"resolution", "seed", "referenceImages", "referenceVideos", "referenceAudios",
		"generate_audio", "content", "images", "videos", "audios"} {
		if raw := payload[key]; len(raw) > 0 {
			v, err := seedanceRawValue(raw)
			if err != nil {
				return nil, err
			}
			out[key] = v
		}
	}
	return json.Marshal(out)
}
