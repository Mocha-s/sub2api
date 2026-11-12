//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type taskLogRepoFake struct {
	filter service.TaskLogFilter
	params pagination.PaginationParams
	rows   []service.TaskLog
}

func (f *taskLogRepoFake) List(_ context.Context, params pagination.PaginationParams, filter service.TaskLogFilter) ([]service.TaskLog, *pagination.PaginationResult, error) {
	f.filter, f.params = filter, params
	return f.rows, &pagination.PaginationResult{Total: int64(len(f.rows))}, nil
}

type taskLogVideoFake struct {
	params service.VideoTaskContentParams
}

func (f *taskLogVideoFake) Content(_ context.Context, p service.VideoTaskContentParams) (*service.VideoContentStream, error) {
	f.params = p
	return nil, service.ErrVideoTaskNotFound
}

type taskLogBatchRepoFake struct {
	service.BatchImageRepository
	job *service.BatchImageJob
}

func (f *taskLogBatchRepoFake) GetBatchImageJobByBatchID(context.Context, string) (*service.BatchImageJob, error) {
	return f.job, nil
}

func taskLogRequest(h *TaskLogHandler, userID int64, target string, headers map[string]string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: userID})
	})
	r.GET("/task-logs", h.List)
	r.GET("/admin/task-logs", h.AdminList)
	r.GET("/task-logs/video/:task_id/content", h.VideoContent)
	r.GET("/task-logs/batch-image/:batch_id/download", h.BatchImageDownload)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	r.ServeHTTP(w, req)
	return w
}

func taskLogFirstItem(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body["data"].(map[string]any)["items"].([]any)[0].(map[string]any)
}

func adminOnlyRow() service.TaskLog {
	return service.TaskLog{Type: service.TaskLogTypeVideo, TaskID: "task_1", ResultURL: "https://upstream/x.mp4", SettlementState: "charged", ActualCostUSD: 1, UserID: 9, UserEmail: "a@b.c"}
}

func TestTaskLogHandlerListIsScopedAndRedacted(t *testing.T) {
	repo := &taskLogRepoFake{rows: []service.TaskLog{adminOnlyRow()}}
	h := &TaskLogHandler{repo: repo}

	item := taskLogFirstItem(t, taskLogRequest(h, 7, "/task-logs?user_id=9&page_size=500&status=running", nil))

	require.Equal(t, int64(7), repo.filter.UserID) // user_id 参数对用户端无效
	require.True(t, repo.filter.ExcludeUserDeleted)
	require.Equal(t, "running", repo.filter.Status)
	require.Equal(t, 100, repo.params.PageSize)
	require.Equal(t, "task_1", item["task_id"])
	for _, key := range []string{"result_url", "settlement_state", "actual_cost_usd", "user_id", "user_email"} {
		require.NotContains(t, item, key)
	}
}

func TestTaskLogHandlerAdminListKeepsAdminFields(t *testing.T) {
	repo := &taskLogRepoFake{rows: []service.TaskLog{adminOnlyRow()}}
	h := &TaskLogHandler{repo: repo}

	item := taskLogFirstItem(t, taskLogRequest(h, 1, "/admin/task-logs?user_id=9", nil))

	require.Equal(t, int64(9), repo.filter.UserID)
	require.False(t, repo.filter.ExcludeUserDeleted)
	require.Equal(t, "https://upstream/x.mp4", item["result_url"])
	require.Equal(t, "charged", item["settlement_state"])
}

func TestTaskLogHandlerListRejectsBadTime(t *testing.T) {
	w := taskLogRequest(&TaskLogHandler{repo: &taskLogRepoFake{}}, 7, "/task-logs?start=yesterday", nil)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestTaskLogHandlerVideoContentUsesCallerAndForwardsOnlyRange(t *testing.T) {
	videos := &taskLogVideoFake{}
	h := &TaskLogHandler{videos: videos}

	w := taskLogRequest(h, 7, "/task-logs/video/task_1/content", map[string]string{"Authorization": "Bearer jwt", "Range": "bytes=0-1"})

	require.Equal(t, http.StatusNotFound, w.Code)
	require.Equal(t, int64(7), videos.params.UserID)
	require.Equal(t, "task_1", videos.params.PublicTaskID)
	require.Equal(t, "bytes=0-1", videos.params.Header.Get("Range"))
	require.Empty(t, videos.params.Header.Get("Authorization"))
}

func TestTaskLogHandlerBatchImageDownloadRejectsOtherUsersJob(t *testing.T) {
	keyID := int64(3)
	h := &TaskLogHandler{batches: &taskLogBatchRepoFake{job: &service.BatchImageJob{BatchID: "batch_1", UserID: 8, APIKeyID: &keyID}}}

	w := taskLogRequest(h, 7, "/task-logs/batch-image/batch_1/download", nil)

	require.Equal(t, http.StatusNotFound, w.Code)
}
