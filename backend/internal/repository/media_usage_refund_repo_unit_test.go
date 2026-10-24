//go:build unit

package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageBillingRepositoryRefundMediaUsage_PartialAndIdempotent(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repo := &usageBillingRepository{db: db}
	cmd := &service.MediaUsageRefundCommand{RequestID: "req-image-refund", APIKeyID: 12, Scope: "image", RequestedUnits: 2, DeliveredUnits: 1, Reason: "missing image"}

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id,request_id,api_key_id,user_id,account_id,group_id,subscription_id,platform,billing_type,total_cost,actual_cost,balance_cost,subscription_cost,api_key_quota_cost,api_key_rate_limit_cost,account_quota_cost,platform_quota_cost,account_stats_cost,window_snapshot FROM media_usage_refund_snapshots").
		WithArgs(cmd.RequestID, cmd.APIKeyID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "request_id", "api_key_id", "user_id", "account_id", "group_id", "subscription_id", "platform", "billing_type", "total_cost", "actual_cost", "balance_cost", "subscription_cost", "api_key_quota_cost", "api_key_rate_limit_cost", "account_quota_cost", "platform_quota_cost", "account_stats_cost", "window_snapshot"}).AddRow(4, cmd.RequestID, 12, 7, 8, 9, nil, "gemini", service.BillingTypeBalance, 2.0, 1.0, 1.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, []byte(`{}`)))
	mock.ExpectQuery("SELECT id,COALESCE\\(refunded_cost,0\\),COALESCE\\(refunded_total_cost,0\\),COALESCE\\(refund_status,'none'\\) FROM usage_logs").
		WithArgs(cmd.RequestID, cmd.APIKeyID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "refunded_cost", "refunded_total_cost", "refund_status"}).AddRow(21, 0.0, 0.0, "none"))
	mock.ExpectExec("UPDATE usage_logs SET refund_status=").WithArgs(int64(21)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO media_usage_refund_events").WithArgs(int64(4), cmd.RequestID, cmd.APIKeyID, cmd.Scope, 2, 1, 0.5, cmd.Reason).WillReturnResult(sqlmock.NewResult(5, 1))
	mock.ExpectCommit()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT state,refunded_cost,refunded_total_cost,requested_units,delivered_units,refund_ratio FROM media_usage_refund_events").WithArgs(int64(4), cmd.Scope).
		WillReturnRows(sqlmock.NewRows([]string{"state", "refunded_cost", "refunded_total_cost", "requested_units", "delivered_units", "refund_ratio"}).AddRow("pending", 0.0, 0.0, 2, 1, 0.5))
	mock.ExpectExec("UPDATE users SET balance=balance\\+\\$1").WithArgs(0.5, int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT id,COALESCE\\(refunded_cost,0\\),COALESCE\\(refunded_total_cost,0\\),COALESCE\\(refund_status,'none'\\) FROM usage_logs").
		WithArgs(cmd.RequestID, cmd.APIKeyID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "refunded_cost", "refunded_total_cost", "refund_status"}).AddRow(21, 0.0, 0.0, "pending"))
	mock.ExpectExec("UPDATE usage_logs SET refunded_cost=").WithArgs(0.5, 1.0, cmd.Reason, int64(21)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE media_usage_refund_events SET state='refunded'").WithArgs(0.5, 1.0, int64(4), cmd.Scope).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	result, err := repo.RefundMediaUsage(context.Background(), cmd)
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.InDelta(t, 0.5, result.RefundedCost, 1e-12)
	require.InDelta(t, 1.0, result.RefundedTotalCost, 1e-12)
	require.Equal(t, int64(7), result.UserID)
	require.Equal(t, int64(9), *result.GroupID)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id,request_id,api_key_id,user_id,account_id,group_id,subscription_id,platform,billing_type,total_cost,actual_cost,balance_cost,subscription_cost,api_key_quota_cost,api_key_rate_limit_cost,account_quota_cost,platform_quota_cost,account_stats_cost,window_snapshot FROM media_usage_refund_snapshots").
		WithArgs(cmd.RequestID, cmd.APIKeyID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "request_id", "api_key_id", "user_id", "account_id", "group_id", "subscription_id", "platform", "billing_type", "total_cost", "actual_cost", "balance_cost", "subscription_cost", "api_key_quota_cost", "api_key_rate_limit_cost", "account_quota_cost", "platform_quota_cost", "account_stats_cost", "window_snapshot"}).AddRow(4, cmd.RequestID, 12, 7, 8, 9, nil, "gemini", service.BillingTypeBalance, 2.0, 1.0, 1.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, []byte(`{}`)))
	mock.ExpectQuery("SELECT id,COALESCE\\(refunded_cost,0\\),COALESCE\\(refunded_total_cost,0\\),COALESCE\\(refund_status,'none'\\) FROM usage_logs").
		WithArgs(cmd.RequestID, cmd.APIKeyID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "refunded_cost", "refunded_total_cost", "refund_status"}).AddRow(21, 0.5, 1.0, "refunded"))
	mock.ExpectExec("UPDATE usage_logs SET refund_status=").WithArgs(int64(21)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO media_usage_refund_events").WithArgs(int64(4), cmd.RequestID, cmd.APIKeyID, cmd.Scope, 2, 1, 0.5, cmd.Reason).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT state,refunded_cost,refunded_total_cost,requested_units,delivered_units,refund_ratio FROM media_usage_refund_events").WithArgs(int64(4), cmd.Scope).
		WillReturnRows(sqlmock.NewRows([]string{"state", "refunded_cost", "refunded_total_cost", "requested_units", "delivered_units", "refund_ratio"}).AddRow("refunded", 0.5, 1.0, 2, 1, 0.5))
	mock.ExpectRollback()
	duplicate, err := repo.RefundMediaUsage(context.Background(), cmd)
	require.NoError(t, err)
	require.False(t, duplicate.Applied)
	require.NoError(t, mock.ExpectationsWereMet())
}
