package service

import (
	"context"
	"slices"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

const (
	TaskLogTypeVideo      = "video"
	TaskLogTypeBatchImage = "batch_image"

	TaskLogStatusQueued    = "queued"
	TaskLogStatusRunning   = "running"
	TaskLogStatusSucceeded = "succeeded"
	TaskLogStatusFailed    = "failed"
	TaskLogStatusCancelled = "cancelled"
	TaskLogStatusExpired   = "expired"
)

// TaskLog 是任务日志的一行：一个视频任务或一个批量图片任务。
type TaskLog struct {
	Type         string     `json:"type"`
	TaskID       string     `json:"task_id"`
	Platform     string     `json:"platform"`
	Model        string     `json:"model"`
	Status       string     `json:"status"`
	RawStatus    string     `json:"raw_status"`
	Progress     int        `json:"progress"`
	SubmittedAt  time.Time  `json:"submitted_at"`
	FinishedAt   *time.Time `json:"finished_at"`
	Resolution   string     `json:"resolution,omitempty"`
	Seconds      int        `json:"seconds,omitempty"`
	ItemCount    int        `json:"item_count,omitempty"`
	SuccessCount int        `json:"success_count,omitempty"`
	FailCount    int        `json:"fail_count,omitempty"`
	CostUSD      float64    `json:"cost_usd"`
	Prompt       string     `json:"prompt"`
	ErrorCode    string     `json:"error_code,omitempty"`
	ErrorMessage string     `json:"error_message,omitempty"`
	HasResult    bool       `json:"has_result"`

	// 以下字段仅管理端可见，用户端响应前必须 RedactForUser。
	ResultURL       string  `json:"result_url,omitempty"`
	SettlementState string  `json:"settlement_state,omitempty"`
	ActualCostUSD   float64 `json:"actual_cost_usd,omitempty"`
	RefundedCostUSD float64 `json:"refunded_cost_usd,omitempty"`
	UserID          int64   `json:"user_id,omitempty"`
	UserEmail       string  `json:"user_email,omitempty"`
}

// RedactForUser 清掉仅管理端可见的字段（上游地址、结算、用户身份）。
func (t *TaskLog) RedactForUser() {
	t.ResultURL, t.SettlementState, t.UserEmail = "", "", ""
	t.ActualCostUSD, t.RefundedCostUSD, t.UserID = 0, 0, 0
}

type TaskLogFilter struct {
	UserID             int64 // 0 = 全部用户
	ExcludeUserDeleted bool
	Type               string
	Status             string // 统一状态
	Platform           string
	TaskID             string
	Start, End         *time.Time // 提交时间 [Start, End)
}

type TaskLogRepository interface {
	List(ctx context.Context, params pagination.PaginationParams, filter TaskLogFilter) ([]TaskLog, *pagination.PaginationResult, error)
}

var taskLogRawStatuses = map[string]map[string][]string{
	TaskLogTypeVideo: {
		TaskLogStatusQueued:    {string(VideoTaskStatusSubmitting), string(VideoTaskStatusQueued)},
		TaskLogStatusRunning:   {string(VideoTaskStatusInProgress), string(VideoTaskStatusUnknown)},
		TaskLogStatusSucceeded: {string(VideoTaskStatusCompleted)},
		TaskLogStatusFailed:    {string(VideoTaskStatusFailed)},
		TaskLogStatusCancelled: {string(VideoTaskStatusCancelled)},
		TaskLogStatusExpired:   {string(VideoTaskStatusExpired)},
	},
	TaskLogTypeBatchImage: {
		TaskLogStatusQueued:    {BatchImageJobStatusCreated, BatchImageJobStatusUploading, BatchImageJobStatusSubmitted},
		TaskLogStatusRunning:   {BatchImageJobStatusRunning, BatchImageJobStatusIndexing, BatchImageJobStatusSettling},
		TaskLogStatusSucceeded: {BatchImageJobStatusCompleted, BatchImageJobStatusOutputDeleted},
		TaskLogStatusFailed:    {BatchImageJobStatusFailed},
		TaskLogStatusCancelled: {BatchImageJobStatusCancelled},
	},
}

// TaskLogRawStatuses 把统一状态展开成某类任务的原始状态；没有对应状态时返回 nil。
func TaskLogRawStatuses(taskType, status string) []string {
	return taskLogRawStatuses[taskType][status]
}

// TaskLogStatus 把原始状态归并为统一状态。不认识的状态算作进行中，
// 这样它仍然可见并保持自动刷新。
func TaskLogStatus(taskType, raw string) string {
	for status, raws := range taskLogRawStatuses[taskType] {
		if slices.Contains(raws, raw) {
			return status
		}
	}
	return TaskLogStatusRunning
}
