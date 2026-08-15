package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGeminiBananaBridgeModel(t *testing.T) {
	require.False(t, IsGeminiBananaBridgeModel("gemini-3.1-flash-image"))
	require.True(t, IsGeminiBananaBridgeModel("nano-banana2-2k"))
	require.True(t, IsGeminiBananaBridgeModel("nano-banana-pro-4k"))
}

func TestBuildOpenAIImagesBodyFromGeminiBanana(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"draw a cat"},{"inlineData":{"mimeType":"image/png","data":"QUJD"}}]}],"generationConfig":{"imageConfig":{"imageSize":"4K"}}}`)
	converted, err := BuildOpenAIImagesBodyFromGeminiBanana("nano-banana-pro-4k", body)
	require.NoError(t, err)
	require.Equal(t, "nano-banana-pro-4k", gjson.GetBytes(converted, "model").String())
	require.Equal(t, "draw a cat", gjson.GetBytes(converted, "prompt").String())
	require.Equal(t, "4K", gjson.GetBytes(converted, "size").String())
	require.Equal(t, "data:image/png;base64,QUJD", gjson.GetBytes(converted, "images.0.image_url").String())
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
