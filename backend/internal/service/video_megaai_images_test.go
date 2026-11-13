package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMegaAiInlineImagesBecomeStoredURLs(t *testing.T) {
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	storage := &fakeImageStorage{}
	uploader := NewImageResultUploader(storage, "images/", 32<<20, nil)
	adapter := &megaAiVideoAdapter{imageStorage: func() (*ImageResultUploader, bool) { return uploader, true }}
	body := []byte(`{"images":["` + dataURL + `","https://example.com/input.png"],"referenceImages":["` + dataURL + `"],"prompt":"waves"}`)
	got, err := adapter.prepareInlineImages(context.Background(), body)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(got, &payload))
	require.Equal(t, "https://example.com/input.png", payload["images"].([]any)[1])
	require.Len(t, storage.saved, 2)
	require.Equal(t, pngBytes, storage.saved[0].data)
	require.Equal(t, "image/png", storage.saved[0].contentType)
	require.Contains(t, storage.saved[0].key, "video-reference/")
	require.NotEqual(t, storage.saved[0].key, storage.saved[1].key)
	require.NotContains(t, string(got), "base64")
	require.Contains(t, string(body), "base64")
}

func TestMegaAiInlineImageFailureDoesNotLeakPayload(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		storage     *fakeImageStorage
		limit       int64
	}{
		{"disabled", "data:image/png;base64,aGVsbG8=", nil, 0},
		{"invalid base64", "data:image/png;base64,SECRET_NOT_BASE64", &fakeImageStorage{}, 0},
		{"too large", "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes), &fakeImageStorage{}, 1},
		{"not image", "data:text/html;base64,aGVsbG8=", &fakeImageStorage{}, 0},
		{"storage failed", "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes), &fakeImageStorage{err: errors.New("secret storage credentials")}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter := &megaAiVideoAdapter{}
			if tc.storage != nil {
				adapter.imageStorage = func() (*ImageResultUploader, bool) {
					return NewImageResultUploader(tc.storage, "images/", tc.limit, nil), true
				}
			}
			_, err := adapter.prepareInlineImages(context.Background(), []byte(`{"images":["`+tc.value+`"]}`))
			require.Error(t, err)
			require.NotContains(t, err.Error(), tc.value)
			require.NotContains(t, err.Error(), "secret storage credentials")
		})
	}
}

func TestMegaAiHTTPImagesNeedNoStorageOrDownload(t *testing.T) {
	adapter := &megaAiVideoAdapter{}
	body := []byte(`{"images":["https://example.com/a.png"],"prompt":"waves"}`)
	got, err := adapter.prepareInlineImages(context.Background(), body)
	require.NoError(t, err)
	require.JSONEq(t, string(body), string(got))
}

func TestMegaAiDispatchUploadsInlineImagesBeforeSending(t *testing.T) {
	for _, uploadFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "upload succeeds", true: "upload fails"}[uploadFailure], func(t *testing.T) {
			storage := &fakeImageStorage{}
			if uploadFailure {
				storage.err = errors.New("storage failed")
			}
			uploader := NewImageResultUploader(storage, "images/", 0, nil)
			calls := 0
			adapter := &megaAiVideoAdapter{
				imageStorage: func() (*ImageResultUploader, bool) { return uploader, true },
				provider: &openAICompatibleVideoProvider{client: &http.Client{Transport: megaAiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					var body map[string]any
					require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
					require.Len(t, storage.saved, 1)
					require.Equal(t, "https://cdn.test/"+storage.saved[0].key, body["images"].([]any)[0])
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":"upstream","status":"queued"}`))}, nil
				})}},
			}
			account := &Account{Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test", "base_url": "https://megaviews.top/v1"}}
			dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
			result, err := adapter.Create(context.Background(), account, []byte(`{"prompt":"waves","images":["`+dataURL+`"]}`), "application/json", "T1-seedance-2.5")
			if uploadFailure {
				require.Error(t, err)
				require.Zero(t, calls)
				return
			}
			require.NoError(t, err)
			require.Equal(t, 1, calls)
			require.Equal(t, "upstream", result.ProviderTaskID)
		})
	}
}
