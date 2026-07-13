package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type videoTaskService interface {
	Create(ctx context.Context, params service.VideoTaskCreateParams) (*service.VideoTaskCreateResult, error)
	Fetch(ctx context.Context, params service.VideoTaskFetchParams) (*service.VideoTaskFetchResult, error)
	Content(ctx context.Context, params service.VideoTaskContentParams) (*service.VideoContentStream, error)
	Refresh(ctx context.Context, params service.VideoTaskActionParams) (*service.VideoTaskFetchResult, error)
	Cancel(ctx context.Context, params service.VideoTaskActionParams) (*service.VideoTaskFetchResult, error)
	List(ctx context.Context, params service.VideoTaskListParams) (*service.VideoTaskListResult, error)
	Estimate(ctx context.Context, params service.VideoTaskEstimateParams) (*service.VideoTaskEstimateResult, error)
	References(ctx context.Context, params service.VideoTaskAssetParams) (*service.VideoTaskAssetResult, error)
	MaterialAssets(ctx context.Context, params service.VideoTaskAssetParams) (*service.VideoTaskAssetResult, error)
}

// VideoTaskHandler handles OpenAI-compatible video task endpoints.
type VideoTaskHandler struct {
	videoTaskService videoTaskService
}

func NewVideoTaskHandler(videoTaskService *service.VideoTaskService) *VideoTaskHandler {
	h := &VideoTaskHandler{}
	if videoTaskService != nil {
		h.videoTaskService = videoTaskService
	}
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
	h.createWithBody(c, svc, apiKey, body, service.VideoTaskEndpointVideos)
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

	h.createWithBody(c, svc, apiKey, body, service.VideoTaskEndpointVideoGenerations)
}

func (h *VideoTaskHandler) createWithBody(c *gin.Context, svc videoTaskService, apiKey *service.APIKey, body []byte, endpoint string) {
	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if idempotencyKey == "" {
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
		videoTaskServiceError(c, err)
		return
	}
	if result == nil {
		videoTaskErrorResponse(c, http.StatusInternalServerError, "server_error", "Video task service returned empty response")
		return
	}
	videoTaskRawJSON(c, http.StatusOK, result.ResponseBody)
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
	videoTaskRawJSON(c, http.StatusOK, result.ResponseBody)
}

func (h *VideoTaskHandler) Refresh(c *gin.Context) {
	h.taskAction(c, func(svc videoTaskService, ctx context.Context, params service.VideoTaskActionParams) (*service.VideoTaskFetchResult, error) {
		return svc.Refresh(ctx, params)
	})
}

func (h *VideoTaskHandler) Cancel(c *gin.Context) {
	h.taskAction(c, func(svc videoTaskService, ctx context.Context, params service.VideoTaskActionParams) (*service.VideoTaskFetchResult, error) {
		return svc.Cancel(ctx, params)
	})
}

func (h *VideoTaskHandler) taskAction(c *gin.Context, fn func(videoTaskService, context.Context, service.VideoTaskActionParams) (*service.VideoTaskFetchResult, error)) {
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
	case errors.Is(err, service.ErrNoAvailableAccounts):
		videoTaskErrorResponse(c, http.StatusServiceUnavailable, "server_error", videoTaskErrorMessage(err))
	case errors.Is(err, service.ErrVideoTaskAccountUnavailable):
		videoTaskErrorResponse(c, http.StatusServiceUnavailable, "server_error", videoTaskErrorMessage(err))
	case errors.Is(err, service.ErrVideoTaskActionUnsupported):
		videoTaskErrorResponse(c, http.StatusNotImplemented, "not_supported_error", videoTaskErrorMessage(err))
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
		return "Upstream video request failed"
	}
	if body := strings.TrimSpace(err.Body); body != "" {
		return body
	}
	return fmt.Sprintf("Upstream video request failed with status %d", err.StatusCode)
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
