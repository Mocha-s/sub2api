package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type fakeVideoTaskService struct {
	createParams         service.VideoTaskCreateParams
	createResult         *service.VideoTaskCreateResult
	createErr            error
	fetchParams          service.VideoTaskFetchParams
	fetchResult          *service.VideoTaskFetchResult
	fetchErr             error
	contentParams        service.VideoTaskContentParams
	contentResult        *service.VideoContentStream
	contentErr           error
	refreshParams        service.VideoTaskActionParams
	refreshResult        *service.VideoTaskFetchResult
	refreshErr           error
	cancelParams         service.VideoTaskActionParams
	cancelResult         *service.VideoTaskFetchResult
	cancelErr            error
	deleteParams         service.VideoTaskActionParams
	deleteResult         *service.VideoTaskFetchResult
	deleteErr            error
	listParams           service.VideoTaskListParams
	listResult           *service.VideoTaskListResult
	listErr              error
	estimateParams       service.VideoTaskEstimateParams
	estimateResult       *service.VideoTaskEstimateResult
	estimateErr          error
	referencesParams     service.VideoTaskAssetParams
	referencesResult     *service.VideoTaskAssetResult
	referencesErr        error
	materialAssetsParams service.VideoTaskAssetParams
	materialAssetsResult *service.VideoTaskAssetResult
	materialAssetsErr    error
}

func (s *fakeVideoTaskService) Create(_ context.Context, params service.VideoTaskCreateParams) (*service.VideoTaskCreateResult, error) {
	s.createParams = params
	return s.createResult, s.createErr
}

func (s *fakeVideoTaskService) Fetch(_ context.Context, params service.VideoTaskFetchParams) (*service.VideoTaskFetchResult, error) {
	s.fetchParams = params
	return s.fetchResult, s.fetchErr
}

func (s *fakeVideoTaskService) Content(_ context.Context, params service.VideoTaskContentParams) (*service.VideoContentStream, error) {
	s.contentParams = params
	return s.contentResult, s.contentErr
}

func (s *fakeVideoTaskService) Refresh(_ context.Context, params service.VideoTaskActionParams) (*service.VideoTaskFetchResult, error) {
	s.refreshParams = params
	return s.refreshResult, s.refreshErr
}

func (s *fakeVideoTaskService) Cancel(_ context.Context, params service.VideoTaskActionParams) (*service.VideoTaskFetchResult, error) {
	s.cancelParams = params
	return s.cancelResult, s.cancelErr
}

func (s *fakeVideoTaskService) Delete(_ context.Context, params service.VideoTaskActionParams) (*service.VideoTaskFetchResult, error) {
	s.deleteParams = params
	return s.deleteResult, s.deleteErr
}

func (s *fakeVideoTaskService) List(_ context.Context, params service.VideoTaskListParams) (*service.VideoTaskListResult, error) {
	s.listParams = params
	return s.listResult, s.listErr
}

func (s *fakeVideoTaskService) Estimate(_ context.Context, params service.VideoTaskEstimateParams) (*service.VideoTaskEstimateResult, error) {
	s.estimateParams = params
	return s.estimateResult, s.estimateErr
}

func (s *fakeVideoTaskService) References(_ context.Context, params service.VideoTaskAssetParams) (*service.VideoTaskAssetResult, error) {
	s.referencesParams = params
	return s.referencesResult, s.referencesErr
}

func (s *fakeVideoTaskService) MaterialAssets(_ context.Context, params service.VideoTaskAssetParams) (*service.VideoTaskAssetResult, error) {
	s.materialAssetsParams = params
	return s.materialAssetsResult, s.materialAssetsErr
}

type closeTrackingReader struct {
	*strings.Reader
	closed bool
}

func (r *closeTrackingReader) Close() error {
	r.closed = true
	return nil
}

func TestVideoTaskHandlerCreatePassesAuthBodyIdempotencyAndReturnsRawJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"sora","prompt":"ocean"}`)
	rec, c, apiKey, subscription := newVideoTaskTestContext(http.MethodPost, "/v1/videos", string(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("User-Agent", "video-client/1.0")
	c.Request.Header.Set("Idempotency-Key", "idem-123")
	c.Request.Header.Set("CF-Connecting-IP", "203.0.113.10")
	setVideoTaskAuthContext(c, apiKey, subscription)

	fake := &fakeVideoTaskService{
		createResult: &service.VideoTaskCreateResult{ResponseBody: []byte(`{"id":"task_123","object":"video","status":"queued"}`)},
	}
	h := &VideoTaskHandler{videoTaskService: fake}

	h.Create(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	require.JSONEq(t, `{"id":"task_123","object":"video","status":"queued"}`, rec.Body.String())
	require.Same(t, apiKey, fake.createParams.APIKey)
	require.Same(t, apiKey.User, fake.createParams.User)
	require.Same(t, subscription, fake.createParams.Subscription)
	require.Equal(t, body, fake.createParams.Body)
	require.Equal(t, "application/json", fake.createParams.ContentType)
	require.Equal(t, "video-client/1.0", fake.createParams.UserAgent)
	require.Equal(t, "203.0.113.10", fake.createParams.IPAddress)
	require.Equal(t, "idem-123", fake.createParams.IdempotencyKey)
}

func TestVideoTaskHandlerCreateUsesXRequestIDAsIdempotencyFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec, c, apiKey, subscription := newVideoTaskTestContext(http.MethodPost, "/v1/videos", `{"model":"seedance-2.0-720p","prompt":"city"}`)
	c.Request.Header.Set("X-Request-ID", "req-fallback-123")
	setVideoTaskAuthContext(c, apiKey, subscription)

	fake := &fakeVideoTaskService{
		createResult: &service.VideoTaskCreateResult{ResponseBody: []byte(`{"id":"task_123","object":"video","status":"queued"}`)},
	}
	h := &VideoTaskHandler{videoTaskService: fake}

	h.Create(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "req-fallback-123", fake.createParams.IdempotencyKey)
	require.Equal(t, service.VideoTaskEndpointVideos, fake.createParams.Endpoint)
}

func TestVideoTaskHandlerCreateGenerationsCompatReturnsAcceptedLocalTaskEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := `{"model":"seedance-2.0-720p","prompt":"city","duration":8,"aspect_ratio":"16:9","resolution":"720p","generate_audio":true}`
	rec, c, apiKey, subscription := newVideoTaskTestContext(http.MethodPost, "/v1/video/generations", body)
	c.Request.Header.Set("Content-Type", "application/json")
	setVideoTaskAuthContext(c, apiKey, subscription)

	fake := &fakeVideoTaskService{
		createResult: &service.VideoTaskCreateResult{
			Task: &service.VideoTask{
				PublicTaskID: "task_123",
				Model:        "seedance-2.0-720p",
				Status:       service.VideoTaskStatusQueued,
				Metadata: map[string]any{
					"progress":                      0,
					"duration":                      8,
					"ratio":                         "16:9",
					"resolution":                    "720p",
					"result_url":                    "https://cdn.example/video.mp4",
					"upstream_base_url":             "https://upstream.example",
					service.VideoAdapterMetadataKey: "seedance_api_v1",
				},
			},
			ResponseBody: []byte(`{"id":"upstream_task_123","adapter":"seedance_api_v1"}`),
		},
	}
	h := &VideoTaskHandler{videoTaskService: fake}

	h.CreateGenerationsCompat(c)

	require.Equal(t, http.StatusAccepted, rec.Code)
	require.JSONEq(t, `{
		"code": 0,
		"message": "accepted",
		"data": {
			"id": "task_123",
			"task_id": "task_123",
			"object": "video",
			"status": "queued",
			"model": "seedance-2.0-720p",
			"progress": 0,
			"duration": 8,
			"ratio": "16:9",
			"resolution": "720p",
			"result_url": "https://cdn.example/video.mp4"
		}
	}`, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "upstream_task_123")
	require.NotContains(t, rec.Body.String(), "upstream_base_url")
	require.NotContains(t, rec.Body.String(), "seedance_api_v1")
	require.JSONEq(t, body, string(fake.createParams.Body))
	require.Equal(t, service.VideoTaskEndpointVideoGenerations, fake.createParams.Endpoint)
}

func TestVideoTaskHandlerCreateGenerationsCompatUsesOnlyXRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec, c, apiKey, subscription := newVideoTaskTestContext(http.MethodPost, "/v1/video/generations", `{"model":"seedance-2.0","prompt":"city"}`)
	c.Request.Header.Set("Idempotency-Key", "legacy-key")
	c.Request.Header.Set("X-Request-ID", "request-key")
	setVideoTaskAuthContext(c, apiKey, subscription)

	fake := &fakeVideoTaskService{createResult: &service.VideoTaskCreateResult{Task: &service.VideoTask{
		PublicTaskID: "task_123",
		Model:        "seedance-2.0",
		Status:       service.VideoTaskStatusQueued,
	}}}
	h := &VideoTaskHandler{videoTaskService: fake}

	h.CreateGenerationsCompat(c)

	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Equal(t, "request-key", fake.createParams.IdempotencyKey)
}

func TestVideoTaskHandlerFetchReturnsRawJSONForCurrentUserTaskID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec, c, apiKey, subscription := newVideoTaskTestContext(http.MethodGet, "/v1/videos/task_123", "")
	c.Params = gin.Params{{Key: "task_id", Value: "task_123"}}
	setVideoTaskAuthContext(c, apiKey, subscription)

	fake := &fakeVideoTaskService{
		fetchResult: &service.VideoTaskFetchResult{ResponseBody: []byte(`{"id":"task_123","object":"video","status":"completed"}`)},
	}
	h := &VideoTaskHandler{videoTaskService: fake}

	h.Fetch(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	require.JSONEq(t, `{"id":"task_123","object":"video","status":"completed"}`, rec.Body.String())
	require.Equal(t, int64(42), fake.fetchParams.UserID)
	require.Equal(t, "task_123", fake.fetchParams.PublicTaskID)
}

func TestVideoTaskHandlerFetchGenerationsReturnsLocalTaskEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec, c, apiKey, subscription := newVideoTaskTestContext(http.MethodGet, "/v1/video/generations/task_123", "")
	c.Params = gin.Params{{Key: "request_id", Value: "task_123"}}
	setVideoTaskAuthContext(c, apiKey, subscription)

	fake := &fakeVideoTaskService{fetchResult: &service.VideoTaskFetchResult{
		Task: &service.VideoTask{
			PublicTaskID: "task_123",
			Model:        "seedance-2.0",
			Status:       service.VideoTaskStatusCompleted,
			Metadata: map[string]any{
				"progress":          100,
				"result_url":        "https://cdn.example/video.mp4",
				"upstream_base_url": "https://upstream.example",
			},
		},
		ResponseBody: []byte(`{"id":"upstream_task_123","status":"completed"}`),
	}}
	h := &VideoTaskHandler{videoTaskService: fake}

	h.Fetch(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{
		"code": 0,
		"message": "success",
		"data": {
			"id": "task_123",
			"task_id": "task_123",
			"object": "video",
			"status": "completed",
			"model": "seedance-2.0",
			"progress": 100,
			"result_url": "https://cdn.example/video.mp4"
		}
	}`, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "upstream_task_123")
	require.NotContains(t, rec.Body.String(), "upstream_base_url")
}

func TestVideoTaskHandlerFetchAcceptsSharedVideoRequestIDParam(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec, c, apiKey, subscription := newVideoTaskTestContext(http.MethodGet, "/v1/videos/task_123", "")
	c.Params = gin.Params{{Key: "request_id", Value: "task_123"}}
	setVideoTaskAuthContext(c, apiKey, subscription)

	fake := &fakeVideoTaskService{
		fetchResult: &service.VideoTaskFetchResult{ResponseBody: []byte(`{"id":"task_123","object":"video","status":"completed"}`)},
	}
	h := &VideoTaskHandler{videoTaskService: fake}

	h.Fetch(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(42), fake.fetchParams.UserID)
	require.Equal(t, "task_123", fake.fetchParams.PublicTaskID)
}

func TestVideoTaskHandlerRefreshGenerationsReturnsLocalTaskEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec, c, apiKey, subscription := newVideoTaskTestContext(http.MethodPost, "/v1/video/generations/task_123/refresh", "")
	c.Params = gin.Params{{Key: "request_id", Value: "task_123"}}
	setVideoTaskAuthContext(c, apiKey, subscription)
	fake := &fakeVideoTaskService{refreshResult: &service.VideoTaskFetchResult{
		Task: &service.VideoTask{
			PublicTaskID: "task_123",
			Model:        "seedance-2.0",
			Status:       service.VideoTaskStatusCompleted,
			Metadata:     map[string]any{"progress": 100},
		},
		ResponseBody: []byte(`{"id":"upstream_task_123","status":"completed"}`),
	}}
	h := &VideoTaskHandler{videoTaskService: fake}

	h.Refresh(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{
		"code": 0,
		"message": "success",
		"data": {
			"id": "task_123",
			"task_id": "task_123",
			"object": "video",
			"status": "completed",
			"model": "seedance-2.0",
			"progress": 100
		}
	}`, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "upstream_task_123")
	require.Equal(t, int64(42), fake.refreshParams.UserID)
	require.Equal(t, "task_123", fake.refreshParams.PublicTaskID)
}

func TestVideoTaskHandlerDeleteGenerationsReturnsDeletedLocalTaskEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec, c, apiKey, subscription := newVideoTaskTestContext(http.MethodDelete, "/v1/video/generations/task_123", "")
	c.Params = gin.Params{{Key: "request_id", Value: "task_123"}}
	setVideoTaskAuthContext(c, apiKey, subscription)
	fake := &fakeVideoTaskService{deleteResult: &service.VideoTaskFetchResult{Task: &service.VideoTask{
		PublicTaskID: "task_123",
		Model:        "seedance-2.0",
		Status:       service.VideoTaskStatusCompleted,
		Metadata:     map[string]any{"progress": 100},
	}}}
	h := &VideoTaskHandler{videoTaskService: fake}

	h.Delete(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{
		"code": 0,
		"message": "success",
		"data": {
			"id": "task_123",
			"task_id": "task_123",
			"object": "video",
			"status": "completed",
			"model": "seedance-2.0",
			"progress": 100,
			"deleted": true
		}
	}`, rec.Body.String())
	require.Equal(t, int64(42), fake.deleteParams.UserID)
	require.Equal(t, "task_123", fake.deleteParams.PublicTaskID)
}

func TestVideoTaskHandlerContentForwardsRangeStatusHeadersAndStreamBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec, c, apiKey, subscription := newVideoTaskTestContext(http.MethodGet, "/v1/videos/task_123/content", "")
	c.Params = gin.Params{{Key: "task_id", Value: "task_123"}}
	c.Request.Header.Set("Range", "bytes=0-4")
	setVideoTaskAuthContext(c, apiKey, subscription)

	streamBody := &closeTrackingReader{Reader: strings.NewReader("video")}
	fake := &fakeVideoTaskService{
		contentResult: &service.VideoContentStream{
			Body:          streamBody,
			StatusCode:    http.StatusPartialContent,
			ContentType:   "video/mp4",
			ContentLength: 5,
			Headers: map[string]string{
				"Accept-Ranges":      "bytes",
				"Content-Range":      "bytes 0-4/10",
				"X-Upstream-Request": "rid-video",
				"Authorization":      "Bearer secret",
				"Connection":         "keep-alive",
				"Set-Cookie":         "session=secret",
			},
		},
	}
	h := &VideoTaskHandler{videoTaskService: fake}

	h.Content(c)

	require.Equal(t, http.StatusPartialContent, rec.Code)
	require.Equal(t, "video", rec.Body.String())
	require.True(t, streamBody.closed)
	require.Equal(t, int64(42), fake.contentParams.UserID)
	require.Equal(t, "task_123", fake.contentParams.PublicTaskID)
	require.Equal(t, []string{"bytes=0-4"}, fake.contentParams.Header.Values("Range"))
	require.Equal(t, "video/mp4", rec.Header().Get("Content-Type"))
	require.Equal(t, "5", rec.Header().Get("Content-Length"))
	require.Equal(t, "bytes 0-4/10", rec.Header().Get("Content-Range"))
	require.Equal(t, "bytes", rec.Header().Get("Accept-Ranges"))
	require.Equal(t, "rid-video", rec.Header().Get("X-Upstream-Request"))
	require.Empty(t, rec.Header().Get("Authorization"))
	require.Empty(t, rec.Header().Get("Connection"))
	require.Empty(t, rec.Header().Get("Set-Cookie"))
}

func TestVideoTaskHandlerContentHeadReturnsHeadersWithoutBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec, c, apiKey, subscription := newVideoTaskTestContext(http.MethodHead, "/v1/video/generations/task_123/content", "")
	c.Params = gin.Params{{Key: "request_id", Value: "task_123"}}
	setVideoTaskAuthContext(c, apiKey, subscription)
	streamBody := &closeTrackingReader{Reader: strings.NewReader("video")}
	fake := &fakeVideoTaskService{contentResult: &service.VideoContentStream{
		Body:          streamBody,
		ContentType:   "video/mp4",
		ContentLength: 5,
		Headers:       map[string]string{"Accept-Ranges": "bytes"},
	}}
	h := &VideoTaskHandler{videoTaskService: fake}

	h.Content(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, rec.Body.String())
	require.True(t, streamBody.closed)
	require.Equal(t, "video/mp4", rec.Header().Get("Content-Type"))
	require.Equal(t, "5", rec.Header().Get("Content-Length"))
	require.Equal(t, "bytes", rec.Header().Get("Accept-Ranges"))
	require.Equal(t, http.MethodHead, fake.contentParams.Method)
}

func TestVideoTaskHandlerServiceErrorsMapToOpenAIErrorResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		handler    string
		err        error
		wantStatus int
		wantType   string
		wantMsg    string
		notContain []string
	}{
		{
			name:       "permission denied",
			handler:    "create",
			err:        fmt.Errorf("wrapped: %w", service.ErrVideoGenerationPermissionDenied),
			wantStatus: http.StatusForbidden,
			wantType:   "permission_error",
		},
		{
			name:       "idempotency conflict",
			handler:    "create",
			err:        fmt.Errorf("wrapped: %w", service.ErrVideoTaskIdempotencyConflict),
			wantStatus: http.StatusConflict,
			wantType:   "idempotency_error",
		},
		{
			name:       "not found",
			handler:    "fetch",
			err:        fmt.Errorf("wrapped: %w", service.ErrVideoTaskNotFound),
			wantStatus: http.StatusNotFound,
			wantType:   "not_found_error",
		},
		{
			name:       "not completed",
			handler:    "content",
			err:        fmt.Errorf("wrapped: %w", service.ErrVideoTaskNotCompleted),
			wantStatus: http.StatusConflict,
			wantType:   "invalid_request_error",
		},
		{
			name:       "account unavailable",
			handler:    "create",
			err:        fmt.Errorf("wrapped: %w", service.ErrVideoTaskAccountUnavailable),
			wantStatus: http.StatusServiceUnavailable,
			wantType:   "server_error",
		},
		{
			name:       "no available accounts",
			handler:    "create",
			err:        fmt.Errorf("wrapped: %w", service.ErrNoAvailableAccounts),
			wantStatus: http.StatusServiceUnavailable,
			wantType:   "server_error",
		},
		{
			name:       "unsupported action",
			handler:    "refresh",
			err:        service.ErrVideoTaskActionUnsupported,
			wantStatus: http.StatusBadRequest,
			wantType:   "invalid_request_error",
		},
		{
			name:       "body parse error",
			handler:    "create",
			err:        errors.New("model is required"),
			wantStatus: http.StatusBadRequest,
			wantType:   "invalid_request_error",
		},
		{
			name:       "forbidden video field parse error",
			handler:    "create",
			err:        errors.New("duration is not supported by /v1/videos"),
			wantStatus: http.StatusBadRequest,
			wantType:   "invalid_request_error",
		},
		{
			name:       "unsupported video model parse error",
			handler:    "create",
			err:        errors.New("model must be video-ds-2.0-fast or video-ds-2.0"),
			wantStatus: http.StatusBadRequest,
			wantType:   "invalid_request_error",
		},
		{
			name:       "upstream rate limit",
			handler:    "fetch",
			err:        &service.OpenAIVideoUpstreamError{StatusCode: http.StatusTooManyRequests, Body: `{"error":"rate limited"}`},
			wantStatus: http.StatusTooManyRequests,
			wantType:   "rate_limit_error",
		},
		{
			name:       "upstream billing detail is sanitized",
			handler:    "fetch",
			err:        &service.OpenAIVideoUpstreamError{StatusCode: http.StatusPaymentRequired, Body: `{"error":{"code":"insufficient_balance","message":"insufficient balance"}}`},
			wantStatus: http.StatusBadGateway,
			wantType:   "server_error",
			notContain: []string{"insufficient_balance", "insufficient balance"},
		},
		{
			name:       "upstream billing code is sanitized",
			handler:    "create",
			err:        &service.OpenAIVideoUpstreamError{StatusCode: http.StatusInternalServerError, Body: `{"code":"insufficient_balance","message":"temporary upstream failure"}`},
			wantStatus: http.StatusBadGateway,
			wantType:   "server_error",
			notContain: []string{"insufficient_balance", "temporary upstream failure"},
		},
		{
			name:       "upstream validation message is passed through",
			handler:    "create",
			err:        &service.OpenAIVideoUpstreamError{StatusCode: http.StatusInternalServerError, Body: `{"code":"build_request_failed","data":null,"message":"kling-v3 视频时长只支持5到15秒"}`},
			wantStatus: http.StatusBadGateway,
			wantType:   "server_error",
			wantMsg:    "kling-v3 视频时长只支持5到15秒",
		},
		{
			name:       "upstream URL detail is sanitized",
			handler:    "create",
			err:        &service.OpenAIVideoUpstreamError{StatusCode: http.StatusInternalServerError, Body: `{"message":"fetch https://upstream.example/v1/videos failed"}`},
			wantStatus: http.StatusBadGateway,
			wantType:   "server_error",
			notContain: []string{"https://upstream.example"},
		},
		{
			name:       "upstream quota detail is sanitized",
			handler:    "create",
			err:        &service.OpenAIVideoUpstreamError{StatusCode: http.StatusInternalServerError, Body: `{"message":"账号额度不足"}`},
			wantStatus: http.StatusBadGateway,
			wantType:   "server_error",
			notContain: []string{"账号额度不足"},
		},
		{
			name:       "plain upstream message is passed through",
			handler:    "create",
			err:        &service.OpenAIVideoUpstreamError{StatusCode: http.StatusInternalServerError, Body: `model kling-v3 not found`},
			wantStatus: http.StatusBadGateway,
			wantType:   "server_error",
			wantMsg:    "model kling-v3 not found",
		},
		{
			name:       "upstream bad request",
			handler:    "create",
			err:        &service.OpenAIVideoUpstreamError{StatusCode: http.StatusBadRequest, Body: `{"error":"bad request"}`},
			wantStatus: http.StatusBadRequest,
			wantType:   "invalid_request_error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, c, apiKey, subscription := newVideoTaskTestContext(http.MethodPost, "/v1/videos", `{"model":"sora","prompt":"ocean"}`)
			c.Params = gin.Params{{Key: "task_id", Value: "task_123"}}
			setVideoTaskAuthContext(c, apiKey, subscription)

			fake := &fakeVideoTaskService{}
			h := &VideoTaskHandler{videoTaskService: fake}
			switch tt.handler {
			case "create":
				fake.createErr = tt.err
				h.Create(c)
			case "fetch":
				fake.fetchErr = tt.err
				h.Fetch(c)
			case "content":
				fake.contentErr = tt.err
				h.Content(c)
			case "refresh":
				fake.refreshErr = tt.err
				h.Refresh(c)
			}

			require.Equal(t, tt.wantStatus, rec.Code)
			require.Equal(t, tt.wantType, gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
			message := gjson.GetBytes(rec.Body.Bytes(), "error.message").String()
			if tt.wantMsg != "" {
				require.Equal(t, tt.wantMsg, message)
			} else {
				require.NotEmpty(t, message)
			}
			for _, value := range tt.notContain {
				require.NotContains(t, rec.Body.String(), value)
			}
		})
	}
}

func TestVideoTaskHandlerRefreshWithNilConstructorServiceReturnsConfiguredError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec, c, apiKey, subscription := newVideoTaskTestContext(http.MethodPost, "/v1/videos/task_123/refresh", "")
	c.Params = gin.Params{{Key: "task_id", Value: "task_123"}}
	setVideoTaskAuthContext(c, apiKey, subscription)
	h := NewVideoTaskHandler(nil)

	require.NotPanics(t, func() { h.Refresh(c) })
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Equal(t, "server_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Equal(t, "Video task service is not configured", gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
}

func TestVideoTaskHandlerRefreshRejectsEmptyTaskID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec, c, apiKey, subscription := newVideoTaskTestContext(http.MethodPost, "/v1/videos//refresh", "")
	setVideoTaskAuthContext(c, apiKey, subscription)
	fake := &fakeVideoTaskService{refreshResult: &service.VideoTaskFetchResult{ResponseBody: []byte(`{"id":"should_not_call"}`)}}
	h := &VideoTaskHandler{videoTaskService: fake}

	h.Refresh(c)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Equal(t, "task_id is required", gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
	require.Empty(t, fake.refreshParams.PublicTaskID)
}

func TestVideoTaskHandlerRefreshRejectsEmptyServiceResult(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec, c, apiKey, subscription := newVideoTaskTestContext(http.MethodPost, "/v1/videos/task_123/refresh", "")
	c.Params = gin.Params{{Key: "task_id", Value: "task_123"}}
	setVideoTaskAuthContext(c, apiKey, subscription)
	fake := &fakeVideoTaskService{}
	h := &VideoTaskHandler{videoTaskService: fake}

	require.NotPanics(t, func() { h.Refresh(c) })
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Equal(t, "server_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Equal(t, "Video task service returned empty response", gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
}

func TestVideoTaskHandlerEstimateRejectsEmptyBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec, c, apiKey, subscription := newVideoTaskTestContext(http.MethodPost, "/v1/videos/estimate", "")
	setVideoTaskAuthContext(c, apiKey, subscription)
	fake := &fakeVideoTaskService{estimateResult: &service.VideoTaskEstimateResult{ResponseBody: []byte(`{"estimated":true}`)}}
	h := &VideoTaskHandler{videoTaskService: fake}

	h.Estimate(c)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Equal(t, "Request body is empty", gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
	require.Nil(t, fake.estimateParams.APIKey)
}

func TestVideoTaskHandlerEstimateReturnsVideoPricingContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec, c, apiKey, subscription := newVideoTaskTestContext(http.MethodPost, "/v1/videos/estimate", `{"model":"seedance","prompt":"city"}`)
	setVideoTaskAuthContext(c, apiKey, subscription)
	response := `{"object":"video.estimate","billing_mode":"video","billing_model":"seedance","effective":{"seconds":5,"resolution":"1080p","video_count":1},"unit_price_usd":0.12,"gross_cost_usd":0.6,"rate_multiplier":0.5,"actual_cost_usd":0.3}`
	h := &VideoTaskHandler{videoTaskService: &fakeVideoTaskService{estimateResult: &service.VideoTaskEstimateResult{ResponseBody: []byte(response)}}}

	h.Estimate(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, response, rec.Body.String())
}

func TestVideoTaskHandlerEstimateGenerationsReturnsSanitizedEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec, c, apiKey, subscription := newVideoTaskTestContext(http.MethodPost, "/v1/video/generations/estimate", `{"model":"seedance","prompt":"city"}`)
	setVideoTaskAuthContext(c, apiKey, subscription)
	response := `{"object":"video.estimate","model":"seedance","upstream_model":"seedance-upstream","adapter":"seedance_api_v1","endpoint":"video_generations","metadata":{"duration":5,"ratio":"16:9","resolution":"1080p"},"billing_mode":"video","billing_model":"seedance","effective":{"seconds":5,"resolution":"1080p","video_count":1},"unit_price_usd":0.12,"gross_cost_usd":0.6,"rate_multiplier":0.5,"actual_cost_usd":0.3}`
	h := &VideoTaskHandler{videoTaskService: &fakeVideoTaskService{estimateResult: &service.VideoTaskEstimateResult{ResponseBody: []byte(response)}}}

	h.EstimateGenerationsCompat(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{
		"code": 0,
		"message": "success",
		"data": {
			"object": "video.estimate",
			"model": "seedance",
			"metadata": {"duration": 5, "ratio": "16:9", "resolution": "1080p"},
			"billing_mode": "video",
			"billing_model": "seedance",
			"effective": {"seconds": 5, "resolution": "1080p", "video_count": 1},
			"unit_price_usd": 0.12,
			"gross_cost_usd": 0.6,
			"rate_multiplier": 0.5,
			"actual_cost_usd": 0.3
		}
	}`, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "seedance-upstream")
	require.NotContains(t, rec.Body.String(), "seedance_api_v1")
	require.NotContains(t, rec.Body.String(), "video_generations")
}

func TestVideoTaskHandlerListGenerationsReturnsLocalTaskEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec, c, apiKey, subscription := newVideoTaskTestContext(http.MethodGet, "/v1/video/generations?limit=10", "")
	setVideoTaskAuthContext(c, apiKey, subscription)
	fake := &fakeVideoTaskService{listResult: &service.VideoTaskListResult{
		Tasks: []*service.VideoTask{{
			PublicTaskID: "task_123",
			Model:        "seedance-2.0",
			Status:       service.VideoTaskStatusInProgress,
			Metadata:     map[string]any{"progress": 25, "duration": 8, "ratio": "16:9"},
		}},
		HasMore:      true,
		ResponseBody: []byte(`{"object":"list","data":[{"id":"upstream_task_123"}]}`),
	}}
	h := &VideoTaskHandler{videoTaskService: fake}

	h.List(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{
		"code": 0,
		"message": "success",
		"data": {
			"object": "list",
			"has_more": true,
			"data": [{
				"id": "task_123",
				"task_id": "task_123",
				"object": "video",
				"status": "in_progress",
				"model": "seedance-2.0",
				"progress": 25,
				"duration": 8,
				"ratio": "16:9"
			}]
		}
	}`, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "upstream_task_123")
}

func TestVideoTaskHandlerListRejectsMalformedLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec, c, apiKey, subscription := newVideoTaskTestContext(http.MethodGet, "/v1/videos?limit=soon", "")
	setVideoTaskAuthContext(c, apiKey, subscription)
	fake := &fakeVideoTaskService{listResult: &service.VideoTaskListResult{ResponseBody: []byte(`{"data":[]}`)}}
	h := &VideoTaskHandler{videoTaskService: fake}

	h.List(c)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Equal(t, "limit must be an integer", gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
	require.Zero(t, fake.listParams.UserID)
}

func TestIsVideoTaskBadRequestErrorClassifiesVideoCreateObjectError(t *testing.T) {
	require.True(t, isVideoTaskBadRequestError(errors.New("video create JSON body must be an object")))
}

func TestIsVideoTaskBadRequestErrorClassifiesJimengValidationWrapper(t *testing.T) {
	err := fmt.Errorf("invalid jimeng OpenAI video create JSON: %w", errors.New("duration must be a number or string"))

	require.True(t, isVideoTaskBadRequestError(err))
}

func TestIsVideoTaskBadRequestErrorClassifiesAdapterNumericValidation(t *testing.T) {
	require.True(t, isVideoTaskBadRequestError(errors.New("seconds must be a number or numeric string")))
	require.True(t, isVideoTaskBadRequestError(errors.New("duration must be a number or numeric string")))
}

func TestVideoTaskPublicDataIncludesStoredErrorAndPricing(t *testing.T) {
	data := videoTaskPublicData(&service.VideoTask{
		PublicTaskID: "task_failed",
		Model:        "seedance-2.0",
		Status:       service.VideoTaskStatusFailed,
		ErrorMessage: "insufficient balance at upstream provider",
		BilledUSD:    0.3,
		Metadata: map[string]any{
			"request_metadata": map[string]any{
				"video_pricing_snapshot": map[string]any{
					"billing_mode":    "video",
					"billing_model":   "seedance-2.0",
					"effective":       map[string]any{"seconds": 5, "resolution": "1080p", "video_count": 1},
					"unit_price_usd":  0.12,
					"gross_cost_usd":  0.6,
					"actual_cost_usd": 0.3,
					"rate_multiplier": 0.5,
				},
			},
			"upstream_base_url":             "https://upstream.example",
			"error_code":                    "insufficient_balance",
			"error_message":                 "insufficient balance at upstream provider",
			service.VideoAdapterMetadataKey: "seedance_api_v1",
		},
	})

	require.Equal(t, gin.H{
		"id":              "task_failed",
		"task_id":         "task_failed",
		"object":          "video",
		"status":          "failed",
		"model":           "seedance-2.0",
		"progress":        100,
		"error":           gin.H{"code": "VIDEO_GENERATION_FAILED", "message": "Video generation failed"},
		"billing_mode":    "video",
		"billing_model":   "seedance-2.0",
		"effective":       map[string]any{"seconds": 5, "resolution": "1080p", "video_count": 1},
		"unit_price_usd":  0.12,
		"gross_cost_usd":  0.6,
		"actual_cost_usd": 0.3,
		"rate_multiplier": 0.5,
		"billed_usd":      0.3,
	}, data)
}

func TestVideoTaskHandlerCreateRejectsEmptyBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec, c, apiKey, subscription := newVideoTaskTestContext(http.MethodPost, "/v1/videos", "")
	setVideoTaskAuthContext(c, apiKey, subscription)
	fake := &fakeVideoTaskService{}
	h := &VideoTaskHandler{videoTaskService: fake}

	h.Create(c)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Nil(t, fake.createParams.APIKey)
}

func newVideoTaskTestContext(method, target, body string) (*httptest.ResponseRecorder, *gin.Context, *service.APIKey, *service.UserSubscription) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))

	groupID := int64(12)
	user := &service.User{ID: 42}
	apiKey := &service.APIKey{
		ID:      7,
		UserID:  user.ID,
		GroupID: &groupID,
		Group: &service.Group{
			ID:       groupID,
			Platform: service.PlatformOpenAI,
		},
		User: user,
	}
	subscription := &service.UserSubscription{ID: 99, UserID: user.ID, GroupID: groupID}
	return rec, c, apiKey, subscription
}

func setVideoTaskAuthContext(c *gin.Context, apiKey *service.APIKey, subscription *service.UserSubscription) {
	c.Set(string(middleware2.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: apiKey.UserID, Concurrency: 1})
	c.Set(string(middleware2.ContextKeySubscription), subscription)
}

var _ io.ReadCloser = (*closeTrackingReader)(nil)
