//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestVideoAdmissionFailureDoesNotOverwriteRecoveredReservation(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, false)
	settlements := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	result, err := settlements.Reserve(ctx, &service.VideoTaskSettlementReserveCommand{PublicTaskID: f.publicID, BillingType: service.BillingTypeBalance, GrossCostUSD: 1, ActualCostUSD: 1, Effects: service.VideoTaskBillingEffects{BalanceCost: 1}})
	require.NoError(t, err)
	require.True(t, result.Applied)
	tasks := NewVideoTaskRepository(testEntClient(t), integrationDB)
	applied, err := tasks.FailAdmission(ctx, f.publicID, "VIDEO_TASK_INSUFFICIENT_BALANCE", "insufficient balance")
	require.NoError(t, err)
	require.False(t, applied)
	var state string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM video_tasks WHERE public_task_id=$1`, f.publicID).Scan(&state))
	require.Equal(t, "submitting", state)
	assertUserMoney(t, f.userID, 19, 1)
}

func TestVideoAdmissionFailureRecordsBusinessReasonWithoutFunding(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, false)
	tasks := NewVideoTaskRepository(testEntClient(t), integrationDB)
	applied, err := tasks.FailAdmission(ctx, f.publicID, "VIDEO_TASK_INSUFFICIENT_BALANCE", "insufficient balance")
	require.NoError(t, err)
	require.True(t, applied)
	var state, code string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status,error_code FROM video_tasks WHERE public_task_id=$1`, f.publicID).Scan(&state, &code))
	require.Equal(t, "failed", state)
	require.Equal(t, "VIDEO_TASK_INSUFFICIENT_BALANCE", code)
	assertUserMoney(t, f.userID, 20, 0)
}
