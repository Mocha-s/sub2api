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

	"github.com/stretchr/testify/require"
)

func TestSeedanceAPIV1AdapterCreateConvertsRequestAndParsesAcceptedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/v1/video-generations", r.URL.Path)
		require.Equal(t, "Bearer sk-seedance", r.Header.Get("Authorization"))
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.JSONEq(t, `{"model":"seedance-2.0","prompt":"city","duration":10,"ratio":"16:9","resolution":"720p","generate_audio":true}`, string(body))
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"code":0,"message":"accepted","data":{"id":123,"request_id":"idem-1","status":"queued","model":"seedance-2.0","duration":10,"ratio":"16:9","resolution":"720p","progress":0}}`)
	}))
	defer server.Close()

	adapter := NewSeedanceAPIV1VideoAdapter(nil)
	account := &Account{Credentials: map[string]any{"base_url": server.URL + "/api/v1", "api_key": "sk-seedance"}}

	result, err := adapter.Create(context.Background(), account, []byte(`{"model":"seedance-2.0","prompt":"city","seconds":"10","aspect_ratio":"16:9","resolution":"720p","generate_audio":true}`), "application/json", "seedance-2.0")

	require.NoError(t, err)
	require.Equal(t, "123", result.ProviderTaskID)
	require.Equal(t, VideoTaskStatusQueued, result.Status)
	require.Equal(t, "queued", result.ProviderStatus)
	require.Equal(t, float64(0), result.Metadata["progress"])
	require.Equal(t, VideoAdapterSeedanceAPIV1, result.Metadata[VideoAdapterMetadataKey])
	require.JSONEq(t, `{"id":"123","task_id":"123","object":"video","model":"seedance-2.0","status":"queued","progress":0,"duration":10,"ratio":"16:9","resolution":"720p"}`, string(result.RawBody))
}

func TestSeedanceAPIV1VideoAdapterEstimateReturnsMappedMetadata(t *testing.T) {
	adapter := NewSeedanceAPIV1VideoAdapter(nil).(*seedanceAPIV1VideoAdapter)
	body := []byte(`{"model":"seedance-2.0","prompt":"city","duration":8,"ratio":"16:9","resolution":"720p"}`)

	result, err := adapter.Estimate(context.Background(), nil, body, "application/json", "seedance-upstream-v2")

	require.NoError(t, err)
	require.JSONEq(t, `{"object":"video.estimate","model":"seedance-2.0","upstream_model":"seedance-upstream-v2","adapter":"seedance_api_v1","endpoint":"videos","metadata":{"duration":8,"ratio":"16:9","resolution":"720p"}}`, string(result.ResponseBody))
}

func TestSeedanceAPIV1AdapterValidateCreateRejectsMissingBaseURLBeforeHTTPCall(t *testing.T) {
	called := false
	adapter := NewSeedanceAPIV1VideoAdapter(nil).(*seedanceAPIV1VideoAdapter)
	adapter.client = &http.Client{Transport: seedanceRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(`{"code":0,"data":{"id":"unexpected","status":"queued"}}`)),
		}, nil
	})}
	validator, ok := any(adapter).(VideoTaskCreateValidator)
	require.True(t, ok)
	account := &Account{Credentials: map[string]any{"api_key": "sk-seedance"}}
	body := []byte(`{"model":"seedance-2.0","prompt":"city"}`)

	err := validator.ValidateCreate(context.Background(), account, body, "application/json", "")

	require.Error(t, err)
	require.Contains(t, err.Error(), "seedance base_url is required")
	require.False(t, called, "validation should not call upstream")

	_, err = adapter.Create(context.Background(), account, body, "application/json", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "seedance base_url is required")
	require.False(t, called, "missing base_url should fail before HTTP request")
}

func TestSeedanceAPIV1AdapterCreatePreservesInboundMediaFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.JSONEq(t, `{"model":"seedance-2.0","prompt":"city","images":[{"url":"https://cdn.example/input.png","role":"first_frame"}],"videos":["https://cdn.example/input.mp4"],"audios":[{"url":"https://cdn.example/input.mp3","volume":0.8}]}`, string(body))
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"code":0,"message":"accepted","data":{"id":"media-task","status":"queued","progress":0}}`)
	}))
	defer server.Close()

	adapter := NewSeedanceAPIV1VideoAdapter(nil)
	account := &Account{Credentials: map[string]any{"base_url": server.URL + "/api/v1", "api_key": "sk-seedance"}}
	body := []byte(`{"model":"seedance-2.0","prompt":"city","images":[{"url":"https://cdn.example/input.png","role":"first_frame"}],"videos":["https://cdn.example/input.mp4"],"audios":[{"url":"https://cdn.example/input.mp3","volume":0.8}]}`)

	result, err := adapter.Create(context.Background(), account, body, "application/json", "")

	require.NoError(t, err)
	require.Equal(t, "media-task", result.ProviderTaskID)
	require.Equal(t, VideoAdapterSeedanceAPIV1, result.Metadata[VideoAdapterMetadataKey])
}

func TestSeedanceAPIV1AdapterCreateRedactsTokenFromApplicationError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"code":401,"message":"invalid api key sk-seedance-secret","data":{}}`)
	}))
	defer server.Close()

	adapter := NewSeedanceAPIV1VideoAdapter(nil)
	account := &Account{Credentials: map[string]any{"base_url": server.URL + "/api/v1", "api_key": "sk-seedance-secret"}}

	_, err := adapter.Create(context.Background(), account, []byte(`{"model":"seedance-2.0","prompt":"city"}`), "application/json", "")

	require.Error(t, err)
	require.Contains(t, err.Error(), "seedance upstream error code 401")
	require.Contains(t, err.Error(), "[redacted]")
	require.NotContains(t, err.Error(), "sk-seedance-secret")
}

func TestSeedanceAPIV1AdapterFetchParsesSucceededResultURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/api/v1/video-generations/123", r.URL.Path)
		require.Equal(t, "Bearer sk-seedance", r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `{"code":0,"message":"success","data":{"id":123,"status":"succeeded","progress":100,"result":{"video_url":"https://cdn.example/seedance.mp4"}}}`)
	}))
	defer server.Close()

	adapter := NewSeedanceAPIV1VideoAdapter(nil)
	account := &Account{Credentials: map[string]any{"base_url": server.URL + "/api/v1", "api_key": "sk-seedance"}}
	task := &VideoTask{ProviderTaskID: "123"}

	result, err := adapter.Fetch(context.Background(), account, task)

	require.NoError(t, err)
	require.Equal(t, "123", result.ProviderTaskID)
	require.Equal(t, VideoTaskStatusCompleted, result.Status)
	require.Equal(t, "succeeded", result.ProviderStatus)
	require.Equal(t, float64(100), result.Metadata["progress"])
	require.Equal(t, "https://cdn.example/seedance.mp4", result.Metadata["result_url"])
	require.Equal(t, VideoAdapterSeedanceAPIV1, result.Metadata[VideoAdapterMetadataKey])
	require.JSONEq(t, `{"id":"123","task_id":"123","object":"video","status":"completed","progress":100,"video_url":"https://cdn.example/seedance.mp4","result":{"video_url":"https://cdn.example/seedance.mp4"}}`, string(result.RawBody))
}

func TestSeedanceAPIV1AdapterFetchUsesPersistedIDWhenResponseOmitsID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"message":"success","data":{"status":"processing","progress":25}}`)
	}))
	defer server.Close()
	adapter := NewSeedanceAPIV1VideoAdapter(nil)
	account := &Account{Credentials: map[string]any{"base_url": server.URL + "/api/v1", "api_key": "sk-seedance"}}

	result, err := adapter.Fetch(context.Background(), account, &VideoTask{ProviderTaskID: "task_persisted"})

	require.NoError(t, err)
	require.Equal(t, "task_persisted", result.ProviderTaskID)
}

func TestSeedanceAPIV1AdapterContentStreamsResultURLAndForwardsRange(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/seedance.mp4", r.URL.Path)
		require.Equal(t, "bytes=5-10", r.Header.Get("Range"))
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", "6")
		w.Header().Set("Content-Range", "bytes 5-10/100")
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

	adapter := NewSeedanceAPIV1VideoAdapter(nil)
	task := &VideoTask{Metadata: map[string]any{"result_url": server.URL + "/seedance.mp4"}}
	headers := http.Header{"Range": []string{"bytes=5-10"}}

	type contentResult struct {
		stream *VideoContentStream
		err    error
	}
	done := make(chan contentResult, 1)
	go func() {
		stream, err := adapter.Content(context.Background(), nil, task, headers)
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
	require.Equal(t, "video/mp4", result.stream.ContentType)
	require.Equal(t, int64(6), result.stream.ContentLength)
	require.Equal(t, "bytes 5-10/100", result.stream.Headers["Content-Range"])

	buf := make([]byte, len("frame"))
	_, err := io.ReadFull(result.stream.Body, buf)
	require.NoError(t, err)
	require.Equal(t, "frame", string(buf))
}

func TestSeedanceAPIV1AdapterContentFetchUsesGatewayHTTPUpstream(t *testing.T) {
	upstream := &openAIVideoHTTPUpstreamRecorder{resp: &http.Response{
		StatusCode:    http.StatusPartialContent,
		Header:        http.Header{"Content-Type": []string{"video/mp4"}, "Content-Range": []string{"bytes 5-10/100"}},
		ContentLength: 6,
		Body:          io.NopCloser(strings.NewReader("frame!")),
	}}
	adapter := NewSeedanceAPIV1VideoAdapter(&OpenAIGatewayService{httpUpstream: upstream}).(*seedanceAPIV1VideoAdapter)
	adapter.client = &http.Client{Transport: seedanceRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("default content client should not be used")
	})}
	account := &Account{ID: 42, Concurrency: 3}
	task := &VideoTask{Metadata: map[string]any{"result_url": "https://cdn.example/seedance.mp4"}}
	headers := http.Header{"Range": []string{"bytes=5-10"}}

	stream, err := adapter.Content(context.Background(), account, task, headers)

	require.NoError(t, err)
	defer func() { _ = stream.Body.Close() }()
	require.True(t, upstream.usedTLSDo)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, http.MethodGet, upstream.lastReq.Method)
	require.Equal(t, "https://cdn.example/seedance.mp4", upstream.lastReq.URL.String())
	require.Equal(t, "bytes=5-10", upstream.lastReq.Header.Get("Range"))
	require.Equal(t, int64(42), upstream.lastAccountID)
	require.Equal(t, 3, upstream.lastAccountConcurrency)
	require.Equal(t, HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileFromContext(upstream.lastReq.Context()))
	require.Equal(t, http.StatusPartialContent, stream.StatusCode)
}

func TestSeedanceAPIV1AdapterValidateCreateRejectsInvalidBodyBeforeHTTPCall(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	adapter := NewSeedanceAPIV1VideoAdapter(nil)
	validator, ok := adapter.(VideoTaskCreateValidator)
	require.True(t, ok)
	account := &Account{Credentials: map[string]any{"base_url": server.URL + "/api/v1", "api_key": "sk-seedance"}}

	err := validator.ValidateCreate(context.Background(), account, []byte(`null`), "application/json", "")

	require.Error(t, err)
	require.Contains(t, err.Error(), "video create JSON body must be an object")
	require.False(t, called, "validation should not call upstream")

	_, err = adapter.Create(context.Background(), account, []byte(`{"model":"seedance-2.0","prompt":"city","seconds":"later"}`), "application/json", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "seconds")
	require.False(t, called, "invalid create body should not call upstream")
	require.NotContains(t, strings.ToLower(err.Error()), "upstream")

	err = validator.ValidateCreate(context.Background(), account, []byte(`{"model":"seedance-2.0","prompt":"city","duration":"NaN"}`), "application/json", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "duration must be a number or numeric string")
	require.False(t, called, "non-finite numeric strings should not call upstream")

	err = validator.ValidateCreate(context.Background(), account, []byte(`{"model":"seedance-2.0","prompt":"city","seconds":"Inf"}`), "application/json", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "seconds must be a number or numeric string")
	require.False(t, called, "non-finite numeric strings should not call upstream")
}

type seedanceRoundTripFunc func(*http.Request) (*http.Response, error)

func (f seedanceRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
