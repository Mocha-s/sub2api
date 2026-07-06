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
	createParams  service.VideoTaskCreateParams
	createResult  *service.VideoTaskCreateResult
	createErr     error
	fetchParams   service.VideoTaskFetchParams
	fetchResult   *service.VideoTaskFetchResult
	fetchErr      error
	contentParams service.VideoTaskContentParams
	contentResult *service.VideoContentStream
	contentErr    error
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

func TestVideoTaskHandlerServiceErrorsMapToOpenAIErrorResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		method     string
		err        error
		wantStatus int
		wantType   string
	}{
		{
			name:       "permission denied",
			method:     "create",
			err:        fmt.Errorf("wrapped: %w", service.ErrVideoGenerationPermissionDenied),
			wantStatus: http.StatusForbidden,
			wantType:   "permission_error",
		},
		{
			name:       "idempotency conflict",
			method:     "create",
			err:        fmt.Errorf("wrapped: %w", service.ErrVideoTaskIdempotencyConflict),
			wantStatus: http.StatusConflict,
			wantType:   "idempotency_error",
		},
		{
			name:       "not found",
			method:     "fetch",
			err:        fmt.Errorf("wrapped: %w", service.ErrVideoTaskNotFound),
			wantStatus: http.StatusNotFound,
			wantType:   "not_found_error",
		},
		{
			name:       "not completed",
			method:     "content",
			err:        fmt.Errorf("wrapped: %w", service.ErrVideoTaskNotCompleted),
			wantStatus: http.StatusConflict,
			wantType:   "invalid_request_error",
		},
		{
			name:       "account unavailable",
			method:     "create",
			err:        fmt.Errorf("wrapped: %w", service.ErrVideoTaskAccountUnavailable),
			wantStatus: http.StatusServiceUnavailable,
			wantType:   "server_error",
		},
		{
			name:       "no available accounts",
			method:     "create",
			err:        fmt.Errorf("wrapped: %w", service.ErrNoAvailableAccounts),
			wantStatus: http.StatusServiceUnavailable,
			wantType:   "server_error",
		},
		{
			name:       "body parse error",
			method:     "create",
			err:        errors.New("model is required"),
			wantStatus: http.StatusBadRequest,
			wantType:   "invalid_request_error",
		},
		{
			name:       "upstream rate limit",
			method:     "fetch",
			err:        &service.OpenAIVideoUpstreamError{StatusCode: http.StatusTooManyRequests, Body: `{"error":"rate limited"}`},
			wantStatus: http.StatusTooManyRequests,
			wantType:   "rate_limit_error",
		},
		{
			name:       "upstream bad request",
			method:     "create",
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
			switch tt.method {
			case "create":
				fake.createErr = tt.err
				h.Create(c)
			case "fetch":
				fake.fetchErr = tt.err
				h.Fetch(c)
			case "content":
				fake.contentErr = tt.err
				h.Content(c)
			}

			require.Equal(t, tt.wantStatus, rec.Code)
			require.Equal(t, tt.wantType, gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
			require.NotEmpty(t, gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
		})
	}
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
