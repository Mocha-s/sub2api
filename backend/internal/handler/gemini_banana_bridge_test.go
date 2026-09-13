package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGeminiBananaBridgeIsSelectedForNativeGenerateContent(t *testing.T) {
	require.True(t, service.IsGeminiBananaBridgeModel("nano-banana-pro-2k"))
	require.True(t, service.IsGeminiBananaBridgeModel("nano-banana2-4k"))
	require.False(t, service.IsGeminiBananaBridgeModel("gemini-3.1-flash-image"))
}

func TestGeminiBananaBridgeResponseKeepsRemoteImageAsInlineData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("PNG"))
	}))
	defer server.Close()

	body := []byte(`{"data":[{"url":"` + server.URL + `/image.png"}]}`)
	_, _, _, err := service.BuildGeminiBananaResponseFromOpenAIImagesContext(context.Background(), "nano-banana-pro-2k", body)
	require.Error(t, err, "HTTP images must remain HTTPS-only")
}

func TestGeminiBananaBridgeRequestRequiresPrompt(t *testing.T) {
	_, _, _, err := service.BuildOpenAIImagesRequestFromGeminiBanana("nano-banana-pro-2k", []byte(`{"contents":[{"parts":[]}]}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "prompt")
}

func TestGeminiBananaBridgeResponseShape(t *testing.T) {
	body := []byte(`{"data":[{"b64_json":"QUJD"}]}`)
	response, _, count, err := service.BuildGeminiBananaResponseFromOpenAIImages("nano-banana-pro-2k", body)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, "model", gjson.GetBytes(response, "candidates.0.content.role").String())
	require.Equal(t, "QUJD", gjson.GetBytes(response, "candidates.0.content.parts.0.inlineData.data").String())
	require.True(t, strings.Contains(string(response), "nano-banana-pro-2k"))
}
