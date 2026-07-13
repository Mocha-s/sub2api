//go:build unit

package handler

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestToUserPricingExposesVideoPricingWhitelist(t *testing.T) {
	price := 0.03
	defaultSeconds := 10
	tierPrice := 0.05
	dto := toUserPricing(&service.ChannelModelPricing{
		BillingMode:         service.BillingModeVideo,
		VideoPricePerSecond: &price,
		VideoDefaultSeconds: &defaultSeconds,
		VideoAllowedSeconds: []int{5, 10},
		Intervals: []service.PricingInterval{{
			TierLabel:           "hd",
			VideoPricePerSecond: &tierPrice,
		}},
	})

	raw, err := json.Marshal(dto)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, "video", decoded["billing_mode"])
	require.Equal(t, 0.03, decoded["video_price_per_second"])
	require.Equal(t, float64(10), decoded["video_default_seconds"])
	require.Equal(t, []any{float64(5), float64(10)}, decoded["video_allowed_seconds"])
	interval := decoded["intervals"].([]any)[0].(map[string]any)
	require.Equal(t, 0.05, interval["video_price_per_second"])
}
