package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type imageLogQueueRepo struct {
	mu        sync.Mutex
	rows      map[string]ImageTaskLogEntry
	completed chan ImageTaskLogEntry
}

func (r *imageLogQueueRepo) Save(_ context.Context, e *ImageTaskLogEntry) error {
	r.mu.Lock()
	r.rows[e.TaskID] = *e
	r.mu.Unlock()
	if e.FinishedAt != nil {
		r.completed <- *e
	}
	return nil
}
func (*imageLogQueueRepo) Get(context.Context, string, int64, int) (*ImageTaskLogEntry, error) {
	return nil, ErrImageTaskNotFound
}

type imageLogQueueUpstream struct {
	HTTPUpstream
	entered chan struct{}
	release chan struct{}
}

func (u *imageLogQueueUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.entered <- struct{}{}
	select {
	case <-u.release:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("\x89PNG\r\n\x1a\n"))}, nil
}
func TestImageTaskLogRetentionQueueOverflowFinalizesWithoutDownloading(t *testing.T) {
	repo := &imageLogQueueRepo{rows: map[string]ImageTaskLogEntry{}, completed: make(chan ImageTaskLogEntry, 10)}
	upstream := &imageLogQueueUpstream{entered: make(chan struct{}, 10), release: make(chan struct{})}
	logs := NewImageTaskLogService(repo, &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream})
	defer logs.Stop()
	defer close(upstream.release)
	payload := []byte(`{"data":[{"url":"https://cdn.example/image.png"}]}`)
	for i := 0; i < 2; i++ {
		entry := ImageTaskLogEntry{TaskID: fmt.Sprint(i), UserID: 7}
		logs.Start(context.Background(), &entry)
		logs.FinishAsync(entry, 200, payload, false)
		select {
		case <-upstream.entered:
		case <-time.After(time.Second):
			t.Fatal("snapshot worker did not start")
		}
	}
	for i := 2; i < 5; i++ {
		entry := ImageTaskLogEntry{TaskID: fmt.Sprint(i), UserID: 7}
		logs.Start(context.Background(), &entry)
		logs.FinishAsync(entry, 200, payload, false)
	}
	select {
	case entry := <-repo.completed:
		require.Equal(t, "4", entry.TaskID)
		require.Equal(t, "completed", entry.Status)
		require.Equal(t, "result_unavailable", entry.ErrorCode)
		require.Zero(t, entry.SuccessCount)
		require.Empty(t, entry.Results)
	case <-time.After(time.Second):
		t.Fatal("overflow task was left processing behind blocked downloads")
	}
}
