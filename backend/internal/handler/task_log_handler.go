package handler

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type taskLogVideoContent interface {
	Content(ctx context.Context, params service.VideoTaskContentParams) (*service.VideoContentStream, error)
	AdminContent(ctx context.Context, params service.VideoTaskContentParams) (*service.VideoContentStream, error)
}

// TaskLogHandler 提供任务日志（视频 + 批量图片）的用户端与管理端接口。
type TaskLogHandler struct {
	repo     service.TaskLogRepository
	videos   taskLogVideoContent
	batches  service.BatchImageRepository
	download *service.BatchImageDownloadService
	images   *service.ImageTaskLogService
}

func NewTaskLogHandler(repo service.TaskLogRepository, videos *service.VideoTaskService, batches service.BatchImageRepository, download *service.BatchImageDownloadService, images *service.ImageTaskLogService) *TaskLogHandler {
	return &TaskLogHandler{repo: repo, videos: videos, batches: batches, download: download, images: images}
}

// List 返回当前用户自己的任务。
// GET /api/v1/task-logs
func (h *TaskLogHandler) List(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	h.list(c, service.TaskLogFilter{UserID: subject.UserID, ExcludeUserDeleted: true}, false)
}

// AdminList 返回全部用户的任务，可按 user_id 筛选。
// GET /api/v1/admin/task-logs
func (h *TaskLogHandler) AdminList(c *gin.Context) {
	userID, _ := strconv.ParseInt(c.Query("user_id"), 10, 64)
	h.list(c, service.TaskLogFilter{UserID: userID}, true)
}

func (h *TaskLogHandler) list(c *gin.Context, filter service.TaskLogFilter, admin bool) {
	page, pageSize := response.ParsePagination(c)
	if pageSize > 100 {
		pageSize = 100
	}
	filter.Type = c.Query("type")
	filter.Status = c.Query("status")
	filter.Platform = strings.TrimSpace(c.Query("platform"))
	filter.TaskID = strings.TrimSpace(c.Query("task_id"))
	for name, dst := range map[string]**time.Time{"start": &filter.Start, "end": &filter.End} {
		value := c.Query(name)
		if value == "" {
			continue
		}
		ts, err := time.Parse(time.RFC3339, value)
		if err != nil {
			response.BadRequest(c, "invalid "+name)
			return
		}
		*dst = &ts
	}

	rows, result, err := h.repo.List(c.Request.Context(), pagination.PaginationParams{Page: page, PageSize: pageSize}, filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if !admin {
		for i := range rows {
			rows[i].RedactForUser()
		}
	}
	response.Paginated(c, rows, result.Total, page, pageSize)
}

// VideoContent 经网关代理返回本人已完成视频的内容，不暴露上游地址。
// GET /api/v1/task-logs/video/:task_id/content
func (h *TaskLogHandler) VideoContent(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	stream, err := h.videos.Content(c.Request.Context(), service.VideoTaskContentParams{
		UserID:       subject.UserID,
		PublicTaskID: strings.TrimSpace(c.Param("task_id")),
		// 只转发 Range：面板请求里的登录凭证不能带给上游。
		Header: http.Header{"Range": c.Request.Header.Values("Range")},
		Method: c.Request.Method,
	})
	if err != nil {
		videoTaskServiceError(c, err)
		return
	}
	writeVideoTaskContentStream(c, stream)
}

func (h *TaskLogHandler) AdminVideoContent(c *gin.Context) {
	stream, err := h.videos.AdminContent(c.Request.Context(), service.VideoTaskContentParams{
		PublicTaskID: strings.TrimSpace(c.Param("task_id")), Header: http.Header{"Range": c.Request.Header.Values("Range")}, Method: c.Request.Method,
	})
	if err != nil {
		videoTaskServiceError(c, err)
		return
	}
	writeVideoTaskContentStream(c, stream)
}

func (h *TaskLogHandler) ImageContent(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	h.imageContent(c, subject.UserID)
}
func (h *TaskLogHandler) AdminImageContent(c *gin.Context) { h.imageContent(c, 0) }
func (h *TaskLogHandler) imageContent(c *gin.Context, userID int64) {
	index, err := strconv.Atoi(c.DefaultQuery("index", "0"))
	if err != nil || index < 0 {
		response.BadRequest(c, "invalid image index")
		return
	}
	data, contentType, err := h.images.Content(c.Request.Context(), userID, strings.TrimSpace(c.Param("task_id")), index)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, contentType, data)
}

// BatchImageDownload 打包下载本人的批量图片结果。
// GET /api/v1/task-logs/batch-image/:batch_id/download
func (h *TaskLogHandler) BatchImageDownload(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	h.batchImageDownload(c, subject.UserID)
}

// AdminBatchImageDownload 打包下载任意用户的批量图片结果。
// GET /api/v1/admin/task-logs/batch-image/:batch_id/download
func (h *TaskLogHandler) AdminBatchImageDownload(c *gin.Context) {
	h.batchImageDownload(c, 0)
}

// userID 为 0 表示不限用户（管理端）。
func (h *TaskLogHandler) batchImageDownload(c *gin.Context, userID int64) {
	batchID := c.Param("batch_id")
	job, err := h.batches.GetBatchImageJobByBatchID(c.Request.Context(), batchID)
	if err != nil {
		batchImageError(c, err)
		return
	}
	// 别人的任务、已被用户删除的任务一律报「不存在」，不泄露任务是否存在。
	if job.APIKeyID == nil || (userID > 0 && (job.UserID != userID || job.UserDeletedAt != nil)) {
		batchImageError(c, service.ErrBatchImageJobNotFound)
		return
	}

	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", service.BatchImageContentDispositionAttachment(batchID+".zip"))
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	owner := service.BatchImageOwner{UserID: job.UserID, APIKeyID: *job.APIKeyID}
	result, err := h.download.StreamZip(c.Request.Context(), owner, batchID, service.BatchImageZipOptions{IncludeManifest: true}, c.Writer)
	if err != nil && (result == nil || !c.Writer.Written()) {
		batchImageError(c, err)
	}
}
