//go:build unit

package handler

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
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
}

func (u *geminiBananaBridgeHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	u.lastBody = body
	u.lastPath = req.URL.Path
	u.lastContentType = req.Header.Get("Content-Type")
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
			"model_mapping": map[string]any{"nano-banana-pro-4k": "nano-banana-pro-4k"},
		},
		AccountGroups: []service.AccountGroup{{GroupID: groupID}},
	}
	upstream := &geminiBananaBridgeHTTPUpstream{}
	openAIService := service.NewOpenAIGatewayService(
		geminiBananaBridgeAccountRepo{accounts: []service.Account{account}},
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
		geminiBananaBridgeAccountRepo{accounts: []service.Account{account}},
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
		gatewayService:       gatewayService,
		openAIGatewayService: openAIService,
		billingCacheService:  billingCacheService,
		concurrencyHelper:    &ConcurrencyHelper{concurrencyService: service.NewConcurrencyService(nil)},
		cfg:                  &config.Config{Gateway: config.GatewayConfig{ImageNonstreamKeepaliveInterval: 10}},
	}
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"draw a cat"},{"inlineData":{"mimeType":"image/png","data":"QUJD"}}]}],"generationConfig":{"imageConfig":{"imageSize":"4K","aspectRatio":"9:16"}}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/nano-banana-pro-4k:generateContent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "modelAction", Value: "nano-banana-pro-4k:generateContent"}}
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

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "image/png", gjson.GetBytes(rec.Body.Bytes(), "candidates.0.content.parts.0.inlineData.mimeType").String())
	require.Equal(t, "QUJD", gjson.GetBytes(rec.Body.Bytes(), "candidates.0.content.parts.0.inlineData.data").String())
	require.Equal(t, "/v1/images/edits", upstream.lastPath)
	mediaType, params, err := mime.ParseMediaType(upstream.lastContentType)
	require.NoError(t, err)
	require.Equal(t, "multipart/form-data", mediaType)
	form, err := multipart.NewReader(bytes.NewReader(upstream.lastBody), params["boundary"]).ReadForm(int64(len(upstream.lastBody)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = form.RemoveAll() })
	require.Equal(t, []string{"nano-banana-pro-4k"}, form.Value["model"])
	require.Equal(t, []string{"draw a cat"}, form.Value["prompt"])
	require.Equal(t, []string{"4K"}, form.Value["size"])
	require.Equal(t, []string{"9:16"}, form.Value["aspect_ratio"])
	require.Len(t, form.File["image"], 1)
	require.True(t, service.OpenAIImagesJSONKeepalivePresent(c))
}
