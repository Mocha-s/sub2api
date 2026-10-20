//go:build unit

package repository

import (
	"database/sql/driver"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSafeDateFormat(t *testing.T) {
	tests := []struct {
		name        string
		granularity string
		expected    string
	}{
		// 合法值
		{"hour", "hour", "YYYY-MM-DD HH24:00"},
		{"day", "day", "YYYY-MM-DD"},
		{"week", "week", "IYYY-IW"},
		{"month", "month", "YYYY-MM"},

		// 非法值回退到默认
		{"空字符串", "", "YYYY-MM-DD"},
		{"未知粒度 year", "year", "YYYY-MM-DD"},
		{"未知粒度 minute", "minute", "YYYY-MM-DD"},

		// 恶意字符串
		{"SQL 注入尝试", "'; DROP TABLE users; --", "YYYY-MM-DD"},
		{"带引号", "day'", "YYYY-MM-DD"},
		{"带括号", "day)", "YYYY-MM-DD"},
		{"Unicode", "日", "YYYY-MM-DD"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := safeDateFormat(tc.granularity)
			require.Equal(t, tc.expected, got, "safeDateFormat(%q)", tc.granularity)
		})
	}
}

func TestUsageLogRefundFields_InsertAndSelectContract(t *testing.T) {
	prepared := prepareUsageLogInsert(&service.UsageLog{
		UserID: 1, APIKeyID: 2, AccountID: 3, Model: "video-model",
		TotalCost: 12, ActualCost: 10,
	})

	require.Len(t, prepared.args, len(usageLogInsertArgTypes))
	require.Equal(t, 12.0, prepared.args[25])
	require.Equal(t, 10.0, prepared.args[26])
	for _, column := range []string{"refunded_cost", "refunded_total_cost", "refunded_account_cost", "refund_reason", "refunded_at"} {
		require.Contains(t, usageLogSelectColumns, column)
	}
}

func TestUsageLogRefundFields_RowRoundTripPreservesGrossAndRefund(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	reason := "provider failed"
	refundedAt := time.Date(2026, 7, 12, 10, 30, 0, 0, time.UTC)
	createdAt := refundedAt.Add(-time.Hour)
	columns := strings.Split(usageLogSelectColumns, ", ")
	values := make([]driver.Value, len(columns))
	for i, column := range columns {
		switch column {
		case "id", "user_id", "api_key_id", "account_id":
			values[i] = int64(0)
		case "model":
			values[i] = ""
		case "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens", "cache_creation_5m_tokens", "cache_creation_1h_tokens", "image_output_tokens", "image_input_tokens", "image_count", "video_count":
			values[i] = 0
		case "image_output_cost", "image_input_cost", "input_cost", "output_cost", "cache_creation_cost", "cache_read_cost", "total_cost", "actual_cost", "refunded_cost", "refunded_total_cost", "refunded_account_cost", "rate_multiplier":
			values[i] = float64(0)
		case "billing_type", "request_type":
			values[i] = int16(0)
		case "stream", "openai_ws_mode", "cache_ttl_overridden", "long_context_billing_applied", "native_compaction_v2":
			values[i] = false
		case "created_at":
			values[i] = createdAt
		}
	}
	set := func(column string, value driver.Value) {
		for i, name := range columns {
			if name == column {
				values[i] = value
				return
			}
		}
		t.Fatalf("unknown usage log column %q", column)
	}
	set("id", int64(9))
	set("user_id", int64(1))
	set("api_key_id", int64(2))
	set("account_id", int64(3))
	set("request_id", "req")
	set("model", "video-model")
	set("requested_model", "video-model")
	set("total_cost", 12.0)
	set("actual_cost", 10.0)
	set("refunded_cost", 4.0)
	set("refunded_total_cost", 5.0)
	set("refunded_account_cost", 3.0)
	set("refund_reason", reason)
	set("refunded_at", refundedAt)
	set("rate_multiplier", 1.0)
	set("billing_type", int16(0))
	set("request_type", int16(1))
	set("video_count", 1)
	set("video_resolution", "720p")
	set("video_duration_seconds", int64(5))
	set("billing_mode", "video")
	set("created_at", createdAt)
	require.Len(t, values, len(columns))
	mock.ExpectQuery("SELECT .* FROM usage_logs WHERE id = \\$1").WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(values...))

	log, err := newUsageLogRepositoryWithSQL(nil, db).GetByID(t.Context(), 9)
	require.NoError(t, err)
	require.Equal(t, 10.0, log.ActualCost)
	require.Equal(t, 12.0, log.TotalCost)
	require.Equal(t, 4.0, log.RefundedCost)
	require.Equal(t, 5.0, log.RefundedTotalCost)
	require.Equal(t, 3.0, log.RefundedAccountCost)
	require.Equal(t, reason, *log.RefundReason)
	require.Equal(t, refundedAt, *log.RefundedAt)
}

func TestUsageLogNetCostExpressions_AreAliasAwareAndNullSafe(t *testing.T) {
	require.Equal(t, "(actual_cost - COALESCE(refunded_cost, 0))", usageLogNetActualCostExpr(""))
	require.Equal(t, "(ul.actual_cost - COALESCE(ul.refunded_cost, 0))", usageLogNetActualCostExpr("ul"))
	require.Equal(t, "(ul.total_cost - COALESCE(ul.refunded_total_cost, 0))", usageLogNetTotalCostExpr("ul"))
	require.Equal(t, "(COALESCE(ul.account_stats_cost, ul.total_cost * COALESCE(ul.account_rate_multiplier, 1)) - COALESCE(ul.refunded_account_cost, 0))", usageLogNetAccountCostExpr("ul"))
	require.Equal(t, "COALESCE(ul.account_stats_cost, ul.total_cost * COALESCE(ul.account_rate_multiplier, 1))", usageLogGrossAccountCostExpr("ul"))
	require.Equal(t, "(d.account_cost - COALESCE(d.refunded_account_cost, 0))", usageLogNetPersistedAccountCostExpr("d"))
	require.Equal(t, usageLogNetActualCostExpr("ul")+" > 0", usageLogSuccessFilterUL)
	require.Equal(t, usageLogNetActualCostExpr("u")+" > 0", usageLogSuccessFilter("u"))
}

func TestUsageLogAggregations_DoNotDuplicateNetCostSQL(t *testing.T) {
	for _, name := range []string{
		"usage_log_repo_stats.go",
		"usage_log_repo_trend.go",
		"usage_log_repo_dashboard.go",
	} {
		source, err := os.ReadFile(name)
		require.NoError(t, err)
		text := string(source)
		for _, duplicated := range []string{
			"actual_cost - COALESCE(",
			"total_cost - COALESCE(",
			"account_stats_cost, total_cost * COALESCE(",
			"account_cost - COALESCE(",
		} {
			require.NotContains(t, text, duplicated, "%s must use centralized usage cost expressions", name)
		}
	}
}

func TestBuildUsageLogBatchInsertQuery_UsesConflictDoNothing(t *testing.T) {
	log := &service.UsageLog{
		UserID:       1,
		APIKeyID:     2,
		AccountID:    3,
		RequestID:    "req-batch-no-update",
		Model:        "gpt-5",
		InputTokens:  10,
		OutputTokens: 5,
		TotalCost:    1.2,
		ActualCost:   1.2,
		CreatedAt:    time.Now().UTC(),
	}
	prepared := prepareUsageLogInsert(log)

	query, _ := buildUsageLogBatchInsertQuery([]string{usageLogBatchKey(log.RequestID, log.APIKeyID)}, map[string]usageLogInsertPrepared{
		usageLogBatchKey(log.RequestID, log.APIKeyID): prepared,
	})

	require.Contains(t, query, "ON CONFLICT (request_id, api_key_id) DO NOTHING")
	require.NotContains(t, strings.ToUpper(query), "DO UPDATE")
}
