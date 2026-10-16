package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration187AddsPrimaryModelPricingDescriptionOnly(t *testing.T) {
	content, err := FS.ReadFile("187_add_channel_model_pricing_description.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Equal(t,
		"ALTER TABLE channel_model_pricing ADD COLUMN IF NOT EXISTS description VARCHAR(500) NOT NULL DEFAULT '';",
		sql,
	)
	require.NotContains(t, sql, "channel_account_stats_model_pricing")
}
