//go:build unit

package service

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCompatibleImagesGeminiModels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, model := range []string{"gemini-2.5-flash-image", "gemini-2.5-flash-image-preview", "gemini-3-pro-image", "gemini-3.1-flash-image"} {
		t.Run(model, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"draw"}`, model))
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, openAIImagesGenerationsEndpoint, bytes.NewReader(body))
			parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			require.True(t, (&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}).SupportsOpenAIImageCapability(parsed.RequiredCapability))
			for _, typ := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
				require.False(t, (&Account{Platform: PlatformOpenAI, Type: typ}).SupportsOpenAIImageCapability(parsed.RequiredCapability))
			}
			require.False(t, isOpenAIImageGenerationModel(model), "compatible image IDs must not enter native Responses normalization")
		})
	}
	for _, model := range []string{"gemini-2.5-pro", "gemini-2.5-flash", "gemini-3-pro-imageless", "unknown-image", "gpt-5.5"} {
		t.Run("reject_"+model, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"draw"}`, model))
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, openAIImagesGenerationsEndpoint, bytes.NewReader(body))
			_, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
			require.ErrorContains(t, err, "images endpoint requires an image model")
		})
	}
}

func TestNanoBananaImagesRequireOpenAIAPIKeyCapability(t *testing.T) {
	model := "nano-banana-pro-4k"
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, openAIImagesGenerationsEndpoint, strings.NewReader(fmt.Sprintf(`{"model":%q,"prompt":"draw"}`, model)))
	parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, []byte(fmt.Sprintf(`{"model":%q,"prompt":"draw"}`, model)))
	require.NoError(t, err)
	require.Equal(t, OpenAIImagesCapabilityAPIKey, parsed.RequiredCapability)
	require.True(t, (&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}).SupportsOpenAIImageCapability(parsed.RequiredCapability))
	require.False(t, (&Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}).SupportsOpenAIImageCapability(parsed.RequiredCapability))
	require.True(t, (&Account{Platform: PlatformGemini, Type: AccountTypeAPIKey}).SupportsOpenAIImageCapability(parsed.RequiredCapability))
}

func TestCompatibleImagesForwardGemini(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, kind := range []string{"generation", "json_edit", "multipart_edit", "composite_multipart_alias", "channel_mapping", "account_mapping"} {
		t.Run(kind, func(t *testing.T) {
			model := "gemini-3.1-flash-image"
			endpoint := openAIImagesGenerationsEndpoint
			contentType := "application/json"
			channelModel := ""
			credentials := map[string]any{"api_key": "image-key", "base_url": "https://compatible.example/v1"}
			requestModel := model
			if kind == "channel_mapping" {
				requestModel, channelModel = "gpt-image-2", model
			}
			if kind == "account_mapping" {
				requestModel = "gpt-image-2"
				credentials["model_mapping"] = map[string]any{requestModel: model}
			}
			body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"draw","size":"1024x1024","custom_field":"preserved"}`, requestModel))
			if kind == "json_edit" {
				endpoint = openAIImagesEditsEndpoint
				body = []byte(fmt.Sprintf(`{"model":%q,"prompt":"draw","images":[{"image_url":"https://source.example/input.png"}],"custom_field":"preserved"}`, model))
			}
			if strings.Contains(kind, "multipart") {
				endpoint = openAIImagesEditsEndpoint
				if kind == "composite_multipart_alias" {
					requestModel = "public-image"
				}
				var buf bytes.Buffer
				writer := multipart.NewWriter(&buf)
				require.NoError(t, writer.WriteField("model", requestModel))
				require.NoError(t, writer.WriteField("prompt", "draw"))
				require.NoError(t, writer.WriteField("custom_field", "preserved"))
				part, err := writer.CreateFormFile("image", "input.png")
				require.NoError(t, err)
				_, err = part.Write([]byte("original-image-bytes"))
				require.NoError(t, err)
				require.NoError(t, writer.Close())
				body, contentType = buf.Bytes(), writer.FormDataContentType()
			}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", contentType)
			if kind == "composite_multipart_alias" {
				ctx := WithResolvedTargetPlatform(c.Request.Context(), PlatformOpenAI)
				ctx = WithCompositeRouteDecision(ctx, CompositeRouteDecision{Matched: true, TargetPlatform: PlatformOpenAI, UpstreamModel: model, PublicModel: requestModel})
				c.Request = c.Request.WithContext(ctx)
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"aW1hZ2U="}]}`))}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			if kind == "channel_mapping" {
				require.Equal(t, OpenAIImagesCapabilityAPIKey, parsed.RequiredCapabilityForModel(channelModel))
			}
			result, err := svc.ForwardImages(c.Request.Context(), c, &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: credentials}, body, parsed, channelModel)
			require.NoError(t, err)
			require.Equal(t, 1, result.ImageCount)
			require.Equal(t, model, result.UpstreamModel)
			require.Equal(t, firstNonEmptyString(channelModel, parsed.Model), result.Model)
			require.Equal(t, "https://compatible.example"+endpoint, upstream.lastReq.URL.String())
			require.Equal(t, "Bearer image-key", upstream.lastReq.Header.Get("Authorization"))
			if strings.Contains(kind, "multipart") {
				r := httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(upstream.lastBody))
				r.Header.Set("Content-Type", upstream.lastReq.Header.Get("Content-Type"))
				require.NoError(t, r.ParseMultipartForm(1<<20))
				require.Equal(t, model, r.FormValue("model"))
				require.Equal(t, "preserved", r.FormValue("custom_field"))
				file, _, err := r.FormFile("image")
				require.NoError(t, err)
				defer file.Close()
				data, err := io.ReadAll(file)
				require.NoError(t, err)
				require.Equal(t, "original-image-bytes", string(data))
			} else {
				require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "model").String())
				require.Equal(t, "preserved", gjson.GetBytes(upstream.lastBody, "custom_field").String())
			}
		})
	}
}

// TestCompatibleImagesNativeAccountsCapabilityFence 验证 OAuth/SetupToken 账号通过
// SupportsOpenAIImageCapability 拒绝 Gemini 兼容模型。调度器通过此检查保证 Gemini
// 模型（RequiredCapability=APIKey）不会被调度到 OAuth/SetupToken 账号，而不是在
// Forward 阶段再次校验模型字符串。
func TestCompatibleImagesNativeAccountsCapabilityFence(t *testing.T) {
	for _, typ := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		t.Run(typ, func(t *testing.T) {
			account := &Account{Platform: PlatformOpenAI, Type: typ}
			// Gemini 兼容模型的 capability 是 APIKey；OAuth/SetupToken 不能服务该 capability。
			require.False(t, account.SupportsOpenAIImageCapability(OpenAIImagesCapabilityAPIKey),
				"OAuth/SetupToken account must not serve images-apikey capability (Gemini/Seedream/Flux)")
			// 原生 OpenAI 模型（Native/Basic）仍然允许。
			require.True(t, account.SupportsOpenAIImageCapability(OpenAIImagesCapabilityNative))
		})
	}
}

func TestCompatibleImagesThirdPartyModels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	thirdParty := []string{"seedream-5.0-pro", "doubao-seedream-5-0-flash", "flux-3-image", "flux-2-pro"}
	for _, model := range thirdParty {
		t.Run(model, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"draw"}`, model))
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, openAIImagesGenerationsEndpoint, strings.NewReader(string(body)))
			parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			require.Equal(t, OpenAIImagesCapabilityAPIKey, parsed.RequiredCapability)
			require.Equal(t, OpenAIImagesCapabilityAPIKey, parsed.RequiredCapabilityForModel(model))
			require.True(t, (&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}).SupportsOpenAIImageCapability(parsed.RequiredCapability))
			for _, typ := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
				require.False(t, (&Account{Platform: PlatformOpenAI, Type: typ}).SupportsOpenAIImageCapability(parsed.RequiredCapability))
			}
		})
	}
	for _, model := range []string{"midjourney", "midjourney-v7", "grok-imagine-image", "grok-imagine-image-2.0"} {
		t.Run("accept_"+model, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"draw"}`, model))
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, openAIImagesGenerationsEndpoint, strings.NewReader(string(body)))
			parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			require.Equal(t, model, parsed.Model)
		})
	}
	for _, model := range []string{"gpt-5.5", "unknown-image", "fluxx-3-image", "seedreaming-1"} {
		t.Run("reject_"+model, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"draw"}`, model))
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, openAIImagesGenerationsEndpoint, strings.NewReader(string(body)))
			_, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
			require.ErrorContains(t, err, "images endpoint requires an image model")
		})
	}
}

func TestCompatibleImagesForwardThirdPartyModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, model := range []string{"seedream-5.0-pro", "doubao-seedream-5-0-flash", "flux-3-image", "midjourney"} {
		t.Run(model, func(t *testing.T) {
			endpoint := openAIImagesGenerationsEndpoint
			body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"draw","size":"1024x1024","custom_field":"preserved"}`, model))
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(body)))
			c.Request.Header.Set("Content-Type", "application/json")
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"aW1hZ2U="}]}`))}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			result, err := svc.ForwardImages(c.Request.Context(), c, &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "image-key", "base_url": "https://compatible.example/v1"}}, body, parsed, "")
			require.NoError(t, err)
			require.Equal(t, model, result.UpstreamModel)
			require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "model").String())
			require.Equal(t, "preserved", gjson.GetBytes(upstream.lastBody, "custom_field").String())
			require.Equal(t, "https://compatible.example"+endpoint, upstream.lastReq.URL.String())
			require.Equal(t, "Bearer image-key", upstream.lastReq.Header.Get("Authorization"))
		})
	}
}

// TestCompatibleImagesForwardAccountAlias 验证账号 model_mapping 映射出的 alias（如
// mj2-midjourney）不被 Forward 阶段的白名单拒绝。公开模型在 Parse 阶段已通过校验；
// 账号级 alias 属于管理员配置的信任输入，不应在 Forward 阶段再次字符串校验。
func TestCompatibleImagesForwardAccountAlias(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// 公开模型 "midjourney" 在 Parse 阶段通过；账号将其映射为 "mj2-midjourney"。
	for _, tc := range []struct {
		publicModel  string
		accountAlias string
	}{
		{"midjourney", "mj2-midjourney"},
		{"seedream-5.0-pro", "seedream-custom-v2"},
		{"flux-3-image", "flux-internal-alias"},
	} {
		t.Run(tc.publicModel+"->"+tc.accountAlias, func(t *testing.T) {
			endpoint := openAIImagesGenerationsEndpoint
			body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"draw","size":"1024x1024"}`, tc.publicModel))
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(body)))
			c.Request.Header.Set("Content-Type", "application/json")
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"aW1hZ2U="}]}`))}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			account := &Account{
				ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Credentials: map[string]any{
					"api_key":       "image-key",
					"base_url":      "https://compatible.example/v1",
					"model_mapping": map[string]any{tc.publicModel: tc.accountAlias},
				},
			}
			result, err := svc.ForwardImages(c.Request.Context(), c, account, body, parsed, "")
			require.NoError(t, err, "account alias %q must not be rejected at Forward stage", tc.accountAlias)
			require.Equal(t, tc.accountAlias, result.UpstreamModel)
			require.Equal(t, tc.accountAlias, gjson.GetBytes(upstream.lastBody, "model").String())
		})
	}
}
