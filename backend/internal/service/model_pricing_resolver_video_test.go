//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveWithChannelOverridePreservesVideoPricing(t *testing.T) {
	r := newResolverWithChannel(t, []ChannelModelPricing{{
		Platform:            "anthropic",
		Models:              []string{"sora-2"},
		BillingMode:         BillingModeVideo,
		VideoPricePerSecond: testPtrFloat64(0.03),
		VideoDefaultSeconds: testPtrInt(10),
		VideoAllowedSeconds: []int{5, 10, 15},
		Intervals: []PricingInterval{
			{TierLabel: "hd", VideoPricePerSecond: testPtrFloat64(0.05)},
		},
	}})

	resolved := r.Resolve(context.Background(), PricingInput{Model: "sora-2", GroupID: groupIDPtr()})

	require.Equal(t, BillingModeVideo, resolved.Mode)
	require.Equal(t, PricingSourceChannel, resolved.Source)
	require.Equal(t, 0.03, resolved.VideoPricePerSecond)
	require.Equal(t, 10, resolved.VideoDefaultSeconds)
	require.Equal(t, []int{5, 10, 15}, resolved.VideoAllowedSeconds)
	require.Len(t, resolved.RequestTiers, 1)
	require.Equal(t, "hd", resolved.RequestTiers[0].TierLabel)
	require.Nil(t, resolved.BasePricing, "video pricing must not fall through to token pricing")
	require.Empty(t, resolved.Intervals, "video tiers must not become token intervals")
}

func TestGetRequestTierPriceNormalizesVideoResolutionAliases(t *testing.T) {
	r := newResolverWithChannel(t, []ChannelModelPricing{{
		Platform:        "anthropic",
		Models:          []string{"sd16-seedance-2.0"},
		BillingMode:     BillingModePerRequest,
		PerRequestPrice: testPtrFloat64(6.96),
		Intervals: []PricingInterval{
			{TierLabel: " 1280x720 ", PerRequestPrice: testPtrFloat64(0.48)},
		},
	}})

	resolved := r.Resolve(context.Background(), PricingInput{Model: "sd16-seedance-2.0", GroupID: groupIDPtr()})

	require.Equal(t, BillingModePerRequest, resolved.Mode)
	require.InDelta(t, 0.48, r.GetRequestTierPrice(resolved, "720p"), 1e-12)
	require.InDelta(t, 0.0, r.GetRequestTierPrice(resolved, "480p"), 1e-12, "未命中档位由调用方回退默认按次价")
}
