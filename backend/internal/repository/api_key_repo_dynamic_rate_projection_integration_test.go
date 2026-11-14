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

func TestGetByKeyForAuthCarriesCompositeDynamicBilling(t *testing.T) {
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	g := mustCreateGroup(t, integrationEntClient, &service.Group{Name: fmt.Sprintf("dynamic-rate-%d", suffix), Platform: service.PlatformComposite, RateMultiplier: 1})
	u := mustCreateUser(t, integrationEntClient, &service.User{Email: fmt.Sprintf("dynamic-rate-%d@example.com", suffix), Concurrency: 1})
	repo := NewAPIKeyRepository(integrationEntClient, integrationDB)
	key := &service.APIKey{UserID: u.ID, GroupID: &g.ID, Key: fmt.Sprintf("sk-dynamic-%d", suffix), Name: "dynamic", Status: service.StatusActive}
	require.NoError(t, repo.Create(ctx, key))
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(ctx, `DELETE FROM auth_cache_invalidation_outbox WHERE cache_key=encode(sha256(convert_to($1,'UTF8')),'hex')`, key.Key)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, `DELETE FROM api_keys WHERE id=$1`, key.ID)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, u.ID)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, `DELETE FROM groups WHERE id=$1`, g.ID)
		require.NoError(t, err)
	})
	for _, enabled := range []bool{true, false} {
		_, err := integrationDB.ExecContext(ctx, `UPDATE groups SET dynamic_rate_billing_enabled=$1 WHERE id=$2`, enabled, g.ID)
		require.NoError(t, err)
		got, err := repo.GetByKeyForAuth(ctx, key.Key)
		require.NoError(t, err)
		require.NotNil(t, got.Group)
		require.Equal(t, enabled, got.Group.DynamicRateBillingEnabled)
	}
}
