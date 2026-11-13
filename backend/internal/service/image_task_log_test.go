package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type imageLogMemoryRepo struct {
	entry ImageTaskLogEntry
	err   error
}

func (r *imageLogMemoryRepo) Save(_ context.Context, e *ImageTaskLogEntry) error {
	r.entry = *e
	return r.err
}
func (r *imageLogMemoryRepo) Get(_ context.Context, id string, owner int64, index int) (*ImageTaskLogEntry, error) {
	if id != r.entry.TaskID || (owner > 0 && owner != r.entry.UserID) {
		return nil, ErrImageTaskNotFound
	}
	e := r.entry
	e.Results = nil
	if index >= 0 && index < len(r.entry.Results) {
		e.Results = []ImageTaskLogResult{r.entry.Results[index]}
	}
	return &e, nil
}
func TestImageTaskLogPersistsOriginalResultsAndScopesContent(t *testing.T) {
	r := &imageLogMemoryRepo{}
	s := NewImageTaskLogService(r, nil)
	e := &ImageTaskLogEntry{TaskID: "img_one", UserID: 7, APIKeyID: 9, RequestMode: "sync", Model: "gpt-image-1", Prompt: "cat", ItemCount: 2}
	s.Start(context.Background(), e)
	require.Equal(t, "processing", r.entry.Status)
	require.Nil(t, r.entry.FinishedAt)
	b64 := base64.StdEncoding.EncodeToString(b64BackfillPNGBytes)
	s.Finish(context.Background(), e, 200, []byte(`{"data":[{"b64_json":"`+b64+`"},{"b64_json":"`+b64+`"}],"authorization":"secret"}`), false)
	require.Equal(t, "completed", r.entry.Status)
	require.Equal(t, 2, r.entry.ItemCount)
	require.Equal(t, 2, r.entry.SuccessCount)
	require.NotNil(t, r.entry.FinishedAt)
	data, ct, err := s.Content(context.Background(), 7, "img_one", 1)
	require.NoError(t, err)
	require.Equal(t, b64BackfillPNGBytes, data)
	require.Equal(t, "image/png", ct)
	_, _, err = s.Content(context.Background(), 8, "img_one", 0)
	require.ErrorIs(t, err, ErrImageTaskNotFound)
	_, _, err = s.Content(context.Background(), 7, "img_one", 2)
	require.ErrorIs(t, err, ErrImageTaskNotFound)
	_, _, err = s.Content(context.Background(), 7, "img_one", -1)
	require.ErrorIs(t, err, ErrImageTaskNotFound)
	encoded, err := json.Marshal(r.entry.Results)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret")
}
func TestImageTaskLogUnavailableOutputsDoNotFailGeneration(t *testing.T) {
	r := &imageLogMemoryRepo{}
	upstream := &httpUpstreamRecorder{}
	gateway := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	s := NewImageTaskLogService(r, gateway)
	e := &ImageTaskLogEntry{TaskID: "img_unsafe", UserID: 7, ItemCount: 1}
	s.Start(context.Background(), e)
	s.Finish(context.Background(), e, 200, []byte(`{"data":[{"url":"http://127.0.0.1/secret"},{"b64_json":"PGh0bWw+"}]}`), false)
	require.Equal(t, "completed", r.entry.Status)
	require.Equal(t, 2, r.entry.ItemCount)
	require.Zero(t, r.entry.SuccessCount)
	require.Empty(t, upstream.requests)
	require.Equal(t, "result_unavailable", r.entry.ErrorCode)
	_, _, err := s.Content(context.Background(), 7, e.TaskID, 0)
	require.ErrorIs(t, err, ErrImageTaskNotFound)
	r.err = errors.New("database down")
	require.NotPanics(t, func() { s.Finish(context.Background(), e, 200, []byte(`{"data":[]}`), false) })
}
func TestImageTaskLogCapturePreservesSSEAndBoundsMemory(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString(b64BackfillPNGBytes)
	capture := NewImageTaskLogCapture()
	stream := "data: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"ignored\"}\n\n" +
		"data: {\"type\":\"image_generation.completed\",\n" +
		"data: \"b64_json\":\"" + b64 + "\"}\n\ndata: [DONE]\n\n"
	for i := 0; i < len(stream); i += 3 {
		capture.Write([]byte(stream[i:min(i+3, len(stream))]), true)
	}
	body, failed := capture.Result(true)
	require.False(t, failed)
	require.Contains(t, string(body), b64)
	require.NotContains(t, string(body), "ignored")
	var result struct {
		Data []any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &result))
	require.Len(t, result.Data, 1)
	capture = NewImageTaskLogCapture()
	capture.limit = 128
	capture.Write([]byte(strings.Repeat("x", 1024)), true)
	require.LessOrEqual(t, len(capture.pending), 128)
	require.LessOrEqual(t, len(capture.body), 128)
}
func TestImageTaskLogCaptureDetectsStreamingErrors(t *testing.T) {
	c := NewImageTaskLogCapture()
	c.Write([]byte("data: {\"type\":\"error\",\"error\":{\"code\":\"upstream_error\",\"message\":\"failed\"}}\n\n"), true)
	body, failed := c.Result(true)
	require.True(t, failed)
	require.Contains(t, string(body), "upstream_error")
}

func TestImageTaskLogErrorDoesNotRetainCredentials(t *testing.T) {
	r := &imageLogMemoryRepo{}
	s := NewImageTaskLogService(r, nil)
	e := &ImageTaskLogEntry{TaskID: "failed", UserID: 7}
	s.Start(context.Background(), e)
	s.Finish(context.Background(), e, 502, []byte(`{"error":{"code":"upstream_error","message":"Rejected Bearer verySecretToken123456 and https://host.example/?key=secret"}}`), false)
	require.Equal(t, "failed", r.entry.Status)
	require.NotContains(t, r.entry.ErrorMessage, "verySecretToken123456")
	require.NotContains(t, r.entry.ErrorMessage, "key=secret")
}

func TestImageTaskLogCaptureOverflowDoesNotTurnSuccessfulStreamIntoFailure(t *testing.T) {
	c := NewImageTaskLogCapture()
	c.limit = 128
	c.Write([]byte("data: {\"type\":\"image_generation.completed\",\"b64_json\":\""+strings.Repeat("A", 512)+"\"}\n\n"), true)
	_, failed := c.Result(true)
	require.False(t, failed, "capture size limit is not a generation failure")
}

func TestImageTaskLogDownloadsUseExistingPublicHostPolicyAndCompactAvailableResults(t *testing.T) {
	r := &imageLogMemoryRepo{}
	upstream := &httpUpstreamRecorder{resp: b64BackfillImageResponse(200, "image/png", b64BackfillPNGBytes)}
	s := NewImageTaskLogService(r, &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream})
	e := &ImageTaskLogEntry{TaskID: "urls", UserID: 7}
	s.Start(context.Background(), e)
	s.Finish(context.Background(), e, 200, []byte(`{"data":[{"url":"https://127.0.0.1/private"},{"url":"https://cdn.example.org/cat.png"}]}`), false)
	require.Equal(t, 2, r.entry.ItemCount)
	require.Equal(t, 1, r.entry.SuccessCount)
	data, _, err := s.Content(context.Background(), 7, e.TaskID, 0)
	require.NoError(t, err)
	require.Equal(t, b64BackfillPNGBytes, data)
	require.Len(t, upstream.requests, 1)
	req := upstream.requests[0]
	require.True(t, HTTPUpstreamPublicHostsOnly(req.Context()))
	require.Empty(t, req.Header.Get("Authorization"))
	require.Empty(t, req.Header.Get("Cookie"))
}
