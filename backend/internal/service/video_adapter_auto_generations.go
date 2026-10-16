package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

const autoVideoGenerationsProbeModel = "__sub2api_auto_video_probe_never_create__"

type autoVideoGenerationsAdapter struct {
	provider *openAICompatibleVideoProvider
	newapi   VideoTaskAdapter
	seedance VideoTaskAdapter

	mu    sync.Mutex
	cache map[string]string
}

func NewAutoVideoGenerationsAdapter(openai *OpenAIGatewayService) VideoTaskAdapter {
	return &autoVideoGenerationsAdapter{
		provider: &openAICompatibleVideoProvider{client: http.DefaultClient, openai: openai},
		newapi:   NewNewAPIVideoGenerationsAdapter(openai),
		seedance: NewSeedanceAPIV1VideoAdapter(openai),
		cache:    map[string]string{},
	}
}

func (a *autoVideoGenerationsAdapter) Name() string { return VideoAdapterAutoVideoGenerations }

func (a *autoVideoGenerationsAdapter) ValidateCreate(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) error {
	if _, err := seedanceCreateBody(body, upstreamModel); err != nil {
		return err
	}
	adapter, err := a.detectedAdapter(ctx, account)
	if err != nil {
		return err
	}
	if validator, ok := adapter.(VideoTaskCreateValidator); ok {
		return validator.ValidateCreate(ctx, account, body, contentType, upstreamModel)
	}
	return nil
}

func (a *autoVideoGenerationsAdapter) Create(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) (*VideoProviderCreateResult, error) {
	adapter, err := a.detectedAdapter(ctx, account)
	if err != nil {
		return nil, err
	}
	result, err := adapter.Create(ctx, account, body, contentType, upstreamModel)
	if err != nil {
		return nil, withVideoTaskErrorMetadata(err, map[string]any{VideoAdapterMetadataKey: adapter.Name()})
	}
	return result, nil
}

func (a *autoVideoGenerationsAdapter) Fetch(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error) {
	adapter, err := a.detectedAdapter(ctx, account)
	if err != nil {
		return nil, err
	}
	return adapter.Fetch(ctx, account, task)
}

func (a *autoVideoGenerationsAdapter) Refresh(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error) {
	adapter, err := a.detectedAdapter(ctx, account)
	if err != nil {
		return nil, err
	}
	refresher, ok := adapter.(VideoTaskRefresher)
	if !ok {
		return nil, unsupportedVideoTaskAction("refresh")
	}
	return refresher.Refresh(ctx, account, task)
}

func (a *autoVideoGenerationsAdapter) Estimate(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) (*VideoTaskEstimateResult, error) {
	adapter, err := a.detectedAdapter(ctx, account)
	if err != nil {
		return nil, err
	}
	estimator, ok := adapter.(VideoTaskEstimator)
	if !ok {
		return nil, unsupportedVideoTaskAction("estimate")
	}
	return estimator.Estimate(ctx, account, body, contentType, upstreamModel)
}

func (a *autoVideoGenerationsAdapter) Content(ctx context.Context, account *Account, task *VideoTask, headers http.Header) (*VideoContentStream, error) {
	adapter, err := a.detectedAdapter(ctx, account)
	if err != nil {
		return nil, err
	}
	return adapter.Content(ctx, account, task, headers)
}

func (a *autoVideoGenerationsAdapter) detectedAdapter(ctx context.Context, account *Account) (VideoTaskAdapter, error) {
	if a == nil {
		return nil, errors.New("video adapter provider is required")
	}
	if adapterName := a.cachedAdapterName(account); adapterName != "" {
		return a.adapterByName(adapterName)
	}

	if ok, err := a.probeOpenAIEndpoint(ctx, account, "/v1/video/generations"); err != nil {
		return nil, err
	} else if ok {
		a.cacheAdapterName(account, VideoAdapterNewAPIVideoGenerations)
		return a.newapi, nil
	}
	if ok, err := a.probeSeedanceEndpoint(ctx, account, "/video-generations"); err != nil {
		return nil, err
	} else if ok {
		a.cacheAdapterName(account, VideoAdapterSeedanceAPIV1)
		return a.seedance, nil
	}
	return nil, fmt.Errorf("unable to auto-detect video adapter for account %d", videoAdapterAccountID(account))
}

func (a *autoVideoGenerationsAdapter) adapterByName(name string) (VideoTaskAdapter, error) {
	switch name {
	case VideoAdapterNewAPIVideoGenerations:
		return a.newapi, nil
	case VideoAdapterSeedanceAPIV1:
		return a.seedance, nil
	default:
		return nil, fmt.Errorf("unknown auto-detected video adapter %q", name)
	}
}

func (a *autoVideoGenerationsAdapter) cachedAdapterName(account *Account) string {
	key := autoVideoGenerationsCacheKey(account)
	if key == "" || a == nil {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cache[key]
}

func (a *autoVideoGenerationsAdapter) cacheAdapterName(account *Account, name string) {
	key := autoVideoGenerationsCacheKey(account)
	if key == "" || a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cache == nil {
		a.cache = map[string]string{}
	}
	a.cache[key] = name
}

func autoVideoGenerationsCacheKey(account *Account) string {
	if account == nil {
		return ""
	}
	baseURL := strings.TrimSpace(account.GetCredential("base_url"))
	baseURL = strings.TrimRight(baseURL, "/")
	if account.ID != 0 {
		return fmt.Sprintf("id:%d:base_url:%s", account.ID, baseURL)
	}
	if baseURL == "" {
		return ""
	}
	return "base_url:" + baseURL
}

func (a *autoVideoGenerationsAdapter) probeOpenAIEndpoint(ctx context.Context, account *Account, path string) (bool, error) {
	provider, err := a.openAIProvider()
	if err != nil {
		return false, err
	}
	endpoint, err := provider.openAIVideoEndpoint(account, path)
	if err != nil {
		return false, err
	}
	return a.probeEndpoint(ctx, account, endpoint)
}

func (a *autoVideoGenerationsAdapter) probeSeedanceEndpoint(ctx context.Context, account *Account, path string) (bool, error) {
	baseURL, err := seedanceBaseURL(account)
	if err != nil {
		return false, err
	}
	if a != nil && a.provider != nil && a.provider.openai != nil && a.provider.openai.cfg != nil {
		validatedURL, err := a.provider.openai.validateUpstreamBaseURL(baseURL)
		if err != nil {
			return false, err
		}
		baseURL = validatedURL
	}
	endpoint, err := buildSeedanceEndpoint(baseURL, path)
	if err != nil {
		return false, err
	}
	return a.probeEndpoint(ctx, account, endpoint)
}

func (a *autoVideoGenerationsAdapter) probeEndpoint(ctx context.Context, account *Account, endpoint string) (bool, error) {
	provider, err := a.openAIProvider()
	if err != nil {
		return false, err
	}
	token, err := provider.openAIVideoToken(ctx, account)
	if err != nil {
		return false, err
	}
	body := []byte(`{"model":"` + autoVideoGenerationsProbeModel + `"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := provider.do(req, account)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}
	return autoVideoGenerationsProbeMatches(resp.StatusCode, resp.Header.Get("Content-Type"), raw)
}

func (a *autoVideoGenerationsAdapter) openAIProvider() (*openAICompatibleVideoProvider, error) {
	if a == nil || a.provider == nil {
		return nil, errors.New("video adapter provider is required")
	}
	return a.provider, nil
}

func autoVideoGenerationsProbeMatches(statusCode int, contentType string, raw []byte) (bool, error) {
	switch statusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return false, nil
	}
	if statusCode == http.StatusNotFound || statusCode == http.StatusMethodNotAllowed || statusCode >= http.StatusInternalServerError {
		return false, nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false, nil
	}
	if !strings.Contains(strings.ToLower(contentType), "json") && !bytes.HasPrefix(trimmed, []byte("{")) {
		return false, nil
	}
	var payload map[string]any
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return false, nil
	}
	if statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices {
		if code, ok := payload["code"].(float64); ok && code != 0 {
			return true, nil
		}
		if _, ok := payload["error"]; ok {
			return true, nil
		}
		return false, errors.New("auto video adapter probe unexpectedly succeeded; refusing to create with uncertain protocol")
	}
	return statusCode == http.StatusBadRequest || statusCode == http.StatusUnprocessableEntity, nil
}

func videoAdapterAccountID(account *Account) int64 {
	if account == nil {
		return 0
	}
	return account.ID
}
