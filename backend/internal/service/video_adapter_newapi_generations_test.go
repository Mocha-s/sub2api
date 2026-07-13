//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewAPIVideoGenerationsAdapterCreateUsesVideoGenerationsPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/v1/video/generations", r.URL.Path)
		require.Equal(t, "Bearer sk-newapi", r.Header.Get("Authorization"))
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.JSONEq(t, `{"model":"seedance-2.0","prompt":"city","duration":5,"ratio":"16:9","resolution":"720p"}`, string(body))
		_, _ = io.WriteString(w, `{"id":"task_newapi","object":"video","model":"seedance-2.0","status":"queued","progress":0}`)
	}))
	defer server.Close()

	adapter := NewNewAPIVideoGenerationsAdapter(nil)
	account := &Account{Credentials: map[string]any{"base_url": server.URL, "api_key": "sk-newapi"}}

	result, err := adapter.Create(context.Background(), account, []byte(`{"model":"seedance-2.0","prompt":"city","duration":5,"ratio":"16:9","resolution":"720p"}`), "application/json", "seedance-2.0")

	require.NoError(t, err)
	require.Equal(t, "task_newapi", result.ProviderTaskID)
	require.Equal(t, VideoTaskStatusQueued, result.Status)
	require.Equal(t, "queued", result.ProviderStatus)
	require.Equal(t, VideoAdapterNewAPIVideoGenerations, result.Metadata[VideoAdapterMetadataKey])
}

func TestNewAPIVideoGenerationsAdapterFetchUsesVideoGenerationsPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/v1/video/generations/task_newapi", r.URL.Path)
		require.Equal(t, "Bearer sk-newapi", r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `{"id":"task_newapi","object":"video","model":"seedance-2.0","status":"completed","progress":100,"video_url":"https://cdn.example/newapi.mp4"}`)
	}))
	defer server.Close()

	adapter := NewNewAPIVideoGenerationsAdapter(nil)
	account := &Account{Credentials: map[string]any{"base_url": server.URL, "api_key": "sk-newapi"}}
	task := &VideoTask{ProviderTaskID: "task_newapi"}

	result, err := adapter.Fetch(context.Background(), account, task)

	require.NoError(t, err)
	require.Equal(t, "task_newapi", result.ProviderTaskID)
	require.Equal(t, VideoTaskStatusCompleted, result.Status)
	require.Equal(t, "completed", result.ProviderStatus)
	require.Equal(t, "https://cdn.example/newapi.mp4", result.Metadata["result_url"])
	require.Equal(t, VideoAdapterNewAPIVideoGenerations, result.Metadata[VideoAdapterMetadataKey])
}

func TestNewAPIVideoGenerationsAdapterFetchUsesPersistedIDWhenResponseOmitsID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"processing","progress":25}`)
	}))
	defer server.Close()
	adapter := NewNewAPIVideoGenerationsAdapter(nil)
	account := &Account{Credentials: map[string]any{"base_url": server.URL, "api_key": "sk-newapi"}}

	result, err := adapter.Fetch(context.Background(), account, &VideoTask{ProviderTaskID: "task_persisted"})

	require.NoError(t, err)
	require.Equal(t, "task_persisted", result.ProviderTaskID)
}
