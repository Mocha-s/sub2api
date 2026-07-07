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
	forwardBody, err := copyValidOpenAIVideoCreateJSON(body)
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

	resp, err := p.do(req, account)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
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
	defer resp.Body.Close()
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
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
		defer resp.Body.Close()
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

func openAIVideoEndpoint(account *Account, path string) string {
	return buildOpenAIVideoEndpoint(openAIVideoBaseURL(account), path)
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

func copyValidOpenAIVideoCreateJSON(body []byte) ([]byte, error) {
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, fmt.Errorf("invalid video create JSON: %w", err)
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, errors.New("video create JSON body must be an object")
	}
	return append([]byte(nil), body...), nil
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
