//go:build unit

package admin

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin/binding"
	"github.com/stretchr/testify/require"
)

func TestChannelModelPricingRequestAcceptsVideoBillingMode(t *testing.T) {
	req := createChannelRequest{
		Name: "video",
		ModelPricing: []channelModelPricingRequest{{
			Models:      []string{"sora-2"},
			BillingMode: "video",
		}},
	}
	require.NoError(t, binding.Validator.ValidateStruct(req))
}

func TestVideoPricingDTOConversions(t *testing.T) {
	req := channelModelPricingRequest{
		Models:              []string{"sora-2"},
		BillingMode:         "video",
		VideoPricePerSecond: float64Ptr(0.03),
		VideoDefaultSeconds: intPtr(10),
		VideoAllowedSeconds: []int{5, 10},
		Intervals: []pricingIntervalRequest{{
			TierLabel:           "hd",
			VideoPricePerSecond: float64Ptr(0.05),
		}},
	}

	pricing := pricingRequestToService([]channelModelPricingRequest{req}, pricingScopePrimary)[0]
	require.Equal(t, service.BillingModeVideo, pricing.BillingMode)
	require.Equal(t, float64Ptr(0.03), pricing.VideoPricePerSecond)
	require.Equal(t, intPtr(10), pricing.VideoDefaultSeconds)
	require.Equal(t, []int{5, 10}, pricing.VideoAllowedSeconds)
	require.Equal(t, float64Ptr(0.05), pricing.Intervals[0].VideoPricePerSecond)

	resp := pricingToResponse(&pricing, pricingScopePrimary)
	raw, err := json.Marshal(resp)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, 0.03, decoded["video_price_per_second"])
	require.Equal(t, float64(10), decoded["video_default_seconds"])
	require.Equal(t, []any{float64(5), float64(10)}, decoded["video_allowed_seconds"])

	intervals := decoded["intervals"].([]any)
	interval := intervals[0].(map[string]any)
	require.Equal(t, 0.05, interval["video_price_per_second"])
}

func TestVideoPricingAPIRequestCanonicalizesTierLabelsAndPreservesZeroPrice(t *testing.T) {
	req := channelModelPricingRequest{
		Models:              []string{"sora-2"},
		BillingMode:         "video",
		VideoDefaultSeconds: intPtr(10),
		Intervals: []pricingIntervalRequest{
			{TierLabel: " 720P ", VideoPricePerSecond: float64Ptr(0)},
			{TierLabel: " CINEMA ", VideoPricePerSecond: float64Ptr(0.05)},
		},
	}

	pricing := pricingRequestToService([]channelModelPricingRequest{req}, pricingScopePrimary)[0]
	require.Equal(t, "720p", pricing.Intervals[0].TierLabel)
	require.Equal(t, float64(0), *pricing.Intervals[0].VideoPricePerSecond)
	require.Equal(t, "cinema", pricing.Intervals[1].TierLabel)
	require.NoError(t, service.ValidateIntervals(pricing.Intervals, pricing.BillingMode))
}

func TestVideoPricingAPIRequestRejectsNormalizedBlankAndDuplicateTierLabels(t *testing.T) {
	zero := 0.0
	tests := []struct {
		name      string
		intervals []pricingIntervalRequest
	}{
		{
			name:      "blank",
			intervals: []pricingIntervalRequest{{TierLabel: " \t ", VideoPricePerSecond: &zero}},
		},
		{
			name: "equivalent",
			intervals: []pricingIntervalRequest{
				{TierLabel: "720p", VideoPricePerSecond: &zero},
				{TierLabel: " 720P ", VideoPricePerSecond: float64Ptr(0.01)},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pricing := pricingRequestToService([]channelModelPricingRequest{{
				Models:              []string{"sora-2"},
				BillingMode:         "video",
				VideoDefaultSeconds: intPtr(10),
				Intervals:           tt.intervals,
			}}, pricingScopePrimary)[0]

			require.Error(t, service.ValidateIntervals(pricing.Intervals, pricing.BillingMode))
		})
	}
}
