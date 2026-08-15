package service

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestResolveVideoTaskQuoteDurationPrecedenceAndAcceptedForms(t *testing.T) {
	defaultSeconds := 20
	price := 0.08
	pricing := &ChannelModelPricing{
		BillingMode:         BillingModeVideo,
		VideoPricePerSecond: &price,
		VideoDefaultSeconds: &defaultSeconds,
		VideoAllowedSeconds: []int{5, 10, 15, 20},
	}
	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "seconds string wins", body: `{"seconds":"5","duration":10,"duration_seconds":15}`, want: 5},
		{name: "seconds integral number", body: `{"seconds":10,"duration":15}`, want: 10},
		{name: "seconds integral decimal number", body: `{"seconds":10.0}`, want: 10},
		{name: "seconds integral exponent number", body: `{"seconds":1e1}`, want: 10},
		{name: "seconds exact long integral string", body: `{"seconds":"10.0000000000000000"}`, want: 10},
		{name: "seconds ordinary leading and trailing zeros", body: `{"seconds":"0000000010.00000000"}`, want: 10},
		{name: "seconds integral negative exponent", body: `{"seconds":"100e-1"}`, want: 10},
		{name: "duration string", body: `{"duration":"15","duration_seconds":5}`, want: 15},
		{name: "duration seconds number", body: `{"duration_seconds":5}`, want: 5},
		{name: "configured default", body: `{}`, want: 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			quote, err := ResolveVideoTaskQuote([]byte(tt.body), "seedance-2.0", pricing, 0.0817, 1.25)
			require.NoError(t, err)
			require.Equal(t, tt.want, quote.Effective.Seconds)
		})
	}
}

func TestResolveVideoTaskQuoteRejectsInvalidDuration(t *testing.T) {
	defaultSeconds := 5
	price := 0.08
	pricing := &ChannelModelPricing{BillingMode: BillingModeVideo, VideoPricePerSecond: &price, VideoDefaultSeconds: &defaultSeconds}
	tests := []struct {
		name string
		body string
	}{
		{name: "fraction number", body: `{"seconds":5.5}`},
		{name: "fraction string", body: `{"seconds":"5.5"}`},
		{name: "fraction hidden by float rounding string", body: `{"seconds":"5.0000000000000001"}`},
		{name: "fraction hidden by float rounding number", body: `{"seconds":5.0000000000000001}`},
		{name: "fraction exponent string", body: `{"seconds":"1.01e1"}`},
		{name: "fraction exponent number", body: `{"seconds":1.01e1}`},
		{name: "huge exponent string", body: `{"seconds":"1e1000000"}`},
		{name: "huge exponent number", body: `{"seconds":1e1000000}`},
		{name: "nan string", body: `{"seconds":"NaN"}`},
		{name: "positive infinity string", body: `{"seconds":"+Inf"}`},
		{name: "zero", body: `{"seconds":0}`},
		{name: "negative", body: `{"seconds":-1}`},
		{name: "above maximum", body: `{"seconds":3601}`},
		{name: "boolean", body: `{"seconds":true}`},
		{name: "null", body: `{"seconds":null}`},
		{name: "empty string", body: `{"seconds":""}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ResolveVideoTaskQuote([]byte(tt.body), "seedance-2.0", pricing, 1, 1)
			require.Error(t, err)
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
			require.Equal(t, "VIDEO_TASK_INVALID_REQUEST", infraerrors.Reason(err))
		})
	}
}

func TestResolveVideoTaskQuoteRejectsDerivedAmountOverflow(t *testing.T) {
	defaultSeconds := 3600
	tests := []struct {
		name        string
		price       float64
		rate        float64
		accountRate float64
	}{
		{name: "gross cost overflow", price: math.MaxFloat64, rate: 1, accountRate: 1},
		{name: "actual cost overflow", price: math.MaxFloat64 / 4000, rate: math.MaxFloat64, accountRate: 1},
		{name: "account cost overflow", price: math.MaxFloat64 / 4000, rate: 1, accountRate: math.MaxFloat64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pricing := &ChannelModelPricing{BillingMode: BillingModeVideo, VideoPricePerSecond: &tt.price, VideoDefaultSeconds: &defaultSeconds}
			_, err := ResolveVideoTaskQuote([]byte(`{}`), "seedance", pricing, tt.rate, tt.accountRate)
			require.Error(t, err)
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
			require.Equal(t, "VIDEO_TASK_PRICING_INVALID_CONFIG", infraerrors.Reason(err))
		})
	}
}

func TestResolveVideoTaskQuoteRejectsLexicallyOversizedDurationBeforeExactParsing(t *testing.T) {
	defaultSeconds := 5
	price := 0.08
	pricing := &ChannelModelPricing{BillingMode: BillingModeVideo, VideoPricePerSecond: &price, VideoDefaultSeconds: &defaultSeconds}
	longIntegralMantissa := "1" + strings.Repeat("0", 4096)
	tests := []struct {
		name string
		body string
	}{
		{name: "string huge positive exponent", body: `{"seconds":"1e+100000000"}`},
		{name: "number huge positive exponent", body: `{"seconds":1e+100000000}`},
		{name: "string huge negative exponent", body: `{"seconds":"1e-100000000"}`},
		{name: "number huge negative exponent", body: `{"seconds":1e-100000000}`},
		{name: "string excessive mantissa", body: `{"seconds":"` + longIntegralMantissa + `"}`},
		{name: "number excessive mantissa", body: `{"seconds":` + longIntegralMantissa + `}`},
		{name: "string excessive leading zeros", body: `{"seconds":"` + strings.Repeat("0", 4096) + `5"}`},
		{name: "number excessive leading zeros", body: `{"seconds":` + strings.Repeat("0", 4096) + `5}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ResolveVideoTaskQuote([]byte(tt.body), "seedance", pricing, 1, 1)
			require.Error(t, err)
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
			require.Equal(t, "VIDEO_TASK_INVALID_REQUEST", infraerrors.Reason(err))
		})
	}
}

func TestResolveVideoTaskQuoteEnforcesAllowedSecondsExactly(t *testing.T) {
	defaultSeconds := 5
	price := 0.08
	pricing := &ChannelModelPricing{
		BillingMode: BillingModeVideo, VideoPricePerSecond: &price,
		VideoDefaultSeconds: &defaultSeconds, VideoAllowedSeconds: []int{5, 15},
	}

	_, err := ResolveVideoTaskQuote([]byte(`{"seconds":10}`), "seedance-2.0", pricing, 1, 1)
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
	require.Equal(t, "VIDEO_TASK_INVALID_REQUEST", infraerrors.Reason(err))
}

func TestResolveVideoTaskQuoteResolutionPrecedence(t *testing.T) {
	defaultSeconds := 5
	price := 0.08
	pricing := &ChannelModelPricing{BillingMode: BillingModeVideo, VideoPricePerSecond: &price, VideoDefaultSeconds: &defaultSeconds}
	tests := []struct {
		name  string
		body  string
		model string
		want  string
	}{
		{name: "resolution field", body: `{"resolution":" 1080P "}`, model: "seedance-2.0-480p", want: "1080p"},
		{name: "recognized size", body: `{"size":"1920x1080"}`, model: "seedance-2.0-480p", want: "1080p"},
		{name: "model suffix", body: `{}`, model: "seedance-2.0-fast-4K", want: "4k"},
		{name: "fallback", body: `{}`, model: "seedance-2.0", want: "720p"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			quote, err := ResolveVideoTaskQuote([]byte(tt.body), tt.model, pricing, 1, 1)
			require.NoError(t, err)
			require.Equal(t, tt.want, quote.Effective.Resolution)
		})
	}
}

func TestResolveVideoTaskQuoteSelectsNormalizedTierBeforeDefaultAndCalculatesAmounts(t *testing.T) {
	defaultSeconds := 5
	defaultPrice := 0.08
	tierPrice := 0.10
	pricing := &ChannelModelPricing{
		BillingMode: BillingModeVideo, VideoPricePerSecond: &defaultPrice, VideoDefaultSeconds: &defaultSeconds,
		Intervals: []PricingInterval{{TierLabel: " 1080P ", VideoPricePerSecond: &tierPrice}},
	}

	quote, err := ResolveVideoTaskQuote([]byte(`{"seconds":"5","resolution":"1080p"}`), "seedance-2.0", pricing, 0.0817, 1.25)
	require.NoError(t, err)
	require.Equal(t, VideoTaskQuote{
		BillingMode:           BillingModeVideo,
		BillingModel:          "seedance-2.0",
		Effective:             VideoTaskEffectiveParams{Seconds: 5, Resolution: "1080p", VideoCount: 1},
		UnitPriceUSD:          0.10,
		GrossCostUSD:          0.50,
		ActualCostUSD:         0.04085,
		AccountUnitPriceUSD:   0.10,
		AccountBaseCostUSD:    0.50,
		AccountCostUSD:        0.625,
		RateMultiplier:        0.0817,
		AccountRateMultiplier: 1.25,
	}, quote)

	quote, err = ResolveVideoTaskQuote([]byte(`{"seconds":5,"resolution":"480p"}`), "seedance-2.0", pricing, 1, 1)
	require.NoError(t, err)
	require.Equal(t, defaultPrice, quote.UnitPriceUSD)
}

func TestResolveVideoTaskQuotePerRequestPricing(t *testing.T) {
	price := 65.0
	defaultSeconds := 30
	videoPrice := 0.08
	intervalPrice := 100.0
	pricing := &ChannelModelPricing{
		BillingMode:         BillingModePerRequest,
		PerRequestPrice:     &price,
		VideoDefaultSeconds: &defaultSeconds,
		VideoPricePerSecond: &videoPrice,
		Intervals:           []PricingInterval{{TierLabel: "480p", PerRequestPrice: &intervalPrice}},
	}

	quote, err := ResolveVideoTaskQuote([]byte(`{"seconds":"5","resolution":"480p"}`), "seedance-2.0", pricing, 0.1065, 0.071)
	require.NoError(t, err)
	require.Equal(t, VideoTaskQuote{
		BillingMode:           BillingModePerRequest,
		BillingModel:          "seedance-2.0",
		Effective:             VideoTaskEffectiveParams{Seconds: 5, Resolution: "480p", VideoCount: 1},
		UnitPriceUSD:          65,
		GrossCostUSD:          65,
		ActualCostUSD:         6.9225,
		AccountUnitPriceUSD:   65,
		AccountBaseCostUSD:    65,
		AccountCostUSD:        4.615,
		RateMultiplier:        0.1065,
		AccountRateMultiplier: 0.071,
	}, quote)
	require.True(t, validVideoTaskQuote(&quote))
}

func TestResolveVideoTaskQuotePerRequestNilPriceIsZeroCost(t *testing.T) {
	defaultSeconds := 30
	videoPrice := 0.08
	intervalPrice := 100.0
	pricing := &ChannelModelPricing{
		BillingMode:         BillingModePerRequest,
		VideoDefaultSeconds: &defaultSeconds,
		VideoPricePerSecond: &videoPrice,
		Intervals:           []PricingInterval{{TierLabel: "480p", PerRequestPrice: &intervalPrice}},
	}

	quote, err := ResolveVideoTaskQuote([]byte(`{}`), "seedance-2.0", pricing, 0.1065, 0.071)
	require.NoError(t, err)
	require.Equal(t, VideoTaskQuote{
		BillingMode:           BillingModePerRequest,
		BillingModel:          "seedance-2.0",
		Effective:             VideoTaskEffectiveParams{VideoCount: 1},
		RateMultiplier:        0.1065,
		AccountRateMultiplier: 0.071,
	}, quote)
	require.True(t, validVideoTaskQuote(&quote))
}

func TestResolveVideoTaskQuotePerRequestRejectsNonfinitePrice(t *testing.T) {
	nan := math.NaN()
	price := 65.0
	tests := []struct {
		name        string
		pricing     *ChannelModelPricing
		rate        float64
		accountRate float64
	}{
		{name: "price", pricing: &ChannelModelPricing{BillingMode: BillingModePerRequest, PerRequestPrice: &nan}, rate: 1, accountRate: 1},
		{name: "rate multiplier", pricing: &ChannelModelPricing{BillingMode: BillingModePerRequest, PerRequestPrice: &price}, rate: nan, accountRate: 1},
		{name: "account multiplier", pricing: &ChannelModelPricing{BillingMode: BillingModePerRequest, PerRequestPrice: &price}, rate: 1, accountRate: nan},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ResolveVideoTaskQuote([]byte(`{}`), "seedance-2.0", tt.pricing, tt.rate, tt.accountRate)
			require.Error(t, err)
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
			require.Equal(t, "VIDEO_TASK_PRICING_INVALID_CONFIG", infraerrors.Reason(err))
		})
	}
}

func TestResolveVideoTaskQuotePerRequestPrioritizesInvalidConfigOverMalformedBody(t *testing.T) {
	nan := math.NaN()
	inf := math.Inf(1)
	price := 65.0
	outOfRangePrice := 10000000000.0
	tests := []struct {
		name        string
		price       float64
		rate        float64
		accountRate float64
	}{
		{name: "NaN price", price: nan, rate: 1, accountRate: 1},
		{name: "infinite price", price: inf, rate: 1, accountRate: 1},
		{name: "out of range price", price: outOfRangePrice, rate: 1, accountRate: 1},
		{name: "NaN rate multiplier", price: price, rate: nan, accountRate: 1},
		{name: "infinite rate multiplier", price: price, rate: inf, accountRate: 1},
		{name: "rate multiplier overflow", price: price, rate: math.MaxFloat64, accountRate: 1},
		{name: "NaN account multiplier", price: price, rate: 1, accountRate: nan},
		{name: "infinite account multiplier", price: price, rate: 1, accountRate: inf},
		{name: "account multiplier overflow", price: price, rate: 1, accountRate: math.MaxFloat64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pricing := &ChannelModelPricing{BillingMode: BillingModePerRequest, PerRequestPrice: &tt.price}
			_, err := ResolveVideoTaskQuote([]byte(`{`), "seedance-2.0", pricing, tt.rate, tt.accountRate)
			require.Error(t, err)
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
			require.Equal(t, "VIDEO_TASK_PRICING_INVALID_CONFIG", infraerrors.Reason(err))
		})
	}
}

func TestResolveVideoTaskQuotePerRequestRejectsMalformedBodyWhenConfigValid(t *testing.T) {
	price := 65.0
	pricing := &ChannelModelPricing{BillingMode: BillingModePerRequest, PerRequestPrice: &price}

	_, err := ResolveVideoTaskQuote([]byte(`{`), "seedance-2.0", pricing, 1, 1)
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
	require.Equal(t, "VIDEO_TASK_INVALID_REQUEST", infraerrors.Reason(err))
}

func TestResolveVideoTaskQuotePerRequestTreatsDurationMetadataAsOptional(t *testing.T) {
	price := 65.0
	pricing := &ChannelModelPricing{BillingMode: BillingModePerRequest, PerRequestPrice: &price}
	tests := []struct {
		name       string
		body       string
		wantSecond int
		wantRes    string
	}{
		{name: "missing duration", body: `{}`, wantSecond: 0, wantRes: ""},
		{name: "empty duration", body: `{"seconds":""}`, wantSecond: 0, wantRes: ""},
		{name: "malformed duration", body: `{"duration":"five"}`, wantSecond: 0, wantRes: ""},
		{name: "out of range duration", body: `{"duration_seconds":3601}`, wantSecond: 0, wantRes: ""},
		{name: "size metadata", body: `{"size":"1920x1080"}`, wantSecond: 0, wantRes: "1080p"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			quote, err := ResolveVideoTaskQuote([]byte(tt.body), "seedance-2.0", pricing, 1, 1)
			require.NoError(t, err)
			require.Equal(t, tt.wantSecond, quote.Effective.Seconds)
			require.Equal(t, tt.wantRes, quote.Effective.Resolution)
			require.Equal(t, 1, quote.Effective.VideoCount)
			require.True(t, validVideoTaskQuote(&quote))
		})
	}
}

func TestResolveVideoTaskQuoteRejectsInvalidConfigurationWithTyped400(t *testing.T) {
	defaultSeconds := 5
	nan := math.NaN()
	tests := []struct {
		name    string
		pricing *ChannelModelPricing
	}{
		{name: "nil pricing", pricing: nil},
		{name: "unsupported mode", pricing: &ChannelModelPricing{BillingMode: BillingModeToken}},
		{name: "missing default", pricing: &ChannelModelPricing{BillingMode: BillingModeVideo}},
		{name: "nonfinite price", pricing: &ChannelModelPricing{BillingMode: BillingModeVideo, VideoDefaultSeconds: &defaultSeconds, VideoPricePerSecond: &nan}},
		{name: "missing matching and default price", pricing: &ChannelModelPricing{BillingMode: BillingModeVideo, VideoDefaultSeconds: &defaultSeconds}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ResolveVideoTaskQuote([]byte(`{}`), "seedance-2.0", tt.pricing, 1, 1)
			require.Error(t, err)
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
			require.Equal(t, "VIDEO_TASK_PRICING_INVALID_CONFIG", infraerrors.Reason(err))
		})
	}
}

func TestVideoTaskQuoteSnapshotJSONRoundTrip(t *testing.T) {
	tests := []VideoTaskQuote{
		{BillingMode: BillingModeVideo, BillingModel: "seedance", Effective: VideoTaskEffectiveParams{Seconds: 5, Resolution: "720p", VideoCount: 1}, UnitPriceUSD: 0.08, GrossCostUSD: 0.4, ActualCostUSD: 0.2, AccountUnitPriceUSD: 0.08, AccountBaseCostUSD: 0.4, AccountCostUSD: 0.4, RateMultiplier: 0.5, AccountRateMultiplier: 1},
		{BillingMode: BillingModePerRequest, BillingModel: "seedance", Effective: VideoTaskEffectiveParams{VideoCount: 1}, UnitPriceUSD: 65, GrossCostUSD: 65, ActualCostUSD: 6.9225, AccountUnitPriceUSD: 65, AccountBaseCostUSD: 65, AccountCostUSD: 4.615, RateMultiplier: 0.1065, AccountRateMultiplier: 0.071},
	}
	for _, want := range tests {
		raw, err := json.Marshal(want)
		require.NoError(t, err)
		var got VideoTaskQuote
		require.NoError(t, json.Unmarshal(raw, &got))
		require.Equal(t, want, got)

		var snapshot map[string]any
		require.NoError(t, json.Unmarshal(raw, &snapshot))
		fromMetadata, ok := videoTaskQuoteFromMetadata(map[string]any{"request_metadata": map[string]any{"video_pricing_snapshot": snapshot}})
		require.True(t, ok)
		require.Equal(t, want, fromMetadata)
	}
}

func TestValidVideoTaskQuoteEnforcesOutputBoundAndAccountBaseFormula(t *testing.T) {
	quote := VideoTaskQuote{
		BillingMode: BillingModeVideo, BillingModel: "video",
		Effective:    VideoTaskEffectiveParams{Seconds: 5, Resolution: "720p", VideoCount: VideoTaskMaxOutputs},
		UnitPriceUSD: 0.08, GrossCostUSD: 0.08 * 5 * VideoTaskMaxOutputs,
		ActualCostUSD: 0.08 * 5 * VideoTaskMaxOutputs, RateMultiplier: 1,
		AccountUnitPriceUSD: 0.10, AccountBaseCostUSD: 0.10 * 5 * VideoTaskMaxOutputs,
		AccountCostUSD: 0.10 * 5 * VideoTaskMaxOutputs, AccountRateMultiplier: 1,
	}
	require.True(t, validVideoTaskQuote(&quote))

	quote.Effective.VideoCount = VideoTaskMaxOutputs + 1
	require.False(t, validVideoTaskQuote(&quote))
	quote.Effective.VideoCount = VideoTaskMaxOutputs
	quote.AccountBaseCostUSD++
	require.False(t, validVideoTaskQuote(&quote))
	quote.AccountBaseCostUSD = 0.10 * 5 * VideoTaskMaxOutputs
	quote.ActualCostUSD++
	require.False(t, validVideoTaskQuote(&quote))
	quote.ActualCostUSD = quote.GrossCostUSD
	quote.AccountCostUSD++
	require.False(t, validVideoTaskQuote(&quote))
}

func TestVideoTaskQuotePreservesPricingPrecisionAndRoundsAppliedAmounts(t *testing.T) {
	seconds := 3
	unit := 0.1234567891
	pricing := &ChannelModelPricing{BillingMode: BillingModeVideo, VideoDefaultSeconds: &seconds, VideoPricePerSecond: &unit}
	quote, err := ResolveVideoTaskQuote([]byte(`{"seconds":3}`), "video", pricing, 0.3333333333, 1.234567891)
	require.NoError(t, err)
	require.Equal(t, 0.3703703673, quote.GrossCostUSD)
	require.Equal(t, 0.3703703673, quote.AccountBaseCostUSD)
	require.Equal(t, 0.12345679, quote.ActualCostUSD)
	require.Equal(t, 0.45724736, quote.AccountCostUSD)
	require.True(t, validVideoTaskQuote(&quote))

	tests := []struct {
		name   string
		tamper func(*VideoTaskQuote)
	}{
		{name: "gross tenth digit", tamper: func(q *VideoTaskQuote) { q.GrossCostUSD += 0.0000000001 }},
		{name: "actual eighth digit", tamper: func(q *VideoTaskQuote) { q.ActualCostUSD += 0.00000001 }},
		{name: "account base tenth digit", tamper: func(q *VideoTaskQuote) { q.AccountBaseCostUSD += 0.0000000001 }},
		{name: "account final eighth digit", tamper: func(q *VideoTaskQuote) { q.AccountCostUSD += 0.00000001 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tampered := quote
			tt.tamper(&tampered)
			require.False(t, validVideoTaskQuote(&tampered))
		})
	}
}

func TestValidVideoTaskQuoteRejectsTamperedPerRequestAmounts(t *testing.T) {
	quote := VideoTaskQuote{
		BillingMode:           BillingModePerRequest,
		BillingModel:          "video",
		Effective:             VideoTaskEffectiveParams{VideoCount: 1},
		UnitPriceUSD:          65,
		GrossCostUSD:          65,
		ActualCostUSD:         6.9225,
		AccountUnitPriceUSD:   65,
		AccountBaseCostUSD:    65,
		AccountCostUSD:        4.615,
		RateMultiplier:        0.1065,
		AccountRateMultiplier: 0.071,
	}
	require.True(t, validVideoTaskQuote(&quote))

	tests := []struct {
		name   string
		tamper func(*VideoTaskQuote)
	}{
		{name: "gross", tamper: func(q *VideoTaskQuote) { q.GrossCostUSD++ }},
		{name: "actual", tamper: func(q *VideoTaskQuote) { q.ActualCostUSD += 0.00000001 }},
		{name: "account base", tamper: func(q *VideoTaskQuote) { q.AccountBaseCostUSD++ }},
		{name: "account final", tamper: func(q *VideoTaskQuote) { q.AccountCostUSD += 0.00000001 }},
		{name: "negative audit duration", tamper: func(q *VideoTaskQuote) { q.Effective.Seconds = -1 }},
		{name: "out of range audit duration", tamper: func(q *VideoTaskQuote) { q.Effective.Seconds = videoTaskMaxSeconds + 1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tampered := quote
			tt.tamper(&tampered)
			require.False(t, validVideoTaskQuote(&tampered))
		})
	}
}

func TestApplyEffectiveVideoDuration(t *testing.T) {
	got, err := applyEffectiveVideoDuration([]byte(`{"model":"seedance","duration":5,"duration_seconds":"10","seconds":"3","prompt":"city"}`), 15)
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"seedance","seconds":"15","prompt":"city"}`, string(got))
}
