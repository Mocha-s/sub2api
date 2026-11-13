//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestTaskLogRepositoryLegacyVideoMetadata(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, false)
	repo := NewTaskLogRepository(integrationDB)
	for _, tc := range []struct {
		status, metadata string
		want             int
	}{
		{"in_progress", `{"progress":42.9,"result_url":"https://cdn.example/old.mp4"}`, 42},
		{"in_progress", `{"progress":1e30}`, 100},
		{"in_progress", `{"progress":-2}`, 0},
		{"in_progress", `{"progress":"garbage"}`, 0},
		{"completed", `{"progress":0,"result_url":"https://cdn.example/old.mp4"}`, 100},
	} {
		_, err := integrationDB.ExecContext(ctx, `UPDATE video_tasks SET status=$2,progress=0,result_url=NULL,result_metadata=$3::jsonb,completed_at=NULL WHERE public_task_id=$1`, f.publicID, tc.status, tc.metadata)
		require.NoError(t, err)
		rows, _, err := repo.List(ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, service.TaskLogFilter{TaskID: f.publicID})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, tc.want, rows[0].Progress)
		if tc.status == "completed" {
			require.Equal(t, "https://cdn.example/old.mp4", rows[0].ResultURL)
			require.NotNil(t, rows[0].FinishedAt)
		}
	}
}

func TestTaskLogRepositoryImageUnionAndExpiry(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, false)
	id := "img_" + uuid.NewString()
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO image_task_logs
 (task_id,user_id,api_key_id,request_mode,platform,model,prompt,status,item_count,created_at,deadline_at)
 VALUES ($1,$2,$3,'async','openai','gpt-image-1','draw cat','processing',2,NOW()+INTERVAL '1 second',NOW()+INTERVAL '30 minutes')`, id, f.userID, f.apiKeyID)
	require.NoError(t, err)
	repo := NewTaskLogRepository(integrationDB)
	params := pagination.PaginationParams{Page: 1, PageSize: 10}
	rows, page, err := repo.List(ctx, params, service.TaskLogFilter{UserID: f.userID})
	require.NoError(t, err)
	require.EqualValues(t, 2, page.Total)
	require.Equal(t, id, rows[0].TaskID)
	require.Equal(t, "image", rows[0].Type)
	require.Equal(t, "async", rows[0].RequestMode)
	require.Equal(t, "running", rows[0].Status)
	require.Equal(t, 2, rows[0].ItemCount)
	require.False(t, rows[0].HasResult)
	require.NotNil(t, rows[0].CostKnown)
	require.False(t, *rows[0].CostKnown)
	rows, _, err = repo.List(ctx, params, service.TaskLogFilter{UserID: f.userID + 999, Type: "image"})
	require.NoError(t, err)
	require.Empty(t, rows)
	_, err = integrationDB.ExecContext(ctx, `UPDATE image_task_logs SET deadline_at=NOW()-INTERVAL '1 minute' WHERE task_id=$1`, id)
	require.NoError(t, err)
	rows, _, err = repo.List(ctx, params, service.TaskLogFilter{UserID: f.userID, Type: "image", Status: "expired"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].FinishedAt)
	require.True(t, rows[0].FinishedAt.Before(time.Now()))
	require.Equal(t, "execution_expired", rows[0].ErrorCode)
}

func TestImageTaskLogRepositoryRetainsResultsOwnershipAndBilling(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, true)
	repo := NewImageTaskLogRepository(integrationDB)
	e := &service.ImageTaskLogEntry{TaskID: "img_" + uuid.NewString(), UserID: f.userID, APIKeyID: f.apiKeyID, RequestMode: "sync",
		Platform: "openai", Model: "gpt-image-1", Prompt: "cat", Status: "processing", ItemCount: 2, CreatedAt: time.Now(), DeadlineAt: time.Now().Add(time.Minute)}
	require.NoError(t, repo.Save(ctx, e))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT request_id FROM usage_logs WHERE id=$1`, f.usageLogID).Scan(&e.RequestID))
	_, err := integrationDB.ExecContext(ctx, `UPDATE usage_logs SET actual_cost=2,refunded_cost=0.5,refunded_at=NOW() WHERE id=$1`, f.usageLogID)
	require.NoError(t, err)
	now := time.Now()
	e.FinishedAt = &now
	e.Status = "completed"
	e.SuccessCount = 1
	e.Results = []service.ImageTaskLogResult{{Data: []byte("image bytes"), ContentType: "image/png"}, {Data: []byte("second image"), ContentType: "image/png"}}
	e.SuccessCount = 2
	require.NoError(t, repo.Save(ctx, e))
	// Metadata cannot accidentally reset a terminal record when a late running write arrives.
	e.Status = "processing"
	require.NoError(t, repo.Save(ctx, e))
	saved, err := repo.Get(ctx, e.TaskID, e.UserID, 0)
	require.NoError(t, err)
	require.Equal(t, "completed", saved.Status)
	require.Equal(t, []byte("image bytes"), saved.Results[0].Data)
	require.Len(t, saved.Results, 1)
	selected, err := repo.Get(ctx, e.TaskID, e.UserID, 1)
	require.NoError(t, err)
	require.Len(t, selected.Results, 1)
	require.Equal(t, []byte("second image"), selected.Results[0].Data)
	selected, err = repo.Get(ctx, e.TaskID, e.UserID, 2)
	require.NoError(t, err)
	require.Empty(t, selected.Results)
	_, err = repo.Get(ctx, e.TaskID, e.UserID, -1)
	require.ErrorIs(t, err, service.ErrImageTaskNotFound)
	_, err = repo.Get(ctx, e.TaskID, e.UserID+1, 0)
	require.ErrorIs(t, err, service.ErrImageTaskNotFound)
	saved, err = repo.Get(ctx, e.TaskID, 0, 0)
	require.NoError(t, err)
	require.Equal(t, e.UserID, saved.UserID)
	rows, _, err := NewTaskLogRepository(integrationDB).List(ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, service.TaskLogFilter{Type: "image", TaskID: e.TaskID, Status: "succeeded"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.True(t, rows[0].HasResult)
	require.Equal(t, 2, rows[0].SuccessCount)
	require.Equal(t, 1.5, rows[0].CostUSD)
	require.NotNil(t, rows[0].CostKnown)
	require.True(t, *rows[0].CostKnown)
}

func TestTaskLogRepository_ListMergesVideoAndBatchImage(t *testing.T) {
	ctx := context.Background()
	f := newVideoSettlementFixture(t, false, false) // 一个 status='submitting' 的视频任务
	batchID := "batch_" + uuid.NewString()
	_, err := integrationDB.ExecContext(ctx, `
		INSERT INTO batch_image_jobs (batch_id,user_id,api_key_id,provider,model,status,item_count,success_count,fail_count,created_at)
		VALUES ($1,$2,$3,'vertex','imagen','running',4,1,1,NOW()+INTERVAL '1 second')`, batchID, f.userID, f.apiKeyID)
	require.NoError(t, err)
	repo := NewTaskLogRepository(integrationDB)
	firstPage := pagination.PaginationParams{Page: 1, PageSize: 20}
	mine := service.TaskLogFilter{UserID: f.userID}

	rows, page, err := repo.List(ctx, firstPage, mine)
	require.NoError(t, err)
	require.EqualValues(t, 2, page.Total)
	require.Len(t, rows, 2)

	require.Equal(t, batchID, rows[0].TaskID) // 提交时间倒序
	require.Equal(t, service.TaskLogTypeBatchImage, rows[0].Type)
	require.Equal(t, "vertex", rows[0].Platform)
	require.Equal(t, service.TaskLogStatusRunning, rows[0].Status)
	require.Equal(t, "running", rows[0].RawStatus)
	require.Equal(t, 50, rows[0].Progress)
	require.Equal(t, 4, rows[0].ItemCount)

	require.Equal(t, f.publicID, rows[1].TaskID)
	require.Equal(t, service.TaskLogTypeVideo, rows[1].Type)
	require.Equal(t, service.TaskLogStatusQueued, rows[1].Status)
	require.Equal(t, "prompt", rows[1].Prompt)
	require.Equal(t, f.userID, rows[1].UserID)
	require.NotEmpty(t, rows[1].UserEmail)
	require.False(t, rows[1].HasResult)

	queued := mine
	queued.Status = service.TaskLogStatusQueued
	rows, _, err = repo.List(ctx, firstPage, queued)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, f.publicID, rows[0].TaskID)

	batches := mine
	batches.Type = service.TaskLogTypeBatchImage
	rows, _, err = repo.List(ctx, firstPage, batches)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, batchID, rows[0].TaskID)

	byID := mine
	byID.TaskID = f.publicID
	rows, _, err = repo.List(ctx, firstPage, byID)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	rows, page, err = repo.List(ctx, pagination.PaginationParams{Page: 2, PageSize: 1}, mine)
	require.NoError(t, err)
	require.EqualValues(t, 2, page.Total)
	require.Len(t, rows, 1)
	require.Equal(t, f.publicID, rows[0].TaskID)

	_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET user_deleted_at=NOW() WHERE public_task_id=$1`, f.publicID)
	require.NoError(t, err)
	visible := mine
	visible.ExcludeUserDeleted = true
	rows, _, err = repo.List(ctx, firstPage, visible)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, batchID, rows[0].TaskID)

	none := service.TaskLogFilter{UserID: f.userID, TaskID: "missing"}
	rows, _, err = repo.List(ctx, firstPage, none)
	require.NoError(t, err)
	require.NotNil(t, rows)
	require.Empty(t, rows)
}
