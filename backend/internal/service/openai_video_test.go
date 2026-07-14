//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

func TestOpenAICompatibleVideoProviderCreateSendsRequestAndParsesResponse(t *testing.T) {
	body := []byte(`{"model":"video-ds-2.0-fast","prompt":"slow dolly","seconds":"15","aspect_ratio":"9:16"}`)
	contentType := "application/json; charset=utf-8"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/videos" {
			t.Errorf("path = %s, want /v1/videos", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-video" {
			t.Errorf("Authorization = %q, want bearer token", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		gotBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll request body: %v", err)
		}
		if !bytes.Equal(gotBody, body) {
			t.Errorf("body = %s, want %s", gotBody, body)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "req_create_123")
		_, _ = io.WriteString(w, `{"id":"video_123","status":"processing","progress":37,"metadata":{"url":"https://cdn.example/video.mp4"}}`)
	}))
	defer server.Close()

	provider := NewOpenAICompatibleVideoProvider(server.Client())
	account := &Account{Credentials: map[string]any{"base_url": server.URL, "api_key": "sk-video"}}

	result, err := provider.Create(context.Background(), account, body, contentType, "")
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if result.ProviderTaskID != "video_123" {
		t.Fatalf("ProviderTaskID = %q, want video_123", result.ProviderTaskID)
	}
	if result.Status != VideoTaskStatusInProgress {
		t.Fatalf("Status = %q, want %q", result.Status, VideoTaskStatusInProgress)
	}
	if result.ProviderStatus != "processing" {
		t.Fatalf("ProviderStatus = %q, want processing", result.ProviderStatus)
	}
	if got := result.Metadata["progress"]; got != float64(37) {
		t.Fatalf("Metadata[progress] = %#v, want 37", got)
	}
	if got := result.Metadata["result_url"]; got != "https://cdn.example/video.mp4" {
		t.Fatalf("Metadata[result_url] = %#v, want result URL", got)
	}
	if got := result.Metadata["request_id"]; got != "req_create_123" {
		t.Fatalf("Metadata[request_id] = %#v, want x-request-id", got)
	}
	response, ok := result.Metadata["response"].(map[string]any)
	if !ok {
		t.Fatalf("Metadata[response] = %#v, want object", result.Metadata["response"])
	}
	if response["id"] != "video_123" {
		t.Fatalf("response id = %#v, want video_123", response["id"])
	}
}

func TestOpenAICompatibleVideoProviderForGatewayUsesGatewayTokenProxyAndHTTPUpstream(t *testing.T) {
	proxyID := int64(8)
	upstream := &openAIVideoHTTPUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"X-Request-Id": []string{"req_gateway_video"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"video_gateway","status":"queued"}`)),
	}}
	openai := &OpenAIGatewayService{
		httpUpstream:        upstream,
		openAITokenProvider: NewOpenAITokenProvider(nil, &openAIVideoTokenCache{token: "gateway-token"}, nil),
	}
	provider := NewOpenAICompatibleVideoProviderForGateway(openai)
	account := &Account{
		ID:          123,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 7,
		Credentials: map[string]any{
			"base_url":     "https://upstream.example/v1",
			"access_token": "direct-token",
		},
		ProxyID: &proxyID,
		Proxy: &Proxy{
			Protocol: "http",
			Host:     "proxy.example",
			Port:     8080,
			Username: "user",
			Password: "pass",
		},
	}

	result, err := provider.Create(context.Background(), account, []byte(`{"model":"video-ds-2.0","prompt":"slow dolly","seconds":"15","aspect_ratio":"9:16"}`), "application/json", "ignored-upstream-model")

	require.NoError(t, err)
	require.Equal(t, "video_gateway", result.ProviderTaskID)
	require.NotNil(t, upstream.lastReq)
	require.True(t, upstream.usedTLSDo, "gateway provider should use HTTPUpstream.DoWithTLS")
	require.Equal(t, http.MethodPost, upstream.lastReq.Method)
	require.Equal(t, "https://upstream.example/v1/videos", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer gateway-token", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "application/json", upstream.lastReq.Header.Get("Content-Type"))
	require.Equal(t, "http://user:pass@proxy.example:8080", upstream.lastProxyURL)
	require.Equal(t, int64(123), upstream.lastAccountID)
	require.Equal(t, 7, upstream.lastAccountConcurrency)
}

func TestOpenAICompatibleVideoProviderCreateReplacesMappedUpstreamModel(t *testing.T) {
	body := []byte(`{"model":"video-ds-2.0-fast","prompt":"slow dolly","seconds":"15","aspect_ratio":"9:16"}`)
	originalBody := append([]byte(nil), body...)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		gotBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll request body: %v", err)
		}
		require.JSONEq(t, `{"model":"upstream-video","prompt":"slow dolly","seconds":"15","aspect_ratio":"9:16"}`, string(gotBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"video_model_preserve","status":"queued"}`)
	}))
	defer server.Close()

	provider := NewOpenAICompatibleVideoProvider(server.Client())
	account := &Account{Credentials: map[string]any{"base_url": server.URL, "api_key": "sk-video"}}

	if _, err := provider.Create(context.Background(), account, body, "application/json", "upstream-video"); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if !bytes.Equal(body, originalBody) {
		t.Fatalf("Create mutated caller body: got %s, want %s", body, originalBody)
	}
}

func TestOpenAICompatibleVideoProviderCreateRejectsInvalidJSON(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	provider := NewOpenAICompatibleVideoProvider(server.Client())
	account := &Account{Credentials: map[string]any{"base_url": server.URL, "api_key": "sk-video"}}

	_, err := provider.Create(context.Background(), account, []byte(`{"model":`), "application/json", "upstream-video")
	if err == nil {
		t.Fatal("Create returned nil error")
	}
	if !strings.Contains(err.Error(), "invalid video create JSON") {
		t.Fatalf("Create error = %q, want invalid JSON context", err.Error())
	}
	if called {
		t.Fatal("upstream was called for invalid JSON body")
	}
}

func TestOpenAICompatibleVideoProviderCreateRejectsNullJSON(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	provider := NewOpenAICompatibleVideoProvider(server.Client())
	account := &Account{Credentials: map[string]any{"base_url": server.URL, "api_key": "sk-video"}}

	_, err := provider.Create(context.Background(), account, []byte(`null`), "application/json", "upstream-video")
	if err == nil {
		t.Fatal("Create returned nil error")
	}
	if !strings.Contains(err.Error(), "video create JSON body must be an object") {
		t.Fatalf("Create error = %q, want object validation context", err.Error())
	}
	if called {
		t.Fatal("upstream was called for null JSON body")
	}
}

func TestOpenAICompatibleVideoProviderBaseURLWithV1SuffixDoesNotDuplicateVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/videos" {
			t.Errorf("path = %s, want /v1/videos", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"video_base_url","status":"queued"}`)
	}))
	defer server.Close()

	provider := NewOpenAICompatibleVideoProvider(server.Client())
	account := &Account{Credentials: map[string]any{"base_url": server.URL + "/v1/", "api_key": "sk-video"}}

	if _, err := provider.Create(context.Background(), account, []byte(`{"model":"sora-2","prompt":"slow dolly"}`), "application/json", "sora-2"); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
}

func TestOpenAICompatibleVideoProviderFetchParsesStatusProgressAndResultURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/v1/videos/video_456" {
			t.Errorf("path = %s, want /v1/videos/video_456", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer pat-video" {
			t.Errorf("Authorization = %q, want access token", got)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"task_id":"video_456","status":"completed","progress":100,"metadata":{"url":"https://cdn.example/final.mp4"}}`)
	}))
	defer server.Close()

	provider := NewOpenAICompatibleVideoProvider(server.Client())
	account := &Account{Credentials: map[string]any{"base_url": server.URL, "access_token": "pat-video"}}
	task := &VideoTask{ProviderTaskID: "video_456"}

	result, err := provider.Fetch(context.Background(), account, task)
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if result.ProviderTaskID != "video_456" {
		t.Fatalf("ProviderTaskID = %q, want video_456", result.ProviderTaskID)
	}
	if result.Status != VideoTaskStatusCompleted {
		t.Fatalf("Status = %q, want %q", result.Status, VideoTaskStatusCompleted)
	}
	if result.ProviderStatus != "completed" {
		t.Fatalf("ProviderStatus = %q, want completed", result.ProviderStatus)
	}
	if got := result.Metadata["progress"]; got != float64(100) {
		t.Fatalf("Metadata[progress] = %#v, want 100", got)
	}
	if got := result.Metadata["result_url"]; got != "https://cdn.example/final.mp4" {
		t.Fatalf("Metadata[result_url] = %#v, want result URL", got)
	}
}

func TestOpenAICompatibleVideoProviderContentForwardsRangeAndStreamsBody(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/v1/videos/video_789/content" {
			t.Errorf("path = %s, want /v1/videos/video_789/content", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-video" {
			t.Errorf("Authorization = %q, want bearer token", got)
		}
		if got := r.Header.Get("Range"); got != "bytes=10-19" {
			t.Errorf("Range = %q, want bytes=10-19", got)
		}

		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", "10")
		w.Header().Set("Content-Range", "bytes 10-19/100")
		w.Header().Set("X-Upstream", "kept")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("frame-"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-release
		_, _ = w.Write([]byte("tail"))
	}))
	defer server.Close()
	defer close(release)

	provider := NewOpenAICompatibleVideoProvider(server.Client())
	account := &Account{Credentials: map[string]any{"base_url": server.URL, "api_key": "sk-video"}}
	task := &VideoTask{ProviderTaskID: "video_789"}
	headers := http.Header{"Range": []string{"bytes=10-19"}}

	type contentResult struct {
		stream *VideoContentStream
		err    error
	}
	done := make(chan contentResult, 1)
	go func() {
		stream, err := provider.Content(context.Background(), account, task, headers)
		done <- contentResult{stream: stream, err: err}
	}()

	var result contentResult
	select {
	case result = <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Content did not return before upstream body completed")
	}
	if result.err != nil {
		t.Fatalf("Content returned error: %v", result.err)
	}
	defer func() { _ = result.stream.Body.Close() }()

	if result.stream.StatusCode != http.StatusPartialContent {
		t.Fatalf("StatusCode = %d, want 206", result.stream.StatusCode)
	}
	if result.stream.ContentType != "video/mp4" {
		t.Fatalf("ContentType = %q, want video/mp4", result.stream.ContentType)
	}
	if result.stream.ContentLength != 10 {
		t.Fatalf("ContentLength = %d, want 10", result.stream.ContentLength)
	}
	if result.stream.Headers["Content-Range"] != "bytes 10-19/100" {
		t.Fatalf("Content-Range header = %#v, want upstream value", result.stream.Headers["Content-Range"])
	}
	if result.stream.Headers["X-Upstream"] != "kept" {
		t.Fatalf("X-Upstream header = %#v, want kept", result.stream.Headers["X-Upstream"])
	}
	buf := make([]byte, len("frame-"))
	if _, err := io.ReadFull(result.stream.Body, buf); err != nil {
		t.Fatalf("ReadFull content body: %v", err)
	}
	if string(buf) != "frame-" {
		t.Fatalf("body prefix = %q, want frame-", buf)
	}
}

func TestValidateVideoResultURLUsesConfiguredUpstreamAllowlist(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
		Enabled:       true,
		UpstreamHosts: []string{"cdn.example"},
	}}}}

	validated, err := validateVideoResultURL(svc, "https://cdn.example/video.mp4")
	require.NoError(t, err)
	require.Equal(t, "https://cdn.example/video.mp4", validated)
	_, err = validateVideoResultURL(svc, "https://internal.example/video.mp4")
	require.Error(t, err)
}

func TestOpenAICompatibleVideoProviderCreateRequiresUpstreamTaskID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"queued","progress":0}`)
	}))
	defer server.Close()

	provider := NewOpenAICompatibleVideoProvider(server.Client())
	account := &Account{Credentials: map[string]any{"base_url": server.URL, "api_key": "sk-video"}}

	_, err := provider.Create(context.Background(), account, []byte(`{"model":"sora-2"}`), "application/json", "sora-2")
	if err == nil {
		t.Fatal("Create returned nil error")
	}
	if !strings.Contains(err.Error(), "id") || !strings.Contains(err.Error(), "task_id") {
		t.Fatalf("Create error = %q, want mention id or task_id", err.Error())
	}
}

func TestOpenAICompatibleVideoProviderNon2XXErrorIncludesStatusWithoutToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":"rate limited","api_key":"sk-video"}`)
	}))
	defer server.Close()

	provider := NewOpenAICompatibleVideoProvider(server.Client())
	account := &Account{Credentials: map[string]any{"base_url": server.URL, "api_key": "sk-video"}}
	task := &VideoTask{ProviderTaskID: "video_429"}

	_, err := provider.Fetch(context.Background(), account, task)
	if err == nil {
		t.Fatal("Fetch returned nil error")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Fatalf("Fetch error = %q, want status code", err.Error())
	}
	if strings.Contains(err.Error(), "sk-video") {
		t.Fatalf("Fetch error leaked token: %q", err.Error())
	}
	var upstreamErr *OpenAIVideoUpstreamError
	require.True(t, errors.As(err, &upstreamErr), "Fetch error should expose typed upstream error")
	require.Equal(t, http.StatusTooManyRequests, upstreamErr.StatusCode)
	require.NotContains(t, upstreamErr.Body, "sk-video")
}

func TestOpenAICompatibleVideoProviderCreateNon2XXReturnsTypedSanitizedError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"bad request for sk-video","type":"invalid_request_error"}}`)
	}))
	defer server.Close()

	provider := NewOpenAICompatibleVideoProvider(server.Client())
	account := &Account{Credentials: map[string]any{"base_url": server.URL, "api_key": "sk-video"}}

	_, err := provider.Create(context.Background(), account, []byte(`{"model":"sora-2","prompt":"slow dolly"}`), "application/json", "sora-2")
	require.Error(t, err)
	var upstreamErr *OpenAIVideoUpstreamError
	require.True(t, errors.As(err, &upstreamErr), "Create error should expose typed upstream error")
	require.Equal(t, http.StatusBadRequest, upstreamErr.StatusCode)
	require.NotContains(t, upstreamErr.Body, "sk-video")
}

func TestOpenAICompatibleVideoProviderForGatewayUsesTokenProxyAndHTTPUpstream(t *testing.T) {
	upstream := &openAIVideoHTTPUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{"req_gateway"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"video_gateway","status":"queued"}`)),
	}}
	provider := NewOpenAICompatibleVideoProviderForGateway(&OpenAIGatewayService{httpUpstream: upstream})
	proxyID := int64(5)
	account := &Account{
		ID:          99,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 7,
		ProxyID:     &proxyID,
		Proxy:       &Proxy{Protocol: "http", Host: "proxy.test", Port: 8080},
		Credentials: map[string]any{"base_url": "https://upstream.example/v1/", "api_key": "sk-gateway"},
	}

	result, err := provider.Create(context.Background(), account, []byte(`{"model":"client-video","prompt":"slow dolly"}`), "application/json", "upstream-video")
	require.NoError(t, err)
	require.Equal(t, "video_gateway", result.ProviderTaskID)
	require.True(t, upstream.usedTLSDo)
	require.Equal(t, "http://proxy.test:8080", upstream.lastProxyURL)
	require.Equal(t, int64(99), upstream.lastAccountID)
	require.Equal(t, 7, upstream.lastAccountConcurrency)
	require.Equal(t, "Bearer sk-gateway", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "/v1/videos", upstream.lastReq.URL.Path)
	require.Equal(t, HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileFromContext(upstream.lastReq.Context()))
	var payload map[string]any
	require.NoError(t, json.Unmarshal(upstream.lastBody, &payload))
	require.Equal(t, "upstream-video", payload["model"])
}

func TestOpenAICompatibleVideoProviderForGatewayValidatesBaseURL(t *testing.T) {
	upstream := &openAIVideoHTTPUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"id":"video_should_not_call","status":"queued"}`)),
	}}
	provider := NewOpenAICompatibleVideoProviderForGateway(&OpenAIGatewayService{
		cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
			Enabled:       true,
			UpstreamHosts: []string{"allowed.example"},
		}}},
		httpUpstream: upstream,
	})
	account := &Account{
		ID:          99,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "http://127.0.0.1", "api_key": "sk-gateway"},
	}

	_, err := provider.Create(context.Background(), account, []byte(`{"model":"client-video","prompt":"slow dolly"}`), "application/json", "upstream-video")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid base_url")
	require.Nil(t, upstream.lastReq)
}

func TestResolveVideoTaskPricingRetainsSupportedPricingModesAndSelectsModeSpecificMultiplier(t *testing.T) {
	groupID := int64(42)
	const (
		requestMultiplier = 0.1065
		videoMultiplier   = 0.0817
	)

	for _, tt := range []struct {
		name           string
		billingMode    BillingMode
		retained       bool
		wantMultiplier float64
	}{
		{name: "per-request keeps normal request multiplier", billingMode: BillingModePerRequest, retained: true, wantMultiplier: requestMultiplier},
		{name: "video keeps independent video multiplier", billingMode: BillingModeVideo, retained: true, wantMultiplier: videoMultiplier},
		{name: "token pricing is discarded", billingMode: BillingModeToken},
		{name: "image pricing is discarded", billingMode: BillingModeImage},
	} {
		t.Run(tt.name, func(t *testing.T) {
			price := 65.0
			channel := &Channel{ID: 7, Status: StatusActive, BillingModelSource: BillingModelSourceRequested}
			cache := newEmptyChannelCache()
			cache.loadedAt = time.Now()
			cache.channelByGroupID[groupID] = channel
			cache.groupPlatform[groupID] = PlatformOpenAI
			cache.pricingByGroupModel[channelModelKey{groupID: groupID, platform: PlatformOpenAI, model: "sora-2"}] = &ChannelModelPricing{
				BillingMode:     tt.billingMode,
				PerRequestPrice: &price,
			}
			channelService := &ChannelService{}
			channelService.cache.Store(cache)
			service := &OpenAIGatewayService{
				cfg:            &config.Config{Default: config.DefaultConfig{RateMultiplier: 0.25}},
				channelService: channelService,
			}

			selection := service.ResolveVideoTaskPricing(context.Background(), VideoTaskPricingResolveInput{
				GroupID: groupID,
				UserID:  99,
				APIKey: &APIKey{
					GroupID: &groupID,
					Group: &Group{
						RateMultiplier:       requestMultiplier,
						VideoRateIndependent: true,
						VideoRateMultiplier:  videoMultiplier,
					},
				},
				Account:        &Account{},
				RequestedModel: "sora-2",
			})

			if !tt.retained {
				require.Nil(t, selection.Pricing)
				return
			}
			require.NotNil(t, selection.Pricing)
			require.Equal(t, tt.billingMode, selection.Pricing.BillingMode)
			require.InDelta(t, tt.wantMultiplier, selection.RateMultiplier, 1e-12)
		})
	}
}

func TestResolveVideoTaskPricingUsesInputGroupForUserRate(t *testing.T) {
	pricingGroupID := int64(42)
	apiKeyGroupID := int64(43)
	userRate := 0.1065
	price := 65.0
	channel := &Channel{ID: 7, Status: StatusActive, BillingModelSource: BillingModelSourceRequested}
	cache := newEmptyChannelCache()
	cache.loadedAt = time.Now()
	cache.channelByGroupID[pricingGroupID] = channel
	cache.groupPlatform[pricingGroupID] = PlatformOpenAI
	cache.pricingByGroupModel[channelModelKey{groupID: pricingGroupID, platform: PlatformOpenAI, model: "sora-2"}] = &ChannelModelPricing{
		BillingMode:     BillingModePerRequest,
		PerRequestPrice: &price,
	}
	channelService := &ChannelService{}
	channelService.cache.Store(cache)
	rateRepo := &openAIVideoRateResolverRepoStub{rate: &userRate}
	service := &OpenAIGatewayService{
		cfg:                   &config.Config{Default: config.DefaultConfig{RateMultiplier: 0.25}},
		channelService:        channelService,
		userGroupRateResolver: newUserGroupRateResolver(rateRepo, nil, time.Minute, nil, "service.openai_video.test"),
	}

	selection := service.ResolveVideoTaskPricing(context.Background(), VideoTaskPricingResolveInput{
		GroupID: pricingGroupID,
		UserID:  99,
		APIKey: &APIKey{
			GroupID: &apiKeyGroupID,
			Group:   &Group{RateMultiplier: 0.5},
		},
		Account:        &Account{},
		RequestedModel: "sora-2",
	})

	require.NotNil(t, selection.Pricing)
	require.InDelta(t, userRate, selection.RateMultiplier, 1e-12)
	require.Equal(t, pricingGroupID, rateRepo.groupID)
}

func TestResolveVideoTaskPricingUsesInputGroupForUserRateWhenAPIKeyGroupIDMissing(t *testing.T) {
	groupID := int64(42)
	userRate := 0.1065
	price := 65.0
	channel := &Channel{ID: 7, Status: StatusActive, BillingModelSource: BillingModelSourceRequested}
	cache := newEmptyChannelCache()
	cache.loadedAt = time.Now()
	cache.channelByGroupID[groupID] = channel
	cache.groupPlatform[groupID] = PlatformOpenAI
	cache.pricingByGroupModel[channelModelKey{groupID: groupID, platform: PlatformOpenAI, model: "sora-2"}] = &ChannelModelPricing{
		BillingMode:     BillingModePerRequest,
		PerRequestPrice: &price,
	}
	channelService := &ChannelService{}
	channelService.cache.Store(cache)
	rateRepo := &openAIVideoRateResolverRepoStub{rate: &userRate}
	service := &OpenAIGatewayService{
		cfg:                   &config.Config{Default: config.DefaultConfig{RateMultiplier: 0.25}},
		channelService:        channelService,
		userGroupRateResolver: newUserGroupRateResolver(rateRepo, nil, time.Minute, nil, "service.openai_video.test"),
	}

	selection := service.ResolveVideoTaskPricing(context.Background(), VideoTaskPricingResolveInput{
		GroupID: groupID,
		UserID:  99,
		APIKey: &APIKey{
			Group: &Group{ID: groupID, RateMultiplier: 0.5},
		},
		Account:        &Account{},
		RequestedModel: "sora-2",
	})

	require.NotNil(t, selection.Pricing)
	require.InDelta(t, userRate, selection.RateMultiplier, 1e-12)
	require.Equal(t, groupID, rateRepo.groupID)
}

func TestResolveVideoTaskPricingUsesUserRateForPerRequestAndIndependentRateForVideo(t *testing.T) {
	groupID := int64(42)
	const (
		userRate    = 0.1065
		videoRate   = 0.0817
		defaultRate = 0.25
		groupRate   = 0.5
	)

	for _, tt := range []struct {
		name           string
		billingMode    BillingMode
		wantMultiplier float64
	}{
		{name: "per-request uses user rate", billingMode: BillingModePerRequest, wantMultiplier: userRate},
		{name: "video uses independent rate", billingMode: BillingModeVideo, wantMultiplier: videoRate},
	} {
		t.Run(tt.name, func(t *testing.T) {
			price := 65.0
			channel := &Channel{ID: 7, Status: StatusActive, BillingModelSource: BillingModelSourceRequested}
			cache := newEmptyChannelCache()
			cache.loadedAt = time.Now()
			cache.channelByGroupID[groupID] = channel
			cache.groupPlatform[groupID] = PlatformOpenAI
			cache.pricingByGroupModel[channelModelKey{groupID: groupID, platform: PlatformOpenAI, model: "sora-2"}] = &ChannelModelPricing{
				BillingMode:     tt.billingMode,
				PerRequestPrice: &price,
			}
			channelService := &ChannelService{}
			channelService.cache.Store(cache)
			rateRepo := &openAIVideoRateResolverRepoStub{rate: testPtrFloat64(userRate)}
			service := &OpenAIGatewayService{
				cfg:                   &config.Config{Default: config.DefaultConfig{RateMultiplier: defaultRate}},
				channelService:        channelService,
				userGroupRateResolver: newUserGroupRateResolver(rateRepo, nil, time.Minute, nil, "service.openai_video.test"),
			}

			selection := service.ResolveVideoTaskPricing(context.Background(), VideoTaskPricingResolveInput{
				GroupID: groupID,
				UserID:  99,
				APIKey: &APIKey{
					GroupID: &groupID,
					Group: &Group{
						ID:                   groupID,
						RateMultiplier:       groupRate,
						VideoRateIndependent: true,
						VideoRateMultiplier:  videoRate,
					},
				},
				Account:        &Account{},
				RequestedModel: "sora-2",
			})

			require.NotNil(t, selection.Pricing)
			require.Equal(t, tt.billingMode, selection.Pricing.BillingMode)
			require.InDelta(t, tt.wantMultiplier, selection.RateMultiplier, 1e-12)
			require.Equal(t, groupID, rateRepo.groupID)
		})
	}
}

func TestResolveVideoTaskPricingFallsBackWithoutAPIKeyGroup(t *testing.T) {
	groupID := int64(42)
	const defaultRate = 0.25

	managedAccount := func() *Account {
		accountRate := 2.0
		return &Account{
			RateMultiplier: &accountRate,
			Credentials: map[string]any{
				"pricing_managed_by":    "api-pricing-sync",
				"pricing_markup_factor": 1.25,
			},
		}
	}

	for _, tt := range []struct {
		name                  string
		apiKey                *APIKey
		account               func() *Account
		wantMultiplier        float64
		wantAccountMultiplier float64
	}{
		{name: "nil api key", account: func() *Account { return &Account{} }, wantMultiplier: defaultRate, wantAccountMultiplier: 1},
		{name: "nil api key group", apiKey: &APIKey{}, account: func() *Account { return &Account{} }, wantMultiplier: defaultRate, wantAccountMultiplier: 1},
		{name: "nil api key uses managed account rate", account: managedAccount, wantMultiplier: 2.5, wantAccountMultiplier: 2},
		{name: "nil api key group uses managed account rate", apiKey: &APIKey{}, account: managedAccount, wantMultiplier: 2.5, wantAccountMultiplier: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			price := 65.0
			channel := &Channel{ID: 7, Status: StatusActive, BillingModelSource: BillingModelSourceRequested}
			cache := newEmptyChannelCache()
			cache.loadedAt = time.Now()
			cache.channelByGroupID[groupID] = channel
			cache.groupPlatform[groupID] = PlatformOpenAI
			cache.pricingByGroupModel[channelModelKey{groupID: groupID, platform: PlatformOpenAI, model: "sora-2"}] = &ChannelModelPricing{
				BillingMode:     BillingModePerRequest,
				PerRequestPrice: &price,
			}
			channelService := &ChannelService{}
			channelService.cache.Store(cache)
			service := &OpenAIGatewayService{
				cfg:            &config.Config{Default: config.DefaultConfig{RateMultiplier: defaultRate}},
				channelService: channelService,
			}

			selection := service.ResolveVideoTaskPricing(context.Background(), VideoTaskPricingResolveInput{
				GroupID: groupID, UserID: 99, APIKey: tt.apiKey, Account: tt.account(), RequestedModel: "sora-2",
			})

			require.NotNil(t, selection.Pricing)
			require.Equal(t, BillingModePerRequest, selection.Pricing.BillingMode)
			require.InDelta(t, tt.wantMultiplier, selection.RateMultiplier, 1e-12)
			require.InDelta(t, tt.wantAccountMultiplier, selection.AccountRateMultiplier, 1e-12)
		})
	}
}

func TestResolveVideoTaskPricingAppliesPeakOnlyToPerRequestPricing(t *testing.T) {
	groupID := int64(42)
	const (
		userRate   = 0.1065
		peakRate   = 1.5
		videoRate  = 0.0817
		price      = 65.0
		peakActual = 10.38375
	)
	peakTime := time.Date(2026, 6, 29, 12, 0, 0, 0, timezone.Location())
	peakEndBoundary := time.Date(2026, 6, 29, 13, 0, 0, 0, timezone.Location())

	for _, tt := range []struct {
		name              string
		billingMode       BillingMode
		at                time.Time
		peakStart         string
		peakEnd           string
		wantRate          float64
		wantQuoteActual   float64
		verifyQuoteActual bool
	}{
		{
			name: "peak per-request includes peak multiplier and quote actual cost", billingMode: BillingModePerRequest,
			at: peakTime, peakStart: "11:00", peakEnd: "13:00", wantRate: userRate * peakRate, wantQuoteActual: peakActual, verifyQuoteActual: true,
		},
		{
			name: "peak end boundary per-request keeps user rate", billingMode: BillingModePerRequest,
			at: peakEndBoundary, peakStart: "11:00", peakEnd: "13:00", wantRate: userRate,
		},
		{
			name: "peak video keeps independent video rate", billingMode: BillingModeVideo,
			at: peakTime, peakStart: "11:00", peakEnd: "13:00", wantRate: videoRate,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			modelPrice := price
			channel := &Channel{ID: 7, Status: StatusActive, BillingModelSource: BillingModelSourceRequested}
			cache := newEmptyChannelCache()
			cache.loadedAt = time.Now()
			cache.channelByGroupID[groupID] = channel
			cache.groupPlatform[groupID] = PlatformOpenAI
			cache.pricingByGroupModel[channelModelKey{groupID: groupID, platform: PlatformOpenAI, model: "sora-2"}] = &ChannelModelPricing{
				BillingMode:     tt.billingMode,
				PerRequestPrice: &modelPrice,
			}
			channelService := &ChannelService{}
			channelService.cache.Store(cache)
			rateRepo := &openAIVideoRateResolverRepoStub{rate: testPtrFloat64(userRate)}
			service := &OpenAIGatewayService{
				cfg:                   &config.Config{Default: config.DefaultConfig{RateMultiplier: 0.25}},
				channelService:        channelService,
				userGroupRateResolver: newUserGroupRateResolver(rateRepo, nil, time.Minute, nil, "service.openai_video.test"),
			}

			selection := service.resolveVideoTaskPricingAt(context.Background(), VideoTaskPricingResolveInput{
				GroupID: groupID,
				UserID:  99,
				APIKey: &APIKey{
					GroupID: &groupID,
					Group: &Group{
						ID:                   groupID,
						RateMultiplier:       0.5,
						SubscriptionType:     SubscriptionTypeSubscription,
						PeakRateEnabled:      true,
						PeakStart:            tt.peakStart,
						PeakEnd:              tt.peakEnd,
						PeakRateMultiplier:   peakRate,
						VideoRateIndependent: true,
						VideoRateMultiplier:  videoRate,
					},
				},
				Account:        &Account{},
				RequestedModel: "sora-2",
			}, tt.at)

			require.NotNil(t, selection.Pricing)
			require.Equal(t, tt.billingMode, selection.Pricing.BillingMode)
			require.InDelta(t, tt.wantRate, selection.RateMultiplier, 1e-12)
			require.Equal(t, groupID, rateRepo.groupID)
			if !tt.verifyQuoteActual {
				return
			}

			quote, err := ResolveVideoTaskQuote([]byte(`{}`), selection.BillingModel, selection.Pricing, selection.RateMultiplier, selection.AccountRateMultiplier)
			require.NoError(t, err)
			require.InDelta(t, tt.wantQuoteActual, quote.ActualCostUSD, 1e-12)
		})
	}
}

type openAIVideoHTTPUpstreamRecorder struct {
	lastReq                *http.Request
	lastBody               []byte
	lastProxyURL           string
	lastAccountID          int64
	lastAccountConcurrency int
	lastTLSProfile         *tlsfingerprint.Profile
	usedTLSDo              bool
	resp                   *http.Response
	err                    error
}

type openAIVideoRateResolverRepoStub struct {
	UserGroupRateRepository

	rate    *float64
	groupID int64
}

func (s *openAIVideoRateResolverRepoStub) GetByUserAndGroup(_ context.Context, _, groupID int64) (*float64, error) {
	s.groupID = groupID
	return s.rate, nil
}

func (u *openAIVideoHTTPUpstreamRecorder) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	u.lastReq = req
	u.lastProxyURL = proxyURL
	u.lastAccountID = accountID
	u.lastAccountConcurrency = accountConcurrency
	if req != nil && req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		u.lastBody = append([]byte(nil), body...)
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	if u.err != nil {
		return nil, u.err
	}
	return u.resp, nil
}

func (u *openAIVideoHTTPUpstreamRecorder) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	u.usedTLSDo = true
	u.lastTLSProfile = profile
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

type openAIVideoTokenCache struct {
	token string
}

func (c *openAIVideoTokenCache) GetAccessToken(ctx context.Context, cacheKey string) (string, error) {
	return c.token, nil
}

func (c *openAIVideoTokenCache) SetAccessToken(ctx context.Context, cacheKey string, token string, ttl time.Duration) error {
	c.token = token
	return nil
}

func (c *openAIVideoTokenCache) DeleteAccessToken(ctx context.Context, cacheKey string) error {
	c.token = ""
	return nil
}

func (c *openAIVideoTokenCache) AcquireRefreshLock(ctx context.Context, cacheKey string, ttl time.Duration) (bool, error) {
	return true, nil
}

func (c *openAIVideoTokenCache) ReleaseRefreshLock(ctx context.Context, cacheKey string) error {
	return nil
}
