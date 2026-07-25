//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestChannelRepository_ModelPricingDescriptionRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := NewChannelRepository(integrationDB)
	channel := service.Channel{
		Name:   fmt.Sprintf("pricing-description-%d", time.Now().UnixNano()),
		Status: service.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, &channel))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM channels WHERE id = $1`, channel.ID)
	})

	inputPrice := 0.001
	pricing := service.ChannelModelPricing{
		ChannelID:      channel.ID,
		Platform:       service.PlatformOpenAI,
		Models:         []string{"sora-2", "sora-2-fast"},
		Description:    "First line\nSecond line",
		BillingMode:    service.BillingModeToken,
		InputPrice:     &inputPrice,
		OutputPrice:    &inputPrice,
		CacheReadPrice: &inputPrice,
	}
	require.NoError(t, repo.CreateModelPricing(ctx, &pricing))

	listed, err := repo.ListModelPricing(ctx, channel.ID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, []string{"sora-2", "sora-2-fast"}, listed[0].Models)
	require.Equal(t, "First line\nSecond line", listed[0].Description)

	pricing.Description = "Updated description"
	require.NoError(t, repo.UpdateModelPricing(ctx, &pricing))

	listed, err = repo.ListModelPricing(ctx, channel.ID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, "Updated description", listed[0].Description)

	replacement := []service.ChannelModelPricing{{
		Platform:    service.PlatformOpenAI,
		Models:      []string{"sora-2", "sora-2-fast"},
		Description: "Replacement line\nShared by aliases",
		BillingMode: service.BillingModeToken,
		InputPrice:  &inputPrice,
		OutputPrice: &inputPrice,
	}}
	require.NoError(t, repo.ReplaceModelPricing(ctx, channel.ID, replacement))

	listed, err = repo.ListModelPricing(ctx, channel.ID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, []string{"sora-2", "sora-2-fast"}, listed[0].Models)
	require.Equal(t, "Replacement line\nShared by aliases", listed[0].Description)
}
