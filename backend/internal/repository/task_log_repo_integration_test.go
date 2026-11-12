//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

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
