//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestMediaRetentionListAndImageContentFence(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, true)
	end := time.Now().Add(-72 * time.Hour)
	_, err := integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='completed',completed_at=$2 WHERE public_task_id=$1`, f.publicID, end)
	require.NoError(t, err)
	imageID, batchID := "img_"+uuid.NewString(), "batch_"+uuid.NewString()
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO image_task_logs(task_id,user_id,api_key_id,request_mode,status,created_at,finished_at,deadline_at,results)
	VALUES($1,$2,$3,'sync','completed',$4,$4,$4,'[{"data":"eA=="}]')`, imageID, f.userID, f.apiKeyID, end)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO batch_image_jobs(batch_id,user_id,api_key_id,provider,model,status,item_count,finished_at) VALUES($1,$2,$3,'vertex','imagen','completed',1,$4)`, batchID, f.userID, f.apiKeyID, end)
	require.NoError(t, err)
	for _, userID := range []int64{0, f.userID} {
		for _, id := range []string{imageID, batchID, f.publicID} {
			rows, page, err := NewTaskLogRepository(integrationDB).List(ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, service.TaskLogFilter{UserID: userID, TaskID: id})
			require.NoError(t, err)
			require.Empty(t, rows, id)
			require.Zero(t, page.Total)
		}
		_, err = NewImageTaskLogRepository(integrationDB).Get(ctx, imageID, userID, 0)
		require.ErrorIs(t, err, service.ErrImageTaskNotFound)
	}
}

func TestMediaRetentionCleanupPreservesFinanceAndPendingReconciliation(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, true)
	end := time.Now().Add(-73 * time.Hour)
	_, err := integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status='completed',completed_at=NULL,updated_at=$2,
	request_body='secret',upstream_response_body='secret',upstream_response='{"url":"secret"}',result_url='secret',
	request_metadata='{"arbitrary_media":"secret","request_metadata":{"video_pricing_snapshot":{"actual_cost_usd":1}}}',result_metadata='{"url":"secret"}'
	WHERE public_task_id=$1`, f.publicID, end)
	require.NoError(t, err)
	var settlementID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO video_task_settlements(video_task_id,user_id,api_key_id,group_id,account_id,platform,charge_request_id,state,billing_type,actual_cost_usd,next_reconcile_at)
	SELECT id,user_id,api_key_id,group_id,account_id,platform,public_task_id,'charged',0,1,NOW() FROM video_tasks WHERE public_task_id=$1 RETURNING id`, f.publicID).Scan(&settlementID))
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO video_task_settlement_events(settlement_id,event_id,event_type,amount_usd) VALUES($1,$2,'capture',1),($1,$3,'refund',0.25)`, settlementID, uuid.NewString(), uuid.NewString())
	require.NoError(t, err)
	cleaner, ok := NewImageTaskLogRepository(integrationDB).(interface {
		CleanupExpired(context.Context, time.Time, int) error
	})
	require.True(t, ok, "repository must support bounded financial-safe cleanup")
	require.NoError(t, cleaner.CleanupExpired(ctx, time.Now(), 100))
	var body []byte
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT request_body FROM video_tasks WHERE public_task_id=$1`, f.publicID).Scan(&body))
	require.Equal(t, []byte("secret"), body, "pending reconciliation payload is protected")
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_task_settlements SET next_reconcile_at=NULL WHERE id=$1`, settlementID)
	require.NoError(t, err)
	require.NoError(t, cleaner.CleanupExpired(ctx, time.Now(), 100))
	var prompt, metadata string
	var completed, scrubbed time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT prompt,request_body,request_metadata::text,completed_at,retention_scrubbed_at FROM video_tasks WHERE public_task_id=$1`, f.publicID).Scan(&prompt, &body, &metadata, &completed, &scrubbed))
	require.Empty(t, prompt)
	require.Empty(t, body)
	require.NotContains(t, metadata, "secret")
	require.Contains(t, metadata, "video_pricing_snapshot")
	require.WithinDuration(t, end, completed, time.Microsecond)
	require.NoError(t, cleaner.CleanupExpired(ctx, time.Now().Add(time.Hour), 100))
	var again time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT completed_at FROM video_tasks WHERE public_task_id=$1`, f.publicID).Scan(&again))
	require.Equal(t, completed, again)
	var events, usage, settlements int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_task_settlement_events WHERE settlement_id=$1`, settlementID).Scan(&events))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_task_settlements WHERE id=$1`, settlementID).Scan(&settlements))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_logs WHERE id=$1`, f.usageLogID).Scan(&usage))
	require.Equal(t, 2, events)
	require.Equal(t, 1, settlements)
	require.Equal(t, 1, usage)
}

func TestMediaRetentionImageCleanupBoundAndLateWrites(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, true)
	repo := NewImageTaskLogRepository(integrationDB)
	cleaner := repo.(interface {
		CleanupExpired(context.Context, time.Time, int) error
	})
	old := time.Now().Add(-73 * time.Hour)
	ids := []string{"img_" + uuid.NewString(), "img_" + uuid.NewString()}
	for _, id := range ids {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO image_task_logs(task_id,user_id,api_key_id,request_mode,status,deadline_at,finished_at,results)
		VALUES($1,$2,$3,'sync','completed',$4,$4,'[{"data":"eA=="}]')`, id, f.userID, f.apiKeyID, old)
		require.NoError(t, err)
	}
	require.NoError(t, cleaner.CleanupExpired(ctx, time.Now(), 1))
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM image_task_logs WHERE task_id IN ($1,$2)`, ids[0], ids[1]).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, cleaner.CleanupExpired(ctx, time.Now(), 100))
	finished := time.Now()
	require.NoError(t, repo.Save(ctx, &service.ImageTaskLogEntry{TaskID: ids[0], UserID: f.userID, APIKeyID: f.apiKeyID,
		RequestMode: "sync", Status: "completed", CreatedAt: old, DeadlineAt: old, FinishedAt: &finished, ItemCount: 1}))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM image_task_logs WHERE task_id IN ($1,$2)`, ids[0], ids[1]).Scan(&count))
	require.Zero(t, count, "late completion must not resurrect an abandoned, physically deleted image log")
}

func TestMediaRetentionBatchOwnerReadsExcludeExpiredWithoutAffectingFinance(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, false)
	id := "batch_" + uuid.NewString()
	old := time.Now().Add(-72 * time.Hour)
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO batch_image_jobs(batch_id,user_id,api_key_id,provider,model,status,item_count,finished_at,task_name)
	VALUES($1,$2,$3,'vertex','imagen','completed',1,$4,'private prompt')`, id, f.userID, f.apiKeyID, old)
	require.NoError(t, err)
	repo := NewBatchImageRepository(integrationDB)
	_, err = repo.GetBatchImageJobByBatchIDForOwner(ctx, f.userID, f.apiKeyID, id)
	require.ErrorIs(t, err, service.ErrBatchImageJobNotFound)
	_, err = repo.ListBatchImageItemsForOwner(ctx, f.userID, f.apiKeyID, id, service.BatchImageItemFilter{})
	require.ErrorIs(t, err, service.ErrBatchImageJobNotFound)
	jobs, err := repo.ListBatchImageJobsForOwner(ctx, f.userID, f.apiKeyID, service.BatchImageJobFilter{})
	require.NoError(t, err)
	require.Empty(t, jobs)
	job, err := repo.GetBatchImageJobByBatchID(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "private prompt", job.TaskName)
	require.Nil(t, job.SettledAt, "unsettled billing metadata is retained internally")
}
