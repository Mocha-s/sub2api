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

func TestAutoVideoGenerationsAdapterDetectsNewAPIAndCachesForCreate(t *testing.T) {
	var newAPIProbeCalls int
	var newAPICreateCalls int
	var seedanceProbeCalls int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		switch r.URL.Path {
		case "/v1/video/generations":
			var payload map[string]any
			require.NoError(t, json.Unmarshal(body, &payload))
			if payload["model"] == autoVideoGenerationsProbeModel {
				newAPIProbeCalls++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"message":"Invalid request: model not found","type":"new_api_error"}}`)
				return
			}
			newAPICreateCalls++
			require.Equal(t, "Bearer sk-auto", r.Header.Get("Authorization"))
			require.JSONEq(t, `{"model":"seedance-2.0","prompt":"city","duration":5,"ratio":"16:9","resolution":"720p"}`, string(body))
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"task_auto","object":"video","model":"seedance-2.0","status":"queued","progress":0}`)
		case "/video-generations":
			seedanceProbeCalls++
			w.WriteHeader(http.StatusMovedPermanently)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	adapter := NewAutoVideoGenerationsAdapter(nil)
	validator, ok := adapter.(VideoTaskCreateValidator)
	require.True(t, ok)
	account := &Account{ID: 42, Credentials: map[string]any{"base_url": server.URL, "api_key": "sk-auto"}}
	body := []byte(`{"model":"seedance-2.0","prompt":"city","duration":5,"ratio":"16:9","resolution":"720p"}`)

	require.NoError(t, validator.ValidateCreate(context.Background(), account, body, "application/json", "seedance-2.0"))
	result, err := adapter.Create(context.Background(), account, body, "application/json", "seedance-2.0")

	require.NoError(t, err)
	require.Equal(t, "task_auto", result.ProviderTaskID)
	require.Equal(t, VideoTaskStatusQueued, result.Status)
	require.Equal(t, VideoAdapterNewAPIVideoGenerations, result.Metadata[VideoAdapterMetadataKey])
	require.Equal(t, 1, newAPIProbeCalls)
	require.Equal(t, 1, newAPICreateCalls)
	require.Zero(t, seedanceProbeCalls)
}

func TestAutoVideoGenerationsAdapterCreateErrorCarriesDetectedAdapterMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		switch r.URL.Path {
		case "/v1/video/generations":
			var payload map[string]any
			require.NoError(t, json.Unmarshal(body, &payload))
			w.Header().Set("Content-Type", "application/json")
			if payload["model"] == autoVideoGenerationsProbeModel {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"message":"model not found"}}`)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"code":"do_request_failed","message":"temporary unavailable"}`)
		case "/video-generations":
			w.WriteHeader(http.StatusMovedPermanently)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	adapter := NewAutoVideoGenerationsAdapter(nil)
	account := &Account{ID: 44, Credentials: map[string]any{"base_url": server.URL, "api_key": "sk-auto"}}
	body := []byte(`{"model":"seedance-2.0","prompt":"city","duration":5,"resolution":"720p"}`)

	result, err := adapter.Create(context.Background(), account, body, "application/json", "seedance-2.0")

	require.Nil(t, result)
	require.Error(t, err)
	var carrier videoTaskMetadataCarrier
	require.ErrorAs(t, err, &carrier)
	require.Equal(t, VideoAdapterNewAPIVideoGenerations, carrier.VideoTaskMetadata()[VideoAdapterMetadataKey])
}

func TestAutoVideoGenerationsAdapterFallsBackToSeedanceAPIV1(t *testing.T) {
	var newAPIProbeCalls int
	var seedanceProbeCalls int
	var seedanceCreateCalls int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		switch r.URL.Path {
		case "/api/v1/video/generations":
			newAPIProbeCalls++
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusMovedPermanently)
		case "/api/v1/video-generations":
			var payload map[string]any
			require.NoError(t, json.Unmarshal(body, &payload))
			if payload["model"] == autoVideoGenerationsProbeModel {
				seedanceProbeCalls++
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"code":400,"message":"model not found","data":null}`)
				return
			}
			seedanceCreateCalls++
			require.Equal(t, "Bearer sk-auto", r.Header.Get("Authorization"))
			require.JSONEq(t, `{"model":"seedance-2.0","prompt":"city","duration":5,"ratio":"16:9","resolution":"720p"}`, string(body))
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"code":0,"message":"accepted","data":{"id":"task_seedance","status":"queued","progress":0}}`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	adapter := NewAutoVideoGenerationsAdapter(nil)
	account := &Account{ID: 43, Credentials: map[string]any{"base_url": server.URL + "/api/v1", "api_key": "sk-auto"}}
	body := []byte(`{"model":"seedance-2.0","prompt":"city","duration":5,"ratio":"16:9","resolution":"720p"}`)

	result, err := adapter.Create(context.Background(), account, body, "application/json", "seedance-2.0")

	require.NoError(t, err)
	require.Equal(t, "task_seedance", result.ProviderTaskID)
	require.Equal(t, VideoTaskStatusQueued, result.Status)
	require.Equal(t, VideoAdapterSeedanceAPIV1, result.Metadata[VideoAdapterMetadataKey])
	require.Equal(t, 1, newAPIProbeCalls)
	require.Equal(t, 1, seedanceProbeCalls)
	require.Equal(t, 1, seedanceCreateCalls)
}

func TestAutoVideoGenerationsProbeRejectsGenericJSONNotFound(t *testing.T) {
	matched, err := autoVideoGenerationsProbeMatches(http.StatusNotFound, "application/json", []byte(`{"error":{"message":"route not found"}}`))
	require.NoError(t, err)
	require.False(t, matched)
}

func TestAutoVideoGenerationsCacheKeyChangesWithAccountBaseURL(t *testing.T) {
	first := autoVideoGenerationsCacheKey(&Account{ID: 42, Credentials: map[string]any{"base_url": "https://first.example/v1"}})
	second := autoVideoGenerationsCacheKey(&Account{ID: 42, Credentials: map[string]any{"base_url": "https://second.example/v1"}})
	require.NotEqual(t, first, second)
}
