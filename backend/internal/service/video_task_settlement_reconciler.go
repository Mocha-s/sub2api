package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const (
	videoTaskSettlementReconcileLimit        = 1
	videoTaskSettlementReconcileLeaseTTL     = 2 * time.Minute
	videoTaskSettlementReconcileWorkTimeout  = 30 * time.Second
	videoTaskSettlementLeaseSafetyMargin     = 5 * time.Second
	videoTaskSettlementReconcileInterval     = 5 * time.Second
	videoTaskSettlementReconcilerStopMaxWait = 5 * time.Second
	videoTaskCacheInvalidationMaxAttempts    = 3
	videoTaskAdmissionOrphanGrace            = 2 * time.Minute
)

type VideoTaskSettlementReconcileClaim struct {
	PublicTaskID string
	LeaseToken   string
	Attempts     int
}

type videoTaskPersistedSettlementReconciler interface {
	ReconcilePersistedTask(context.Context, string) error
}

type videoTaskRefundReportingProcessor interface {
	ProcessRefundReporting(context.Context, VideoTaskRefundReportingClaim) error
}

type videoTaskCacheInvalidationProcessor interface {
	ProcessCacheInvalidation(context.Context, VideoTaskCacheInvalidationClaim) error
}

type VideoTaskSettlementReconciler struct {
	repo        VideoTaskSettlementRepository
	hook        videoTaskPersistedSettlementReconciler
	reporting   videoTaskRefundReportingProcessor
	cacheJobs   videoTaskCacheInvalidationProcessor
	now         func() time.Time
	logf        func(string, ...any)
	workTimeout time.Duration

	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	stopping bool
	active   map[uint64]context.CancelFunc
	seq      uint64
}

func NewVideoTaskSettlementReconciler(repo VideoTaskSettlementRepository, settlement *VideoTaskSettlementService) *VideoTaskSettlementReconciler {
	return newVideoTaskSettlementReconciler(repo, settlement)
}

func newVideoTaskSettlementReconciler(repo VideoTaskSettlementRepository, hook videoTaskPersistedSettlementReconciler) *VideoTaskSettlementReconciler {
	worker := &VideoTaskSettlementReconciler{repo: repo, hook: hook, now: time.Now, workTimeout: videoTaskSettlementReconcileWorkTimeout, logf: func(format string, args ...any) {
		logger.LegacyPrintf("service.video_task_settlement_reconciler", format, args...)
	}, active: map[uint64]context.CancelFunc{}}
	worker.reporting, _ = hook.(videoTaskRefundReportingProcessor)
	worker.cacheJobs, _ = hook.(videoTaskCacheInvalidationProcessor)
	return worker
}

func (r *VideoTaskSettlementReconciler) Start() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.cancel != nil || r.stopping {
		r.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	r.cancel, r.done = cancel, done
	r.mu.Unlock()
	go r.run(ctx, done)
}

func (r *VideoTaskSettlementReconciler) Stop() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.stopping = true
	cancel, done := r.cancel, r.done
	for _, activeCancel := range r.active {
		activeCancel()
	}
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		timer := time.NewTimer(videoTaskSettlementReconcilerStopMaxWait)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
		}
	}
}

func (r *VideoTaskSettlementReconciler) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(videoTaskSettlementReconcileInterval)
	defer ticker.Stop()
	for {
		if err := r.ReconcileOnce(ctx, r.now()); err != nil && !errors.Is(err, context.Canceled) {
			r.log("reconcile iteration failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *VideoTaskSettlementReconciler) ReconcileOnce(ctx context.Context, now time.Time) error {
	if r == nil || r.repo == nil || r.hook == nil {
		return errors.New("video task settlement reconciler is not configured")
	}
	r.mu.Lock()
	if r.stopping {
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()
	backgroundErr := errors.Join(r.ReconcileAdmissionOrphansOnce(ctx, now), r.ReconcileRefundReportingOnce(ctx, now), r.ReconcileCacheInvalidationOnce(ctx, now))
	token, err := generateVideoTaskSettlementLeaseToken()
	if err != nil {
		r.log("lease token generation failed: %v", err)
		return errors.Join(backgroundErr, err)
	}
	claims, err := r.repo.ClaimDueReconciliation(ctx, now, videoTaskSettlementReconcileLimit, token, videoTaskSettlementReconcileLeaseTTL)
	if err != nil {
		r.log("claim due reconciliation failed: %v", err)
		return err
	}
	var errs []error
	for _, claim := range claims {
		claimToken := claim.LeaseToken
		if claimToken == "" {
			claimToken = token
		}
		workCtx, finish, admitted := r.begin(ctx)
		if !admitted {
			break
		}
		lockedUntil, renewed, renewErr := r.repo.RenewSettlementClaim(workCtx, claim.PublicTaskID, claimToken, videoTaskSettlementReconcileLeaseTTL)
		if renewErr != nil || !renewed {
			finish()
			if renewErr == nil {
				renewErr = ErrVideoTaskSettlementLeaseLost
			}
			errs = append(errs, renewErr)
			r.log("task %s lease renewal failed token=%s: %v", claim.PublicTaskID, claimToken, renewErr)
			continue
		}
		workTimeout := r.workTimeout
		if workTimeout <= 0 || workTimeout >= videoTaskSettlementReconcileLeaseTTL {
			workTimeout = videoTaskSettlementReconcileWorkTimeout
		}
		leaseWorkTime := time.Until(lockedUntil) - videoTaskSettlementLeaseSafetyMargin
		var reconcileErr error
		if leaseWorkTime <= 0 {
			reconcileErr = ErrVideoTaskSettlementLeaseInsufficient
			r.log("task %s renewed lease has insufficient remaining time token=%s locked_until=%s", claim.PublicTaskID, claimToken, lockedUntil)
		} else {
			deadline := time.Now().Add(workTimeout)
			leaseDeadline := lockedUntil.Add(-videoTaskSettlementLeaseSafetyMargin)
			if leaseDeadline.Before(deadline) {
				deadline = leaseDeadline
			}
			hookCtx, cancelHook := context.WithDeadline(workCtx, deadline)
			reconcileErr = r.hook.ReconcilePersistedTask(hookCtx, claim.PublicTaskID)
			cancelHook()
		}
		var applied bool
		if reconcileErr == nil {
			applied, err = r.repo.CompleteReconciliation(workCtx, claim.PublicTaskID, claimToken)
		} else {
			next := now.Add(videoTaskSettlementRetryDelay(claim.Attempts))
			applied, err = r.repo.RetryReconciliation(workCtx, claim.PublicTaskID, claimToken, reconcileErr.Error(), next)
		}
		finish()
		if err == nil && !applied {
			err = ErrVideoTaskSettlementLeaseLost
		}
		if reconcileErr != nil || err != nil {
			itemErr := errors.Join(reconcileErr, err)
			errs = append(errs, itemErr)
			r.log("task %s reconciliation persistence failed token=%s: %v", claim.PublicTaskID, claimToken, itemErr)
		}
	}
	return errors.Join(append([]error{backgroundErr}, errs...)...)
}

func (r *VideoTaskSettlementReconciler) ReconcileAdmissionOrphansOnce(ctx context.Context, now time.Time) error {
	if r == nil || r.repo == nil {
		return nil
	}
	token, err := generateVideoTaskSettlementLeaseToken()
	if err != nil {
		return err
	}
	claims, err := r.repo.ClaimDueAdmissionOrphans(ctx, now, videoTaskAdmissionOrphanGrace, videoTaskSettlementReconcileLimit, token, videoTaskSettlementReconcileLeaseTTL)
	if err != nil {
		return err
	}
	var errs []error
	for _, claim := range claims {
		claimToken := claim.LeaseToken
		if claimToken == "" {
			claimToken = token
		}
		applied, failErr := r.repo.FailAdmissionOrphan(ctx, claim.PublicTaskID, claimToken, VideoTaskAdmissionInterruptedCode, VideoTaskAdmissionInterruptedMessage)
		if failErr == nil && !applied {
			failErr = ErrVideoTaskSettlementLeaseLost
		}
		if failErr != nil {
			errs = append(errs, failErr)
			r.log("task %s admission orphan terminalization failed token=%s: %v", claim.PublicTaskID, claimToken, failErr)
		}
	}
	return errors.Join(errs...)
}

func (r *VideoTaskSettlementReconciler) ReconcileCacheInvalidationOnce(ctx context.Context, now time.Time) error {
	if r == nil || r.repo == nil || r.cacheJobs == nil {
		return nil
	}
	token, err := generateVideoTaskSettlementLeaseToken()
	if err != nil {
		return err
	}
	var errs []error
	for processed := 0; processed < 16; processed++ {
		claims, err := r.repo.ClaimDueCacheInvalidation(ctx, videoTaskSettlementReconcileLimit, token, videoTaskSettlementReconcileLeaseTTL)
		if err != nil {
			errs = append(errs, err)
			return errors.Join(errs...)
		}
		if len(claims) == 0 {
			break
		}
		claim := claims[0]
		claim.LeaseToken = token
		workCtx, cancel := context.WithTimeout(ctx, r.workTimeout)
		processErr := r.cacheJobs.ProcessCacheInvalidation(workCtx, claim)
		cancel()
		var applied bool
		if processErr == nil {
			applied, err = r.repo.CompleteCacheInvalidation(ctx, claim.JobID, token)
		} else if claim.Attempts >= videoTaskCacheInvalidationMaxAttempts && strings.Contains(processErr.Error(), "cache invalidation payload") {
			applied, err = r.repo.DeadLetterCacheInvalidation(ctx, claim.JobID, token, processErr.Error())
		} else {
			applied, err = r.repo.RetryCacheInvalidation(ctx, claim.JobID, token, processErr.Error(), now.Add(videoTaskSettlementRetryDelay(claim.Attempts)))
		}
		if err == nil && !applied {
			err = ErrVideoTaskSettlementLeaseLost
		}
		if processErr != nil || err != nil {
			errs = append(errs, errors.Join(processErr, err))
		}
	}
	return errors.Join(errs...)
}

func (r *VideoTaskSettlementReconciler) ReconcileRefundReportingOnce(ctx context.Context, now time.Time) error {
	if r == nil || r.repo == nil || r.reporting == nil {
		return nil
	}
	token, err := generateVideoTaskSettlementLeaseToken()
	if err != nil {
		return err
	}
	claims, err := r.repo.ClaimDueRefundReporting(ctx, now, videoTaskSettlementReconcileLimit, token, videoTaskSettlementReconcileLeaseTTL)
	if err != nil {
		return err
	}
	var errs []error
	for _, claim := range claims {
		claimToken := claim.LeaseToken
		if claimToken == "" {
			claimToken = token
		}
		claim.LeaseToken = claimToken
		workTimeout := r.workTimeout
		if workTimeout <= 0 || workTimeout >= videoTaskSettlementReconcileLeaseTTL {
			workTimeout = videoTaskSettlementReconcileWorkTimeout
		}
		workCtx, cancel := context.WithTimeout(ctx, workTimeout)
		processErr := r.reporting.ProcessRefundReporting(workCtx, claim)
		cancel()
		var applied bool
		if processErr == nil {
			applied, err = r.repo.CompleteRefundReporting(ctx, claim.JobID, claimToken)
		} else {
			applied, err = r.repo.RetryRefundReporting(ctx, claim.JobID, claimToken, processErr.Error(), now.Add(videoTaskSettlementRetryDelay(claim.Attempts)))
		}
		if err == nil && !applied {
			err = ErrVideoTaskSettlementLeaseLost
		}
		if processErr != nil || err != nil {
			errs = append(errs, errors.Join(processErr, err))
		}
	}
	return errors.Join(errs...)
}

func (r *VideoTaskSettlementReconciler) log(format string, args ...any) {
	if r != nil && r.logf != nil {
		r.logf(format, args...)
	}
}

func (r *VideoTaskSettlementReconciler) begin(ctx context.Context) (context.Context, func(), bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopping || ctx.Err() != nil {
		return nil, func() {}, false
	}
	r.seq++
	id := r.seq
	workCtx, cancel := context.WithCancel(ctx)
	r.active[id] = cancel
	return workCtx, func() {
		r.mu.Lock()
		delete(r.active, id)
		r.mu.Unlock()
		cancel()
	}, true
}

func videoTaskSettlementRetryDelay(attempts int) time.Duration {
	if attempts < 0 {
		attempts = 0
	}
	delay := 5 * time.Second
	for i := 0; i < attempts && delay < 10*time.Minute; i++ {
		delay *= 2
	}
	if delay > 10*time.Minute {
		return 10 * time.Minute
	}
	return delay
}

func generateVideoTaskSettlementLeaseToken() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate settlement lease token: %w", err)
	}
	return "video-settlement-" + hex.EncodeToString(raw[:]), nil
}

// ReconcilePersistedTask settles only the state that won durable task persistence.
func (s *VideoTaskSettlementService) ReconcilePersistedTask(ctx context.Context, publicTaskID string) error {
	if s == nil || s.repo == nil || s.tasks == nil {
		return errors.New("video task settlement service is not configured")
	}
	publicTaskID = strings.TrimSpace(publicTaskID)
	task, err := s.tasks.GetByPublicTaskID(ctx, publicTaskID)
	if err != nil {
		return err
	}
	settlement, err := s.repo.GetByPublicTaskID(ctx, publicTaskID)
	if err != nil {
		if errors.Is(err, ErrVideoTaskSettlementNotFound) {
			return nil
		}
		return err
	}
	if task == nil || settlement == nil {
		return nil
	}
	if task.Status == VideoTaskStatusFailed {
		var result *VideoTaskSettlementResult
		switch settlement.State {
		case VideoTaskSettlementReserved:
			result, err = s.repo.ReleaseFailed(ctx, &VideoTaskSettlementReleaseCommand{PublicTaskID: publicTaskID})
		case VideoTaskSettlementCharged:
			reason := strings.TrimSpace(task.ErrorMessage)
			if reason == "" {
				reason = "video task provider failed"
			}
			result, err = s.repo.RefundFailed(ctx, &VideoTaskSettlementRefundCommand{PublicTaskID: publicTaskID, Reason: reason})
		}
		if err != nil {
			return err
		}
		s.afterCommit(ctx, result)
		return nil
	}

	if settlement.State == VideoTaskSettlementCharged {
		result, err := s.repo.RepairChargedUsage(ctx, publicTaskID)
		if err != nil {
			return err
		}
		s.afterCommit(ctx, result)
	}
	if task.Status == VideoTaskStatusCancelled || task.Status == VideoTaskStatusExpired {
		return nil
	}
	if settlement.State == VideoTaskSettlementReserved {
		_, err := s.Reconcile(ctx, task)
		return err
	}
	return nil
}
