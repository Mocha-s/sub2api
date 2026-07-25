//go:build integration

package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestMigrationsRunner_ConcurrentInstancesSerializeOnSessionLock(t *testing.T) {
	const instances = 2
	errorsByInstance := make([]error, instances)
	var wg sync.WaitGroup
	for i := 0; i < instances; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			errorsByInstance[index] = ApplyMigrations(ctx, integrationDB)
		}(i)
	}
	wg.Wait()
	for i, err := range errorsByInstance {
		require.NoErrorf(t, err, "migration instance %d", i)
	}
}

func TestMigrationsRunner_IsIdempotent_AndSchemaIsUpToDate(t *testing.T) {
	tx := testTx(t)

	// Re-apply migrations to verify idempotency (no errors, no duplicate rows).
	require.NoError(t, ApplyMigrations(context.Background(), integrationDB))

	// schema_migrations should have at least the current migration set.
	var applied int
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM schema_migrations").Scan(&applied))
	require.GreaterOrEqual(t, applied, 7, "expected schema_migrations to contain applied migrations")

	// users: columns required by repository queries
	requireColumn(t, tx, "users", "username", "character varying", 100, false)
	requireColumn(t, tx, "users", "notes", "text", 0, false)

	// accounts: schedulable and rate-limit fields
	requireColumn(t, tx, "accounts", "notes", "text", 0, true)
	requireColumn(t, tx, "accounts", "schedulable", "boolean", 0, false)
	requireColumn(t, tx, "accounts", "rate_limited_at", "timestamp with time zone", 0, true)
	requireColumn(t, tx, "accounts", "rate_limit_reset_at", "timestamp with time zone", 0, true)
	requireColumn(t, tx, "accounts", "overload_until", "timestamp with time zone", 0, true)
	requireColumn(t, tx, "accounts", "session_window_status", "character varying", 20, true)
	requireIndex(t, tx, "accounts", "idx_accounts_autopause_expiry_due")

	// api_keys: key length should be 128
	requireColumn(t, tx, "api_keys", "key", "character varying", 128, false)

	// redeem_codes: subscription fields
	requireColumn(t, tx, "redeem_codes", "group_id", "bigint", 0, true)
	requireColumn(t, tx, "redeem_codes", "validity_days", "integer", 0, false)

	// usage_logs: billing_type used by filters/stats
	requireColumn(t, tx, "usage_logs", "billing_type", "smallint", 0, false)
	requireColumn(t, tx, "usage_logs", "request_type", "smallint", 0, false)
	requireColumn(t, tx, "usage_logs", "openai_ws_mode", "boolean", 0, false)
	requireColumn(t, tx, "usage_logs", "image_input_size", "character varying", 32, true)
	requireColumn(t, tx, "usage_logs", "image_output_size", "character varying", 32, true)
	requireColumn(t, tx, "usage_logs", "image_size_source", "character varying", 16, true)
	requireColumn(t, tx, "usage_logs", "image_size_breakdown", "jsonb", 0, true)
	requireColumn(t, tx, "usage_logs", "video_count", "integer", 0, false)
	requireColumn(t, tx, "usage_logs", "video_resolution", "character varying", 10, true)
	requireColumn(t, tx, "usage_logs", "video_duration_seconds", "integer", 0, true)
	requireNumericColumn(t, tx, "usage_logs", "refunded_cost", false)
	requireNumericColumn(t, tx, "usage_logs", "refunded_total_cost", false)
	requireNumericColumn(t, tx, "usage_logs", "refunded_account_cost", false)
	requireColumn(t, tx, "usage_logs", "refund_reason", "text", 0, true)
	requireColumn(t, tx, "usage_logs", "refunded_at", "timestamp with time zone", 0, true)
	requireColumnDefaultContains(t, tx, "usage_logs", "refunded_cost", "0")
	requireColumnDefaultContains(t, tx, "usage_logs", "refunded_total_cost", "0")
	requireColumnDefaultContains(t, tx, "usage_logs", "refunded_account_cost", "0")
	requireConstraintDefinitionContains(
		t,
		tx,
		"usage_logs",
		"usage_logs_image_size_source_check",
		"image_size_source",
		"'output'",
		"'input'",
		"'default'",
		"'legacy'",
	)
	requireConstraintDefinitionContains(
		t,
		tx,
		"usage_logs",
		"usage_logs_image_billing_size_check",
		"image_count",
		"billing_mode",
		"'video'",
		"video_count",
		"image_size IS NOT NULL",
		"'1K'",
		"'2K'",
		"'4K'",
		"'mixed'",
	)

	// Channel video pricing is mirrored for customer billing and account statistics.
	requireColumn(t, tx, "channel_model_pricing", "description", "character varying", 500, false)
	requireColumnDefaultContains(t, tx, "channel_model_pricing", "description", "''")
	requireNumericColumn(t, tx, "channel_model_pricing", "video_price_per_second", true)
	requireColumn(t, tx, "channel_model_pricing", "video_default_seconds", "integer", 0, true)
	requireColumn(t, tx, "channel_model_pricing", "video_allowed_seconds", "jsonb", 0, true)
	requireNumericColumn(t, tx, "channel_pricing_intervals", "video_price_per_second", true)
	requireColumnCount(t, tx, "channel_account_stats_model_pricing", "description", 0)
	requireNumericColumn(t, tx, "channel_account_stats_model_pricing", "video_price_per_second", true)
	requireColumn(t, tx, "channel_account_stats_model_pricing", "video_default_seconds", "integer", 0, true)
	requireColumn(t, tx, "channel_account_stats_model_pricing", "video_allowed_seconds", "jsonb", 0, true)
	requireNumericColumn(t, tx, "channel_account_stats_pricing_intervals", "video_price_per_second", true)
	requireStoredMigrationChecksum(t, tx, "187_add_channel_model_pricing_description.sql")

	// Dashboard costs remain gross; refund totals are tracked separately.
	requireNumericColumn(t, tx, "usage_dashboard_hourly", "refunded_total_cost", false)
	requireNumericColumn(t, tx, "usage_dashboard_hourly", "refunded_cost", false)
	requireNumericColumn(t, tx, "usage_dashboard_hourly", "refunded_account_cost", false)
	requireNumericColumn(t, tx, "usage_dashboard_daily", "refunded_total_cost", false)
	requireNumericColumn(t, tx, "usage_dashboard_daily", "refunded_cost", false)
	requireNumericColumn(t, tx, "usage_dashboard_daily", "refunded_account_cost", false)
	requireColumnDefaultContains(t, tx, "usage_dashboard_hourly", "refunded_total_cost", "0")
	requireColumnDefaultContains(t, tx, "usage_dashboard_hourly", "refunded_cost", "0")
	requireColumnDefaultContains(t, tx, "usage_dashboard_hourly", "refunded_account_cost", "0")
	requireColumnDefaultContains(t, tx, "usage_dashboard_daily", "refunded_total_cost", "0")
	requireColumnDefaultContains(t, tx, "usage_dashboard_daily", "refunded_cost", "0")
	requireColumnDefaultContains(t, tx, "usage_dashboard_daily", "refunded_account_cost", "0")
	requireColumn(t, tx, "user_platform_quotas", "revision", "bigint", 0, false)
	requireColumnDefaultContains(t, tx, "user_platform_quotas", "revision", "0")
	requireConstraintDefinitionContains(t, tx, "usage_logs", "usage_logs_refunds_nonnegative_check", "refunded_cost", ">=", "refunded_total_cost", "refunded_account_cost")
	requireConstraintDefinitionContains(t, tx, "usage_logs", "usage_logs_refunds_not_over_gross_check", "refunded_cost", "actual_cost", "refunded_total_cost", "total_cost", "refunded_account_cost", "account_stats_cost")
	requireConstraintDefinitionContains(t, tx, "usage_logs", "usage_logs_refund_metadata_check", "refunded_at", "refund_reason")

	// Durable video settlement ledger and idempotent event audit trail.
	var videoTaskSettlementsRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.video_task_settlements')").Scan(&videoTaskSettlementsRegclass))
	require.True(t, videoTaskSettlementsRegclass.Valid, "expected video_task_settlements table to exist")
	var cacheInvalidationJobsRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.video_task_cache_invalidation_jobs')").Scan(&cacheInvalidationJobsRegclass))
	require.True(t, cacheInvalidationJobsRegclass.Valid, "expected video_task_cache_invalidation_jobs table to exist")
	requireColumn(t, tx, "video_task_settlements", "video_task_id", "bigint", 0, false)
	requireColumn(t, tx, "video_task_settlements", "user_id", "bigint", 0, false)
	requireColumn(t, tx, "video_task_settlements", "api_key_id", "bigint", 0, false)
	requireColumn(t, tx, "video_task_settlements", "group_id", "bigint", 0, false)
	requireColumn(t, tx, "video_task_settlements", "account_id", "bigint", 0, false)
	requireColumn(t, tx, "video_task_settlements", "platform", "character varying", 50, false)
	requireColumn(t, tx, "video_task_settlements", "channel_id", "bigint", 0, true)
	requireColumn(t, tx, "video_task_settlements", "subscription_id", "bigint", 0, true)
	requireColumn(t, tx, "video_task_settlements", "usage_log_id", "bigint", 0, true)
	requireColumn(t, tx, "video_task_settlements", "charge_request_id", "character varying", 160, false)
	requireColumn(t, tx, "video_task_settlements", "state", "character varying", 24, false)
	requireColumn(t, tx, "video_task_settlements", "billing_type", "smallint", 0, false)
	requireNumericColumn(t, tx, "video_task_settlements", "gross_cost_usd", false)
	requireNumericColumn(t, tx, "video_task_settlements", "actual_cost_usd", false)
	requireNumericColumn(t, tx, "video_task_settlements", "account_cost_usd", false)
	requireNumericColumn(t, tx, "video_task_settlements", "refunded_cost_usd", false)
	requireColumn(t, tx, "video_task_settlements", "pricing_snapshot", "jsonb", 0, false)
	requireColumn(t, tx, "video_task_settlements", "effect_snapshot", "jsonb", 0, false)
	requireColumn(t, tx, "video_task_settlements", "applied_snapshot", "jsonb", 0, true)
	requireColumn(t, tx, "video_task_settlements", "last_error", "text", 0, true)
	requireColumn(t, tx, "video_task_settlements", "next_reconcile_at", "timestamp with time zone", 0, true)
	requireColumn(t, tx, "video_task_settlements", "locked_by", "character varying", 128, true)
	requireColumn(t, tx, "video_task_settlements", "locked_until", "timestamp with time zone", 0, true)
	requireColumn(t, tx, "video_task_settlements", "reserved_at", "timestamp with time zone", 0, true)
	requireColumn(t, tx, "video_task_settlements", "charged_at", "timestamp with time zone", 0, true)
	requireColumn(t, tx, "video_task_settlements", "released_at", "timestamp with time zone", 0, true)
	requireColumn(t, tx, "video_task_settlements", "refunded_at", "timestamp with time zone", 0, true)
	requireColumn(t, tx, "video_task_settlements", "created_at", "timestamp with time zone", 0, false)
	requireColumn(t, tx, "video_task_settlements", "updated_at", "timestamp with time zone", 0, false)
	requireForeignKeyOnDelete(t, tx, "video_task_settlements", "video_task_id", "video_tasks", "CASCADE")
	requireForeignKeyOnDelete(t, tx, "video_task_settlements", "user_id", "users", "RESTRICT")
	requireForeignKeyOnDelete(t, tx, "video_task_settlements", "api_key_id", "api_keys", "RESTRICT")
	requireForeignKeyOnDelete(t, tx, "video_task_settlements", "group_id", "groups", "RESTRICT")
	requireForeignKeyOnDelete(t, tx, "video_task_settlements", "account_id", "accounts", "RESTRICT")
	requireForeignKeyOnDelete(t, tx, "video_task_settlements", "channel_id", "channels", "SET NULL")
	requireForeignKeyOnDelete(t, tx, "video_task_settlements", "subscription_id", "user_subscriptions", "SET NULL")
	requireForeignKeyOnDelete(t, tx, "video_task_settlements", "usage_log_id", "usage_logs", "SET NULL")
	requireConstraintDefinitionContains(t, tx, "video_task_settlements", "video_task_settlements_task_unique", "UNIQUE", "video_task_id")
	requireConstraintDefinitionContains(t, tx, "video_task_settlements", "video_task_settlements_charge_key_unique", "UNIQUE", "charge_request_id")
	requireConstraintDefinitionContains(t, tx, "video_task_settlements", "video_task_settlements_state_check", "reserved", "charged", "released", "refunded")
	requireConstraintDefinitionContains(t, tx, "video_task_settlements", "video_task_settlements_billing_type_check", "billing_type", "0", "1")
	requireConstraintDefinitionContains(t, tx, "video_task_settlements", "video_task_settlements_amounts_nonnegative_check", "gross_cost_usd", ">=", "actual_cost_usd", "account_cost_usd", "refunded_cost_usd")
	requireIndexDefinitionContains(t, tx, "video_task_settlements", "idx_video_task_settlements_reconcile", "state", "next_reconcile_at", "WHERE")

	var refundReportingRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.video_task_refund_reporting_jobs')").Scan(&refundReportingRegclass))
	require.True(t, refundReportingRegclass.Valid, "expected video_task_refund_reporting_jobs table to exist")
	requireColumn(t, tx, "video_task_refund_reporting_jobs", "settlement_id", "bigint", 0, false)
	requireColumn(t, tx, "video_task_refund_reporting_jobs", "usage_log_id", "bigint", 0, false)
	requireColumn(t, tx, "video_task_refund_reporting_jobs", "usage_created_at", "timestamp with time zone", 0, false)
	requireColumn(t, tx, "video_task_refund_reporting_jobs", "attempts", "integer", 0, false)
	requireColumn(t, tx, "video_task_refund_reporting_jobs", "next_attempt_at", "timestamp with time zone", 0, true)
	requireColumn(t, tx, "video_task_refund_reporting_jobs", "locked_by", "character varying", 128, true)
	requireColumn(t, tx, "video_task_refund_reporting_jobs", "locked_until", "timestamp with time zone", 0, true)
	requireColumn(t, tx, "video_task_refund_reporting_jobs", "last_error", "text", 0, true)
	requireColumn(t, tx, "video_task_refund_reporting_jobs", "completed_at", "timestamp with time zone", 0, true)
	requireConstraintDefinitionContains(t, tx, "video_task_refund_reporting_jobs", "video_task_refund_reporting_jobs_settlement_unique", "UNIQUE", "settlement_id")
	requireConstraintDefinitionContains(t, tx, "video_task_refund_reporting_jobs", "video_task_refund_reporting_jobs_usage_unique", "UNIQUE", "usage_log_id")
	requireForeignKeyOnDelete(t, tx, "video_task_refund_reporting_jobs", "settlement_id", "video_task_settlements", "RESTRICT")
	requireForeignKeyOnDelete(t, tx, "video_task_refund_reporting_jobs", "usage_log_id", "usage_logs", "RESTRICT")
	requireIndexDefinitionContains(t, tx, "video_task_refund_reporting_jobs", "idx_video_task_refund_reporting_jobs_due", "next_attempt_at", "locked_until", "WHERE")

	var videoTaskSettlementEventsRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.video_task_settlement_events')").Scan(&videoTaskSettlementEventsRegclass))
	require.True(t, videoTaskSettlementEventsRegclass.Valid, "expected video_task_settlement_events table to exist")
	requireColumn(t, tx, "video_task_settlement_events", "settlement_id", "bigint", 0, false)
	requireColumn(t, tx, "video_task_settlement_events", "event_id", "character varying", 200, false)
	requireColumn(t, tx, "video_task_settlement_events", "event_type", "character varying", 24, false)
	requireNumericColumn(t, tx, "video_task_settlement_events", "amount_usd", false)
	requireColumn(t, tx, "video_task_settlement_events", "metadata", "jsonb", 0, false)
	requireColumn(t, tx, "video_task_settlement_events", "created_at", "timestamp with time zone", 0, false)
	requireForeignKeyOnDelete(t, tx, "video_task_settlement_events", "settlement_id", "video_task_settlements", "CASCADE")
	requireConstraintDefinitionContains(t, tx, "video_task_settlement_events", "video_task_settlement_events_unique", "UNIQUE", "settlement_id", "event_type")
	requireConstraintDefinitionContains(t, tx, "video_task_settlement_events", "video_task_settlement_events_event_id_unique", "UNIQUE", "event_id")
	requireConstraintDefinitionContains(t, tx, "video_task_settlement_events", "video_task_settlement_events_type_check", "reserve", "capture", "release", "refund", "legacy_refund")
	requireConstraintDefinitionContains(t, tx, "video_task_settlement_events", "video_task_settlement_events_amount_nonnegative_check", "amount_usd", ">=")

	// usage_billing_dedup: billing idempotency narrow table
	var usageBillingDedupRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.usage_billing_dedup')").Scan(&usageBillingDedupRegclass))
	require.True(t, usageBillingDedupRegclass.Valid, "expected usage_billing_dedup table to exist")
	requireColumn(t, tx, "usage_billing_dedup", "request_fingerprint", "character varying", 64, false)
	requireIndex(t, tx, "usage_billing_dedup", "idx_usage_billing_dedup_request_api_key")
	requireIndex(t, tx, "usage_billing_dedup", "idx_usage_billing_dedup_created_at_brin")

	var usageBillingDedupArchiveRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.usage_billing_dedup_archive')").Scan(&usageBillingDedupArchiveRegclass))
	require.True(t, usageBillingDedupArchiveRegclass.Valid, "expected usage_billing_dedup_archive table to exist")
	requireColumn(t, tx, "usage_billing_dedup_archive", "request_fingerprint", "character varying", 64, false)
	requireIndex(t, tx, "usage_billing_dedup_archive", "usage_billing_dedup_archive_pkey")

	// settings table should exist
	var settingsRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.settings')").Scan(&settingsRegclass))
	require.True(t, settingsRegclass.Valid, "expected settings table to exist")

	// security_secrets table should exist
	var securitySecretsRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.security_secrets')").Scan(&securitySecretsRegclass))
	require.True(t, securitySecretsRegclass.Valid, "expected security_secrets table to exist")

	// scheduler_outbox pending dedup support
	requireColumn(t, tx, "scheduler_outbox", "dedup_key", "text", 0, true)
	requireIndex(t, tx, "scheduler_outbox", "idx_scheduler_outbox_pending_dedup_key")

	// ops_system_logs: API key id index for operational log triage
	requireColumn(t, tx, "ops_system_logs", "api_key_id", "bigint", 0, true)
	requireIndex(t, tx, "ops_system_logs", "idx_ops_system_logs_api_key_id_created_at")

	// Bounded ingress rejection security aggregates.
	requireColumn(t, tx, "ops_ingress_reject_aggregates", "bucket_start", "timestamp with time zone", 0, false)
	requireColumn(t, tx, "ops_ingress_reject_aggregates", "client_ip", "inet", 0, false)
	requireColumn(t, tx, "ops_ingress_reject_aggregates", "request_count", "bigint", 0, false)
	requireIndex(t, tx, "ops_ingress_reject_aggregates", "idx_ops_ingress_reject_aggregates_bucket")
	requireIndex(t, tx, "ops_ingress_reject_aggregates", "idx_ops_ingress_reject_aggregates_ip_bucket")

	// user_allowed_groups table should exist
	var uagRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.user_allowed_groups')").Scan(&uagRegclass))
	require.True(t, uagRegclass.Valid, "expected user_allowed_groups table to exist")

	// user_subscriptions: deleted_at for soft delete support (migration 012)
	requireColumn(t, tx, "user_subscriptions", "deleted_at", "timestamp with time zone", 0, true)

	// orphan_allowed_groups_audit table should exist (migration 013)
	var orphanAuditRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.orphan_allowed_groups_audit')").Scan(&orphanAuditRegclass))
	require.True(t, orphanAuditRegclass.Valid, "expected orphan_allowed_groups_audit table to exist")

	// account_groups: created_at should be timestamptz
	requireColumn(t, tx, "account_groups", "created_at", "timestamp with time zone", 0, false)

	// user_allowed_groups: created_at should be timestamptz
	requireColumn(t, tx, "user_allowed_groups", "created_at", "timestamp with time zone", 0, false)
}

func TestMigrationsRunner_AuthIdentityAndPaymentSchemaStayAligned(t *testing.T) {
	tx := testTx(t)

	requireColumn(t, tx, "auth_identity_migration_reports", "report_type", "character varying", 80, false)
	requireColumn(t, tx, "users", "signup_source", "character varying", 20, false)
	requireColumnDefaultContains(t, tx, "users", "signup_source", "email")
	requireConstraintDefinitionContains(
		t,
		tx,
		"users",
		"users_signup_source_check",
		"signup_source",
		"'email'",
		"'linuxdo'",
		"'wechat'",
		"'oidc'",
	)

	requireForeignKeyOnDelete(t, tx, "auth_identities", "user_id", "users", "CASCADE")
	requireForeignKeyOnDelete(t, tx, "auth_identity_channels", "identity_id", "auth_identities", "CASCADE")
	requireForeignKeyOnDelete(t, tx, "pending_auth_sessions", "target_user_id", "users", "SET NULL")
	requireForeignKeyOnDelete(t, tx, "identity_adoption_decisions", "pending_auth_session_id", "pending_auth_sessions", "CASCADE")
	requireForeignKeyOnDelete(t, tx, "identity_adoption_decisions", "identity_id", "auth_identities", "SET NULL")

	requireIndex(t, tx, "payment_orders", "paymentorder_out_trade_no")
	requirePartialUniqueIndexDefinition(t, tx, "payment_orders", "paymentorder_out_trade_no", "out_trade_no", "WHERE")
	requireIndexAbsent(t, tx, "payment_orders", "paymentorder_out_trade_no_unique")
}

func requireIndex(t *testing.T, tx *sql.Tx, table, index string) {
	t.Helper()

	var exists bool
	err := tx.QueryRowContext(context.Background(), `
SELECT EXISTS (
	SELECT 1
	FROM pg_indexes
	WHERE schemaname = 'public'
	  AND tablename = $1
	  AND indexname = $2
)
`, table, index).Scan(&exists)
	require.NoError(t, err, "query pg_indexes for %s.%s", table, index)
	require.True(t, exists, "expected index %s on %s", index, table)
}

func requireIndexAbsent(t *testing.T, tx *sql.Tx, table, index string) {
	t.Helper()

	var exists bool
	err := tx.QueryRowContext(context.Background(), `
SELECT EXISTS (
	SELECT 1
	FROM pg_indexes
	WHERE schemaname = 'public'
	  AND tablename = $1
	  AND indexname = $2
)
`, table, index).Scan(&exists)
	require.NoError(t, err, "query pg_indexes for %s.%s", table, index)
	require.False(t, exists, "expected index %s on %s to be absent", index, table)
}

func requireIndexDefinitionContains(t *testing.T, tx *sql.Tx, table, index string, fragments ...string) {
	t.Helper()

	var def string
	err := tx.QueryRowContext(context.Background(), `
SELECT pg_get_indexdef(i.indexrelid)
FROM pg_class idx
JOIN pg_index i ON i.indexrelid = idx.oid
JOIN pg_class tbl ON tbl.oid = i.indrelid
JOIN pg_namespace ns ON ns.oid = tbl.relnamespace
WHERE ns.nspname = 'public'
  AND tbl.relname = $1
  AND idx.relname = $2
`, table, index).Scan(&def)
	require.NoError(t, err, "query index definition for %s.%s", table, index)

	for _, fragment := range fragments {
		require.Contains(t, def, fragment, "expected index definition for %s.%s to contain %q", table, index, fragment)
	}
}

func requirePartialUniqueIndexDefinition(t *testing.T, tx *sql.Tx, table, index string, fragments ...string) {
	t.Helper()

	var (
		unique bool
		def    string
	)

	err := tx.QueryRowContext(context.Background(), `
SELECT
	i.indisunique,
	pg_get_indexdef(i.indexrelid)
FROM pg_class idx
JOIN pg_index i ON i.indexrelid = idx.oid
JOIN pg_class tbl ON tbl.oid = i.indrelid
JOIN pg_namespace ns ON ns.oid = tbl.relnamespace
WHERE ns.nspname = 'public'
  AND tbl.relname = $1
  AND idx.relname = $2
`, table, index).Scan(&unique, &def)
	require.NoError(t, err, "query index definition for %s.%s", table, index)
	require.True(t, unique, "expected index %s on %s to be unique", index, table)

	for _, fragment := range fragments {
		require.Contains(t, def, fragment, "expected index definition for %s.%s to contain %q", table, index, fragment)
	}
}

func requireForeignKeyOnDelete(t *testing.T, tx *sql.Tx, table, column, refTable, expected string) {
	t.Helper()

	var actual string
	err := tx.QueryRowContext(context.Background(), `
SELECT CASE c.confdeltype
	WHEN 'a' THEN 'NO ACTION'
	WHEN 'r' THEN 'RESTRICT'
	WHEN 'c' THEN 'CASCADE'
	WHEN 'n' THEN 'SET NULL'
	WHEN 'd' THEN 'SET DEFAULT'
END
FROM pg_constraint c
JOIN pg_class tbl ON tbl.oid = c.conrelid
JOIN pg_namespace ns ON ns.oid = tbl.relnamespace
JOIN pg_class ref_tbl ON ref_tbl.oid = c.confrelid
JOIN pg_attribute attr ON attr.attrelid = tbl.oid AND attr.attnum = ANY(c.conkey)
WHERE ns.nspname = 'public'
  AND c.contype = 'f'
  AND tbl.relname = $1
  AND attr.attname = $2
  AND ref_tbl.relname = $3
LIMIT 1
`, table, column, refTable).Scan(&actual)
	require.NoError(t, err, "query foreign key action for %s.%s -> %s", table, column, refTable)
	require.Equal(t, expected, actual, "unexpected ON DELETE action for %s.%s -> %s", table, column, refTable)
}

func requireConstraintDefinitionContains(t *testing.T, tx *sql.Tx, table, constraint string, fragments ...string) {
	t.Helper()

	var def string
	err := tx.QueryRowContext(context.Background(), `
SELECT pg_get_constraintdef(c.oid)
FROM pg_constraint c
JOIN pg_class tbl ON tbl.oid = c.conrelid
JOIN pg_namespace ns ON ns.oid = tbl.relnamespace
WHERE ns.nspname = 'public'
  AND tbl.relname = $1
  AND c.conname = $2
`, table, constraint).Scan(&def)
	require.NoError(t, err, "query constraint definition for %s.%s", table, constraint)

	for _, fragment := range fragments {
		require.Contains(t, def, fragment, "expected constraint definition for %s.%s to contain %q", table, constraint, fragment)
	}
}

func requireColumnDefaultContains(t *testing.T, tx *sql.Tx, table, column string, fragments ...string) {
	t.Helper()

	var columnDefault sql.NullString
	err := tx.QueryRowContext(context.Background(), `
SELECT column_default
FROM information_schema.columns
WHERE table_schema = 'public'
  AND table_name = $1
  AND column_name = $2
`, table, column).Scan(&columnDefault)
	require.NoError(t, err, "query column_default for %s.%s", table, column)
	require.True(t, columnDefault.Valid, "expected column_default for %s.%s", table, column)

	for _, fragment := range fragments {
		require.Contains(t, columnDefault.String, fragment, "expected default for %s.%s to contain %q", table, column, fragment)
	}
}

func requireColumnCount(t *testing.T, tx *sql.Tx, table, column string, expected int) {
	t.Helper()

	var count int
	err := tx.QueryRowContext(context.Background(), `
SELECT COUNT(*)
FROM information_schema.columns
WHERE table_schema = 'public'
  AND table_name = $1
  AND column_name = $2
`, table, column).Scan(&count)
	require.NoError(t, err, "query column count for %s.%s", table, column)
	require.Equal(t, expected, count, "column count mismatch for %s.%s", table, column)
}

func requireStoredMigrationChecksum(t *testing.T, tx *sql.Tx, filename string) {
	t.Helper()

	content, err := migrations.FS.ReadFile(filename)
	require.NoError(t, err, "read migration %s", filename)

	sum := sha256.Sum256([]byte(strings.TrimSpace(string(content))))
	expected := hex.EncodeToString(sum[:])

	var actual string
	err = tx.QueryRowContext(context.Background(), `
SELECT checksum
FROM schema_migrations
WHERE filename = $1
`, filename).Scan(&actual)
	require.NoError(t, err, "query schema_migrations checksum for %s", filename)
	require.Equal(t, expected, actual, "checksum mismatch for %s", filename)
}

func requireNumericColumn(t *testing.T, tx *sql.Tx, table, column string, nullable bool) {
	t.Helper()

	var row struct {
		DataType  string
		Precision sql.NullInt64
		Scale     sql.NullInt64
		Nullable  string
	}

	err := tx.QueryRowContext(context.Background(), `
SELECT
  data_type,
  numeric_precision,
  numeric_scale,
  is_nullable
FROM information_schema.columns
WHERE table_schema = 'public'
  AND table_name = $1
  AND column_name = $2
`, table, column).Scan(&row.DataType, &row.Precision, &row.Scale, &row.Nullable)
	require.NoError(t, err, "query numeric column metadata for %s.%s", table, column)
	require.Equal(t, "numeric", row.DataType, "data_type mismatch for %s.%s", table, column)
	require.Equal(t, sql.NullInt64{Int64: 20, Valid: true}, row.Precision, "numeric_precision mismatch for %s.%s", table, column)
	require.Equal(t, sql.NullInt64{Int64: 10, Valid: true}, row.Scale, "numeric_scale mismatch for %s.%s", table, column)

	if nullable {
		require.Equal(t, "YES", row.Nullable, "nullable mismatch for %s.%s", table, column)
	} else {
		require.Equal(t, "NO", row.Nullable, "nullable mismatch for %s.%s", table, column)
	}
}

func requireColumn(t *testing.T, tx *sql.Tx, table, column, dataType string, maxLen int, nullable bool) {
	t.Helper()

	var row struct {
		DataType string
		MaxLen   sql.NullInt64
		Nullable string
	}

	err := tx.QueryRowContext(context.Background(), `
SELECT
  data_type,
  character_maximum_length,
  is_nullable
FROM information_schema.columns
WHERE table_schema = 'public'
  AND table_name = $1
  AND column_name = $2
`, table, column).Scan(&row.DataType, &row.MaxLen, &row.Nullable)
	require.NoError(t, err, "query information_schema.columns for %s.%s", table, column)
	require.Equal(t, dataType, row.DataType, "data_type mismatch for %s.%s", table, column)

	if maxLen > 0 {
		require.True(t, row.MaxLen.Valid, "expected maxLen for %s.%s", table, column)
		require.Equal(t, int64(maxLen), row.MaxLen.Int64, "maxLen mismatch for %s.%s", table, column)
	}

	if nullable {
		require.Equal(t, "YES", row.Nullable, "nullable mismatch for %s.%s", table, column)
	} else {
		require.Equal(t, "NO", row.Nullable, "nullable mismatch for %s.%s", table, column)
	}
}
