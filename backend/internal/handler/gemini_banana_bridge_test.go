//go:build unit

package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type geminiBananaBridgeAccountRepo struct {
	service.AccountRepository
	accounts []service.Account
}

func (r geminiBananaBridgeAccountRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, _ int64, platform string) ([]service.Account, error) {
	return r.accountsForPlatform(platform), nil
}

func (r geminiBananaBridgeAccountRepo) ListSchedulableByGroupIDAndPlatforms(_ context.Context, _ int64, platforms []string) ([]service.Account, error) {
	return r.accountsForPlatforms(platforms), nil
}

func (r geminiBananaBridgeAccountRepo) ListSchedulableByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	return r.accountsForPlatform(platform), nil
}

func (r geminiBananaBridgeAccountRepo) ListSchedulableUngroupedByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	return r.accountsForPlatform(platform), nil
}

func (r geminiBananaBridgeAccountRepo) accountsForPlatform(platform string) []service.Account {
	return r.accountsForPlatforms([]string{platform})
}

func (r geminiBananaBridgeAccountRepo) accountsForPlatforms(platforms []string) []service.Account {
	allowed := make(map[string]struct{}, len(platforms))
	for _, platform := range platforms {
		allowed[platform] = struct{}{}
	}
	out := make([]service.Account, 0, len(r.accounts))
	for _, account := range r.accounts {
		if _, ok := allowed[account.Platform]; ok {
			out = append(out, account)
		}
	}
	return out
}

type geminiBananaBridgeGroupRepo struct {
	service.GroupRepository
	group *service.Group
}

func (r geminiBananaBridgeGroupRepo) GetByID(_ context.Context, _ int64) (*service.Group, error) {
	return r.group, nil
}

func (r geminiBananaBridgeGroupRepo) GetByIDLite(ctx context.Context, id int64) (*service.Group, error) {
	return r.GetByID(ctx, id)
}

type geminiBananaBridgeHTTPUpstream struct {
	service.HTTPUpstream
	lastBody        []byte
	lastPath        string
	lastContentType string
	lastAccountID   int64
	lastURL         string
	accountIDs      []int64
	failAccountID   int64
	catalog         string
}

func (u *geminiBananaBridgeHTTPUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	if req.Method == http.MethodGet {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(u.catalog))}, nil
	}
	body, _ := io.ReadAll(req.Body)
	u.lastBody = body
	u.lastPath = req.URL.Path
	u.lastContentType = req.Header.Get("Content-Type")
	u.lastAccountID = accountID
	u.lastURL = req.URL.String()
	u.accountIDs = append(u.accountIDs, accountID)
	if strings.Contains(req.URL.Path, "/v1beta/models/") {
		return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"Invalid request. Please check the request parameters."}}`))}, nil
	}
	if u.failAccountID > 0 && accountID == u.failAccountID {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(`{"error":{"message":"temporary upstream failure"}}`)),
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"X-Request-Id": []string{"req_gemini_banana"},
		},
		Body: io.NopCloser(bytes.NewBufferString(`{"created":1710000007,"data":[{"b64_json":"QUJD"}],"usage":{"input_tokens":2,"output_tokens":3,"output_tokens_details":{"image_tokens":3}},"output_format":"png"}`)),
	}, nil
}

func TestGatewayHandlerGeminiBananaBridgeReturnsGeminiJSON(t *testing.T) {
	testGatewayHandlerGeminiImageBridge(t, "gemini-nano-banana-2.1", true)
}

func TestGatewayHandlerGeminiDeclaredImagesReturnsGeminiJSON(t *testing.T) {
	for _, reference := range []bool{false, true} {
		t.Run(fmt.Sprintf("reference=%t", reference), func(t *testing.T) {
			testGatewayHandlerGeminiImageBridge(t, "gemini-3.1-flash-image-preview", reference)
		})
	}
}

func testGatewayHandlerGeminiImageBridge(t *testing.T, model string, reference bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	groupID := int64(441)
	account := service.Account{
		ID:          9,
		Name:        "openai-banana",
		Platform:    service.PlatformGemini,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"api_key":       "test-key",
			"base_url":      "https://compatible.example/v1",
			"model_mapping": map[string]any{model: "models/" + model},
		},
		AccountGroups: []service.AccountGroup{{GroupID: groupID}},
	}
	fallbackAccount := account
	fallbackAccount.ID = 10
	fallbackAccount.Priority = 2
	oauthAccount := account
	oauthAccount.ID = 8
	oauthAccount.Type = service.AccountTypeOAuth
	oauthAccount.Priority = 0
	oauthAccount.Credentials = map[string]any{
		"access_token":  "oauth-token",
		"model_mapping": map[string]any{model: "models/" + model},
	}
	if !service.IsGeminiBananaBridgeModel(model) {
		oauthAccount.Priority = 99
	}
	account.Priority = 1
	upstream := &geminiBananaBridgeHTTPUpstream{catalog: `{"data":[{"id":"` + model + `","supported_endpoint_types":["openai-image"]}]}`}
	openAIService := service.NewOpenAIGatewayService(
		geminiBananaBridgeAccountRepo{accounts: []service.Account{oauthAccount, account, fallbackAccount}},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		&config.Config{RunMode: config.RunModeSimple},
		nil,
		nil,
		nil,
		nil,
		nil,
		upstream,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	gatewayService := service.NewGatewayService(
		geminiBananaBridgeAccountRepo{accounts: []service.Account{oauthAccount, account, fallbackAccount}},
		geminiBananaBridgeGroupRepo{group: &service.Group{ID: groupID, Platform: service.PlatformGemini, Status: service.StatusActive, AllowImageGeneration: true}},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		&config.Config{RunMode: config.RunModeSimple},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	billingCacheService := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, &config.Config{RunMode: config.RunModeSimple}, nil)
	t.Cleanup(billingCacheService.Stop)
	h := &GatewayHandler{
		gatewayService:           gatewayService,
		openAIGatewayService:     openAIService,
		geminiCompatService:      service.NewGeminiMessagesCompatService(nil, nil, nil, nil, nil, nil, upstream, nil, &config.Config{}),
		billingCacheService:      billingCacheService,
		concurrencyHelper:        &ConcurrencyHelper{concurrencyService: service.NewConcurrencyService(nil)},
		maxAccountSwitchesGemini: 3,
		cfg:                      &config.Config{Gateway: config.GatewayConfig{ImageNonstreamKeepaliveInterval: 10}},
	}
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"draw a cat"},{"inlineData":{"mimeType":"image/png","data":"QUJD"}}]}],"generationConfig":{"imageConfig":{"imageSize":"4K","aspectRatio":"9:16"}}}`)
	if !reference {
		body = []byte(`{"contents":[{"role":"user","parts":[{"text":"draw a cat"}]}],"generationConfig":{"imageConfig":{"imageSize":"4K","aspectRatio":"9:16"}}}`)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+model+":generateContent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "modelAction", Value: model + ":generateContent"}}
	c.Request = req
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      19,
		GroupID: &groupID,
		Group: &service.Group{
			ID:                   groupID,
			Platform:             service.PlatformGemini,
			AllowImageGeneration: true,
		},
		User: &service.User{ID: 29},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 29, Concurrency: 0})

	h.GeminiV1BetaModels(c)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "image/png", gjson.GetBytes(rec.Body.Bytes(), "candidates.0.content.parts.0.inlineData.mimeType").String())
	require.Equal(t, "QUJD", gjson.GetBytes(rec.Body.Bytes(), "candidates.0.content.parts.0.inlineData.data").String())
	require.Equal(t, []int64{9}, upstream.accountIDs)
	if !reference {
		require.Equal(t, "/v1/images/generations", upstream.lastPath)
		require.Equal(t, "/v1/images/generations", GetUpstreamEndpoint(c, service.PlatformGemini))
		require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "model").String())
		return
	}
	require.Equal(t, "/v1/images/edits", upstream.lastPath)
	require.Equal(t, "/v1/images/edits", GetUpstreamEndpoint(c, service.PlatformGemini))
	require.Equal(t, "https://compatible.example/v1/images/edits", upstream.lastURL)
	mediaType, params, err := mime.ParseMediaType(upstream.lastContentType)
	require.NoError(t, err)
	require.Equal(t, "multipart/form-data", mediaType)
	form, err := multipart.NewReader(bytes.NewReader(upstream.lastBody), params["boundary"]).ReadForm(int64(len(upstream.lastBody)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = form.RemoveAll() })
	require.Equal(t, []string{model}, form.Value["model"])
	require.Equal(t, []string{"draw a cat"}, form.Value["prompt"])
	require.Equal(t, []string{"9:16"}, form.Value["size"])
	require.Equal(t, []string{"4k"}, form.Value["quality"])
	require.Len(t, form.File["image"], 1)
}

func TestGeminiImagesBridgeRequiresImagePermission(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{Group: &service.Group{AllowImageGeneration: false}})
	h := &GatewayHandler{}
	result, err := h.forwardGeminiBananaBridge(context.Background(), c, nil, "gemini-3.1-flash-image-preview", nil, service.ChannelMappingResult{})
	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusForbidden, rec.Code)
}
