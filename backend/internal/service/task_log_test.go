//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTaskLogStatusMapping(t *testing.T) {
	cases := []struct{ taskType, raw, want string }{
		{TaskLogTypeVideo, "submitting", TaskLogStatusQueued},
		{TaskLogTypeVideo, "queued", TaskLogStatusQueued},
		{TaskLogTypeVideo, "in_progress", TaskLogStatusRunning},
		{TaskLogTypeVideo, "unknown", TaskLogStatusRunning},
		{TaskLogTypeVideo, "completed", TaskLogStatusSucceeded},
		{TaskLogTypeVideo, "failed", TaskLogStatusFailed},
		{TaskLogTypeVideo, "cancelled", TaskLogStatusCancelled},
		{TaskLogTypeVideo, "expired", TaskLogStatusExpired},
		{TaskLogTypeBatchImage, "created", TaskLogStatusQueued},
		{TaskLogTypeBatchImage, "uploading", TaskLogStatusQueued},
		{TaskLogTypeBatchImage, "submitted", TaskLogStatusQueued},
		{TaskLogTypeBatchImage, "running", TaskLogStatusRunning},
		{TaskLogTypeBatchImage, "indexing", TaskLogStatusRunning},
		{TaskLogTypeBatchImage, "settling", TaskLogStatusRunning},
		{TaskLogTypeBatchImage, "completed", TaskLogStatusSucceeded},
		{TaskLogTypeBatchImage, "output_deleted", TaskLogStatusSucceeded},
		{TaskLogTypeBatchImage, "failed", TaskLogStatusFailed},
		{TaskLogTypeBatchImage, "cancelled", TaskLogStatusCancelled},
	}
	for _, c := range cases {
		require.Equal(t, c.want, TaskLogStatus(c.taskType, c.raw), "%s/%s", c.taskType, c.raw)
		require.Contains(t, TaskLogRawStatuses(c.taskType, c.want), c.raw, "%s/%s", c.taskType, c.want)
	}
	require.Empty(t, TaskLogRawStatuses(TaskLogTypeBatchImage, TaskLogStatusExpired))
	require.Equal(t, TaskLogStatusRunning, TaskLogStatus(TaskLogTypeVideo, "something_new"))
}

func TestTaskLogRedactForUser(t *testing.T) {
	row := TaskLog{TaskID: "task_1", ResultURL: "https://upstream/x.mp4", SettlementState: "charged", ActualCostUSD: 1, RefundedCostUSD: 1, UserID: 7, UserEmail: "a@b.c", CostUSD: 0.5}
	row.RedactForUser()
	require.Equal(t, TaskLog{TaskID: "task_1", CostUSD: 0.5}, row)
}
