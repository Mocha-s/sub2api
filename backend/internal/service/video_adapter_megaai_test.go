package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type megaAiRoundTripFunc func(*http.Request) (*http.Response, error)

func (f megaAiRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMegaAiDurationDoesNotExcludeMatchingAccount(t *testing.T) {
	account := &Account{ID: 736, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"base_url": "https://newapi.megabyai.cc", "api_key": "test-key",
		"model_mapping": map[string]any{"T1-seedance-2.5": "T1-seedance-2.5"},
	}}
	body := []byte(`{"model":"T1-seedance-2.5","prompt":"waves","duration":15}`)
	provider := NewAccountVideoTaskProvider(nil, nil).(*accountVideoTaskProvider)
	require.True(t, account.IsModelSupported("T1-seedance-2.5"))
	err := provider.ValidateCreate(context.Background(), account, body, "application/json", "T1-seedance-2.5")
	require.Error(t, err)
	require.True(t, isVideoTaskPreSubmitAdapterMismatch(err))
	account.Credentials[VideoAdapterMetadataKey] = VideoAdapterMegaAi
	require.NoError(t, provider.ValidateCreate(context.Background(), account, body, "application/json", "T1-seedance-2.5"))
	forwarded, err := megaAiCreateBody(body, "T1-seedance-2.5")
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(forwarded, &payload))
	require.Equal(t, float64(15), payload["duration"])
	require.NotContains(t, payload, "seconds")
}

func TestMegaAiAllConfiguredModelsDispatchCanonicalRequests(t *testing.T) {
	models := []string{"T1-seedance-2.5", "T2-seedance-2.5", "T3-seedance-2.0", "video-sd20-10s", "sd2-full-480p-15s", "seedance-2.0-m5", "seedance-2.5-m2", "seedance-2.5-m5", "seedance-2.5-flf-m5", "seedance-2.0-fast-m5", "seedance-2.0-mini-m5"}
	for _, model := range models {
		for _, base := range []string{"https://megaviews.top/v1", "https://www.megaviews.top", "https://newapi.megaviews.top/v1/", "https://newapi.megabyai.cc"} {
			t.Run(model+"/"+base, func(t *testing.T) {
				calls := 0
				client := &http.Client{Transport: megaAiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					require.Equal(t, "/v1/videos", req.URL.Path)
					require.Equal(t, http.MethodPost, req.Method)
					require.Equal(t, "Bearer test-key", req.Header.Get("Authorization"))
					var body map[string]json.RawMessage
					require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
					require.JSONEq(t, `"`+model+`"`, string(body["model"]))
					require.Equal(t, "15", string(body["duration"]))
					require.JSONEq(t, `"1080p"`, string(body["resolution"]))
					require.JSONEq(t, `"16:9"`, string(body["ratio"]))
					require.JSONEq(t, `["reference.mp4"]`, string(body["referenceVideos"]))
					require.NotContains(t, body, "size")
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":"upstream-task","status":"queued"}`))}, nil
				})}
				account := &Account{ID: 736, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": base, "api_key": "test-key", "video_adapter": "megaai", "model_mapping": map[string]any{model: model}}}
				adapter := &megaAiVideoAdapter{provider: &openAICompatibleVideoProvider{client: client}}
				result, err := adapter.Create(context.Background(), account, []byte(`{"model":"client-alias","prompt":"waves","seconds":"15.0","size":"1920x1080","referenceVideos":["reference.mp4"]}`), "application/json", account.GetMappedModel(model))
				require.NoError(t, err)
				require.Equal(t, 1, calls)
				require.Equal(t, "upstream-task", result.ProviderTaskID)
			})
		}
	}
}

func TestMegaAiDurationAndSizeNormalization(t *testing.T) {
	for _, duration := range []string{`15`, `15.0`, `1.5e1`, `"15"`, `"15.000"`} {
		body, err := megaAiCreateBody([]byte(`{"duration":`+duration+`,"size":"720x1280"}`), "Seedance-ExactCase")
		require.NoError(t, err)
		require.JSONEq(t, `{"model":"Seedance-ExactCase","duration":15,"resolution":"720p","ratio":"9:16"}`, string(body))
	}
	for _, duration := range []string{`1.5`, `null`, `true`, `"15.000000000000001"`} {
		_, err := megaAiCreateBody([]byte(`{"duration":`+duration+`}`), "video-model")
		require.Error(t, err)
	}
	body, err := megaAiCreateBody([]byte(`{"size":"1920x1080","resolution":"480p","ratio":"1:1","seconds":5,"duration":15}`), "video-model")
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"video-model","duration":5,"resolution":"480p","ratio":"1:1"}`, string(body))
}

func TestMegaAiPortraitSizeMatchesBillingResolution(t *testing.T) {
	seconds := 15
	base, tier := 0.2, 1.2
	pricing := &ChannelModelPricing{BillingMode: BillingModeVideoReference, VideoDefaultSeconds: &seconds, VideoPricePerSecond: &base,
		Intervals: []PricingInterval{{TierLabel: "1080p", VideoPricePerSecond: &tier}}}
	body := []byte(`{"prompt":"waves","size":"1080x1920","duration":15}`)
	quote, err := ResolveVideoTaskQuote(body, "video-model", pricing, 1, 1)
	require.NoError(t, err)
	require.Equal(t, "1080p", quote.Effective.Resolution)
	require.Equal(t, 18.0, quote.GrossCostUSD)
	forwarded, err := megaAiCreateBody(body, "video-model")
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(forwarded, &payload))
	require.Equal(t, quote.Effective.Resolution, payload["resolution"])
	require.Equal(t, "9:16", payload["ratio"])
}
