package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const (
	videoTaskPollLimit          = 20
	videoTaskPollLockTTL        = 2 * time.Minute
	videoTaskPollInterval       = 5 * time.Second
	videoTaskPollReleaseTimeout = 5 * time.Second
)

// VideoTaskPoller reclaims due video tasks and refreshes their provider status.
type VideoTaskPoller struct {
	repo          VideoTaskRepository
	accountLookup videoTaskAccountLookup
	provider      VideoTaskProvider

	workerID string
	interval time.Duration

	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	stopping bool
}

func NewVideoTaskPoller(repo VideoTaskRepository, accountRepo AccountRepository, provider VideoTaskProvider) *VideoTaskPoller {
	return newVideoTaskPoller(repo, accountRepo, provider)
}

func newVideoTaskPoller(repo VideoTaskRepository, accountLookup videoTaskAccountLookup, provider VideoTaskProvider) *VideoTaskPoller {
	if provider == nil {
		provider = NewOpenAICompatibleVideoProvider(nil)
	}
	return &VideoTaskPoller{
		repo:          repo,
		accountLookup: accountLookup,
		provider:      provider,
		workerID:      defaultVideoTaskPollerWorkerID(),
		interval:      videoTaskPollInterval,
	}
}

func (p *VideoTaskPoller) Start() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.cancel != nil || p.stopping {
		p.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	p.cancel = cancel
	p.done = done
	workerID := p.workerID
	interval := p.interval
	p.mu.Unlock()

	go p.run(ctx, done, workerID, interval)
}

func (p *VideoTaskPoller) Stop() {
	if p == nil {
		return
	}
	p.mu.Lock()
	cancel := p.cancel
	done := p.done
	if cancel == nil {
		p.mu.Unlock()
		return
	}
	p.stopping = true
	p.mu.Unlock()

	cancel()
	<-done

	p.mu.Lock()
	if p.done == done {
		p.cancel = nil
		p.done = nil
		p.stopping = false
	}
	p.mu.Unlock()
}

func (p *VideoTaskPoller) PollOnce(ctx context.Context, workerID string, now time.Time) error {
	if p == nil {
		return errors.New("video task poller is nil")
	}
	if p.repo == nil {
		return errors.New("video task repository is required")
	}
	if p.accountLookup == nil {
		return errors.New("video task account lookup is required")
	}
	if p.provider == nil {
		return errors.New("video task provider is required")
	}
	if workerID == "" {
		workerID = p.workerID
	}
	if now.IsZero() {
		now = time.Now()
	}

	tasks, err := p.repo.ClaimDuePollTasks(ctx, now, videoTaskPollLimit, workerID, videoTaskPollLockTTL)
	if err != nil {
		return err
	}

	var errs []error
	for _, task := range tasks {
		if task == nil {
			errs = append(errs, errors.New("claimed video task is nil"))
			continue
		}
		if err := p.pollClaimedTask(ctx, task, now); err != nil {
			errs = append(errs, fmt.Errorf("video task poll %s: %w", task.PublicTaskID, err))
		}
		if err := p.releasePollLock(ctx, task.PublicTaskID, workerID); err != nil {
			errs = append(errs, fmt.Errorf("video task poll %s release lock: %w", task.PublicTaskID, err))
		}
	}
	return errors.Join(errs...)
}

func (p *VideoTaskPoller) releasePollLock(ctx context.Context, publicTaskID string, workerID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), videoTaskPollReleaseTimeout)
	defer cancel()
	return p.repo.ReleasePollLock(releaseCtx, publicTaskID, workerID)
}

func (p *VideoTaskPoller) run(ctx context.Context, done chan<- struct{}, workerID string, interval time.Duration) {
	defer close(done)
	if interval <= 0 {
		interval = videoTaskPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if err := p.PollOnce(ctx, workerID, time.Now()); err != nil && !errors.Is(err, context.Canceled) {
			logger.LegacyPrintf("service.video_task_poller", "poll failed: %v", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *VideoTaskPoller) pollClaimedTask(ctx context.Context, task *VideoTask, now time.Time) error {
	account, err := p.accountLookup.GetByID(ctx, task.AccountID)
	if err != nil {
		return fmt.Errorf("video task %s account %d: %w", task.PublicTaskID, task.AccountID, err)
	}
	if account == nil {
		return fmt.Errorf("video task %s account %d not found", task.PublicTaskID, task.AccountID)
	}

	result, err := p.provider.Fetch(ctx, account, task)
	if err != nil {
		return err
	}
	if result == nil {
		return errors.New("video task provider returned nil fetch result")
	}

	status := result.Status
	if status == "" {
		status = task.Status
	}
	completedAt := result.CompletedAt
	if completedAt == nil && status.Terminal() {
		completedAt = &now
	}
	update := VideoTaskProviderUpdate{
		Status:         status,
		ProviderStatus: result.ProviderStatus,
		ResponseBody:   result.RawBody,
		Metadata:       result.Metadata,
		ErrorMessage:   result.ErrorMessage,
		CompletedAt:    completedAt,
		ExpiresAt:      result.ExpiresAt,
	}
	if status.Terminal() {
		update.ClearNextPollAt = true
	} else {
		nextPollAt := now.Add(nextVideoPollDelay(task.PollAttempts))
		update.NextPollAt = &nextPollAt
	}

	return p.repo.UpdateFromProvider(ctx, task.PublicTaskID, update)
}

func defaultVideoTaskPollerWorkerID() string {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown-host"
	}
	return fmt.Sprintf("video-task-poller-%s-%d-%d", hostname, os.Getpid(), time.Now().UnixNano())
}
