//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

func TestOpenAIVideosDurationAdapterCreateConvertsSecondsToDuration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/v1/videos", r.URL.Path)
		require.Equal(t, "Bearer sk-duration", r.Header.Get("Authorization"))
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.JSONEq(t, `{"model":"seedance-2.0-720p","prompt":"rain","duration":8,"aspect_ratio":"16:9","resolution":"720p","generate_audio":true}`, string(body))
		_, _ = io.WriteString(w, `{"id":"video_duration","status":"queued","progress":0}`)
	}))
	defer server.Close()

	adapter := NewOpenAIVideosDurationAdapter(nil)
	account := &Account{Credentials: map[string]any{"base_url": server.URL, "api_key": "sk-duration"}}

	result, err := adapter.Create(context.Background(), account, []byte(`{"model":"client-model","prompt":"rain","seconds":"8","ratio":"16:9","resolution":"720p","generate_audio":true}`), "application/json", "seedance-2.0-720p")

	require.NoError(t, err)
	require.Equal(t, "video_duration", result.ProviderTaskID)
	require.Equal(t, VideoTaskStatusQueued, result.Status)
	require.Equal(t, VideoAdapterOpenAIVideosDuration, result.Metadata[VideoAdapterMetadataKey])
}

func TestOpenAIVideosDurationAdapterCreateBodyPreservesDocumentedRefsAndConvertsSeconds(t *testing.T) {
	body := []byte(`{"model":"client-model","prompt":"city","seconds":"8","ratio":"16:9","resolution":"720p","size":"1280x720","reference_mode":"style","quality":"high","image_reference":"https://cdn.example/image-ref.png","image_url":"https://cdn.example/image.png","reference_images":["https://cdn.example/ref.png"],"referenceImages":["https://cdn.example/ref-camel.png"],"style_references":["https://cdn.example/style.png"],"styleReferences":["https://cdn.example/style-camel.png"],"element_references":[{"url":"https://cdn.example/element.png","name":"lamp"}],"elementReferences":[{"url":"https://cdn.example/element-camel.png","name":"chair"}],"video_url":"https://cdn.example/ref.mp4","input_video":"https://cdn.example/input.mp4","inputVideo":"https://cdn.example/input-camel.mp4","input_reference":"https://cdn.example/input.png","inputReference":"https://cdn.example/input-camel.png","start_frame":"https://cdn.example/start.png","end_frame":"https://cdn.example/end.png","audio_reference":"https://cdn.example/audio.wav","reference_audio":"https://cdn.example/reference-audio.wav","reference_audio_url":"https://cdn.example/reference-audio-url.wav","reference_audio_urls":["https://cdn.example/reference-audio-urls.wav"],"referenceAudio":"https://cdn.example/reference-audio-camel.wav"}`)

	got, err := openAIDurationCreateBody(body, "upstream-video")

	require.NoError(t, err)
	require.JSONEq(t, `{"model":"upstream-video","prompt":"city","duration":8,"aspect_ratio":"16:9","resolution":"720p","size":"1280x720","reference_mode":"style","quality":"high","image_reference":"https://cdn.example/image-ref.png","image_url":"https://cdn.example/image.png","reference_images":["https://cdn.example/ref.png"],"referenceImages":["https://cdn.example/ref-camel.png"],"style_references":["https://cdn.example/style.png"],"styleReferences":["https://cdn.example/style-camel.png"],"element_references":[{"url":"https://cdn.example/element.png","name":"lamp"}],"elementReferences":[{"url":"https://cdn.example/element-camel.png","name":"chair"}],"video_url":"https://cdn.example/ref.mp4","input_video":"https://cdn.example/input.mp4","inputVideo":"https://cdn.example/input-camel.mp4","input_reference":"https://cdn.example/input.png","inputReference":"https://cdn.example/input-camel.png","start_frame":"https://cdn.example/start.png","end_frame":"https://cdn.example/end.png","audio_reference":"https://cdn.example/audio.wav","reference_audio":"https://cdn.example/reference-audio.wav","reference_audio_url":"https://cdn.example/reference-audio-url.wav","reference_audio_urls":["https://cdn.example/reference-audio-urls.wav"],"referenceAudio":"https://cdn.example/reference-audio-camel.wav"}`, string(got))

	validator := NewOpenAIVideosDurationAdapter(nil).(VideoTaskCreateValidator)
	err = validator.ValidateCreate(
		withVideoTaskEndpoint(context.Background(), VideoTaskEndpointVideoGenerations),
		&Account{Credentials: map[string]any{"base_url": "https://upstream.example", "api_key": "sk-duration"}},
		body,
		"application/json",
		"upstream-video",
	)
	require.NoError(t, err)
}

func TestOpenAIVideosDurationAdapterCreateBodyPreservesBase64Aliases(t *testing.T) {
	body := []byte(`{"model":"client-model","prompt":"city","seconds":8,"imagesBase64":["image-b64"],"audiosBase64":["audio-b64"]}`)

	got, err := openAIDurationCreateBody(body, "upstream-video")

	require.NoError(t, err)
	require.JSONEq(t, `{"model":"upstream-video","prompt":"city","duration":8,"imagesBase64":["image-b64"],"audiosBase64":["audio-b64"]}`, string(got))

	validator := NewOpenAIVideosDurationAdapter(nil).(VideoTaskCreateValidator)
	err = validator.ValidateCreate(
		withVideoTaskEndpoint(context.Background(), VideoTaskEndpointVideoGenerations),
		&Account{Credentials: map[string]any{"base_url": "https://upstream.example", "api_key": "sk-duration"}},
		body,
		"application/json",
		"upstream-video",
	)
	require.NoError(t, err)
}

func TestOpenAIVideosDurationAdapterEstimateReturnsDurationMetadata(t *testing.T) {
	adapter := NewOpenAIVideosDurationAdapter(nil).(*openAIVideosDurationAdapter)
	body := []byte(`{"model":"seedance-2.0","prompt":"city","seconds":"5","aspect_ratio":"16:9"}`)

	result, err := adapter.Estimate(context.Background(), nil, body, "application/json", "seedance-upstream-v2")

	require.NoError(t, err)
	require.JSONEq(t, `{"object":"video.estimate","model":"seedance-2.0","upstream_model":"seedance-upstream-v2","adapter":"openai_videos_duration","endpoint":"videos","metadata":{"duration":5,"aspect_ratio":"16:9"}}`, string(result.ResponseBody))
}

func TestOpenAIVideosDurationAdapterFetchExtractsDataURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/v1/videos/video_duration", r.URL.Path)
		require.Equal(t, "Bearer sk-duration", r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `{"id":"video_duration","status":"completed","progress":100,"data":[{"kind":"preview"},{"url":"https://cdn.example/openai-duration.mp4"}]}`)
	}))
	defer server.Close()

	adapter := NewOpenAIVideosDurationAdapter(nil)
	account := &Account{Credentials: map[string]any{"base_url": server.URL, "api_key": "sk-duration"}}
	task := &VideoTask{ProviderTaskID: "video_duration"}

	result, err := adapter.Fetch(context.Background(), account, task)

	require.NoError(t, err)
	require.Equal(t, VideoTaskStatusCompleted, result.Status)
	require.Equal(t, "https://cdn.example/openai-duration.mp4", result.Metadata["result_url"])
	require.Equal(t, VideoAdapterOpenAIVideosDuration, result.Metadata[VideoAdapterMetadataKey])
}

func TestOpenAIVideosDurationAdapterContentFallbackStreamsResultURLAndForwardsRange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/videos/video_duration/content":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"content endpoint unavailable"}`)
		case "/openai-duration.mp4":
			require.Equal(t, http.MethodGet, r.Method)
			require.Equal(t, "bytes=5-10", r.Header.Get("Range"))
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Length", "6")
			w.Header().Set("Content-Range", "bytes 5-10/100")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("frame!"))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	adapter := NewOpenAIVideosDurationAdapter(nil)
	account := &Account{Credentials: map[string]any{"base_url": server.URL, "api_key": "sk-duration"}}
	task := &VideoTask{ProviderTaskID: "video_duration", Metadata: map[string]any{"result_url": server.URL + "/openai-duration.mp4"}}
	headers := http.Header{"Range": []string{"bytes=5-10"}}

	stream, err := adapter.Content(context.Background(), account, task, headers)

	require.NoError(t, err)
	defer func() { _ = stream.Body.Close() }()
	require.Equal(t, http.StatusPartialContent, stream.StatusCode)
	require.Equal(t, "video/mp4", stream.ContentType)
	require.Equal(t, int64(6), stream.ContentLength)
	require.Equal(t, "bytes 5-10/100", stream.Headers["Content-Range"])
	body, err := io.ReadAll(stream.Body)
	require.NoError(t, err)
	require.Equal(t, "frame!", string(body))
}

func TestOpenAIVideosDurationAdapterContentFallbackUsesGatewayHTTPUpstream(t *testing.T) {
	upstream := &openAIDurationHTTPUpstreamSequence{responses: []*http.Response{
		{
			StatusCode: http.StatusNotFound,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":"not found"}`)),
		},
		{
			StatusCode:    http.StatusPartialContent,
			Header:        http.Header{"Content-Type": []string{"video/mp4"}, "Content-Range": []string{"bytes 5-10/100"}},
			ContentLength: 6,
			Body:          io.NopCloser(strings.NewReader("frame!")),
		},
	}}
	adapter := NewOpenAIVideosDurationAdapter(&OpenAIGatewayService{httpUpstream: upstream}).(*openAIVideosDurationAdapter)
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 3, Credentials: map[string]any{"base_url": "https://api.example", "api_key": "sk-duration"}}
	task := &VideoTask{ProviderTaskID: "video_duration", Metadata: map[string]any{"result_url": "https://cdn.example/openai-duration.mp4"}}
	headers := http.Header{"Range": []string{"bytes=5-10"}}

	stream, err := adapter.Content(context.Background(), account, task, headers)

	require.NoError(t, err)
	defer func() { _ = stream.Body.Close() }()
	require.True(t, upstream.usedTLSDo)
	require.Len(t, upstream.requests, 2)
	require.Equal(t, "https://api.example/v1/videos/video_duration/content", upstream.requests[0].URL.String())
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, http.MethodGet, upstream.lastReq.Method)
	require.Equal(t, "https://cdn.example/openai-duration.mp4", upstream.lastReq.URL.String())
	require.Equal(t, "bytes=5-10", upstream.lastReq.Header.Get("Range"))
	require.Equal(t, int64(42), upstream.lastAccountID)
	require.Equal(t, 3, upstream.lastAccountConcurrency)
	require.Equal(t, HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileFromContext(upstream.lastReq.Context()))
	require.Equal(t, http.StatusPartialContent, stream.StatusCode)
}

func TestOpenAIVideosDurationAdapterContentFallbackDoesNotMaskUnauthorized(t *testing.T) {
	resultURLCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/videos/video_duration/content":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"unauthorized"}`)
		case "/openai-duration.mp4":
			resultURLCalled = true
			w.WriteHeader(http.StatusPartialContent)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	adapter := NewOpenAIVideosDurationAdapter(nil)
	account := &Account{Credentials: map[string]any{"base_url": server.URL, "api_key": "sk-duration"}}
	task := &VideoTask{ProviderTaskID: "video_duration", Metadata: map[string]any{"result_url": server.URL + "/openai-duration.mp4"}}

	stream, err := adapter.Content(context.Background(), account, task, nil)

	require.Nil(t, stream)
	require.Error(t, err)
	require.False(t, resultURLCalled)
}

func TestOpenAIVideosDurationAdapterValidateCreateRejectsInvalidBodyBeforeHTTPCall(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	adapter := NewOpenAIVideosDurationAdapter(nil)
	validator, ok := adapter.(VideoTaskCreateValidator)
	require.True(t, ok)
	account := &Account{Credentials: map[string]any{"base_url": server.URL, "api_key": "sk-duration"}}

	err := validator.ValidateCreate(context.Background(), account, []byte(`null`), "application/json", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "video create JSON body must be an object")
	require.False(t, called, "validation should not call upstream")

	err = validator.ValidateCreate(context.Background(), account, []byte(`{"model":"seedance-2.0","prompt":"city","seconds":"later"}`), "application/json", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "seconds")
	require.False(t, called, "validation should not call upstream")

	_, err = adapter.Create(context.Background(), account, []byte(`{"model":"seedance-2.0","prompt":"city","duration":"soon"}`), "application/json", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "duration")
	require.False(t, called, "invalid create body should not call upstream")

	err = validator.ValidateCreate(context.Background(), account, []byte(`{"model":"seedance-2.0","prompt":"city","duration":"NaN"}`), "application/json", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "duration must be a number or numeric string")
	require.False(t, called, "non-finite numeric strings should not call upstream")

	err = validator.ValidateCreate(context.Background(), account, []byte(`{"model":"seedance-2.0","prompt":"city","seconds":"Inf"}`), "application/json", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "seconds must be a number or numeric string")
	require.False(t, called, "non-finite numeric strings should not call upstream")
}

func TestOpenAIVideosDurationAdapterRejectsUnsupportedUnifiedField(t *testing.T) {
	adapter := NewOpenAIVideosDurationAdapter(nil)
	validator, ok := adapter.(VideoTaskCreateValidator)
	require.True(t, ok)

	err := validator.ValidateCreate(
		withVideoTaskEndpoint(context.Background(), VideoTaskEndpointVideoGenerations),
		&Account{Credentials: map[string]any{"base_url": "https://upstream.example", "api_key": "sk-duration"}},
		[]byte(`{"model":"seedance-2.0","prompt":"city","return_last_frame":true}`),
		"application/json",
		"seedance-2.0",
	)

	require.Error(t, err)
	require.ErrorContains(t, err, "return_last_frame is not supported")
}

type openAIDurationHTTPUpstreamSequence struct {
	responses              []*http.Response
	requests               []*http.Request
	lastReq                *http.Request
	lastProxyURL           string
	lastAccountID          int64
	lastAccountConcurrency int
	lastTLSProfile         *tlsfingerprint.Profile
	usedTLSDo              bool
	index                  int
}

func (u *openAIDurationHTTPUpstreamSequence) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	u.lastReq = req
	u.requests = append(u.requests, req)
	u.lastProxyURL = proxyURL
	u.lastAccountID = accountID
	u.lastAccountConcurrency = accountConcurrency
	if u.index >= len(u.responses) {
		return nil, errors.New("unexpected upstream request")
	}
	resp := u.responses[u.index]
	u.index++
	return resp, nil
}

func (u *openAIDurationHTTPUpstreamSequence) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	u.usedTLSDo = true
	u.lastTLSProfile = profile
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func TestOpenAIVideosDurationAdapterContentFallbackReturnsBeforeBodyCompletes(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/videos/video_duration/content" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		require.Equal(t, "/openai-duration.mp4", r.URL.Path)
		w.Header().Set("Content-Type", "video/mp4")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("frame"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-release
		_, _ = w.Write([]byte("!"))
	}))
	defer server.Close()
	defer close(release)

	adapter := NewOpenAIVideosDurationAdapter(nil)
	account := &Account{Credentials: map[string]any{"base_url": server.URL, "api_key": "sk-duration"}}
	task := &VideoTask{ProviderTaskID: "video_duration", Metadata: map[string]any{"result_url": server.URL + "/openai-duration.mp4"}}

	type contentResult struct {
		stream *VideoContentStream
		err    error
	}
	done := make(chan contentResult, 1)
	go func() {
		stream, err := adapter.Content(context.Background(), account, task, nil)
		done <- contentResult{stream: stream, err: err}
	}()

	var result contentResult
	select {
	case result = <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Content did not return before upstream body completed")
	}
	require.NoError(t, result.err)
	defer func() { _ = result.stream.Body.Close() }()
	require.Equal(t, http.StatusPartialContent, result.stream.StatusCode)
}
