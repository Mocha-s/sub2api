package schema

import (
	"os"
	"strings"
	"testing"

	"entgo.io/ent/entc/load"
	"github.com/stretchr/testify/require"
)

func TestCompositeDynamicSchemas(t *testing.T) {
	spec, err := (&load.Config{Path: "."}).Load()
	require.NoError(t, err)
	schemas := make(map[string]*load.Schema, len(spec.Schemas))
	for _, schema := range spec.Schemas {
		schemas[schema.Name] = schema
	}

	target := requireSchema(t, schemas, "CompositeGroupTarget")
	requireSchemaFields(t, target, "composite_group_id", "target_group_id", "priority", "enabled", "deleted_at")
	group := requireSchema(t, schemas, "Group")
	requireSchemaFields(t, group, "dynamic_rate_billing_enabled")
	billingFlag := requireSchemaField(t, group, "dynamic_rate_billing_enabled")
	require.True(t, billingFlag.Default)
	require.Equal(t, false, billingFlag.DefaultValue)
	route := requireSchema(t, schemas, "CompositeModelRoute")
	requireSchemaFields(t, route, "target_group_id")
	targetGroupID := requireSchemaField(t, route, "target_group_id")
	require.True(t, targetGroupID.Optional)
	require.True(t, targetGroupID.Nillable)

	migration, err := os.ReadFile("../../migrations/246_composite_dynamic_targets_and_billing.sql")
	require.NoError(t, err)
	migrationSQL := strings.ToLower(string(migration))
	require.Contains(t, migrationSQL, "dynamic_rate_billing_enabled boolean not null default false")
	require.Contains(t, migrationSQL, "create table if not exists composite_group_targets")
	require.Contains(t, migrationSQL, "idx_composite_group_targets_active_unique")
	require.Contains(t, migrationSQL, "add column if not exists target_group_id bigint null")
}
