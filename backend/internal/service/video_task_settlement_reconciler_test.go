//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestVideoTaskSettlementServiceReconcilePersistedTaskUsesPersistedState(t *testing.T) {
	for _, tt := range []struct {
		name        string
		taskStatus  VideoTaskStatus
		settlement  VideoTaskSettlementState
		wantRelease int
		wantRefund  int
		wantRepair  int
	}{
		{name: "failed reserved releases", taskStatus: VideoTaskStatusFailed, settlement: VideoTaskSettlementReserved, wantRelease: 1},
		{name: "failed charged refunds before usage repair", taskStatus: VideoTaskStatusFailed, settlement: VideoTaskSettlementCharged, wantRefund: 1},
		{name: "completed charged repairs usage", taskStatus: VideoTaskStatusCompleted, settlement: VideoTaskSettlementCharged, wantRepair: 1},
		{name: "cancelled charged repairs usage", taskStatus: VideoTaskStatusCancelled, settlement: VideoTaskSettlementCharged, wantRepair: 1},
		{name: "expired charged repairs usage", taskStatus: VideoTaskStatusExpired, settlement: VideoTaskSettlementCharged, wantRepair: 1},
		{name: "cancelled reserved remains reserved", taskStatus: VideoTaskStatusCancelled, settlement: VideoTaskSettlementReserved},
		{name: "expired reserved remains reserved", taskStatus: VideoTaskStatusExpired, settlement: VideoTaskSettlementReserved},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tasks := newFakeVideoTaskRepository(nil)
			tasks.seedTask(&VideoTask{PublicTaskID: "task_1", Status: tt.taskStatus, ErrorMessage: "provider failed"})
			repo := &reconcileSettlementRepositoryFake{snapshot: &VideoTaskSettlementSnapshot{PublicTaskID: "task_1", State: tt.settlement}}
			svc := NewVideoTaskSettlementService(repo, tasks, nil, nil, nil, nil, nil)

			if err := svc.ReconcilePersistedTask(context.Background(), "task_1"); err != nil {
				t.Fatalf("ReconcilePersistedTask returned error: %v", err)
			}
			if repo.releaseCalls != tt.wantRelease || repo.refundCalls != tt.wantRefund {
				t.Fatalf("effects release=%d refund=%d, want release=%d refund=%d", repo.releaseCalls, repo.refundCalls, tt.wantRelease, tt.wantRefund)
			}
			if repo.repairCalls != tt.wantRepair {
				t.Fatalf("repair calls = %d, want %d", repo.repairCalls, tt.wantRepair)
			}
		})
	}
}

func TestVideoTaskSettlementServiceReconcilePersistedTaskRefundsFailedChargeBeforeUsageRepair(t *testing.T) {
	tasks := newFakeVideoTaskRepository(nil)
	tasks.seedTask(&VideoTask{PublicTaskID: "task_failed_charge", Status: VideoTaskStatusFailed, ErrorMessage: "provider failed"})
	repairErr := errors.New("usage integrity drift")
	repo := &reconcileSettlementRepositoryFake{
		snapshot:  &VideoTaskSettlementSnapshot{PublicTaskID: "task_failed_charge", State: VideoTaskSettlementCharged},
		repairErr: repairErr,
	}
	svc := NewVideoTaskSettlementService(repo, tasks, nil, nil, nil, nil, nil)

	if err := svc.ReconcilePersistedTask(context.Background(), "task_failed_charge"); err != nil {
		t.Fatalf("ReconcilePersistedTask returned error: %v", err)
	}
	if repo.refundCalls != 1 {
		t.Fatalf("refund calls = %d, want 1", repo.refundCalls)
	}
	if repo.repairCalls != 0 {
		t.Fatalf("repair calls = %d, want 0", repo.repairCalls)
	}
}

func TestVideoTaskSettlementReconcilerPersistsFencedSuccessAndRetry(t *testing.T) {
	now := time.Date(2026, 7, 12, 4, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name      string
		reconcile error
		wantOK    int
		wantFail  int
	}{
		{name: "success", wantOK: 1},
		{name: "retry", reconcile: errors.New("billing unavailable"), wantFail: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := &reconcilerRepositoryFake{claims: []VideoTaskSettlementReconcileClaim{{PublicTaskID: "task_1", LeaseToken: "token_1", Attempts: 2}}}
			hook := &reconcileHookFake{err: tt.reconcile}
			worker := newVideoTaskSettlementReconciler(repo, hook)
			worker.now = func() time.Time { return now }

			err := worker.ReconcileOnce(context.Background(), now)
			if tt.reconcile == nil && err != nil {
				t.Fatalf("ReconcileOnce returned error: %v", err)
			}
			if tt.reconcile != nil && !errors.Is(err, tt.reconcile) {
				t.Fatalf("ReconcileOnce error = %v, want %v", err, tt.reconcile)
			}
			if repo.successes != tt.wantOK || repo.failures != tt.wantFail {
				t.Fatalf("successes=%d failures=%d, want %d/%d", repo.successes, repo.failures, tt.wantOK, tt.wantFail)
			}
			if repo.lastToken != "token_1" {
				t.Fatalf("persistence token = %q, want token_1", repo.lastToken)
			}
			if tt.reconcile != nil && (repo.lastError != tt.reconcile.Error() || !repo.nextRetry.After(now)) {
				t.Fatalf("retry marker error=%q next=%s", repo.lastError, repo.nextRetry)
			}
		})
	}
}

func TestVideoTaskSettlementReconcilerTerminalizesPristineAdmissionOrphanWithoutSettlementEffects(t *testing.T) {
	now := time.Date(2026, 7, 13, 4, 0, 0, 0, time.UTC)
	repo := &reconcilerRepositoryFake{orphanClaims: []VideoTaskAdmissionOrphanClaim{{PublicTaskID: "task_crashed", LeaseToken: "orphan-token"}}}
	hook := &reconcileHookFake{}
	worker := newVideoTaskSettlementReconciler(repo, hook)

	err := worker.ReconcileOnce(context.Background(), now)

	if err != nil {
		t.Fatalf("ReconcileOnce returned error: %v", err)
	}
	if repo.orphanFailures != 1 || repo.lastToken != "orphan-token" {
		t.Fatalf("orphan failures/token = %d/%q", repo.orphanFailures, repo.lastToken)
	}
	if repo.orphanCode != VideoTaskAdmissionInterruptedCode || repo.orphanMessage != VideoTaskAdmissionInterruptedMessage {
		t.Fatalf("orphan reason = %q/%q", repo.orphanCode, repo.orphanMessage)
	}
	if hook.calls != 0 {
		t.Fatalf("settlement hook calls = %d, want 0", hook.calls)
	}
}

func TestVideoTaskSettlementReconcilerRejectsStaleAdmissionOrphanClaim(t *testing.T) {
	repo := &reconcilerRepositoryFake{
		orphanClaims:  []VideoTaskAdmissionOrphanClaim{{PublicTaskID: "task_crashed", LeaseToken: "stale-token"}},
		orphanApplied: testBoolPtr(false),
	}
	worker := newVideoTaskSettlementReconciler(repo, &reconcileHookFake{})

	err := worker.ReconcileAdmissionOrphansOnce(context.Background(), time.Now())

	if !errors.Is(err, ErrVideoTaskSettlementLeaseLost) {
		t.Fatalf("error = %v, want lease lost", err)
	}
}

func TestVideoTaskSettlementReconcilerProcessesRefundReportingWithFencedCompletionAndRetry(t *testing.T) {
	now := time.Date(2026, 7, 12, 4, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name      string
		process   error
		wantDone  int
		wantRetry int
	}{
		{name: "complete after ordered reporting", wantDone: 1},
		{name: "retry reporting failure", process: errors.New("dashboard unavailable"), wantRetry: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			events := []string{}
			repo := &reconcilerRepositoryFake{reportingClaims: []VideoTaskRefundReportingClaim{{JobID: 9, UsageLogID: 7, UsageCreatedAt: now, LeaseToken: "report-token", Attempts: 2}}, reportingEvents: &events}
			hook := &reconcileHookFake{reportingErr: tt.process, reportingRun: func() { events = append(events, "recompute", "invalidate") }}
			worker := newVideoTaskSettlementReconciler(repo, hook)
			worker.now = func() time.Time { return now }
			err := worker.ReconcileRefundReportingOnce(context.Background(), now)
			if tt.process == nil && err != nil {
				t.Fatalf("ReconcileRefundReportingOnce returned error: %v", err)
			}
			if tt.process != nil && !errors.Is(err, tt.process) {
				t.Fatalf("error=%v want=%v", err, tt.process)
			}
			if repo.reportingDone != tt.wantDone || repo.reportingRetry != tt.wantRetry {
				t.Fatalf("done/retry=%d/%d want=%d/%d", repo.reportingDone, repo.reportingRetry, tt.wantDone, tt.wantRetry)
			}
			if repo.lastToken != "report-token" {
				t.Fatalf("token=%q", repo.lastToken)
			}
			if tt.process == nil && !reflect.DeepEqual(events, []string{"recompute", "invalidate", "done"}) {
				t.Fatalf("events=%v", events)
			}
		})
	}
}

func TestVideoTaskSettlementReconcilerProcessesCacheInvalidationBeforeFencedCompletion(t *testing.T) {
	now := time.Date(2026, 7, 12, 4, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name        string
		err         error
		done, retry int
	}{
		{name: "success", done: 1},
		{name: "redis outage retries", err: errors.New("redis unavailable"), retry: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			events := []string{}
			repo := &reconcilerRepositoryFake{cacheClaims: []VideoTaskCacheInvalidationClaim{{JobID: 3, Attempts: 1}}, cacheEvents: &events}
			hook := &reconcileHookFake{cacheErr: tt.err, cacheRun: func() { events = append(events, "invalidate") }}
			worker := newVideoTaskSettlementReconciler(repo, hook)
			err := worker.ReconcileCacheInvalidationOnce(context.Background(), now)
			if tt.err == nil && err != nil {
				t.Fatal(err)
			}
			if tt.err != nil && !errors.Is(err, tt.err) {
				t.Fatalf("error=%v", err)
			}
			if repo.cacheDone != tt.done || repo.cacheRetry != tt.retry {
				t.Fatalf("done/retry=%d/%d", repo.cacheDone, repo.cacheRetry)
			}
			if tt.err == nil && !reflect.DeepEqual(events, []string{"invalidate", "done"}) {
				t.Fatalf("events=%v", events)
			}
		})
	}
}

func TestVideoTaskSettlementReconcilerDeadLettersPoisonAndContinues(t *testing.T) {
	repo := &reconcilerRepositoryFake{cacheClaims: []VideoTaskCacheInvalidationClaim{
		{JobID: 1, Attempts: videoTaskCacheInvalidationMaxAttempts, Payload: []byte(`{"Version":1`)},
		{JobID: 2, Attempts: 1, Payload: []byte(`{"Version":1,"UserID":7,"Platform":"openai","BillingType":0,"Effects":{}}`)},
	}}
	cache := &settlementInvalidationCacheStub{}
	worker := newVideoTaskSettlementReconciler(repo, &VideoTaskSettlementService{cache: cache})
	err := worker.ReconcileCacheInvalidationOnce(context.Background(), time.Now())
	if err == nil || !strings.Contains(err.Error(), "decode cache invalidation payload") {
		t.Fatalf("error=%v", err)
	}
	if repo.cacheDead != 1 || repo.cacheDone != 1 {
		t.Fatalf("dead/done=%d/%d", repo.cacheDead, repo.cacheDone)
	}
	if !reflect.DeepEqual(cache.balanceUsers, []int64{7}) {
		t.Fatalf("invalidations=%v", cache.balanceUsers)
	}
}

func TestVideoTaskSettlementReconcilerStopCancelsPersistenceAndRejectsNewWork(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	repo := &reconcilerRepositoryFake{claims: []VideoTaskSettlementReconcileClaim{{PublicTaskID: "task_1", LeaseToken: "token_1"}}}
	hook := &reconcileHookFake{run: func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		close(canceled)
		return ctx.Err()
	}}
	worker := newVideoTaskSettlementReconciler(repo, hook)
	done := make(chan struct{})
	go func() {
		_ = worker.ReconcileOnce(context.Background(), time.Now())
		close(done)
	}()
	<-started
	worker.Stop()
	select {
	case <-canceled:
	case <-time.After(videoTaskSettlementReconcilerStopMaxWait + time.Second):
		t.Fatal("active reconciliation was not canceled")
	}
	<-done
	claims := repo.claimCalls
	if err := worker.ReconcileOnce(context.Background(), time.Now()); err != nil {
		t.Fatalf("ReconcileOnce after Stop returned error: %v", err)
	}
	if repo.claimCalls != claims {
		t.Fatalf("claim calls after Stop = %d, want %d", repo.claimCalls, claims)
	}
}

func TestVideoTaskSettlementReconcilerStaleClaimCannotStartEffects(t *testing.T) {
	repo := &reconcilerRepositoryFake{claims: []VideoTaskSettlementReconcileClaim{{PublicTaskID: "task_1", LeaseToken: "stale"}}, renewAllowed: testBoolPtr(false)}
	hook := &reconcileHookFake{}
	worker := newVideoTaskSettlementReconciler(repo, hook)
	err := worker.ReconcileOnce(context.Background(), time.Now())
	if !errors.Is(err, ErrVideoTaskSettlementLeaseLost) {
		t.Fatalf("ReconcileOnce error = %v, want lease lost", err)
	}
	if hook.calls != 0 {
		t.Fatalf("hook calls = %d, want 0", hook.calls)
	}
}

func TestVideoTaskSettlementReconcilerRejectsStaleCompletionAndRetry(t *testing.T) {
	for _, reconcileErr := range []error{nil, errors.New("repair failed")} {
		repo := &reconcilerRepositoryFake{claims: []VideoTaskSettlementReconcileClaim{{PublicTaskID: "task_1", LeaseToken: "old"}}, persistenceApplied: testBoolPtr(false)}
		worker := newVideoTaskSettlementReconciler(repo, &reconcileHookFake{err: reconcileErr})
		err := worker.ReconcileOnce(context.Background(), time.Now())
		if !errors.Is(err, ErrVideoTaskSettlementLeaseLost) {
			t.Fatalf("ReconcileOnce error = %v, want lease lost", err)
		}
	}
}

func TestVideoTaskSettlementReconcilerBoundsClaimProcessing(t *testing.T) {
	repo := &reconcilerRepositoryFake{claims: []VideoTaskSettlementReconcileClaim{{PublicTaskID: "task_1", LeaseToken: "token"}}}
	hook := &reconcileHookFake{run: func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > videoTaskSettlementReconcileWorkTimeout+time.Second {
			return errors.New("missing bounded deadline")
		}
		return nil
	}}
	worker := newVideoTaskSettlementReconciler(repo, hook)
	if err := worker.ReconcileOnce(context.Background(), time.Now()); err != nil {
		t.Fatalf("ReconcileOnce returned error: %v", err)
	}
}

func TestVideoTaskSettlementReconcilerBlockedProcessingTimesOutAndRetries(t *testing.T) {
	repo := &reconcilerRepositoryFake{claims: []VideoTaskSettlementReconcileClaim{{PublicTaskID: "task_blocked", LeaseToken: "token"}}}
	hook := &reconcileHookFake{run: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	worker := newVideoTaskSettlementReconciler(repo, hook)
	worker.workTimeout = 20 * time.Millisecond
	started := time.Now()
	err := worker.ReconcileOnce(context.Background(), started)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ReconcileOnce error = %v, want deadline exceeded", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("blocked reconciliation exceeded bound: %s", time.Since(started))
	}
	if repo.failures != 1 {
		t.Fatalf("retry persistence calls = %d, want 1", repo.failures)
	}
}

func TestVideoTaskSettlementReconcilerDeadlineDoesNotExceedRenewedLease(t *testing.T) {
	leaseUntil := time.Now().Add(12 * time.Second)
	repo := &reconcilerRepositoryFake{claims: []VideoTaskSettlementReconcileClaim{{PublicTaskID: "task_deadline", LeaseToken: "token"}}, renewedUntil: leaseUntil}
	hook := &reconcileHookFake{run: func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok || deadline.After(leaseUntil.Add(-videoTaskSettlementLeaseSafetyMargin)) {
			return fmt.Errorf("deadline %s exceeds safe lease %s", deadline, leaseUntil)
		}
		return nil
	}}
	worker := newVideoTaskSettlementReconciler(repo, hook)
	if err := worker.ReconcileOnce(context.Background(), time.Now()); err != nil {
		t.Fatalf("ReconcileOnce returned error: %v", err)
	}
}

func TestVideoTaskSettlementReconcilerInsufficientLeaseSkipsEffectsAndRetries(t *testing.T) {
	repo := &reconcilerRepositoryFake{claims: []VideoTaskSettlementReconcileClaim{{PublicTaskID: "task_short", LeaseToken: "token"}}, renewedUntil: time.Now().Add(videoTaskSettlementLeaseSafetyMargin / 2)}
	hook := &reconcileHookFake{}
	worker := newVideoTaskSettlementReconciler(repo, hook)
	err := worker.ReconcileOnce(context.Background(), time.Now())
	if !errors.Is(err, ErrVideoTaskSettlementLeaseInsufficient) {
		t.Fatalf("ReconcileOnce error = %v, want insufficient lease", err)
	}
	if hook.calls != 0 {
		t.Fatalf("hook calls = %d, want 0", hook.calls)
	}
	if repo.failures != 1 {
		t.Fatalf("retry calls = %d, want 1", repo.failures)
	}
}

func TestVideoTaskSettlementReconcilerLogsClaimAndTaskPersistenceErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		repo *reconcilerRepositoryFake
		want string
	}{
		{name: "claim", repo: &reconcilerRepositoryFake{claimErr: errors.New("claim unavailable")}, want: "claim unavailable"},
		{name: "completion", repo: &reconcilerRepositoryFake{claims: []VideoTaskSettlementReconcileClaim{{PublicTaskID: "task_log", LeaseToken: "token"}}, persistenceErr: errors.New("completion unavailable")}, want: "task_log"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			worker := newVideoTaskSettlementReconciler(tt.repo, &reconcileHookFake{})
			var logs []string
			worker.logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
			_ = worker.ReconcileOnce(context.Background(), time.Now())
			joined := strings.Join(logs, "\n")
			if !strings.Contains(joined, tt.want) {
				t.Fatalf("logs = %q, want %q", joined, tt.want)
			}
		})
	}
}

type reconcileHookFake struct {
	err          error
	run          func(context.Context) error
	calls        int
	reportingErr error
	reportingRun func()
	cacheErr     error
	cacheRun     func()
}

func (f *reconcileHookFake) ProcessCacheInvalidation(context.Context, VideoTaskCacheInvalidationClaim) error {
	if f.cacheRun != nil {
		f.cacheRun()
	}
	return f.cacheErr
}

func (f *reconcileHookFake) ProcessRefundReporting(context.Context, VideoTaskRefundReportingClaim) error {
	if f.reportingRun != nil {
		f.reportingRun()
	}
	return f.reportingErr
}

func (f *reconcileHookFake) ReconcilePersistedTask(ctx context.Context, _ string) error {
	f.calls++
	if f.run != nil {
		return f.run(ctx)
	}
	return f.err
}

type reconcilerRepositoryFake struct {
	VideoTaskSettlementRepository
	claims             []VideoTaskSettlementReconcileClaim
	claimCalls         int
	successes          int
	failures           int
	lastToken          string
	lastError          string
	nextRetry          time.Time
	renewAllowed       *bool
	persistenceApplied *bool
	claimErr           error
	persistenceErr     error
	renewedUntil       time.Time
	reportingClaims    []VideoTaskRefundReportingClaim
	reportingDone      int
	reportingRetry     int
	reportingEvents    *[]string
	cacheClaims        []VideoTaskCacheInvalidationClaim
	cacheDone          int
	cacheRetry         int
	cacheDead          int
	cacheEvents        *[]string
	orphanClaims       []VideoTaskAdmissionOrphanClaim
	orphanFailures     int
	orphanCode         string
	orphanMessage      string
	orphanApplied      *bool
}

func (r *reconcilerRepositoryFake) ClaimDueAdmissionOrphans(context.Context, time.Time, time.Duration, int, string, time.Duration) ([]VideoTaskAdmissionOrphanClaim, error) {
	return append([]VideoTaskAdmissionOrphanClaim(nil), r.orphanClaims...), nil
}

func (r *reconcilerRepositoryFake) FailAdmissionOrphan(_ context.Context, _ string, token, code, message string) (bool, error) {
	r.orphanFailures++
	r.lastToken, r.orphanCode, r.orphanMessage = token, code, message
	if r.orphanApplied != nil {
		return *r.orphanApplied, nil
	}
	return true, nil
}

func (r *reconcilerRepositoryFake) ClaimDueCacheInvalidation(context.Context, int, string, time.Duration) ([]VideoTaskCacheInvalidationClaim, error) {
	if len(r.cacheClaims) == 0 {
		return nil, nil
	}
	claim := r.cacheClaims[0]
	r.cacheClaims = r.cacheClaims[1:]
	return []VideoTaskCacheInvalidationClaim{claim}, nil
}
func (r *reconcilerRepositoryFake) CompleteCacheInvalidation(context.Context, int64, string) (bool, error) {
	r.cacheDone++
	if r.cacheEvents != nil {
		*r.cacheEvents = append(*r.cacheEvents, "done")
	}
	return true, nil
}
func (r *reconcilerRepositoryFake) RetryCacheInvalidation(context.Context, int64, string, string, time.Time) (bool, error) {
	r.cacheRetry++
	return true, nil
}
func (r *reconcilerRepositoryFake) DeadLetterCacheInvalidation(context.Context, int64, string, string) (bool, error) {
	r.cacheDead++
	return true, nil
}

func (r *reconcilerRepositoryFake) ClaimDueRefundReporting(context.Context, time.Time, int, string, time.Duration) ([]VideoTaskRefundReportingClaim, error) {
	return append([]VideoTaskRefundReportingClaim(nil), r.reportingClaims...), nil
}

func (r *reconcilerRepositoryFake) CompleteRefundReporting(_ context.Context, _ int64, token string) (bool, error) {
	r.reportingDone++
	r.lastToken = token
	if r.reportingEvents != nil {
		*r.reportingEvents = append(*r.reportingEvents, "done")
	}
	return true, nil
}

func (r *reconcilerRepositoryFake) RetryRefundReporting(_ context.Context, _ int64, token, message string, next time.Time) (bool, error) {
	r.reportingRetry++
	r.lastToken, r.lastError, r.nextRetry = token, message, next
	return true, nil
}

func (r *reconcilerRepositoryFake) ClaimDueReconciliation(context.Context, time.Time, int, string, time.Duration) ([]VideoTaskSettlementReconcileClaim, error) {
	r.claimCalls++
	return append([]VideoTaskSettlementReconcileClaim(nil), r.claims...), r.claimErr
}

func (r *reconcilerRepositoryFake) CompleteReconciliation(_ context.Context, _ string, token string) (bool, error) {
	r.successes++
	r.lastToken = token
	if r.persistenceErr != nil {
		return false, r.persistenceErr
	}
	if r.persistenceApplied != nil {
		return *r.persistenceApplied, nil
	}
	return true, nil
}

func (r *reconcilerRepositoryFake) RetryReconciliation(_ context.Context, _ string, token, message string, next time.Time) (bool, error) {
	r.failures++
	r.lastToken = token
	r.lastError = message
	r.nextRetry = next
	if r.persistenceErr != nil {
		return false, r.persistenceErr
	}
	if r.persistenceApplied != nil {
		return *r.persistenceApplied, nil
	}
	return true, nil
}

func (r *reconcilerRepositoryFake) RenewSettlementClaim(context.Context, string, string, time.Duration) (time.Time, bool, error) {
	if r.renewAllowed != nil {
		return r.renewedUntil, *r.renewAllowed, nil
	}
	if r.renewedUntil.IsZero() {
		r.renewedUntil = time.Now().Add(videoTaskSettlementReconcileLeaseTTL)
	}
	return r.renewedUntil, true, nil
}

func testBoolPtr(value bool) *bool { return &value }

func TestVideoTaskSettlementServiceReconcilePersistedTaskConcurrentCallsHaveOneRepositoryEffect(t *testing.T) {
	tasks := &immutableVideoTaskRepositoryFake{task: &VideoTask{PublicTaskID: "task_1", Status: VideoTaskStatusFailed}}
	repo := &reconcileSettlementRepositoryFake{snapshot: &VideoTaskSettlementSnapshot{PublicTaskID: "task_1", State: VideoTaskSettlementCharged}}
	svc := NewVideoTaskSettlementService(repo, tasks, nil, nil, nil, nil, nil)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := svc.ReconcilePersistedTask(context.Background(), "task_1"); err != nil {
				t.Errorf("ReconcilePersistedTask returned error: %v", err)
			}
		}()
	}
	wg.Wait()
	if repo.appliedRefunds != 1 {
		t.Fatalf("applied refunds = %d, want 1", repo.appliedRefunds)
	}
}

type immutableVideoTaskRepositoryFake struct {
	VideoTaskRepository
	task *VideoTask
}

func (r *immutableVideoTaskRepositoryFake) GetByPublicTaskID(context.Context, string) (*VideoTask, error) {
	copy := *r.task
	return &copy, nil
}

func TestVideoTaskSettlementServiceReconcilePersistedTaskIgnoresUnpricedTask(t *testing.T) {
	tasks := newFakeVideoTaskRepository(nil)
	tasks.seedTask(&VideoTask{PublicTaskID: "task_unpriced", Status: VideoTaskStatusFailed})
	repo := &reconcileSettlementRepositoryFake{getErr: ErrVideoTaskSettlementNotFound}
	svc := NewVideoTaskSettlementService(repo, tasks, nil, nil, nil, nil, nil)
	if err := svc.ReconcilePersistedTask(context.Background(), "task_unpriced"); err != nil {
		t.Fatalf("ReconcilePersistedTask returned error: %v", err)
	}
}

type reconcileSettlementRepositoryFake struct {
	VideoTaskSettlementRepository
	mu             sync.Mutex
	snapshot       *VideoTaskSettlementSnapshot
	getErr         error
	releaseCalls   int
	refundCalls    int
	appliedRefunds int
	repairCalls    int
	repairErr      error
}

func (r *reconcileSettlementRepositoryFake) RepairChargedUsage(context.Context, string) (*VideoTaskSettlementResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.repairCalls++
	return &VideoTaskSettlementResult{Settlement: r.snapshot}, r.repairErr
}

func (r *reconcileSettlementRepositoryFake) GetByPublicTaskID(context.Context, string) (*VideoTaskSettlementSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return nil, r.getErr
	}
	copy := *r.snapshot
	return &copy, nil
}

func (r *reconcileSettlementRepositoryFake) Release(context.Context, *VideoTaskSettlementReleaseCommand) (*VideoTaskSettlementResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releaseCalls++
	applied := r.snapshot.State == VideoTaskSettlementReserved
	if applied {
		r.snapshot.State = VideoTaskSettlementReleased
	}
	return &VideoTaskSettlementResult{Applied: applied, Settlement: r.snapshot}, nil
}

func (r *reconcileSettlementRepositoryFake) ReleaseFailed(ctx context.Context, cmd *VideoTaskSettlementReleaseCommand) (*VideoTaskSettlementResult, error) {
	return r.Release(ctx, cmd)
}

func (r *reconcileSettlementRepositoryFake) RefundFailed(context.Context, *VideoTaskSettlementRefundCommand) (*VideoTaskSettlementResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refundCalls++
	applied := r.snapshot.State == VideoTaskSettlementCharged
	if applied {
		r.snapshot.State = VideoTaskSettlementRefunded
		r.appliedRefunds++
	}
	return &VideoTaskSettlementResult{Applied: applied, Settlement: r.snapshot}, nil
}
func (r *reconcileSettlementRepositoryFake) ClaimDueCacheInvalidation(context.Context, int, string, time.Duration) ([]VideoTaskCacheInvalidationClaim, error) {
	return nil, nil
}
func (r *reconcileSettlementRepositoryFake) CompleteCacheInvalidation(context.Context, int64, string) (bool, error) {
	return false, nil
}
func (r *reconcileSettlementRepositoryFake) RetryCacheInvalidation(context.Context, int64, string, string, time.Time) (bool, error) {
	return false, nil
}
func (r *reconcileSettlementRepositoryFake) DeadLetterCacheInvalidation(context.Context, int64, string, string) (bool, error) {
	return false, nil
}
