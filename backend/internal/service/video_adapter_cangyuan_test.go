//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// Client-only fields (size/watermark/return_last_frame/omni_reference_task_type) are not part of
// the cangyuan contract and stay dropped, exactly as with openai_videos_duration.
func TestCangyuanReferenceAdapterCreateRenamesPlainImages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/v1/videos", r.URL.Path)
		require.Equal(t, "Bearer sk-ref", r.Header.Get("Authorization"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.JSONEq(t, `{"model":"sd17-seedance-2.5-3","prompt":"city","duration":10,"aspect_ratio":"9:16","resolution":"720p","generate_audio":true,"reference_image_urls":["https://cdn.example/1.jpg","https://cdn.example/2.jpg"]}`, string(body))
		_, _ = io.WriteString(w, `{"id":"video_ref","status":"queued","progress":0}`)
	}))
	defer server.Close()

	adapter := NewCangyuanReferenceURLsVideoAdapter(nil)
	account := &Account{Credentials: map[string]any{"base_url": server.URL, "api_key": "sk-ref"}}

	result, err := adapter.Create(context.Background(), account,
		[]byte(`{"model":"client-model","prompt":"city","duration":10,"aspect_ratio":"9:16","resolution":"720p","generate_audio":true,"images":["https://cdn.example/1.jpg","https://cdn.example/2.jpg"],"omni_reference_task_type":"reference","size":"720x1280","watermark":false,"return_last_frame":false}`),
		"application/json", "sd17-seedance-2.5-3")

	require.NoError(t, err)
	require.Equal(t, "video_ref", result.ProviderTaskID)
	require.Equal(t, VideoTaskStatusQueued, result.Status)
	require.Equal(t, VideoAdapterCangyuanReferenceURLs, result.Metadata[VideoAdapterMetadataKey])
}

func TestCangyuanReferenceCreateBodySplitsFrameRoles(t *testing.T) {
	body := []byte(`{"model":"sd17-seedance-2.5-3","prompt":"city","images":[{"url":"https://cdn.example/first.jpg","role":"first_frame"},{"url":"https://cdn.example/last.jpg","role":"last_frame"},"https://cdn.example/ref.jpg"]}`)

	out, err := cangyuanReferenceCreateBody(body, "")

	require.NoError(t, err)
	require.JSONEq(t, `{"model":"sd17-seedance-2.5-3","prompt":"city","first_image_url":"https://cdn.example/first.jpg","last_image_url":"https://cdn.example/last.jpg","reference_image_urls":["https://cdn.example/ref.jpg"]}`, string(out))
}

func TestCangyuanReferenceCreateBodyMapsFrameAndMediaAliases(t *testing.T) {
	body := []byte(`{"model":"m","prompt":"p","start_frame":"https://cdn.example/start.jpg","end_frame":"https://cdn.example/end.jpg","reference_images":["https://cdn.example/r1.jpg"],"videos":["https://cdn.example/v.mp4"],"audios":[{"url":"https://cdn.example/a.mp3","volume":0.8}],"seconds":"6"}`)

	out, err := cangyuanReferenceCreateBody(body, "up-model")

	require.NoError(t, err)
	require.JSONEq(t, `{"model":"up-model","prompt":"p","duration":6,"first_image_url":"https://cdn.example/start.jpg","last_image_url":"https://cdn.example/end.jpg","reference_image_urls":["https://cdn.example/r1.jpg"],"reference_videos":["https://cdn.example/v.mp4"],"reference_audios":["https://cdn.example/a.mp3"]}`, string(out))
}

func TestCangyuanReferenceCreateBodyMapsSingleImageToFirstFrame(t *testing.T) {
	out, err := cangyuanReferenceCreateBody([]byte(`{"model":"m","prompt":"p","image":"https://cdn.example/only.jpg"}`), "")

	require.NoError(t, err)
	require.JSONEq(t, `{"model":"m","prompt":"p","first_image_url":"https://cdn.example/only.jpg"}`, string(out))
}

func TestCangyuanReferenceCreateBodyWithoutReferencesKeepsBaseShape(t *testing.T) {
	out, err := cangyuanReferenceCreateBody([]byte(`{"model":"m","prompt":"p","resolution":"720p"}`), "")

	require.NoError(t, err)
	require.JSONEq(t, `{"model":"m","prompt":"p","resolution":"720p"}`, string(out))
}

func TestCangyuanReferenceCreateBodyMapsLoneAndPluralAliases(t *testing.T) {
	body := []byte(`{"model":"m","prompt":"p","input_reference":"https://cdn.example/lone.jpg","image_reference":"https://cdn.example/iref.jpg","video_url":"https://cdn.example/v.mp4","audio_url":"https://cdn.example/a.mp3"}`)

	out, err := cangyuanReferenceCreateBody(body, "")

	require.NoError(t, err)
	require.JSONEq(t, `{"model":"m","prompt":"p","first_image_url":"https://cdn.example/lone.jpg","reference_image_urls":["https://cdn.example/iref.jpg"],"reference_videos":["https://cdn.example/v.mp4"],"reference_audios":["https://cdn.example/a.mp3"]}`, string(out))
}

func TestCangyuanReferenceCreateBodyPreservesPresignedURLShape(t *testing.T) {
	// Same shape as a real presigned asset URL (percent-encoded credential, query chain);
	// values are placeholders.
	presigned := "https://media.example.test/media/relay/20261103/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.jpg?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=EXAMPLEKEY%2F20261103%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Date=20261103T000000Z&X-Amz-Expires=7200&X-Amz-SignedHeaders=host&X-Amz-Signature=0000000000000000000000000000000000000000000000000000000000000000"
	body := []byte(`{"model":"sd17-seedance-2.5-3","prompt":"p","duration":10,"resolution":"720p","aspect_ratio":"9:16","images":["` + presigned + `","` + presigned + `"]}`)

	out, err := cangyuanReferenceCreateBody(body, "sd17-seedance-2.5-3")

	require.NoError(t, err)
	var payload struct {
		References []string `json:"reference_image_urls"`
	}
	require.NoError(t, json.Unmarshal(out, &payload))
	require.Equal(t, []string{presigned, presigned}, payload.References)
}
