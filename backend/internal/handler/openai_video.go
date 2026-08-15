package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type videoTaskService interface {
	Create(ctx context.Context, params service.VideoTaskCreateParams) (*service.VideoTaskCreateResult, error)
	Fetch(ctx context.Context, params service.VideoTaskFetchParams) (*service.VideoTaskFetchResult, error)
	Content(ctx context.Context, params service.VideoTaskContentParams) (*service.VideoContentStream, error)
	Refresh(ctx context.Context, params service.VideoTaskActionParams) (*service.VideoTaskFetchResult, error)
	Cancel(ctx context.Context, params service.VideoTaskActionParams) (*service.VideoTaskFetchResult, error)
	Delete(ctx context.Context, params service.VideoTaskActionParams) (*service.VideoTaskFetchResult, error)
	List(ctx context.Context, params service.VideoTaskListParams) (*service.VideoTaskListResult, error)
	Estimate(ctx context.Context, params service.VideoTaskEstimateParams) (*service.VideoTaskEstimateResult, error)
	References(ctx context.Context, params service.VideoTaskAssetParams) (*service.VideoTaskAssetResult, error)
	MaterialAssets(ctx context.Context, params service.VideoTaskAssetParams) (*service.VideoTaskAssetResult, error)
}

// VideoTaskHandler handles OpenAI-compatible video task endpoints.
type VideoTaskHandler struct {
	videoTaskService         videoTaskService
	securityAuditCoordinator *securityaudit.Coordinator
}

func NewVideoTaskHandler(videoTaskService *service.VideoTaskService) *VideoTaskHandler {
	h := &VideoTaskHandler{}
	if videoTaskService != nil {
		h.videoTaskService = videoTaskService
	}
	return h
}

func ProvideVideoTaskHandler(videoTaskService *service.VideoTaskService, coordinator *securityaudit.Coordinator) *VideoTaskHandler {
	h := NewVideoTaskHandler(videoTaskService)
	h.securityAuditCoordinator = coordinator
	return h
}

func (h *VideoTaskHandler) Create(c *gin.Context) {
	apiKey, _, ok := videoTaskAuthContext(c)
	if !ok {
		return
	}
	svc, ok := h.videoTaskServiceForRequest(c)
	if !ok {
		return
	}

	body, ok := readVideoTaskBody(c)
	if !ok {
		return
	}
	h.createWithBody(c, svc, apiKey, body, service.VideoTaskEndpointVideos, false)
}

func (h *VideoTaskHandler) CreateGenerationsCompat(c *gin.Context) {
	apiKey, _, ok := videoTaskAuthContext(c)
	if !ok {
		return
	}
	svc, ok := h.videoTaskServiceForRequest(c)
	if !ok {
		return
	}

	body, ok := readVideoTaskBody(c)
	if !ok {
		return
	}

	h.createWithBody(c, svc, apiKey, body, service.VideoTaskEndpointVideoGenerations, true)
}

func (h *VideoTaskHandler) createWithBody(c *gin.Context, svc videoTaskService, apiKey *service.APIKey, body []byte, endpoint string, unified bool) {
	if !h.checkSecurityAuditBeforeSubmit(c, apiKey, body) {
		return
	}
	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if endpoint == service.VideoTaskEndpointVideoGenerations {
		idempotencyKey = strings.TrimSpace(c.GetHeader("X-Request-ID"))
	} else if idempotencyKey == "" {
		idempotencyKey = strings.TrimSpace(c.GetHeader("X-Request-ID"))
	}
	result, err := svc.Create(c.Request.Context(), service.VideoTaskCreateParams{
		APIKey:         apiKey,
		User:           apiKey.User,
		Subscription:   subscription,
		Body:           body,
		ContentType:    c.GetHeader("Content-Type"),
		UserAgent:      c.GetHeader("User-Agent"),
		IPAddress:      ip.GetClientIP(c),
		IdempotencyKey: idempotencyKey,
		Endpoint:       endpoint,
	})
	if err != nil {
		requestLogger(c, "handler.video_task.create").Error("video_task.create_failed", zap.String("endpoint", endpoint), zap.Error(err))
		videoTaskServiceError(c, err)
		return
	}
	if result == nil {
		videoTaskErrorResponse(c, http.StatusInternalServerError, "server_error", "Video task service returned empty response")
		return
	}
	if unified {
		if result.Task == nil {
			videoTaskErrorResponse(c, http.StatusInternalServerError, "server_error", "Video task service returned empty task")
			return
		}
		videoTaskControlPlaneResponse(c, http.StatusAccepted, "accepted", videoTaskPublicData(result.Task))
		return
	}
	videoTaskRawJSON(c, http.StatusOK, result.ResponseBody)
}

func (h *VideoTaskHandler) checkSecurityAuditBeforeSubmit(c *gin.Context, apiKey *service.APIKey, body []byte) bool {
	if h == nil || h.securityAuditCoordinator == nil {
		return true
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		videoTaskErrorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return false
	}
	if len(body) == 0 {
		c.Set(securityAuditCompletedContextKey, true)
		return true
	}
	model := videoTaskAuditModel(body)
	reqLog := requestLogger(c, "handler.video_task.security_audit",
		zap.Int64("user_id", subject.UserID), zap.String("model", model))
	if apiKey != nil {
		reqLog = reqLog.With(zap.Int64("api_key_id", apiKey.ID))
	}
	decision := runSecurityAudit(c, reqLog, h.securityAuditCoordinator, nil, apiKey, subject, service.ContentModerationProtocolOpenAIImages, model, body, "http")
	if decision != nil && !decision.AllowNextStage {
		videoTaskSecurityAuditError(c, decision)
		return false
	}
	return true
}

func videoTaskAuditModel(body []byte) string {
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Model)
}

func videoTaskSecurityAuditError(c *gin.Context, decision *securityaudit.Decision) {
	if decision == nil {
		return
	}
	errType := "api_error"
	if decision.Kind == securityaudit.DecisionBlock {
		errType = "permission_error"
	}
	videoTaskErrorResponse(c, securityAuditStatus(decision), errType, securityAuditMessage(decision))
}

func (h *VideoTaskHandler) Fetch(c *gin.Context) {
	_, subject, ok := videoTaskAuthContext(c)
	if !ok {
		return
	}
	svc, ok := h.videoTaskServiceForRequest(c)
	if !ok {
		return
	}

	taskID := strings.TrimSpace(c.Param("task_id"))
	if taskID == "" {
		taskID = strings.TrimSpace(c.Param("request_id"))
	}
	if taskID == "" {
		videoTaskErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "task_id is required")
		return
	}

	result, err := svc.Fetch(c.Request.Context(), service.VideoTaskFetchParams{
		UserID:       subject.UserID,
		PublicTaskID: taskID,
	})
	if err != nil {
		videoTaskServiceError(c, err)
		return
	}
	if result == nil {
		videoTaskErrorResponse(c, http.StatusInternalServerError, "server_error", "Video task service returned empty response")
		return
	}
	if isUnifiedVideoGenerationsRequest(c) {
		if result.Task == nil {
			videoTaskErrorResponse(c, http.StatusInternalServerError, "server_error", "Video task service returned empty task")
			return
		}
		videoTaskControlPlaneResponse(c, http.StatusOK, "success", videoTaskPublicData(result.Task))
		return
	}
	videoTaskRawJSON(c, http.StatusOK, result.ResponseBody)
}

func (h *VideoTaskHandler) Refresh(c *gin.Context) {
	h.taskAction(c, false, func(svc videoTaskService, ctx context.Context, params service.VideoTaskActionParams) (*service.VideoTaskFetchResult, error) {
		return svc.Refresh(ctx, params)
	})
}

func (h *VideoTaskHandler) Cancel(c *gin.Context) {
	h.taskAction(c, false, func(svc videoTaskService, ctx context.Context, params service.VideoTaskActionParams) (*service.VideoTaskFetchResult, error) {
		return svc.Cancel(ctx, params)
	})
}

func (h *VideoTaskHandler) Delete(c *gin.Context) {
	h.taskAction(c, true, func(svc videoTaskService, ctx context.Context, params service.VideoTaskActionParams) (*service.VideoTaskFetchResult, error) {
		return svc.Delete(ctx, params)
	})
}

func (h *VideoTaskHandler) taskAction(c *gin.Context, deleted bool, fn func(videoTaskService, context.Context, service.VideoTaskActionParams) (*service.VideoTaskFetchResult, error)) {
	_, subject, ok := videoTaskAuthContext(c)
	if !ok {
		return
	}
	svc, ok := h.videoTaskServiceForRequest(c)
	if !ok {
		return
	}
	taskID := strings.TrimSpace(c.Param("task_id"))
	if taskID == "" {
		taskID = strings.TrimSpace(c.Param("request_id"))
	}
	if taskID == "" {
		videoTaskErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "task_id is required")
		return
	}
	result, err := fn(svc, c.Request.Context(), service.VideoTaskActionParams{UserID: subject.UserID, PublicTaskID: taskID, IdempotencyKey: strings.TrimSpace(c.GetHeader("Idempotency-Key"))})
	if err != nil {
		videoTaskServiceError(c, err)
		return
	}
	if result == nil {
		videoTaskErrorResponse(c, http.StatusInternalServerError, "server_error", "Video task service returned empty response")
		return
	}
	if isUnifiedVideoGenerationsRequest(c) {
		if result.Task == nil {
			videoTaskErrorResponse(c, http.StatusInternalServerError, "server_error", "Video task service returned empty task")
			return
		}
		data := videoTaskPublicData(result.Task)
		if deleted {
			data["deleted"] = true
		}
		videoTaskControlPlaneResponse(c, http.StatusOK, "success", data)
		return
	}
	videoTaskRawJSON(c, http.StatusOK, result.ResponseBody)
}

func (h *VideoTaskHandler) List(c *gin.Context) {
	_, subject, ok := videoTaskAuthContext(c)
	if !ok {
		return
	}
	svc, ok := h.videoTaskServiceForRequest(c)
	if !ok {
		return
	}
	limitText := strings.TrimSpace(c.Query("limit"))
	limit := 0
	if limitText != "" {
		parsedLimit, err := strconv.Atoi(limitText)
		if err != nil {
			videoTaskErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "limit must be an integer")
			return
		}
		limit = parsedLimit
	}
	result, err := svc.List(c.Request.Context(), service.VideoTaskListParams{UserID: subject.UserID, Status: strings.TrimSpace(c.Query("status")), Model: strings.TrimSpace(c.Query("model")), Limit: limit})
	if err != nil {
		videoTaskServiceError(c, err)
		return
	}
	if result == nil {
		videoTaskErrorResponse(c, http.StatusInternalServerError, "server_error", "Video task service returned empty response")
		return
	}
	if isUnifiedVideoGenerationsRequest(c) {
		items := make([]gin.H, 0, len(result.Tasks))
		for _, task := range result.Tasks {
			if task == nil {
				videoTaskErrorResponse(c, http.StatusInternalServerError, "server_error", "Video task service returned empty task")
				return
			}
			items = append(items, videoTaskPublicData(task))
		}
		videoTaskControlPlaneResponse(c, http.StatusOK, "success", gin.H{"object": "list", "data": items, "has_more": result.HasMore})
		return
	}
	videoTaskRawJSON(c, http.StatusOK, result.ResponseBody)
}

func (h *VideoTaskHandler) Estimate(c *gin.Context) {
	h.bodyAction(c, service.VideoTaskEndpointVideos)
}

func (h *VideoTaskHandler) EstimateGenerationsCompat(c *gin.Context) {
	h.bodyAction(c, service.VideoTaskEndpointVideoGenerations)
}

func (h *VideoTaskHandler) bodyAction(c *gin.Context, endpoint string) {
	apiKey, _, ok := videoTaskAuthContext(c)
	if !ok {
		return
	}
	svc, ok := h.videoTaskServiceForRequest(c)
	if !ok {
		return
	}
	body, ok := readVideoTaskBody(c)
	if !ok {
		return
	}
	result, err := svc.Estimate(c.Request.Context(), service.VideoTaskEstimateParams{APIKey: apiKey, User: apiKey.User, Body: body, ContentType: c.GetHeader("Content-Type"), Endpoint: endpoint})
	if err != nil {
		videoTaskServiceError(c, err)
		return
	}
	if result == nil {
		videoTaskErrorResponse(c, http.StatusInternalServerError, "server_error", "Video task service returned empty response")
		return
	}
	if endpoint == service.VideoTaskEndpointVideoGenerations {
		data, err := videoTaskPublicEstimateData(result.ResponseBody)
		if err != nil {
			videoTaskErrorResponse(c, http.StatusInternalServerError, "server_error", "Video task service returned invalid estimate response")
			return
		}
		videoTaskControlPlaneResponse(c, http.StatusOK, "success", data)
		return
	}
	videoTaskRawJSON(c, http.StatusOK, result.ResponseBody)
}

func (h *VideoTaskHandler) References(c *gin.Context) {
	h.assetAction(c, service.VideoTaskEndpointVideos, func(svc videoTaskService, ctx context.Context, params service.VideoTaskAssetParams) (*service.VideoTaskAssetResult, error) {
		return svc.References(ctx, params)
	})
}

func (h *VideoTaskHandler) ReferencesGenerationsCompat(c *gin.Context) {
	h.assetAction(c, service.VideoTaskEndpointVideoGenerations, func(svc videoTaskService, ctx context.Context, params service.VideoTaskAssetParams) (*service.VideoTaskAssetResult, error) {
		return svc.References(ctx, params)
	})
}

func (h *VideoTaskHandler) MaterialAssets(c *gin.Context) {
	h.assetAction(c, service.VideoTaskEndpointVideos, func(svc videoTaskService, ctx context.Context, params service.VideoTaskAssetParams) (*service.VideoTaskAssetResult, error) {
		return svc.MaterialAssets(ctx, params)
	})
}

func (h *VideoTaskHandler) MaterialAssetsGenerationsCompat(c *gin.Context) {
	h.assetAction(c, service.VideoTaskEndpointVideoGenerations, func(svc videoTaskService, ctx context.Context, params service.VideoTaskAssetParams) (*service.VideoTaskAssetResult, error) {
		return svc.MaterialAssets(ctx, params)
	})
}

func (h *VideoTaskHandler) assetAction(c *gin.Context, endpoint string, fn func(videoTaskService, context.Context, service.VideoTaskAssetParams) (*service.VideoTaskAssetResult, error)) {
	apiKey, _, ok := videoTaskAuthContext(c)
	if !ok {
		return
	}
	svc, ok := h.videoTaskServiceForRequest(c)
	if !ok {
		return
	}
	body, ok := readVideoTaskBody(c)
	if !ok {
		return
	}
	result, err := fn(svc, c.Request.Context(), service.VideoTaskAssetParams{APIKey: apiKey, User: apiKey.User, Body: body, ContentType: c.GetHeader("Content-Type"), Endpoint: endpoint, IdempotencyKey: strings.TrimSpace(c.GetHeader("Idempotency-Key"))})
	if err != nil {
		videoTaskServiceError(c, err)
		return
	}
	if result == nil {
		videoTaskErrorResponse(c, http.StatusInternalServerError, "server_error", "Video task service returned empty response")
		return
	}
	videoTaskRawJSON(c, http.StatusOK, result.ResponseBody)
}

func (h *VideoTaskHandler) Content(c *gin.Context) {
	_, subject, ok := videoTaskAuthContext(c)
	if !ok {
		return
	}
	svc, ok := h.videoTaskServiceForRequest(c)
	if !ok {
		return
	}

	taskID := strings.TrimSpace(c.Param("task_id"))
	if taskID == "" {
		taskID = strings.TrimSpace(c.Param("request_id"))
	}
	if taskID == "" {
		videoTaskErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "task_id is required")
		return
	}

	stream, err := svc.Content(c.Request.Context(), service.VideoTaskContentParams{
		UserID:       subject.UserID,
		PublicTaskID: taskID,
		Header:       c.Request.Header.Clone(),
		Method:       c.Request.Method,
	})
	if err != nil {
		videoTaskServiceError(c, err)
		return
	}
	if stream == nil || stream.Body == nil {
		videoTaskErrorResponse(c, http.StatusInternalServerError, "server_error", "Video task service returned empty stream")
		return
	}
	defer func() { _ = stream.Body.Close() }()

	status := stream.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	copyVideoTaskContentHeaders(c, stream)
	c.Status(status)
	if c.Request.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(c.Writer, stream.Body); err != nil {
		_ = c.Error(err)
	}
}

func (h *VideoTaskHandler) videoTaskServiceForRequest(c *gin.Context) (videoTaskService, bool) {
	if h == nil || h.videoTaskService == nil {
		videoTaskErrorResponse(c, http.StatusInternalServerError, "server_error", "Video task service is not configured")
		return nil, false
	}
	if svc, ok := h.videoTaskService.(*service.VideoTaskService); ok && svc == nil {
		videoTaskErrorResponse(c, http.StatusInternalServerError, "server_error", "Video task service is not configured")
		return nil, false
	}
	return h.videoTaskService, true
}

func readVideoTaskBody(c *gin.Context) ([]byte, bool) {
	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		if maxErr, ok := extractMaxBytesError(err); ok {
			videoTaskErrorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
			return nil, false
		}
		videoTaskErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return nil, false
	}
	if len(body) == 0 {
		videoTaskErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return nil, false
	}
	return body, true
}

func videoTaskAuthContext(c *gin.Context) (*service.APIKey, middleware2.AuthSubject, bool) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		videoTaskErrorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return nil, middleware2.AuthSubject{}, false
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		videoTaskErrorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return nil, middleware2.AuthSubject{}, false
	}
	return apiKey, subject, true
}

func videoTaskRawJSON(c *gin.Context, status int, body []byte) {
	c.Data(status, "application/json", body)
}

func videoTaskControlPlaneResponse(c *gin.Context, status int, message string, data any) {
	c.JSON(status, gin.H{"code": 0, "message": message, "data": data})
}

func videoTaskPublicData(task *service.VideoTask) gin.H {
	if task == nil {
		return nil
	}
	status := string(task.Status)
	if status == "" {
		status = string(service.VideoTaskStatusUnknown)
	}
	progress := any(0)
	if task.Status.Terminal() {
		progress = 100
	}
	if value, ok := task.Metadata["progress"]; ok {
		progress = value
	}
	data := gin.H{
		"id":       task.PublicTaskID,
		"task_id":  task.PublicTaskID,
		"object":   "video",
		"status":   status,
		"model":    task.Model,
		"progress": progress,
	}
	if value, ok := videoTaskPublicMetadataValue(task.Metadata, "duration", "seconds", "duration_seconds"); ok {
		data["duration"] = value
	}
	if value, ok := videoTaskPublicMetadataValue(task.Metadata, "ratio", "aspect_ratio"); ok {
		data["ratio"] = value
	}
	if value, ok := videoTaskPublicMetadataValue(task.Metadata, "resolution"); ok {
		data["resolution"] = value
	}
	if resultURL := strings.TrimSpace(videoTaskPublicString(task.Metadata, "result_url")); resultURL != "" {
		data["result_url"] = resultURL
	}
	if errorData := videoTaskPublicError(task); errorData != nil {
		data["error"] = errorData
	}
	for key, value := range videoTaskPublicPricing(task.Metadata) {
		data[key] = value
	}
	if task.BilledUSD != 0 {
		data["billed_usd"] = task.BilledUSD
	}
	return data
}

func videoTaskPublicMetadataValue(metadata map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		if value, ok := metadata[key]; ok && value != nil {
			return value, true
		}
	}
	return nil, false
}

func videoTaskPublicString(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return value
}

func videoTaskPublicError(task *service.VideoTask) gin.H {
	if task == nil {
		return nil
	}
	message := strings.TrimSpace(task.ErrorMessage)
	if message == "" {
		message = strings.TrimSpace(videoTaskPublicString(task.Metadata, "error_message"))
	}
	code := strings.TrimSpace(videoTaskPublicString(task.Metadata, "error_code"))
	if message == "" && code == "" && task.Status != service.VideoTaskStatusFailed && task.Status != service.VideoTaskStatusExpired {
		return nil
	}
	return gin.H{
		"code":    "VIDEO_GENERATION_FAILED",
		"message": "Video generation failed",
	}
}

func videoTaskPublicPricing(metadata map[string]any) gin.H {
	requestMetadata, ok := videoTaskPublicMap(metadata["request_metadata"])
	if !ok {
		return nil
	}
	snapshot, ok := videoTaskPublicMap(requestMetadata["video_pricing_snapshot"])
	if !ok {
		return nil
	}
	pricing := gin.H{}
	for _, key := range []string{"billing_mode", "billing_model", "effective", "unit_price_usd", "gross_cost_usd", "actual_cost_usd", "rate_multiplier"} {
		if value, ok := snapshot[key]; ok {
			pricing[key] = value
		}
	}
	return pricing
}

func videoTaskPublicMap(value any) (map[string]any, bool) {
	if values, ok := value.(map[string]any); ok {
		return values, true
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return nil, false
	}
	return values, true
}

func videoTaskPublicEstimateData(body []byte) (map[string]any, error) {
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil || data == nil {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("video estimate response must be an object")
	}
	delete(data, "adapter")
	delete(data, "endpoint")
	delete(data, "upstream_model")
	return data, nil
}

func isUnifiedVideoGenerationsRequest(c *gin.Context) bool {
	if c == nil {
		return false
	}
	path := c.FullPath()
	if path == "" && c.Request != nil && c.Request.URL != nil {
		path = c.Request.URL.Path
	}
	path = strings.TrimPrefix(path, "/v1")
	return path == "/video/generations" || strings.HasPrefix(path, "/video/generations/")
}

func videoTaskServiceError(c *gin.Context, err error) {
	var upstreamErr *service.OpenAIVideoUpstreamError
	if errors.As(err, &upstreamErr) {
		status, errType := videoTaskUpstreamErrorStatus(upstreamErr.StatusCode)
		videoTaskErrorResponse(c, status, errType, videoTaskUpstreamErrorMessage(upstreamErr))
		return
	}
	switch {
	case errors.Is(err, service.ErrVideoTaskNotFound):
		videoTaskErrorResponse(c, http.StatusNotFound, "not_found_error", videoTaskErrorMessage(err))
	case errors.Is(err, service.ErrVideoGenerationPermissionDenied):
		videoTaskErrorResponse(c, http.StatusForbidden, "permission_error", videoTaskErrorMessage(err))
	case errors.Is(err, service.ErrVideoTaskIdempotencyConflict):
		videoTaskErrorResponse(c, http.StatusConflict, "idempotency_error", videoTaskErrorMessage(err))
	case errors.Is(err, service.ErrVideoTaskNotCompleted):
		videoTaskErrorResponse(c, http.StatusConflict, "invalid_request_error", videoTaskErrorMessage(err))
	case errors.Is(err, service.ErrVideoTaskDeleteNotReady):
		videoTaskErrorResponse(c, http.StatusConflict, "invalid_request_error", videoTaskErrorMessage(err))
	case errors.Is(err, service.ErrNoAvailableAccounts):
		videoTaskErrorResponse(c, http.StatusServiceUnavailable, "server_error", videoTaskErrorMessage(err))
	case errors.Is(err, service.ErrVideoTaskAccountUnavailable):
		videoTaskErrorResponse(c, http.StatusServiceUnavailable, "server_error", videoTaskErrorMessage(err))
	case errors.Is(err, service.ErrVideoTaskActionUnsupported):
		videoTaskErrorResponse(c, http.StatusBadRequest, "invalid_request_error", videoTaskErrorMessage(err))
	case isVideoTaskBadRequestError(err):
		videoTaskErrorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
	default:
		videoTaskErrorResponse(c, http.StatusInternalServerError, "server_error", "Internal server error")
	}
}

func videoTaskUpstreamErrorStatus(upstreamStatus int) (int, string) {
	switch upstreamStatus {
	case http.StatusUnauthorized:
		return http.StatusUnauthorized, "authentication_error"
	case http.StatusForbidden:
		return http.StatusForbidden, "permission_error"
	case http.StatusTooManyRequests:
		return http.StatusTooManyRequests, "rate_limit_error"
	case http.StatusPaymentRequired:
		return http.StatusBadGateway, "server_error"
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return upstreamStatus, "invalid_request_error"
	default:
		if upstreamStatus >= http.StatusBadRequest && upstreamStatus < http.StatusInternalServerError {
			return upstreamStatus, "invalid_request_error"
		}
		return http.StatusBadGateway, "server_error"
	}
}

func videoTaskUpstreamErrorMessage(err *service.OpenAIVideoUpstreamError) string {
	if err == nil {
		return "Video generation request failed"
	}
	if message := safeVideoTaskUpstreamMessage(err.Body); message != "" {
		return message
	}
	switch err.StatusCode {
	case http.StatusUnauthorized:
		return "Video generation authentication failed"
	case http.StatusForbidden:
		return "Video generation request was rejected"
	case http.StatusTooManyRequests:
		return "Video generation is temporarily rate limited"
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return "Video generation request is invalid"
	default:
		return "Video generation service is temporarily unavailable"
	}
}

var videoTaskUpstreamURLPattern = regexp.MustCompile(`(?i)https?://\S+`)

func safeVideoTaskUpstreamMessage(body string) string {
	message, markers := videoTaskUpstreamMessage(body)
	if message == "" || containsBlockedVideoTaskUpstreamDetail(message) {
		return ""
	}
	for _, marker := range markers {
		if containsBlockedVideoTaskUpstreamDetail(marker) {
			return ""
		}
	}
	return message
}

func videoTaskUpstreamMessage(body string) (string, []string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return body, nil
	}
	markers := videoTaskUpstreamMarkers(payload)
	if message, ok := payload["message"].(string); ok {
		return strings.TrimSpace(message), markers
	}
	if errorValue, ok := payload["error"].(map[string]any); ok {
		if message, ok := errorValue["message"].(string); ok {
			return strings.TrimSpace(message), markers
		}
	}
	if message, ok := payload["error"].(string); ok {
		return strings.TrimSpace(message), markers
	}
	return "", markers
}

func videoTaskUpstreamMarkers(payload map[string]any) []string {
	markers := make([]string, 0, 2)
	if code, ok := payload["code"].(string); ok {
		markers = append(markers, code)
	}
	if errorValue, ok := payload["error"].(map[string]any); ok {
		if code, ok := errorValue["code"].(string); ok {
			markers = append(markers, code)
		}
	}
	return markers
}

func containsBlockedVideoTaskUpstreamDetail(message string) bool {
	lower := strings.ToLower(message)
	for _, token := range []string{"欠费", "余额", "额度", "insufficient balance", "insufficient_quota", "quota", "balance"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return videoTaskUpstreamURLPattern.MatchString(message)
}

func videoTaskErrorMessage(err error) string {
	message := strings.TrimSpace(infraerrors.Message(err))
	if message == "" || message == infraerrors.UnknownMessage {
		return err.Error()
	}
	return message
}

func isVideoTaskBadRequestError(err error) bool {
	if err == nil {
		return false
	}
	if infraerrors.IsBadRequest(err) {
		return true
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return true
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return true
	}
	message := strings.TrimSpace(err.Error())
	if strings.HasPrefix(message, "invalid jimeng OpenAI video create JSON:") {
		return true
	}
	switch message {
	case "model is required", "model must be video-ds-2.0-fast or video-ds-2.0", "prompt is required", "unexpected end of JSON input", "video create JSON body must be an object":
		return true
	default:
		return strings.HasSuffix(message, " is not supported by /v1/videos") ||
			strings.HasSuffix(message, " must be a number or numeric string")
	}
}

func videoTaskErrorResponse(c *gin.Context, status int, errType, message string) {
	c.JSON(status, gin.H{
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
}

func copyVideoTaskContentHeaders(c *gin.Context, stream *service.VideoContentStream) {
	for key, value := range stream.Headers {
		if shouldSkipVideoTaskResponseHeader(key) || strings.TrimSpace(value) == "" {
			continue
		}
		c.Header(http.CanonicalHeaderKey(key), value)
	}
	if stream.ContentType != "" {
		c.Header("Content-Type", stream.ContentType)
	}
	if stream.ContentLength > 0 {
		c.Header("Content-Length", strconv.FormatInt(stream.ContentLength, 10))
	}
}

func shouldSkipVideoTaskResponseHeader(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "", "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	case "authorization", "cookie", "set-cookie", "x-api-key":
		return true
	default:
		return false
	}
}
