//go:build unit

package service

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBillingModeVideoIsValid(t *testing.T) {
	require.True(t, BillingModeVideo.IsValid())
}

func TestChannelModelPricingCloneCopiesVideoAllowedSeconds(t *testing.T) {
	original := ChannelModelPricing{VideoAllowedSeconds: []int{5, 10}}
	cloned := original.Clone()

	cloned.VideoAllowedSeconds[0] = 30
	require.Equal(t, []int{5, 10}, original.VideoAllowedSeconds)
}

func TestValidateIntervalsVideoUsesLabelsInsteadOfTokenRanges(t *testing.T) {
	intervals := []PricingInterval{
		{TierLabel: "standard", VideoPricePerSecond: testPtrFloat64(0.01)},
		{TierLabel: "premium", VideoPricePerSecond: testPtrFloat64(0.02)},
	}

	require.NoError(t, ValidateIntervals(intervals, BillingModeVideo))
}

func TestValidateIntervalsVideoRequiresCanonicalUniquePricedLabels(t *testing.T) {
	zero := 0.0
	tests := []struct {
		name      string
		intervals []PricingInterval
		match     string
	}{
		{
			name:      "blank priced label",
			intervals: []PricingInterval{{TierLabel: " \t ", VideoPricePerSecond: &zero}},
			match:     "tier_label",
		},
		{
			name: "case and whitespace equivalent labels",
			intervals: []PricingInterval{
				{TierLabel: "720p", VideoPricePerSecond: &zero},
				{TierLabel: " 720P ", VideoPricePerSecond: testPtrFloat64(0.02)},
			},
			match: "unique",
		},
		{
			name: "quote alias equivalent labels",
			intervals: []PricingInterval{
				{TierLabel: "1280x720", VideoPricePerSecond: &zero},
				{TierLabel: "720P", VideoPricePerSecond: testPtrFloat64(0.02)},
			},
			match: "unique",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateIntervals(tt.intervals, BillingModeVideo)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.match)
		})
	}

	require.NoError(t, ValidateIntervals([]PricingInterval{
		{TierLabel: " Preview ", VideoPricePerSecond: &zero},
		{TierLabel: "cinema", VideoPricePerSecond: testPtrFloat64(0.02)},
	}, BillingModeVideo))
}

func TestValidatePricingBillingModeVideoValidConfigurations(t *testing.T) {
	tests := []struct {
		name    string
		pricing ChannelModelPricing
	}{
		{
			name: "default price",
			pricing: ChannelModelPricing{
				BillingMode:         BillingModeVideo,
				VideoDefaultSeconds: testPtrInt(10),
				VideoPricePerSecond: testPtrFloat64(0.02),
			},
		},
		{
			name: "interval price",
			pricing: ChannelModelPricing{
				BillingMode:         BillingModeVideo,
				VideoDefaultSeconds: testPtrInt(10),
				VideoAllowedSeconds: []int{5, 10},
				Intervals: []PricingInterval{
					{TierLabel: "fast", VideoPricePerSecond: testPtrFloat64(0.03)},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, validatePricingEntries([]ChannelModelPricing{tt.pricing}))
		})
	}
}

func TestValidatePricingBillingModeRejectsInvalidMode(t *testing.T) {
	err := validatePricingBillingMode([]ChannelModelPricing{{BillingMode: BillingMode("invalid")}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "billing_mode")
}

func TestValidatePricingBillingModeVideoRejectsInvalidConfiguration(t *testing.T) {
	valid := func() ChannelModelPricing {
		return ChannelModelPricing{
			BillingMode:         BillingModeVideo,
			VideoDefaultSeconds: testPtrInt(10),
			VideoPricePerSecond: testPtrFloat64(0.02),
			VideoAllowedSeconds: []int{5, 10},
		}
	}

	tests := []struct {
		name   string
		mutate func(*ChannelModelPricing)
		match  string
	}{
		{"missing default seconds", func(p *ChannelModelPricing) { p.VideoDefaultSeconds = nil }, "video_default_seconds"},
		{"zero default seconds", func(p *ChannelModelPricing) { p.VideoDefaultSeconds = testPtrInt(0) }, "video_default_seconds"},
		{"default seconds above bound", func(p *ChannelModelPricing) { p.VideoDefaultSeconds = testPtrInt(3601) }, "video_default_seconds"},
		{"missing all video prices", func(p *ChannelModelPricing) { p.VideoPricePerSecond = nil }, "video price"},
		{"duplicate allowed seconds", func(p *ChannelModelPricing) { p.VideoAllowedSeconds = []int{10, 10} }, "video_allowed_seconds"},
		{"zero allowed seconds", func(p *ChannelModelPricing) { p.VideoAllowedSeconds = []int{0, 10} }, "video_allowed_seconds"},
		{"allowed seconds above bound", func(p *ChannelModelPricing) { p.VideoAllowedSeconds = []int{10, 3601} }, "video_allowed_seconds"},
		{"default absent from allowed seconds", func(p *ChannelModelPricing) { p.VideoAllowedSeconds = []int{5, 15} }, "video_default_seconds"},
		{"negative default price", func(p *ChannelModelPricing) { p.VideoPricePerSecond = testPtrFloat64(-0.01) }, "video_price_per_second"},
		{"infinite default price", func(p *ChannelModelPricing) { p.VideoPricePerSecond = testPtrFloat64(math.Inf(1)) }, "video_price_per_second"},
		{"nan default price", func(p *ChannelModelPricing) { p.VideoPricePerSecond = testPtrFloat64(math.NaN()) }, "video_price_per_second"},
		{"negative interval price", func(p *ChannelModelPricing) {
			p.Intervals = []PricingInterval{{TierLabel: "fast", VideoPricePerSecond: testPtrFloat64(-0.01)}}
		}, "video_price_per_second"},
		{"infinite interval price", func(p *ChannelModelPricing) {
			p.Intervals = []PricingInterval{{TierLabel: "fast", VideoPricePerSecond: testPtrFloat64(math.Inf(1))}}
		}, "video_price_per_second"},
		{"interval without price", func(p *ChannelModelPricing) {
			p.Intervals = []PricingInterval{{TierLabel: "fast"}}
		}, "has no price fields set"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pricing := valid()
			tt.mutate(&pricing)
			err := validatePricingEntries([]ChannelModelPricing{pricing})
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.match)
		})
	}
}
