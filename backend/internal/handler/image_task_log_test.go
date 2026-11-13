package handler

import (
	"context"
	"encoding/base64"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type handlerImageLogRepo struct {
	mu       sync.Mutex
	rows     map[string]service.ImageTaskLogEntry
	saves    int
	finished chan struct{}
}

func (r *handlerImageLogRepo) Save(_ context.Context, e *service.ImageTaskLogEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows[e.TaskID] = *e
	r.saves++
	if e.FinishedAt != nil && r.finished != nil {
		close(r.finished)
	}
	return nil
}

type blockedSnapshotUpstream struct {
	service.HTTPUpstream
	entered, release chan struct{}
}

func (u *blockedSnapshotUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	close(u.entered)
	select {
	case <-u.release:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("\x89PNG\r\n\x1a\n"))}, nil
}
func TestImageTaskLogRetentionDoesNotDelayHTTPCompletion(t *testing.T) {
	upstream := &blockedSnapshotUpstream{entered: make(chan struct{}), release: make(chan struct{})}
	gateway := service.NewOpenAIGatewayService(nil, nil, nil, nil, nil, nil, nil, &config.Config{}, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil)
	repo := &handlerImageLogRepo{rows: map[string]service.ImageTaskLogEntry{}}
	logs := service.NewImageTaskLogService(repo, gateway)
	defer logs.Stop()
	h := &OpenAIGatewayHandler{imageLogs: logs}
	router := gin.New()
	payload := `{"data":[{"url":"https://cdn.example/image.png"}]}`
	router.POST("/", func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 2, UserID: 7})
		defer h.startImageTaskLog(c)()
		c.Data(200, "application/json", []byte(payload))
	})
	server := httptest.NewServer(router)
	defer server.Close()
	defer close(upstream.release)
	done := make(chan string, 1)
	go func() {
		resp, err := http.Post(server.URL, "application/json", nil)
		if err != nil {
			done <- err.Error()
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			done <- err.Error()
			return
		}
		done <- string(body)
	}()
	select {
	case <-upstream.entered:
	case <-time.After(time.Second):
		t.Fatal("snapshot did not start")
	}
	select {
	case body := <-done:
		require.Equal(t, payload, body)
	case <-time.After(300 * time.Millisecond):
		t.Error("HTTP completion blocked on optional retention")
	}
}

func TestAsyncImageTaskLogPersistsOneTaskAcrossAliases(t *testing.T) {
	for _, path := range []string{"/v1/images/generations/async", "/v1/images/edits/async", "/images/generations/async", "/images/edits/async"} {
		for _, status := range []int{200, 502} {
			t.Run(path+http.StatusText(status), func(t *testing.T) {
				repo := &handlerImageLogRepo{rows: map[string]service.ImageTaskLogEntry{}, finished: make(chan struct{})}
				openAI := &OpenAIGatewayHandler{imageLogs: service.NewImageTaskLogService(repo, nil)}
				tasks := service.NewImageTaskServiceWithUploader(&asyncImageMemoryStore{tasks: map[string]*service.ImageTaskRecord{}}, nil, time.Hour, time.Minute)
				h := NewAsyncImageHandler(tasks, openAI)
				release := make(chan struct{})
				h.execute = func(_ string, c *gin.Context) {
					<-release
					defer openAI.startImageTaskLog(c)()
					if status == 502 {
						c.JSON(status, gin.H{"error": gin.H{"message": "upstream failed", "code": "upstream_error"}})
						return
					}
					encoded := base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
					c.JSON(status, gin.H{"data": []gin.H{{"b64_json": encoded}}})
				}
				r := gin.New()
				r.POST(path, func(c *gin.Context) {
					c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 2, UserID: 7, Group: &service.Group{Platform: "openai", AllowImageGeneration: true}})
					c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7})
					h.Submit(c)
				})
				w := httptest.NewRecorder()
				req := httptest.NewRequest("POST", path, strings.NewReader(`{"model":"gpt-image-1","prompt":"cat","n":1}`))
				req.Header.Set("Content-Type", "application/json")
				r.ServeHTTP(w, req)
				require.Equal(t, 202, w.Code)
				require.Len(t, repo.rows, 1)
				var id string
				for key, e := range repo.rows {
					id = key
					require.Equal(t, "processing", e.Status)
					require.Equal(t, "cat", e.Prompt)
					require.Equal(t, "async", e.RequestMode)
				}
				close(release)
				select {
				case <-repo.finished:
				case <-time.After(time.Second):
					t.Fatal("image task did not finish")
				}
				require.Len(t, repo.rows, 1)
				e := repo.rows[id]
				require.NotNil(t, e.FinishedAt)
				require.Contains(t, w.Body.String(), id)
				if status == 200 {
					require.Equal(t, "completed", e.Status)
					require.Equal(t, 1, e.SuccessCount)
				} else {
					require.Equal(t, "failed", e.Status)
					require.Equal(t, "upstream_error", e.ErrorCode)
				}
			})
		}
	}
}

func TestAsyncImageTaskLogRecordsRejectedRequest(t *testing.T) {
	repo := &handlerImageLogRepo{rows: map[string]service.ImageTaskLogEntry{}}
	openAI := &OpenAIGatewayHandler{imageLogs: service.NewImageTaskLogService(repo, nil)}
	tasks := service.NewImageTaskServiceWithUploader(&asyncImageMemoryStore{tasks: map[string]*service.ImageTaskRecord{}}, nil, time.Hour, time.Minute)
	h := NewAsyncImageHandler(tasks, openAI)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/images/generations/async", strings.NewReader(`{"model":"gpt-image-1","prompt":"cat","stream":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 2, UserID: 7, Group: &service.Group{Platform: "openai", AllowImageGeneration: true}})
	h.Submit(c)
	require.Len(t, repo.rows, 1)
	for _, e := range repo.rows {
		require.Equal(t, "failed", e.Status)
		require.Equal(t, "async", e.RequestMode)
		require.Equal(t, "cat", e.Prompt)
	}
}
func (r *handlerImageLogRepo) Get(_ context.Context, id string, userID int64, index int) (*service.ImageTaskLogEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.rows[id]
	if !ok || (userID > 0 && e.UserID != userID) {
		return nil, service.ErrImageTaskNotFound
	}
	results := e.Results
	e.Results = nil
	if index >= 0 && index < len(results) {
		e.Results = []service.ImageTaskLogResult{results[index]}
	}
	return &e, nil
}
func TestImageTaskLogObserverPreservesStreamAndMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &handlerImageLogRepo{rows: map[string]service.ImageTaskLogEntry{}}
	h := &OpenAIGatewayHandler{imageLogs: service.NewImageTaskLogService(repo, nil)}
	r := gin.New()
	r.POST("/images/edits", func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 2, UserID: 7, Group: &service.Group{Platform: "openai"}})
		defer h.startImageTaskLog(c)()
		h.imageTaskLogMetadata(c, "gpt-image-1", "edited cat", 2)
		for _, e := range repo.rows {
			require.Equal(t, "processing", e.Status)
			require.Equal(t, "edited cat", e.Prompt)
		}
		c.Header("Content-Type", "text/event-stream")
		b64 := base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
		c.Writer.WriteString("data: {\"type\":\"image_generation.completed\",\"b64_json\":\"" + b64 + "\"}\n\n")
		c.Writer.Flush()
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/images/edits", nil))
	h.imageLogs.Stop()
	require.Equal(t, 200, w.Code)
	require.True(t, w.Flushed)
	require.Contains(t, w.Body.String(), "image_generation.completed")
	require.Len(t, repo.rows, 1)
	for _, e := range repo.rows {
		require.Equal(t, "sync", e.RequestMode)
		require.Equal(t, "completed", e.Status)
		require.Equal(t, 1, e.SuccessCount)
		require.Equal(t, "edited cat", e.Prompt)
	}
}
func TestImageTaskLogObserverSkipsAsyncExecutor(t *testing.T) {
	repo := &handlerImageLogRepo{rows: map[string]service.ImageTaskLogEntry{}}
	h := &OpenAIGatewayHandler{imageLogs: service.NewImageTaskLogService(repo, nil)}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/images/generations", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 2, UserID: 7})
	c.Set(asyncImageExecutionKey, true)
	finish := h.startImageTaskLog(c)
	finish()
	require.Empty(t, repo.rows)
}

type disconnectedImageWriter struct{ gin.ResponseWriter }

func (w disconnectedImageWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestImageTaskLogWriterRetainsGeneratedResponseAfterClientDisconnect(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	w := &imageTaskLogWriter{ResponseWriter: disconnectedImageWriter{c.Writer}, capture: service.NewImageTaskLogCapture()}
	payload := []byte(`{"data":[{"b64_json":"retained"}]}`)
	_, err := w.Write(payload)
	require.ErrorIs(t, err, io.ErrClosedPipe)
	body, _ := w.capture.Result(false)
	require.Equal(t, payload, body)
}
func TestImageTaskLogCapturesGatewayFailureWithoutAsyncEnabled(t *testing.T) {
	repo := &handlerImageLogRepo{rows: map[string]service.ImageTaskLogEntry{}}
	h := &OpenAIGatewayHandler{imageLogs: service.NewImageTaskLogService(repo, nil)}
	c, w := gin.CreateTestContext(httptest.NewRecorder())
	_ = w
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"prompt":"cat"}`))
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 2, UserID: 7, Group: &service.Group{Platform: "openai"}})
	h.Images(c)
	h.imageLogs.Stop()
	require.Len(t, repo.rows, 1)
	for _, e := range repo.rows {
		require.Equal(t, "failed", e.Status)
		require.NotNil(t, e.FinishedAt)
	}
}
