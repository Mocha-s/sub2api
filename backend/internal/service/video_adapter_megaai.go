package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// VideoAdapterMegaAi handles MegaAi-compatible upstream APIs.
// Endpoints: POST /v1/videos (create), GET /v1/videos/{id} (fetch), GET /v1/videos/{id}/content (download).
// Requires integer duration; reuses seedanceCreateBody for numeric field normalization.
const VideoAdapterMegaAi = "megaai"

type megaAiVideoAdapter struct {
	provider *openAICompatibleVideoProvider
}

func NewMegaAiVideoAdapter(openai *OpenAIGatewayService) VideoTaskAdapter {
	return &megaAiVideoAdapter{provider: &openAICompatibleVideoProvider{client: http.DefaultClient, openai: openai}}
}

func (a *megaAiVideoAdapter) Name() string { return VideoAdapterMegaAi }

func (a *megaAiVideoAdapter) ValidateCreate(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) error {
	if _, err := seedanceCreateBody(body, upstreamModel); err != nil {
		return err
	}
	if err := validateUnifiedVideoGenerationFields(ctx, body,
		"resolution", "ratio", "aspect_ratio", "duration", "seconds", "duration_seconds",
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
	// duration: accept duration/seconds/duration_seconds, all as integer
	for _, key := range []string{"duration", "seconds", "duration_seconds"} {
		if num, ok, err := seedanceRawNumericField(payload, key); err != nil {
			return nil, err
		} else if ok {
			out["duration"] = num
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
