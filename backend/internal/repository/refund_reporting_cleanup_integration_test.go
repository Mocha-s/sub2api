//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func insertRefundReportingJob(t *testing.T, usageID int64) int64 {
	t.Helper()
	var jobID int64
	err := integrationDB.QueryRowContext(context.Background(), `INSERT INTO video_task_refund_reporting_jobs (settlement_id,usage_log_id,usage_created_at)
		SELECT s.id,$1,l.created_at FROM video_task_settlements s JOIN usage_logs l ON l.id=$1
		WHERE s.usage_log_id=$1 RETURNING id`, usageID).Scan(&jobID)
	require.NoError(t, err)
	return jobID
}

func usageCleanupRange(t *testing.T, usageID, userID int64) service.UsageCleanupFilters {
	t.Helper()
	var createdAt time.Time
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT created_at FROM usage_logs WHERE id=$1`, usageID).Scan(&createdAt))
	return service.UsageCleanupFilters{StartTime: createdAt.Add(-time.Minute), EndTime: createdAt.Add(time.Minute), UserID: &userID}
}

func requireUsageExists(t *testing.T, usageID int64, exists bool) {
	t.Helper()
	var got bool
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT EXISTS(SELECT 1 FROM usage_logs WHERE id=$1)`, usageID).Scan(&got))
	require.Equal(t, exists, got)
}

func TestRefundReportingCleanup_PendingBlocksRetentionAndAdminCleanup(t *testing.T) {
	retention := newChargedUsageRepairFixture(t)
	insertRefundReportingJob(t, retention.usageID)
	agg := newDashboardAggregationRepositoryWithSQL(integrationDB)
	require.NoError(t, agg.CleanupUsageLogs(context.Background(), time.Now().Add(time.Hour)))
	requireUsageExists(t, retention.usageID, true)

	admin := newChargedUsageRepairFixture(t)
	insertRefundReportingJob(t, admin.usageID)
	repo := newUsageCleanupRepositoryWithSQL(nil, integrationDB)
	deleted, err := repo.DeleteUsageLogsBatch(context.Background(), usageCleanupRange(t, admin.usageID, admin.userID), 10)
	require.NoError(t, err)
	require.Zero(t, deleted)
	requireUsageExists(t, admin.usageID, true)
}

func TestRefundReportingCleanup_ClaimedAndRetryJobsBlock(t *testing.T) {
	for _, state := range []string{"claimed", "retry"} {
		t.Run(state, func(t *testing.T) {
			f := newChargedUsageRepairFixture(t)
			jobID := insertRefundReportingJob(t, f.usageID)
			if state == "claimed" {
				_, err := integrationDB.ExecContext(context.Background(), `UPDATE video_task_refund_reporting_jobs SET locked_by='worker',locked_until=NOW()+INTERVAL '1 minute' WHERE id=$1`, jobID)
				require.NoError(t, err)
			} else {
				_, err := integrationDB.ExecContext(context.Background(), `UPDATE video_task_refund_reporting_jobs SET attempts=2,next_attempt_at=NOW()+INTERVAL '1 minute',last_error='retry' WHERE id=$1`, jobID)
				require.NoError(t, err)
			}
			require.NoError(t, newDashboardAggregationRepositoryWithSQL(integrationDB).CleanupUsageLogs(context.Background(), time.Now().Add(time.Hour)))
			requireUsageExists(t, f.usageID, true)
			deleted, err := newUsageCleanupRepositoryWithSQL(nil, integrationDB).DeleteUsageLogsBatch(context.Background(), usageCleanupRange(t, f.usageID, f.userID), 10)
			require.NoError(t, err)
			require.Zero(t, deleted)
			requireUsageExists(t, f.usageID, true)
		})
	}
}

func TestRefundReportingCleanup_CompletedJobPermitsDeletion(t *testing.T) {
	retention := newChargedUsageRepairFixture(t)
	jobID := insertRefundReportingJob(t, retention.usageID)
	_, err := integrationDB.ExecContext(context.Background(), `UPDATE video_task_refund_reporting_jobs SET completed_at=NOW() WHERE id=$1`, jobID)
	require.NoError(t, err)
	require.NoError(t, newDashboardAggregationRepositoryWithSQL(integrationDB).CleanupUsageLogs(context.Background(), time.Now().Add(time.Hour)))
	requireUsageExists(t, retention.usageID, false)

	admin := newChargedUsageRepairFixture(t)
	jobID = insertRefundReportingJob(t, admin.usageID)
	_, err = integrationDB.ExecContext(context.Background(), `UPDATE video_task_refund_reporting_jobs SET completed_at=NOW() WHERE id=$1`, jobID)
	require.NoError(t, err)
	deleted, err := newUsageCleanupRepositoryWithSQL(nil, integrationDB).DeleteUsageLogsBatch(context.Background(), usageCleanupRange(t, admin.usageID, admin.userID), 10)
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)
	requireUsageExists(t, admin.usageID, false)
}

func TestRefundReportingCleanup_ConcurrentRefundInsertSerializesWithoutLosingJob(t *testing.T) {
	f := newChargedUsageRepairFixture(t)
	tx, err := integrationDB.BeginTx(context.Background(), &sql.TxOptions{})
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.ExecContext(context.Background(), `UPDATE usage_logs SET refunded_at=NOW() WHERE id=$1`, f.usageID)
	require.NoError(t, err)
	_, err = tx.ExecContext(context.Background(), `INSERT INTO video_task_refund_reporting_jobs (settlement_id,usage_log_id,usage_created_at)
		SELECT s.id,$1,l.created_at FROM video_task_settlements s JOIN usage_logs l ON l.id=$1 WHERE s.usage_log_id=$1`, f.usageID)
	require.NoError(t, err)

	type cleanupResult struct {
		deleted int64
		err     error
	}
	resultCh := make(chan cleanupResult, 1)
	filters := usageCleanupRange(t, f.usageID, f.userID)
	go func() {
		deleted, cleanupErr := newUsageCleanupRepositoryWithSQL(nil, integrationDB).DeleteUsageLogsBatch(context.Background(), filters, 10)
		resultCh <- cleanupResult{deleted: deleted, err: cleanupErr}
	}()
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, tx.Commit())
	result := <-resultCh
	require.NoError(t, result.err)
	require.Zero(t, result.deleted)
	requireUsageExists(t, f.usageID, true)
	var jobs int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM video_task_refund_reporting_jobs WHERE usage_log_id=$1 AND completed_at IS NULL`, f.usageID).Scan(&jobs))
	require.Equal(t, 1, jobs)
}

func TestRefundReportingCleanup_ProcessesCorrectionThenAllowsCleanup(t *testing.T) {
	f := newChargedUsageRepairFixture(t)
	insertRefundReportingJob(t, f.usageID)
	_, err := integrationDB.ExecContext(context.Background(), `UPDATE video_task_refund_reporting_jobs SET next_attempt_at=NOW()+INTERVAL '1 hour' WHERE usage_log_id<>$1 AND completed_at IS NULL`, f.usageID)
	require.NoError(t, err)
	repo := NewVideoTaskSettlementRepository(testEntClient(t), integrationDB)
	cache := &refundReportingDashboardCache{}
	dashboard := service.NewDashboardAggregationService(NewDashboardAggregationRepository(integrationDB), nil, &config.Config{DashboardAgg: config.DashboardAggregationConfig{Enabled: true}})
	worker := service.NewVideoTaskSettlementReconciler(repo, service.NewVideoTaskSettlementService(repo, nil, nil, nil, nil, dashboard, cache))
	require.NoError(t, worker.ReconcileRefundReportingOnce(context.Background(), time.Now()))
	require.Equal(t, 1, cache.invalidations)

	deleted, err := newUsageCleanupRepositoryWithSQL(nil, integrationDB).DeleteUsageLogsBatch(context.Background(), usageCleanupRange(t, f.usageID, f.userID), 10)
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)
	requireUsageExists(t, f.usageID, false)
}

func TestUsageLogsCleanupBatch_PartitionedTableDeletesOnlySelectedIDs(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
		CREATE TEMP TABLE cleanup_usage_logs (id BIGINT NOT NULL, created_at TIMESTAMPTZ NOT NULL) PARTITION BY RANGE (created_at);
		CREATE TEMP TABLE cleanup_usage_logs_old PARTITION OF cleanup_usage_logs FOR VALUES FROM ('2025-01-01') TO ('2025-02-01');
		CREATE TEMP TABLE cleanup_usage_logs_new PARTITION OF cleanup_usage_logs FOR VALUES FROM ('2025-02-01') TO ('2025-03-01');
		CREATE TEMP TABLE cleanup_reporting_jobs (id BIGSERIAL PRIMARY KEY, usage_log_id BIGINT NOT NULL, completed_at TIMESTAMPTZ);
		INSERT INTO cleanup_usage_logs VALUES (101,'2025-01-10'),(102,'2025-01-11'),(201,'2025-02-10'),(202,'2025-02-11');
		INSERT INTO cleanup_reporting_jobs (usage_log_id,completed_at) VALUES (101,NOW()),(201,NOW());
	`)
	require.NoError(t, err)

	var oldCTID, newCTID string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT ctid::text FROM cleanup_usage_logs WHERE id=101`).Scan(&oldCTID))
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT ctid::text FROM cleanup_usage_logs WHERE id=201`).Scan(&newCTID))
	require.Equal(t, oldCTID, newCTID, "fixture must exercise reused partition-local ctid")

	result, err := tx.ExecContext(ctx, buildUsageLogsCleanupBatchSQL("cleanup_usage_logs", "cleanup_reporting_jobs"), time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC), 100)
	require.NoError(t, err)
	deleted, err := result.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(2), deleted)

	var remainingIDs []int64
	rows, err := tx.QueryContext(ctx, `SELECT id FROM cleanup_usage_logs ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		remainingIDs = append(remainingIDs, id)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []int64{201, 202}, remainingIDs)

	var remainingJobUsageIDs []int64
	jobRows, err := tx.QueryContext(ctx, `SELECT usage_log_id FROM cleanup_reporting_jobs ORDER BY usage_log_id`)
	require.NoError(t, err)
	defer jobRows.Close()
	for jobRows.Next() {
		var id int64
		require.NoError(t, jobRows.Scan(&id))
		remainingJobUsageIDs = append(remainingJobUsageIDs, id)
	}
	require.NoError(t, jobRows.Err())
	require.Equal(t, []int64{201}, remainingJobUsageIDs, "completed jobs outside the selected IDs must remain")
}
