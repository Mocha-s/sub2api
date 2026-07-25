package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const (
	videoTaskPollLimit         = 20
	videoTaskPollLockTTL       = 2 * time.Minute
	videoTaskPollFetchMargin   = 10 * time.Second
	videoTaskPollFetchTimeout  = videoTaskPollLockTTL - videoTaskPollFetchMargin
	videoTaskPollInterval      = 5 * time.Second
	videoTaskPollerStopMaxWait = 5 * time.Second
	videoTaskPollClaimTimeout  = videoTaskPollerStopMaxWait
)

// VideoTaskPoller reclaims due video tasks and refreshes their provider status.
type VideoTaskPoller struct {
	repo          VideoTaskRepository
	accountLookup videoTaskAccountLookup
	provider      VideoTaskProvider
	settlement    videoTaskPersistedSettlementReconciler

	workerID string
	interval time.Duration
	now      func() time.Time

	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	stopping bool

	activeClaims int
	claimsDone   chan struct{}

	activePersistence int
	persistenceDone   chan struct{}
	persistenceSeq    uint64
	persistenceCancel map[uint64]context.CancelFunc

	// beforePersistenceAdmission is a deterministic unit-test hook.
	beforePersistenceAdmission func()
}

func NewVideoTaskPoller(repo VideoTaskRepository, accountRepo AccountRepository, provider VideoTaskProvider, settlement ...videoTaskPersistedSettlementReconciler) *VideoTaskPoller {
	return newVideoTaskPoller(repo, accountRepo, provider, settlement...)
}

func newVideoTaskPoller(repo VideoTaskRepository, accountLookup videoTaskAccountLookup, provider VideoTaskProvider, settlement ...videoTaskPersistedSettlementReconciler) *VideoTaskPoller {
	if provider == nil {
		provider = NewOpenAICompatibleVideoProvider(nil)
	}
	poller := &VideoTaskPoller{
		repo:          repo,
		accountLookup: accountLookup,
		provider:      provider,
		workerID:      defaultVideoTaskPollerWorkerID(),
		interval:      videoTaskPollInterval,
		now:           time.Now,
	}
	if len(settlement) > 0 {
		poller.settlement = settlement[0]
	}
	return poller
}

func (p *VideoTaskPoller) Start() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.cancel != nil {
		p.mu.Unlock()
		return
	}
	p.stopping = false
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
	p.stopping = true
	cancel := p.cancel
	done := p.done
	claimsDone := p.claimsDone
	persistenceDone := p.persistenceDone
	p.mu.Unlock()
	if done == nil && claimsDone == nil && persistenceDone == nil {
		if cancel != nil {
			cancel()
		}
		return
	}

	timer := time.NewTimer(videoTaskPollerStopMaxWait)
	defer timer.Stop()
	if cancel != nil {
		cancel()
	}
	if persistenceDone != nil {
		p.cancelActivePersistence()
		select {
		case <-persistenceDone:
		case <-timer.C:
			return
		}
	}
	for done != nil || claimsDone != nil {
		select {
		case <-done:
			done = nil
		case <-claimsDone:
			claimsDone = nil
		case <-timer.C:
			return
		}
	}
}

func (p *VideoTaskPoller) PollOnce(ctx context.Context, _ string, now time.Time) error {
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
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if now.IsZero() {
		now = p.currentTime()
	}
	if !p.beginClaim() {
		return nil
	}
	leaseToken, err := generateVideoTaskPollLeaseToken()
	if err != nil {
		p.finishClaim()
		return fmt.Errorf("generate video task poll lease token: %w", err)
	}

	claimCtx, cancelClaim := context.WithTimeout(ctx, videoTaskPollClaimTimeout)
	tasks, err := p.repo.ClaimDuePollTasks(claimCtx, now, videoTaskPollLimit, leaseToken, videoTaskPollLockTTL)
	cancelClaim()
	p.finishClaim()
	if err != nil {
		return err
	}
	if p.isStopping() {
		return p.releaseClaimedTasks(tasks, leaseToken)
	}

	var errs []error
	for index, task := range tasks {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), videoTaskPersistenceTimeout)
			for _, unvisited := range tasks[index:] {
				if unvisited == nil {
					continue
				}
				if _, releaseErr := p.releasePollLockWithContext(cleanupCtx, unvisited.PublicTaskID, leaseToken); releaseErr != nil {
					errs = append(errs, fmt.Errorf("video task poll %s release lock: %w", unvisited.PublicTaskID, releaseErr))
				}
			}
			cancelCleanup()
			return errors.Join(errs...)
		}
		if task == nil {
			errs = append(errs, errors.New("claimed video task is nil"))
			continue
		}
		leaseLost, err := p.pollClaimedTask(ctx, task, leaseToken)
		if err != nil {
			errs = append(errs, fmt.Errorf("video task poll %s: %w", task.PublicTaskID, err))
		}
		if leaseLost {
			continue
		}
		_, err = p.releasePollLock(ctx, task.PublicTaskID, leaseToken)
		if err != nil {
			errs = append(errs, fmt.Errorf("video task poll %s release lock: %w", task.PublicTaskID, err))
		}
	}
	return errors.Join(errs...)
}

func (p *VideoTaskPoller) beginClaim() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopping {
		return false
	}
	if p.activeClaims == 0 {
		p.claimsDone = make(chan struct{})
	}
	p.activeClaims++
	return true
}

func (p *VideoTaskPoller) finishClaim() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.activeClaims--
	if p.activeClaims == 0 {
		close(p.claimsDone)
		p.claimsDone = nil
	}
}

func (p *VideoTaskPoller) releaseClaimedTasks(tasks []*VideoTask, leaseToken string) error {
	cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), videoTaskPersistenceTimeout)
	defer cancelCleanup()
	var errs []error
	for _, task := range tasks {
		if task == nil {
			continue
		}
		if _, err := p.releasePollLockWithContext(cleanupCtx, task.PublicTaskID, leaseToken); err != nil {
			errs = append(errs, fmt.Errorf("video task poll %s release lock: %w", task.PublicTaskID, err))
		}
	}
	return errors.Join(errs...)
}

func (p *VideoTaskPoller) releasePollLock(ctx context.Context, publicTaskID, leaseToken string) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), videoTaskPersistenceTimeout)
	defer cancel()
	return p.releasePollLockWithContext(releaseCtx, publicTaskID, leaseToken)
}

func (p *VideoTaskPoller) releasePollLockWithContext(ctx context.Context, publicTaskID, leaseToken string) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return p.repo.ReleasePollLock(ctx, publicTaskID, leaseToken)
}

func (p *VideoTaskPoller) renewPollLock(ctx context.Context, publicTaskID, leaseToken string, validAt time.Time) (bool, error) {
	return p.admitPersistence(ctx, func(renewCtx context.Context) (bool, error) {
		return p.repo.RenewPollLock(renewCtx, publicTaskID, leaseToken, validAt, videoTaskPollLockTTL)
	})
}

func (p *VideoTaskPoller) run(ctx context.Context, done chan struct{}, workerID string, interval time.Duration) {
	defer func() {
		p.mu.Lock()
		if p.done == done {
			p.cancel = nil
			p.done = nil
		}
		p.mu.Unlock()
		close(done)
	}()
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

func (p *VideoTaskPoller) pollClaimedTask(ctx context.Context, task *VideoTask, leaseToken string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	account, err := p.accountLookup.GetByID(ctx, task.AccountID)
	if err != nil {
		return p.rescheduleAfterPollError(ctx, task, leaseToken, p.currentTime(), fmt.Errorf("video task %s account %d: %w", task.PublicTaskID, task.AccountID, err))
	}
	if account == nil {
		return p.rescheduleAfterPollError(ctx, task, leaseToken, p.currentTime(), fmt.Errorf("video task %s account %d not found", task.PublicTaskID, task.AccountID))
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	renewed, err := p.renewPollLock(ctx, task.PublicTaskID, leaseToken, p.currentTime())
	if err != nil {
		return false, err
	}
	if !renewed {
		if p.isStopping() {
			if _, releaseErr := p.releasePollLock(ctx, task.PublicTaskID, leaseToken); releaseErr != nil {
				return true, fmt.Errorf("release stopped video task poll lease: %w", releaseErr)
			}
		}
		return true, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}

	fetchCtx, cancelFetch := context.WithTimeout(ctx, videoTaskPollFetchTimeout)
	defer cancelFetch()
	result, err := p.provider.Fetch(fetchCtx, account, task)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err != nil {
		return p.rescheduleAfterPollError(ctx, task, leaseToken, p.currentTime(), err)
	}
	if result == nil {
		return p.rescheduleAfterPollError(ctx, task, leaseToken, p.currentTime(), errors.New("video task provider returned nil fetch result"))
	}

	completedAt := p.currentTime()
	status := result.Status
	if status == "" {
		status = task.Status
	}
	providerCompletedAt := result.CompletedAt
	if providerCompletedAt == nil && status.Terminal() {
		providerCompletedAt = &completedAt
	}
	update := VideoTaskProviderUpdate{
		Status:         status,
		ProviderStatus: result.ProviderStatus,
		ResponseBody:   result.RawBody,
		Metadata:       result.Metadata,
		ErrorMessage:   result.ErrorMessage,
		CompletedAt:    providerCompletedAt,
		ExpiresAt:      result.ExpiresAt,
	}
	if status.Terminal() {
		update.ClearNextPollAt = true
	} else {
		nextPollAt := completedAt.Add(nextVideoPollDelay(task.PollAttempts))
		update.NextPollAt = &nextPollAt
	}

	applied, err := p.persistProviderUpdate(ctx, task.PublicTaskID, leaseToken, completedAt, update)
	if err != nil {
		return false, err
	}
	if !applied {
		return true, nil
	}
	if status == VideoTaskStatusFailed && p.settlement != nil {
		persisted, reloadErr := p.repo.GetByPublicTaskID(ctx, task.PublicTaskID)
		if reloadErr != nil {
			return false, reloadErr
		}
		if persisted != nil && persisted.Status == VideoTaskStatusFailed {
			if reconcileErr := p.settlement.ReconcilePersistedTask(ctx, persisted.PublicTaskID); reconcileErr != nil {
				return false, errors.Join(ErrVideoTaskSettlementRetriable, reconcileErr)
			}
		}
	}
	return false, nil
}

func (p *VideoTaskPoller) rescheduleAfterPollError(ctx context.Context, task *VideoTask, leaseToken string, completedAt time.Time, pollErr error) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	nextPollAt := completedAt.Add(nextVideoPollDelay(task.PollAttempts))
	applied, err := p.persistProviderUpdate(ctx, task.PublicTaskID, leaseToken, completedAt, VideoTaskProviderUpdate{NextPollAt: &nextPollAt})
	if err != nil {
		return false, errors.Join(pollErr, fmt.Errorf("schedule next poll: %w", err))
	}
	if !applied {
		return true, nil
	}
	return false, pollErr
}

func (p *VideoTaskPoller) persistProviderUpdate(ctx context.Context, publicTaskID, leaseToken string, completedAt time.Time, update VideoTaskProviderUpdate) (bool, error) {
	return p.admitPersistence(ctx, func(updateCtx context.Context) (bool, error) {
		return p.repo.UpdateFromProviderWithPollLease(updateCtx, publicTaskID, leaseToken, completedAt, update)
	})
}

// admitPersistence serializes non-cleanup writes with Stop so a detached context cannot outlive cancellation.
func (p *VideoTaskPoller) admitPersistence(ctx context.Context, persist func(context.Context) (bool, error)) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	admitted, persistenceID, updateCtx, cancelUpdate, err := p.beginPersistence(ctx)
	if err != nil {
		return false, err
	}
	if !admitted {
		return false, nil
	}
	defer p.finishPersistence(persistenceID)
	defer cancelUpdate()
	return persist(updateCtx)
}

func (p *VideoTaskPoller) beginPersistence(ctx context.Context) (bool, uint64, context.Context, context.CancelFunc, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, 0, nil, nil, err
	}
	if p.stopping {
		return false, 0, nil, nil, nil
	}
	if p.activePersistence == 0 {
		p.persistenceDone = make(chan struct{})
	}
	p.activePersistence++
	p.persistenceSeq++
	persistenceID := p.persistenceSeq
	updateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), videoTaskPersistenceTimeout)
	if p.persistenceCancel == nil {
		p.persistenceCancel = map[uint64]context.CancelFunc{}
	}
	p.persistenceCancel[persistenceID] = cancel
	if p.beforePersistenceAdmission != nil {
		p.beforePersistenceAdmission()
	}
	return true, persistenceID, updateCtx, cancel, nil
}

func (p *VideoTaskPoller) finishPersistence(persistenceID uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.persistenceCancel, persistenceID)
	p.activePersistence--
	if p.activePersistence == 0 {
		close(p.persistenceDone)
		p.persistenceDone = nil
	}
}

func (p *VideoTaskPoller) cancelActivePersistence() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, cancel := range p.persistenceCancel {
		cancel()
	}
}

func (p *VideoTaskPoller) currentTime() time.Time {
	if p != nil && p.now != nil {
		return p.now()
	}
	return time.Now()
}

func (p *VideoTaskPoller) isStopping() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopping
}

func generateVideoTaskPollLeaseToken() (string, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

func defaultVideoTaskPollerWorkerID() string {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown-host"
	}
	return fmt.Sprintf("video-task-poller-%s-%d-%d", hostname, os.Getpid(), time.Now().UnixNano())
}
