package service

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type geminiBananaSafeImageUpstream struct {
	publicOnly  bool
	body        []byte
	contentType string
}

func (u *geminiBananaSafeImageUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.publicOnly = HTTPUpstreamPublicHostsOnly(req.Context())
	contentType := u.contentType
	if contentType == "" {
		contentType = "image/png"
	}
	body := u.body
	if body == nil {
		body = []byte("PNG")
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(bytes.NewReader(body))}, nil
}

func (u *geminiBananaSafeImageUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func TestBuildOpenAIImagesRequestFromGeminiBananaUsesMultipartForInlineReference(t *testing.T) {
	body := []byte(`{"contents":[{"parts":[{"text":"preserve face"},{"inlineData":{"mimeType":"image/png","data":"QUJD"}}]}],"generationConfig":{"imageConfig":{"imageSize":"2K","aspectRatio":"3:4"}}}`)
	converted, contentType, endpoint, err := BuildOpenAIImagesRequestFromGeminiBanana("nano-banana-pro-2k", body)
	require.NoError(t, err)
	require.Equal(t, "/v1/images/edits", endpoint)

	mediaType, params, err := mime.ParseMediaType(contentType)
	require.NoError(t, err)
	require.Equal(t, "multipart/form-data", mediaType)
	form, err := multipart.NewReader(bytes.NewReader(converted), params["boundary"]).ReadForm(int64(len(converted)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = form.RemoveAll() })
	require.Equal(t, []string{"nano-banana-pro-2k"}, form.Value["model"])
	require.Equal(t, []string{"preserve face"}, form.Value["prompt"])
	require.Equal(t, []string{"2K"}, form.Value["size"])
	require.Equal(t, []string{"3:4"}, form.Value["aspect_ratio"])
	require.Len(t, form.File["image"], 1)
	require.Equal(t, "image/png", form.File["image"][0].Header.Get("Content-Type"))
	file, err := form.File["image"][0].Open()
	require.NoError(t, err)
	data, err := io.ReadAll(file)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	require.Equal(t, []byte("ABC"), data)
}

func TestBuildOpenAIImagesRequestFromGeminiBananaKeepsTextGenerationJSON(t *testing.T) {
	body := []byte(`{"contents":[{"parts":[{"text":"draw a cat"}]}]}`)
	converted, contentType, endpoint, err := BuildOpenAIImagesRequestFromGeminiBanana("nano-banana-pro-2k", body)
	require.NoError(t, err)
	require.Equal(t, "/v1/images/generations", endpoint)
	require.Equal(t, "application/json", contentType)
	require.Equal(t, "draw a cat", gjson.GetBytes(converted, "prompt").String())
}

func TestGeminiBananaBridgeModel(t *testing.T) {
	require.False(t, IsGeminiBananaBridgeModel("gemini-3.1-flash-image"))
	require.True(t, IsGeminiBananaBridgeModel("nano-banana2-2k"))
	require.True(t, IsGeminiBananaBridgeModel("nano-banana-pro-4k"))
}

func TestBuildOpenAIImagesBodyFromGeminiBanana(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"draw a cat"},{"inlineData":{"mimeType":"image/png","data":"QUJD"}}]}],"generationConfig":{"imageConfig":{"imageSize":"4K","aspectRatio":"9:16"}}}`)
	converted, err := BuildOpenAIImagesBodyFromGeminiBanana("nano-banana-pro-4k", body)
	require.NoError(t, err)
	require.Equal(t, "nano-banana-pro-4k", gjson.GetBytes(converted, "model").String())
	require.Equal(t, "draw a cat", gjson.GetBytes(converted, "prompt").String())
	require.Equal(t, "4K", gjson.GetBytes(converted, "size").String())
	require.Equal(t, "9:16", gjson.GetBytes(converted, "aspect_ratio").String())
	require.Equal(t, "data:image/png;base64,QUJD", gjson.GetBytes(converted, "images.0.image_url").String())
}

func TestBuildOpenAIImagesBodyFromGeminiBananaAcceptsSnakeCaseImageParts(t *testing.T) {
	body := []byte(`{"contents":[{"parts":[{"text":"use this reference"},{"inline_data":{"mimeType":"image/jpeg","data":"QUJD"}}]}]}`)
	converted, err := BuildOpenAIImagesBodyFromGeminiBanana("nano-banana-pro-2k", body)
	require.NoError(t, err)
	require.Equal(t, "data:image/jpeg;base64,QUJD", gjson.GetBytes(converted, "images.0.image_url").String())
}

func TestBuildGeminiBananaResponseFromOpenAIImages(t *testing.T) {
	body := []byte(`{"created":1710000007,"data":[{"b64_json":"QUJD","revised_prompt":"draw a cat"}],"usage":{"input_tokens":2,"output_tokens":3,"output_tokens_details":{"image_tokens":3}},"output_format":"png"}`)
	geminiBody, usage, imageCount, err := BuildGeminiBananaResponseFromOpenAIImages("nano-banana-pro-4k", body)
	require.NoError(t, err)
	require.Equal(t, 1, imageCount)
	require.Equal(t, 2, usage.InputTokens)
	require.Equal(t, 3, usage.OutputTokens)
	require.Equal(t, "image/png", gjson.GetBytes(geminiBody, "candidates.0.content.parts.0.inlineData.mimeType").String())
	require.Equal(t, "QUJD", gjson.GetBytes(geminiBody, "candidates.0.content.parts.0.inlineData.data").String())
	require.Equal(t, "draw a cat", gjson.GetBytes(geminiBody, "candidates.0.content.parts.1.text").String())
}

func TestBuildGeminiBananaResponseFromOpenAIImagesDownloadsURL(t *testing.T) {
	upstream := &geminiBananaSafeImageUpstream{body: []byte("JPEG"), contentType: "image/jpeg"}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	body := []byte(`{"data":[{"url":"data:image/jpeg;base64,SlBFRw=="}]}`)
	body = []byte(`{"data":[{"url":"https://image.example/out.jpg"}]}`)
	geminiBody, _, count, err := svc.BuildGeminiBananaResponseFromOpenAIImages(context.Background(), nil, "nano-banana-pro-2k", body)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.True(t, upstream.publicOnly)
	require.Equal(t, "image/jpeg", gjson.GetBytes(geminiBody, "candidates.0.content.parts.0.inlineData.mimeType").String())
	require.Equal(t, "SlBFRw==", gjson.GetBytes(geminiBody, "candidates.0.content.parts.0.inlineData.data").String())
}

func TestOpenAIGatewayServiceBuildGeminiBananaResponseUsesSafeUpstream(t *testing.T) {
	upstream := &geminiBananaSafeImageUpstream{}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	body := []byte(`{"data":[{"url":"https://image.example/out.png"}]}`)
	response, _, count, err := svc.BuildGeminiBananaResponseFromOpenAIImages(context.Background(), &Account{ID: 7}, "nano-banana-pro-2k", body)

	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.True(t, upstream.publicOnly)
	require.Equal(t, "UE5H", gjson.GetBytes(response, "candidates.0.content.parts.0.inlineData.data").String())
}
