//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const videoTaskPollerStopTestTimeout = 6 * time.Second

func TestVideoTaskPollerPollOnceUpdatesDueTasks(t *testing.T) {
	now := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	completedAt := now.Add(30 * time.Second)
	expiresAt := now.Add(24 * time.Hour)
	repo := &pollerVideoTaskRepositoryFake{
		claimed: []*VideoTask{{
			PublicTaskID:   "task_due",
			ProviderTaskID: "upstream_task",
			AccountID:      99,
			Status:         VideoTaskStatusQueued,
			PollAttempts:   2,
		}},
	}
	accountRepo := &pollerAccountRepositoryFake{accounts: map[int64]*Account{
		99: {ID: 99, Name: "stored account"},
	}}
	provider := &pollerVideoTaskProviderFake{results: map[string]*VideoProviderFetchResult{
		"task_due": {
			Status:         VideoTaskStatusCompleted,
			ProviderStatus: "completed",
			RawBody:        []byte(`{"id":"upstream_task","status":"completed"}`),
			Metadata:       map[string]any{"request_id": "req_123"},
			CompletedAt:    &completedAt,
			ExpiresAt:      &expiresAt,
		},
	}}

	poller := NewVideoTaskPoller(repo, accountRepo, provider)
	err := poller.PollOnce(context.Background(), "worker-1", now)

	if err != nil {
		t.Fatalf("PollOnce returned error: %v", err)
	}
	if repo.claimLimit != 20 {
		t.Fatalf("claim limit = %d, want 20", repo.claimLimit)
	}
	if repo.claimLockOwner == "" || repo.claimLockOwner == "worker-1" {
		t.Fatalf("claim lease token = %q, want a non-worker token", repo.claimLockOwner)
	}
	if repo.claimTTL != 2*time.Minute {
		t.Fatalf("claim TTL = %s, want 2m", repo.claimTTL)
	}
	if !repo.claimNow.Equal(now) {
		t.Fatalf("claim now = %s, want %s", repo.claimNow, now)
	}
	if !reflect.DeepEqual(provider.fetchAccountIDs, map[string]int64{"task_due": 99}) {
		t.Fatalf("provider fetch account IDs = %#v, want account 99", provider.fetchAccountIDs)
	}
	update, ok := repo.updates["task_due"]
	if !ok {
		t.Fatalf("missing provider update")
	}
	if update.Status != VideoTaskStatusCompleted {
		t.Fatalf("update status = %q, want completed", update.Status)
	}
	if update.ProviderStatus != "completed" {
		t.Fatalf("update provider status = %q, want completed", update.ProviderStatus)
	}
	if string(update.ResponseBody) != `{"id":"upstream_task","status":"completed"}` {
		t.Fatalf("update response body = %s", string(update.ResponseBody))
	}
	if update.Metadata["request_id"] != "req_123" {
		t.Fatalf("update metadata = %#v, want request_id", update.Metadata)
	}
	if update.CompletedAt == nil || !update.CompletedAt.Equal(completedAt) {
		t.Fatalf("update completed at = %#v, want %s", update.CompletedAt, completedAt)
	}
	if update.ExpiresAt == nil || !update.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("update expires at = %#v, want %s", update.ExpiresAt, expiresAt)
	}
	if !update.ClearNextPollAt || update.NextPollAt != nil {
		t.Fatalf("terminal update next poll = %#v clear=%v, want cleared", update.NextPollAt, update.ClearNextPollAt)
	}
	if got := repo.releaseTaskIDs(); !reflect.DeepEqual(got, []string{"task_due"}) {
		t.Fatalf("released tasks = %#v, want task_due", got)
	}
}

func TestVideoTaskPollerPollOnceSchedulesNonTerminalTask(t *testing.T) {
	now := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	repo := &pollerVideoTaskRepositoryFake{
		claimed: []*VideoTask{{
			PublicTaskID:   "task_processing",
			ProviderTaskID: "upstream_task",
			AccountID:      99,
			Status:         VideoTaskStatusQueued,
			PollAttempts:   2,
		}},
	}
	accountRepo := &pollerAccountRepositoryFake{accounts: map[int64]*Account{99: {ID: 99}}}
	provider := &pollerVideoTaskProviderFake{results: map[string]*VideoProviderFetchResult{
		"task_processing": {
			Status:         VideoTaskStatusInProgress,
			ProviderStatus: "processing",
			RawBody:        []byte(`{"id":"upstream_task","status":"processing"}`),
		},
	}}

	poller := NewVideoTaskPoller(repo, accountRepo, provider)
	poller.now = func() time.Time { return now }
	err := poller.PollOnce(context.Background(), "worker-1", now)

	if err != nil {
		t.Fatalf("PollOnce returned error: %v", err)
	}
	update := repo.updates["task_processing"]
	wantNextPollAt := now.Add(nextVideoPollDelay(2))
	if update.NextPollAt == nil || !update.NextPollAt.Equal(wantNextPollAt) {
		t.Fatalf("next poll = %#v, want %s", update.NextPollAt, wantNextPollAt)
	}
	if update.ClearNextPollAt {
		t.Fatalf("ClearNextPollAt = true, want false")
	}
}

func TestVideoTaskPollerPollOnceSchedulesFromTaskCompletionTime(t *testing.T) {
	for _, tt := range []struct {
		name          string
		providerError error
		result        *VideoProviderFetchResult
	}{
		{
			name: "successful nonterminal update",
			result: &VideoProviderFetchResult{
				Status:         VideoTaskStatusInProgress,
				ProviderStatus: "processing",
			},
		},
		{
			name:          "provider error backoff",
			providerError: errors.New("provider unavailable"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			batchStartedAt := time.Now()
			repo := &pollerVideoTaskRepositoryFake{claimed: []*VideoTask{{
				PublicTaskID: "task_slow",
				AccountID:    99,
				Status:       VideoTaskStatusQueued,
				PollAttempts: 2,
			}}}
			provider := &pollerVideoTaskProviderFake{
				fetchDelay: 75 * time.Millisecond,
				errors:     map[string]error{"task_slow": tt.providerError},
				results:    map[string]*VideoProviderFetchResult{"task_slow": tt.result},
			}
			poller := NewVideoTaskPoller(repo, &pollerAccountRepositoryFake{accounts: map[int64]*Account{99: {ID: 99}}}, provider)

			err := poller.PollOnce(context.Background(), "worker-1", batchStartedAt)

			if tt.providerError == nil && err != nil {
				t.Fatalf("PollOnce returned error: %v", err)
			}
			if tt.providerError != nil && !errors.Is(err, tt.providerError) {
				t.Fatalf("PollOnce error = %v, want provider error", err)
			}
			update, ok := repo.updates["task_slow"]
			if !ok || update.NextPollAt == nil {
				t.Fatalf("next poll update = %#v, want a nonterminal schedule", update)
			}
			completedAt, ok := provider.fetchCompletedAt["task_slow"]
			if !ok {
				t.Fatal("provider completion time was not recorded")
			}
			wantNotBefore := completedAt.Add(nextVideoPollDelay(2))
			if update.NextPollAt.Before(wantNotBefore) {
				t.Fatalf("next poll = %s, want no earlier than completion-based %s", update.NextPollAt, wantNotBefore)
			}
		})
	}
}

func TestVideoTaskPollerPollOnceUsesDistinctLeaseTokensForConsecutiveClaims(t *testing.T) {
	now := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	repo := &pollerVideoTaskRepositoryFake{claimed: []*VideoTask{{
		PublicTaskID: "task_due",
		AccountID:    99,
		Status:       VideoTaskStatusQueued,
	}}}
	poller := NewVideoTaskPoller(repo, &pollerAccountRepositoryFake{accounts: map[int64]*Account{99: {ID: 99}}}, &pollerVideoTaskProviderFake{
		results: map[string]*VideoProviderFetchResult{"task_due": {Status: VideoTaskStatusCompleted}},
	})

	if err := poller.PollOnce(context.Background(), "worker-1", now); err != nil {
		t.Fatalf("first PollOnce returned error: %v", err)
	}
	if err := poller.PollOnce(context.Background(), "worker-1", now.Add(time.Second)); err != nil {
		t.Fatalf("second PollOnce returned error: %v", err)
	}

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.claimLockOwners) != 2 {
		t.Fatalf("claim token count = %d, want 2", len(repo.claimLockOwners))
	}
	if repo.claimLockOwners[0] == "worker-1" || repo.claimLockOwners[1] == "worker-1" {
		t.Fatalf("claim tokens = %#v, must not use stable worker ID", repo.claimLockOwners)
	}
	if repo.claimLockOwners[0] == repo.claimLockOwners[1] {
		t.Fatalf("claim tokens = %#v, want a new token for each PollOnce", repo.claimLockOwners)
	}
}

func TestVideoTaskPollerPollOnceTreatsLostLeaseAsNormalAndDoesNotRelease(t *testing.T) {
	for _, tt := range []struct {
		name        string
		providerErr error
		result      *VideoProviderFetchResult
	}{
		{
			name:   "guarded provider update",
			result: &VideoProviderFetchResult{Status: VideoTaskStatusCompleted},
		},
		{
			name:        "guarded error backoff",
			providerErr: errors.New("upstream unavailable"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
			repo := &pollerVideoTaskRepositoryFake{
				claimed:   []*VideoTask{{PublicTaskID: "task_due", AccountID: 99, Status: VideoTaskStatusQueued}},
				leaseLost: true,
			}
			poller := NewVideoTaskPoller(repo, &pollerAccountRepositoryFake{accounts: map[int64]*Account{99: {ID: 99}}}, &pollerVideoTaskProviderFake{
				errors:  map[string]error{"task_due": tt.providerErr},
				results: map[string]*VideoProviderFetchResult{"task_due": tt.result},
			})

			err := poller.PollOnce(context.Background(), "worker-1", now)

			if err != nil {
				t.Fatalf("PollOnce error = %v, want nil after lease loss", err)
			}
			if got := repo.releaseTaskIDs(); len(got) != 0 {
				t.Fatalf("released tasks = %#v, want no release after lease loss", got)
			}
		})
	}
}

func TestVideoTaskPollerReconcilesFailedTaskOnlyAfterLeasedUpdateApplies(t *testing.T) {
	for _, tt := range []struct {
		name      string
		leaseLost bool
		wantCalls int
	}{
		{name: "applied", wantCalls: 1},
		{name: "lost lease", leaseLost: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := &pollerVideoTaskRepositoryFake{claimed: []*VideoTask{{PublicTaskID: "task_1", AccountID: 9, Status: VideoTaskStatusQueued}}, leaseLost: tt.leaseLost}
			hook := &reconcileHookFake{}
			poller := newVideoTaskPoller(repo, &pollerAccountRepositoryFake{accounts: map[int64]*Account{9: {ID: 9}}}, &pollerVideoTaskProviderFake{results: map[string]*VideoProviderFetchResult{"task_1": {Status: VideoTaskStatusFailed, ErrorMessage: "failed"}}}, hook)

			if err := poller.PollOnce(context.Background(), "worker", time.Now()); err != nil {
				t.Fatalf("PollOnce returned error: %v", err)
			}
			if hook.calls != tt.wantCalls {
				t.Fatalf("reconcile calls = %d, want %d", hook.calls, tt.wantCalls)
			}
		})
	}
}

func TestVideoTaskPollerPollOnceSkipsTaskWhoseExpiredLeaseWasReclaimedBeforeItsTurn(t *testing.T) {
	now := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	repo := &pollerVideoTaskRepositoryFake{
		claimed: []*VideoTask{
			{PublicTaskID: "task_first", AccountID: 99, Status: VideoTaskStatusQueued},
			{PublicTaskID: "task_reclaimed", AccountID: 99, Status: VideoTaskStatusQueued},
		},
		enforceLeaseContract: true,
	}
	provider := &pollerVideoTaskProviderFake{
		results: map[string]*VideoProviderFetchResult{"task_first": {Status: VideoTaskStatusCompleted}},
		afterFetch: func(taskID string) {
			if taskID == "task_first" {
				repo.replaceLease("task_reclaimed", "newer-worker", now.Add(2*videoTaskPollLockTTL))
			}
		},
	}
	poller := NewVideoTaskPoller(repo, &pollerAccountRepositoryFake{accounts: map[int64]*Account{99: {ID: 99}}}, provider)
	currentTimeCalls := 0
	poller.now = func() time.Time {
		currentTimeCalls++
		if currentTimeCalls >= 3 {
			return now.Add(videoTaskPollLockTTL)
		}
		return now
	}

	err := poller.PollOnce(context.Background(), "worker-1", now)

	if err != nil {
		t.Fatalf("PollOnce returned error: %v", err)
	}
	if _, fetched := provider.fetchAccountIDs["task_reclaimed"]; fetched {
		t.Fatal("provider Fetch called for task whose lease was reclaimed")
	}
	if got := repo.releaseTaskIDs(); !reflect.DeepEqual(got, []string{"task_first"}) {
		t.Fatalf("released tasks = %#v, want only task_first", got)
	}
	leaseToken, leaseUntil := repo.leaseFor("task_reclaimed")
	if leaseToken != "newer-worker" || !leaseUntil.Equal(now.Add(2*videoTaskPollLockTTL)) {
		t.Fatalf("newer lease changed: token=%q until=%s", leaseToken, leaseUntil)
	}
}

func TestVideoTaskPollerPollOnceBoundsSlowFetchBeforeRenewedLeaseExpires(t *testing.T) {
	now := time.Now()
	repo := &pollerVideoTaskRepositoryFake{claimed: []*VideoTask{{PublicTaskID: "task_slow", AccountID: 99, Status: VideoTaskStatusQueued}}}
	provider := &pollerVideoTaskProviderFake{requireFetchDeadline: true, slowFetch: true}
	poller := NewVideoTaskPoller(repo, &pollerAccountRepositoryFake{accounts: map[int64]*Account{99: {ID: 99}}}, provider)
	poller.now = time.Now

	err := poller.PollOnce(context.Background(), "worker-1", now)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("PollOnce error = %v, want fetch deadline exceeded", err)
	}
	deadline, ok := provider.fetchDeadline("task_slow")
	if !ok {
		t.Fatal("slow provider Fetch did not receive a deadline")
	}
	leaseUntil := repo.renewedLeaseUntil("task_slow")
	if !deadline.Before(leaseUntil) {
		t.Fatalf("fetch deadline = %s, want before renewed lease expiration %s", deadline, leaseUntil)
	}
	if got := repo.releaseTaskIDs(); !reflect.DeepEqual(got, []string{"task_slow"}) {
		t.Fatalf("released tasks = %#v, want task_slow after bounded fetch", got)
	}
}

func TestVideoTaskPollerPollOnceAccountLookupErrorSchedulesBackoffBeforeLockRelease(t *testing.T) {
	now := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	oldDueAt := now.Add(-time.Minute)
	accountErr := errors.New("account lookup unavailable")
	repo := &pollerVideoTaskRepositoryFake{claimed: []*VideoTask{{PublicTaskID: "task_due", AccountID: 99, Status: VideoTaskStatusQueued, PollAttempts: 2, NextPollAt: &oldDueAt}}}
	accountRepo := &pollerAccountRepositoryFake{err: accountErr}
	provider := &pollerVideoTaskProviderFake{}
	poller := NewVideoTaskPoller(repo, accountRepo, provider)
	poller.now = func() time.Time { return now }
	err := poller.PollOnce(context.Background(), "worker-1", now)

	if !errors.Is(err, accountErr) {
		t.Fatalf("PollOnce error = %v, want account lookup error", err)
	}
	assertPollErrorBackoffBeforeRelease(t, repo, "task_due", oldDueAt, now.Add(nextVideoPollDelay(2)))
}

func TestVideoTaskPollerPollOnceProviderErrorSchedulesBackoffBeforeLockRelease(t *testing.T) {
	now := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	oldDueAt := now.Add(-time.Minute)
	providerErr := errors.New("provider unavailable")
	repo := &pollerVideoTaskRepositoryFake{claimed: []*VideoTask{{PublicTaskID: "task_due", AccountID: 99, Status: VideoTaskStatusQueued, PollAttempts: 3, NextPollAt: &oldDueAt}}}
	accountRepo := &pollerAccountRepositoryFake{accounts: map[int64]*Account{99: {ID: 99}}}
	provider := &pollerVideoTaskProviderFake{errors: map[string]error{"task_due": providerErr}}

	poller := NewVideoTaskPoller(repo, accountRepo, provider)
	poller.now = func() time.Time { return now }
	err := poller.PollOnce(context.Background(), "worker-1", now)

	if !errors.Is(err, providerErr) {
		t.Fatalf("PollOnce error = %v, want provider error", err)
	}
	if !strings.Contains(err.Error(), "task_due") {
		t.Fatalf("PollOnce error = %v, want public task id", err)
	}
	assertPollErrorBackoffBeforeRelease(t, repo, "task_due", oldDueAt, now.Add(nextVideoPollDelay(3)))
}

func TestVideoTaskPollerPollOnceNilProviderResultSchedulesBackoffBeforeLockRelease(t *testing.T) {
	now := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	oldDueAt := now.Add(-time.Minute)
	repo := &pollerVideoTaskRepositoryFake{claimed: []*VideoTask{{PublicTaskID: "task_due", AccountID: 99, Status: VideoTaskStatusQueued, PollAttempts: 4, NextPollAt: &oldDueAt}}}
	accountRepo := &pollerAccountRepositoryFake{accounts: map[int64]*Account{99: {ID: 99}}}
	provider := &pollerVideoTaskProviderFake{results: map[string]*VideoProviderFetchResult{"task_due": nil}}

	poller := NewVideoTaskPoller(repo, accountRepo, provider)
	poller.now = func() time.Time { return now }
	err := poller.PollOnce(context.Background(), "worker-1", now)

	if err == nil || !strings.Contains(err.Error(), "nil fetch result") {
		t.Fatalf("PollOnce error = %v, want nil provider result error", err)
	}
	assertPollErrorBackoffBeforeRelease(t, repo, "task_due", oldDueAt, now.Add(nextVideoPollDelay(4)))
}

func TestVideoTaskPollerBackoffLetsTaskBeyondClaimLimitRunNextCycle(t *testing.T) {
	now := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	failingDueAt := now.Add(-time.Minute)
	healthyDueAt := now.Add(-time.Second)

	for _, tt := range []struct {
		name           string
		accountErrors  map[int64]error
		providerErrors map[string]error
		nilResults     bool
	}{
		{
			name:           "provider_error",
			providerErrors: map[string]error{},
		},
		{
			name:          "account_lookup_error",
			accountErrors: map[int64]error{99: errors.New("account lookup unavailable")},
		},
		{
			name:       "nil_provider_result",
			nilResults: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dueTasks := make([]*VideoTask, 0, videoTaskPollLimit+1)
			providerErrors := tt.providerErrors
			if providerErrors == nil {
				providerErrors = map[string]error{}
			}
			nilResults := map[string]*VideoProviderFetchResult{}
			for i := 0; i < videoTaskPollLimit; i++ {
				taskID := fmt.Sprintf("task_failing_%02d", i)
				dueTasks = append(dueTasks, &VideoTask{
					PublicTaskID: taskID,
					AccountID:    99,
					Status:       VideoTaskStatusQueued,
					NextPollAt:   &failingDueAt,
				})
				if tt.providerErrors != nil {
					providerErrors[taskID] = errors.New("provider unavailable")
				}
				if tt.nilResults {
					nilResults[taskID] = nil
				}
			}
			dueTasks = append(dueTasks, &VideoTask{
				PublicTaskID: "task_healthy_beyond_limit",
				AccountID:    100,
				Status:       VideoTaskStatusQueued,
				NextPollAt:   &healthyDueAt,
			})
			repo := &pollerVideoTaskRepositoryFake{dueTasks: dueTasks}
			provider := &pollerVideoTaskProviderFake{
				errors: providerErrors,
				results: map[string]*VideoProviderFetchResult{
					"task_healthy_beyond_limit": {Status: VideoTaskStatusCompleted, ProviderStatus: "completed"},
				},
			}
			for taskID, result := range nilResults {
				provider.results[taskID] = result
			}
			accounts := map[int64]*Account{99: {ID: 99}, 100: {ID: 100}}
			poller := NewVideoTaskPoller(repo, &pollerAccountRepositoryFake{accounts: accounts, errors: tt.accountErrors}, provider)
			poller.now = func() time.Time { return now }

			firstErr := poller.PollOnce(context.Background(), "worker-1", now)
			if firstErr == nil {
				t.Fatal("first PollOnce error = nil, want failing batch error")
			}
			requirePollerClaimedIDs(t, repo, 0, pollerFailingTaskIDs())
			secondNow := now.Add(videoTaskPollInterval)
			poller.now = func() time.Time { return secondNow }
			if err := poller.PollOnce(context.Background(), "worker-1", secondNow); err != nil {
				t.Fatalf("second PollOnce returned error: %v", err)
			}
			requirePollerClaimedIDs(t, repo, 1, []string{"task_healthy_beyond_limit"})
			if got := provider.fetchAccountIDs["task_healthy_beyond_limit"]; got != 100 {
				t.Fatalf("healthy task fetch account ID = %d, want 100", got)
			}
		})
	}
}

func TestVideoTaskPollerPollOnceProviderErrorReportsBackoffUpdateFailureAndReleasesLock(t *testing.T) {
	now := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	providerErr := errors.New("provider unavailable")
	updateErr := errors.New("backoff update unavailable")
	repo := &pollerVideoTaskRepositoryFake{
		claimed:      []*VideoTask{{PublicTaskID: "task_due", AccountID: 99, Status: VideoTaskStatusQueued, NextPollAt: pollerTimePtr(now.Add(-time.Minute))}},
		updateErrors: map[string]error{"task_due": updateErr},
	}
	accountRepo := &pollerAccountRepositoryFake{accounts: map[int64]*Account{99: {ID: 99}}}
	provider := &pollerVideoTaskProviderFake{errors: map[string]error{"task_due": providerErr}}

	poller := NewVideoTaskPoller(repo, accountRepo, provider)
	poller.now = func() time.Time { return now }
	err := poller.PollOnce(context.Background(), "worker-1", now)

	if !errors.Is(err, providerErr) {
		t.Fatalf("PollOnce error = %v, want provider error", err)
	}
	if !errors.Is(err, updateErr) {
		t.Fatalf("PollOnce error = %v, want backoff update error", err)
	}
	if got := repo.releaseTaskIDs(); !reflect.DeepEqual(got, []string{"task_due"}) {
		t.Fatalf("released tasks = %#v, want task_due", got)
	}
}

func TestVideoTaskPollerPollOnceContinuesAfterTaskError(t *testing.T) {
	now := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	providerErr := errors.New("first task failed")
	repo := &pollerVideoTaskRepositoryFake{claimed: []*VideoTask{
		{PublicTaskID: "task_first", AccountID: 99, Status: VideoTaskStatusQueued},
		{PublicTaskID: "task_second", AccountID: 100, Status: VideoTaskStatusQueued},
	}}
	accountRepo := &pollerAccountRepositoryFake{accounts: map[int64]*Account{
		99:  {ID: 99},
		100: {ID: 100},
	}}
	provider := &pollerVideoTaskProviderFake{
		errors: map[string]error{"task_first": providerErr},
		results: map[string]*VideoProviderFetchResult{
			"task_second": {
				Status:         VideoTaskStatusCompleted,
				ProviderStatus: "completed",
				RawBody:        []byte(`{"id":"second","status":"completed"}`),
			},
		},
	}

	poller := NewVideoTaskPoller(repo, accountRepo, provider)
	err := poller.PollOnce(context.Background(), "worker-1", now)

	if !errors.Is(err, providerErr) {
		t.Fatalf("PollOnce error = %v, want first task error", err)
	}
	if !strings.Contains(err.Error(), "task_first") {
		t.Fatalf("PollOnce error = %v, want first task id", err)
	}
	if _, ok := repo.updates["task_second"]; !ok {
		t.Fatalf("second task was not updated after first task error")
	}
	if got := repo.releaseTaskIDs(); !reflect.DeepEqual(got, []string{"task_first", "task_second"}) {
		t.Fatalf("released tasks = %#v, want both tasks", got)
	}
}

func TestVideoTaskPollerStopIsIdempotent(t *testing.T) {
	repo := &pollerVideoTaskRepositoryFake{}
	accountRepo := &pollerAccountRepositoryFake{}
	provider := &pollerVideoTaskProviderFake{}
	poller := NewVideoTaskPoller(repo, accountRepo, provider)

	poller.Start()
	poller.Start()
	poller.Stop()
	poller.Stop()
}

func TestVideoTaskPollerStopReturnsAfterDeadlineWhenProviderIgnoresCancellation(t *testing.T) {
	repo := &pollerVideoTaskRepositoryFake{claimed: []*VideoTask{{
		PublicTaskID: "task_blocked_fetch",
		AccountID:    99,
		Status:       VideoTaskStatusQueued,
	}}}
	provider := &blockingPollerVideoTaskProvider{
		started:  make(chan struct{}, 1),
		unblock:  make(chan struct{}),
		returned: make(chan struct{}, 1),
		result: &VideoProviderFetchResult{
			Status:         VideoTaskStatusCompleted,
			ProviderStatus: "completed",
			RawBody:        []byte(`{"id":"upstream_blocked","status":"completed"}`),
		},
	}
	poller := NewVideoTaskPoller(repo, &pollerAccountRepositoryFake{accounts: map[int64]*Account{99: {ID: 99}}}, provider)
	poller.interval = time.Hour
	poller.Start()
	waitForPollerTestSignal(t, provider.started, "provider fetch to start")

	stopDone := make(chan struct{})
	go func() {
		poller.Stop()
		close(stopDone)
	}()

	select {
	case <-stopDone:
	case <-time.After(videoTaskPollerStopTestTimeout):
		close(provider.unblock)
		t.Fatal("Stop did not return by its bounded deadline")
	}
	close(provider.unblock)
	waitForPollerTestSignal(t, provider.returned, "blocked provider fetch to return")
	waitForPollerTestCondition(t, func() bool {
		poller.mu.Lock()
		defer poller.mu.Unlock()
		return poller.done == nil
	}, "poller worker to exit after provider returns")

	repo.mu.Lock()
	updates := len(repo.updates)
	repo.mu.Unlock()
	if updates != 0 {
		t.Fatalf("late provider result persisted %d updates after Stop", updates)
	}

	poller.Stop()
}

func TestVideoTaskPollerStopWaitsForCooperativeProvider(t *testing.T) {
	repo := &pollerVideoTaskRepositoryFake{claimed: []*VideoTask{{
		PublicTaskID: "task_cooperative_fetch",
		AccountID:    99,
		Status:       VideoTaskStatusQueued,
	}}}
	provider := &cooperativeBlockingPollerVideoTaskProvider{
		started:          make(chan struct{}, 1),
		cancellationSeen: make(chan struct{}, 1),
		unblock:          make(chan struct{}),
	}
	poller := NewVideoTaskPoller(repo, &pollerAccountRepositoryFake{accounts: map[int64]*Account{99: {ID: 99}}}, provider)
	poller.interval = time.Hour
	poller.Start()
	waitForPollerTestSignal(t, provider.started, "provider fetch to start")

	stopDone := make(chan struct{})
	go func() {
		poller.Stop()
		close(stopDone)
	}()
	waitForPollerTestSignal(t, provider.cancellationSeen, "provider cancellation")
	select {
	case <-stopDone:
		t.Fatal("Stop returned before cooperative provider finished")
	case <-time.After(25 * time.Millisecond):
	}
	close(provider.unblock)
	waitForPollerTestSignal(t, stopDone, "Stop to wait for cooperative provider")

	poller.Stop()
}

func TestVideoTaskPollerAdmissionPrecedesStopCancellation(t *testing.T) {
	for _, tt := range []struct {
		name        string
		providerErr error
		result      *VideoProviderFetchResult
	}{
		{
			name:   "successful provider result",
			result: &VideoProviderFetchResult{Status: VideoTaskStatusCompleted, ProviderStatus: "completed"},
		},
		{
			name:        "provider error backoff",
			providerErr: errors.New("provider unavailable"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			previousMaxProcs := runtime.GOMAXPROCS(1)
			defer runtime.GOMAXPROCS(previousMaxProcs)

			repo := &pollerVideoTaskRepositoryFake{claimed: []*VideoTask{{
				PublicTaskID: "task_due",
				AccountID:    99,
				Status:       VideoTaskStatusQueued,
			}}}
			provider := &pollerVideoTaskProviderFake{
				errors:                map[string]error{"task_due": tt.providerErr},
				results:               map[string]*VideoProviderFetchResult{"task_due": tt.result},
				fetchCancellationSeen: make(chan struct{}, 1),
			}
			poller := NewVideoTaskPoller(repo, &pollerAccountRepositoryFake{accounts: map[int64]*Account{99: {ID: 99}}}, provider)
			poller.interval = time.Hour
			stopCalled := make(chan struct{})
			stopDone := make(chan struct{})
			admissions := 0
			poller.beforePersistenceAdmission = func() {
				admissions++
				if admissions != 2 {
					return
				}
				go func() {
					close(stopCalled)
					poller.Stop()
					close(stopDone)
				}()
				<-stopCalled
				runtime.Gosched()
			}
			repo.beforeProviderUpdate = func() {
				if provider.fetchContext("task_due").Err() != nil {
					repo.recordLateProviderUpdate()
				}
			}

			poller.Start()
			waitForPollerTestSignal(t, stopDone, "Stop to complete")

			if got := repo.updateCallCount(); got != 1 {
				t.Fatalf("provider result/backoff updates = %d, want one admitted before Stop cancellation", got)
			}
			if got := repo.lateProviderUpdateCount(); got != 0 {
				t.Fatalf("provider result/backoff updates after Stop cancellation = %d, want none", got)
			}
			if got := repo.releaseTaskIDs(); !reflect.DeepEqual(got, []string{"task_due"}) {
				t.Fatalf("released tasks = %#v, want cleanup release", got)
			}
		})
	}
}

func TestVideoTaskPollerStopBeforeRenewalPreventsRenewalAndProviderFetch(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	repo := &pollerVideoTaskRepositoryFake{}
	provider := &pollerVideoTaskProviderFake{results: map[string]*VideoProviderFetchResult{
		"task_due": {Status: VideoTaskStatusCompleted},
	}}
	poller := NewVideoTaskPoller(repo, &pollerAccountRepositoryFake{accounts: map[int64]*Account{99: {ID: 99}}}, provider)
	poller.now = func() time.Time { return now }
	poller.Stop()

	leaseLost, err := poller.pollClaimedTask(context.Background(), &VideoTask{
		PublicTaskID: "task_due",
		AccountID:    99,
		Status:       VideoTaskStatusQueued,
	}, "lease-token")

	if err != nil {
		t.Fatalf("pollClaimedTask returned error: %v", err)
	}
	if !leaseLost {
		t.Fatal("leaseLost = false, want true when Stop prevents renewal")
	}
	if got := pollerRenewalIDs(repo.renewals); len(got) != 0 {
		t.Fatalf("renewals = %#v, want none after Stop", got)
	}
	if got := len(provider.fetchAccountIDs); got != 0 {
		t.Fatalf("provider fetches = %d, want none after Stop", got)
	}
}

func TestVideoTaskPollerStopWaitsForClaimAdmissionAndReleasesClaimedTask(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	repo := &pollerVideoTaskRepositoryFake{
		claimed: []*VideoTask{{
			PublicTaskID: "task_claimed_during_stop",
			AccountID:    99,
			Status:       VideoTaskStatusQueued,
		}},
		claimStarted:    make(chan struct{}),
		blockFirstClaim: make(chan struct{}),
	}
	accountRepo := &pollerAccountRepositoryFake{accounts: map[int64]*Account{99: {ID: 99}}}
	provider := &pollerVideoTaskProviderFake{results: map[string]*VideoProviderFetchResult{
		"task_claimed_during_stop": {Status: VideoTaskStatusCompleted},
	}}
	poller := NewVideoTaskPoller(repo, accountRepo, provider)

	var unblockClaim sync.Once
	t.Cleanup(func() {
		unblockClaim.Do(func() { close(repo.blockFirstClaim) })
	})
	pollDone := make(chan error, 1)
	go func() {
		pollDone <- poller.PollOnce(context.Background(), "worker-1", now)
	}()
	waitForPollerTestSignal(t, repo.claimStarted, "claim admission to start")

	stopDone := make(chan struct{})
	go func() {
		poller.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
		t.Fatal("Stop completed while claim admission was still active")
	case <-time.After(25 * time.Millisecond):
	}

	unblockClaim.Do(func() { close(repo.blockFirstClaim) })
	waitForPollerTestSignal(t, stopDone, "Stop to complete after claim admission")
	if err := <-pollDone; err != nil {
		t.Fatalf("PollOnce returned error: %v", err)
	}

	if got := pollerRenewalIDs(repo.renewals); len(got) != 0 {
		t.Fatalf("renewals = %#v, want none after Stop", got)
	}
	if got := len(provider.fetchAccountIDs); got != 0 {
		t.Fatalf("provider fetches = %d, want none after Stop", got)
	}
	if got := repo.updateCallCount(); got != 0 {
		t.Fatalf("provider updates = %d, want none after Stop", got)
	}
	repo.mu.Lock()
	claimToken := repo.claimLockOwner
	releases := append([]pollerRelease(nil), repo.releases...)
	repo.mu.Unlock()
	if len(releases) != 1 || releases[0].publicTaskID != "task_claimed_during_stop" {
		t.Fatalf("released tasks = %#v, want claimed task", releases)
	}
	if releases[0].lockOwner != claimToken {
		t.Fatalf("release token = %q, want claimed token %q", releases[0].lockOwner, claimToken)
	}
}

func TestVideoTaskPollerStopCancelsActiveClaimBeforeWaitingForIt(t *testing.T) {
	repo := &pollerVideoTaskRepositoryFake{
		claimStarted:       make(chan struct{}),
		firstClaimCanceled: make(chan struct{}),
		unblockFirstClaim:  make(chan struct{}),
	}
	poller := NewVideoTaskPoller(repo, &pollerAccountRepositoryFake{}, &pollerVideoTaskProviderFake{})
	poller.interval = time.Hour
	poller.Start()
	waitForPollerTestSignal(t, repo.claimStarted, "claim to start")

	stopDone := make(chan struct{})
	go func() {
		poller.Stop()
		close(stopDone)
	}()
	waitForPollerTestSignal(t, repo.firstClaimCanceled, "active claim cancellation")
	select {
	case <-stopDone:
		t.Fatal("Stop completed before canceled claim returned")
	case <-time.After(25 * time.Millisecond):
	}

	close(repo.unblockFirstClaim)
	waitForPollerTestSignal(t, stopDone, "Stop to wait for canceled claim")
	if got := repo.claimCallCount(); got != 1 {
		t.Fatalf("claim calls = %d, want 1", got)
	}
}

func TestVideoTaskPollerStopDeadlineIncludesActivePersistence(t *testing.T) {
	pollCtx, cancel := context.WithCancel(context.Background())
	persistStarted := make(chan struct{})
	persistContext := make(chan context.Context, 1)
	unblockPersist := make(chan struct{})
	poller := NewVideoTaskPoller(&pollerVideoTaskRepositoryFake{}, &pollerAccountRepositoryFake{}, &pollerVideoTaskProviderFake{})
	poller.mu.Lock()
	poller.cancel = cancel
	poller.mu.Unlock()

	persistDone := make(chan struct{})
	go func() {
		_, _ = poller.admitPersistence(pollCtx, func(updateCtx context.Context) (bool, error) {
			persistContext <- updateCtx
			close(persistStarted)
			<-unblockPersist
			return true, nil
		})
		close(persistDone)
	}()
	waitForPollerTestSignal(t, persistStarted, "persistence to start")
	updateCtx := <-persistContext

	stopDone := make(chan struct{})
	go func() {
		poller.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
	case <-time.After(videoTaskPollerStopTestTimeout):
		close(unblockPersist)
		t.Fatal("Stop did not enforce its deadline while persistence was blocked")
	}
	if !errors.Is(pollCtx.Err(), context.Canceled) {
		t.Fatalf("poll context error = %v, want context.Canceled", pollCtx.Err())
	}
	if !errors.Is(updateCtx.Err(), context.Canceled) {
		t.Fatalf("persistence context error = %v, want context.Canceled", updateCtx.Err())
	}
	close(unblockPersist)
	waitForPollerTestSignal(t, persistDone, "persistence to return")
}

func TestVideoTaskPollerAdmissionPreventsLateRepositoryUpdateAfterStop(t *testing.T) {
	for _, tt := range []struct {
		name     string
		ungated  bool
		wantLate int
	}{
		{name: "deliberately ungated", ungated: true, wantLate: 1},
		{name: "default gate", wantLate: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pollCtx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			repo := &pollerVideoTaskRepositoryFake{}
			repo.beforeProviderUpdate = func() {
				if pollCtx.Err() != nil {
					repo.recordLateProviderUpdate()
				}
			}
			poller := NewVideoTaskPoller(repo, &pollerAccountRepositoryFake{}, &pollerVideoTaskProviderFake{})
			poller.mu.Lock()
			poller.cancel = cancel
			poller.mu.Unlock()
			poller.Stop()
			if err := pollCtx.Err(); !errors.Is(err, context.Canceled) {
				t.Fatalf("poll context error = %v, want context.Canceled", err)
			}

			persist := func(updateCtx context.Context) (bool, error) {
				return repo.UpdateFromProviderWithPollLease(updateCtx, "task_due", "lease-token", time.Now(), VideoTaskProviderUpdate{
					Status: VideoTaskStatusCompleted,
				})
			}
			var (
				applied bool
				err     error
			)
			if tt.ungated {
				applied, err = runUngatedPollerPersistenceForTest(pollCtx, persist)
			} else {
				applied, err = poller.admitPersistence(pollCtx, persist)
			}

			if tt.ungated {
				if err != nil || !applied {
					t.Fatalf("ungated persistence = (%v, %v), want (true, nil)", applied, err)
				}
			} else if !errors.Is(err, context.Canceled) || applied {
				t.Fatalf("gated persistence = (%v, %v), want (false, context.Canceled)", applied, err)
			}
			if got := repo.lateProviderUpdateCount(); got != tt.wantLate {
				t.Fatalf("late provider updates = %d, want %d", got, tt.wantLate)
			}
			if got := repo.updateCallCount(); got != tt.wantLate {
				t.Fatalf("provider update calls = %d, want %d", got, tt.wantLate)
			}
			repo.mu.Lock()
			_, updated := repo.updates["task_due"]
			repo.mu.Unlock()
			if updated != tt.ungated {
				t.Fatalf("provider update persisted = %v, want %v", updated, tt.ungated)
			}
		})
	}
}

func TestVideoTaskPollerPollOnceAfterStopDoesNotPerformWork(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	repo := &pollerVideoTaskRepositoryFake{claimed: []*VideoTask{{
		PublicTaskID: "task_due",
		AccountID:    99,
		Status:       VideoTaskStatusQueued,
	}}}
	provider := &pollerVideoTaskProviderFake{results: map[string]*VideoProviderFetchResult{
		"task_due": {Status: VideoTaskStatusCompleted},
	}}
	poller := NewVideoTaskPoller(repo, &pollerAccountRepositoryFake{accounts: map[int64]*Account{99: {ID: 99}}}, provider)
	poller.Stop()

	if err := poller.PollOnce(context.Background(), "worker-1", now); err != nil {
		t.Fatalf("PollOnce returned error: %v", err)
	}
	if got := repo.claimCallCount(); got != 0 {
		t.Fatalf("claim calls = %d, want none after Stop", got)
	}
	if got := pollerRenewalIDs(repo.renewals); len(got) != 0 {
		t.Fatalf("renewals = %#v, want none after Stop", got)
	}
	if got := len(provider.fetchAccountIDs); got != 0 {
		t.Fatalf("provider fetches = %d, want none after Stop", got)
	}
	if got := repo.updateCallCount(); got != 0 {
		t.Fatalf("provider updates/backoffs = %d, want none after Stop", got)
	}
}

func TestVideoTaskPollerPollOnceDoesNotProcessTasksWithCanceledContext(t *testing.T) {
	now := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	repo := &pollerVideoTaskRepositoryFake{claimed: []*VideoTask{{PublicTaskID: "task_due", AccountID: 99, Status: VideoTaskStatusQueued}}}
	accountRepo := &pollerAccountRepositoryFake{accounts: map[int64]*Account{99: {ID: 99}}}
	provider := &pollerVideoTaskProviderFake{results: map[string]*VideoProviderFetchResult{
		"task_due": {Status: VideoTaskStatusCompleted, ProviderStatus: "completed"},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	poller := NewVideoTaskPoller(repo, accountRepo, provider)
	err := poller.PollOnce(ctx, "worker-1", now)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("PollOnce error = %v, want context.Canceled", err)
	}
	if len(provider.fetchAccountIDs) != 0 {
		t.Fatalf("provider fetches = %#v, want none", provider.fetchAccountIDs)
	}
	if len(repo.updates) != 0 {
		t.Fatalf("provider updates = %#v, want none", repo.updates)
	}
	if got := repo.releaseTaskIDs(); len(got) != 0 {
		t.Fatalf("released tasks = %#v, want none", got)
	}
}

func TestVideoTaskPollerPollOnceReleasesUnvisitedClaimsAfterFetchCancellation(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	releaseErr := errors.New("unvisited release failed")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo := &pollerVideoTaskRepositoryFake{
		claimed: []*VideoTask{
			{PublicTaskID: "task_first", AccountID: 99, Status: VideoTaskStatusQueued},
			{PublicTaskID: "task_second", AccountID: 99, Status: VideoTaskStatusQueued},
			{PublicTaskID: "task_third", AccountID: 99, Status: VideoTaskStatusQueued},
		},
		releaseErrors: map[string]error{"task_second": releaseErr},
	}
	provider := &pollerVideoTaskProviderFake{
		results:          map[string]*VideoProviderFetchResult{"task_first": {Status: VideoTaskStatusInProgress, ProviderStatus: "processing"}},
		cancelAfterFetch: cancel,
	}
	poller := NewVideoTaskPoller(repo, &pollerAccountRepositoryFake{accounts: map[int64]*Account{99: {ID: 99}}}, provider)

	err := poller.PollOnce(ctx, "worker-1", now)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("PollOnce error = %v, want context.Canceled", err)
	}
	if !errors.Is(err, releaseErr) {
		t.Fatalf("PollOnce error = %v, want release error", err)
	}
	if _, fetched := provider.fetchAccountIDs["task_second"]; fetched {
		t.Fatal("provider Fetch called for unvisited second task")
	}
	if _, fetched := provider.fetchAccountIDs["task_third"]; fetched {
		t.Fatal("provider Fetch called for unvisited third task")
	}
	if _, updated := repo.updates["task_second"]; updated {
		t.Fatal("provider update applied to unvisited second task")
	}
	if _, updated := repo.updates["task_third"]; updated {
		t.Fatal("provider update applied to unvisited third task")
	}
	if _, updated := repo.updates["task_first"]; updated {
		t.Fatal("provider update applied after fetch canceled the poll context")
	}

	repo.mu.Lock()
	attempts := append([]pollerRelease(nil), repo.releaseAttempts...)
	released := append([]pollerRelease(nil), repo.releases...)
	renewals := append([]pollerRenewal(nil), repo.renewals...)
	events := append([]string(nil), repo.events...)
	releaseCtxErrs := make(map[string]error, len(repo.releaseCtxErrs))
	for taskID, ctxErr := range repo.releaseCtxErrs {
		releaseCtxErrs[taskID] = ctxErr
	}
	releaseDeadlines := make(map[string]time.Time, len(repo.releaseDeadlines))
	for taskID, deadline := range repo.releaseDeadlines {
		releaseDeadlines[taskID] = deadline
	}
	claimToken := repo.claimLockOwner
	repo.mu.Unlock()

	if got := pollerReleaseIDs(attempts); !reflect.DeepEqual(got, []string{"task_first", "task_second", "task_third"}) {
		t.Fatalf("release attempts = %#v, want each claimed task exactly once", got)
	}
	if got := pollerReleaseIDs(released); !reflect.DeepEqual(got, []string{"task_first", "task_third"}) {
		t.Fatalf("released tasks = %#v, want only successful releases", got)
	}
	if leaseToken, leaseUntil := repo.leaseFor("task_second"); leaseToken != claimToken || leaseUntil.IsZero() {
		t.Fatalf("failed release lease = (%q, %s), want (%q, non-zero)", leaseToken, leaseUntil, claimToken)
	}
	if wantEvents := []string{"release:task_first", "release:task_third"}; !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("poll events = %#v, want %#v", events, wantEvents)
	}
	if got := pollerRenewalIDs(renewals); !reflect.DeepEqual(got, []string{"task_first"}) {
		t.Fatalf("lease renewals = %#v, want only processed first task", got)
	}
	for _, attempt := range attempts {
		if attempt.lockOwner != claimToken {
			t.Fatalf("release lease token for %s = %q, want claimed token %q", attempt.publicTaskID, attempt.lockOwner, claimToken)
		}
		if releaseCtxErrs[attempt.publicTaskID] != nil {
			t.Fatalf("release context for %s was canceled: %v", attempt.publicTaskID, releaseCtxErrs[attempt.publicTaskID])
		}
		if deadline := releaseDeadlines[attempt.publicTaskID]; deadline.IsZero() || !deadline.After(time.Now()) {
			t.Fatalf("release deadline for %s = %s, want active bounded context", attempt.publicTaskID, deadline)
		}
	}
}

func TestVideoTaskPollerPollOnceSharesOneCleanupDeadlineForUnvisitedClaimsAfterCancellation(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	slowReleaseErr := errors.New("slow unvisited release failed")
	failingReleaseErr := errors.New("failing unvisited release failed")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo := &pollerVideoTaskRepositoryFake{
		claimed: []*VideoTask{
			{PublicTaskID: "task_first", AccountID: 99, Status: VideoTaskStatusQueued},
			{PublicTaskID: "task_second", AccountID: 99, Status: VideoTaskStatusQueued},
			{PublicTaskID: "task_third", AccountID: 99, Status: VideoTaskStatusQueued},
		},
		releaseWaitForDeadline: map[string]bool{
			"task_second": true,
			"task_third":  true,
		},
		releaseErrors: map[string]error{
			"task_second": slowReleaseErr,
			"task_third":  failingReleaseErr,
		},
	}
	provider := &pollerVideoTaskProviderFake{
		results:          map[string]*VideoProviderFetchResult{"task_first": {Status: VideoTaskStatusInProgress, ProviderStatus: "processing"}},
		cancelAfterFetch: cancel,
	}
	poller := NewVideoTaskPoller(repo, &pollerAccountRepositoryFake{accounts: map[int64]*Account{99: {ID: 99}}}, provider)

	startedAt := time.Now()
	err := poller.PollOnce(ctx, "worker-1", now)
	elapsed := time.Since(startedAt)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("PollOnce error = %v, want context.Canceled", err)
	}
	if !errors.Is(err, slowReleaseErr) {
		t.Fatalf("PollOnce error = %v, want slow release error", err)
	}
	if !errors.Is(err, failingReleaseErr) {
		t.Fatalf("PollOnce error = %v, want failing release error", err)
	}
	if elapsed > videoTaskPersistenceTimeout+time.Second {
		t.Fatalf("PollOnce cleanup took %s, want no more than one %s cleanup window", elapsed, videoTaskPersistenceTimeout)
	}

	repo.mu.Lock()
	attempts := append([]pollerRelease(nil), repo.releaseAttempts...)
	secondDeadline := repo.releaseDeadlines["task_second"]
	thirdDeadline := repo.releaseDeadlines["task_third"]
	thirdContextErr := repo.releaseCtxErrs["task_third"]
	repo.mu.Unlock()

	if got := pollerReleaseIDs(attempts); !reflect.DeepEqual(got, []string{"task_first", "task_second", "task_third"}) {
		t.Fatalf("release attempts = %#v, want every claimed task", got)
	}
	if secondDeadline.IsZero() || !secondDeadline.Equal(thirdDeadline) {
		t.Fatalf("unvisited release deadlines = (%s, %s), want one shared deadline", secondDeadline, thirdDeadline)
	}
	if !errors.Is(thirdContextErr, context.DeadlineExceeded) {
		t.Fatalf("third unvisited release context error = %v, want deadline exceeded without extending cleanup", thirdContextErr)
	}
	if _, updated := repo.updates["task_first"]; updated {
		t.Fatal("provider update applied after fetch canceled the poll context")
	}
}

func TestVideoTaskPollerPollOnceDoesNotPersistSuccessfulResultAfterFetchCancelsContext(t *testing.T) {
	now := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo := &pollerVideoTaskRepositoryFake{
		claimed:                   []*VideoTask{{PublicTaskID: "task_due", AccountID: 99, Status: VideoTaskStatusQueued, PollAttempts: 2}},
		rejectCanceledPersistence: true,
	}
	provider := &pollerVideoTaskProviderFake{
		results:          map[string]*VideoProviderFetchResult{"task_due": {Status: VideoTaskStatusInProgress, ProviderStatus: "processing"}},
		cancelAfterFetch: cancel,
	}
	poller := NewVideoTaskPoller(repo, &pollerAccountRepositoryFake{accounts: map[int64]*Account{99: {ID: 99}}}, provider)

	err := poller.PollOnce(ctx, "worker-1", now)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("PollOnce error = %v, want context.Canceled", err)
	}
	if _, updated := repo.updates["task_due"]; updated {
		t.Fatalf("successful result persisted after fetch canceled the poll context: %#v", repo.updates["task_due"])
	}
	if got := repo.releaseCtxErrs["task_due"]; got != nil {
		t.Fatalf("release context err = %v, want nil", got)
	}
	if got := repo.releaseTaskIDs(); !reflect.DeepEqual(got, []string{"task_due"}) {
		t.Fatalf("released tasks = %#v, want task_due", got)
	}
}

func TestVideoTaskPollerPollOnceUsesClaimedLeaseForUpdateAndRelease(t *testing.T) {
	for _, tt := range []struct {
		name        string
		providerErr error
		result      *VideoProviderFetchResult
	}{
		{
			name:   "successful result",
			result: &VideoProviderFetchResult{Status: VideoTaskStatusInProgress, ProviderStatus: "processing"},
		},
		{
			name:        "error reschedule",
			providerErr: errors.New("provider unavailable"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Now()
			provider := &pollerVideoTaskProviderFake{
				errors:  map[string]error{"task_due": tt.providerErr},
				results: map[string]*VideoProviderFetchResult{"task_due": tt.result},
			}
			repo := &pollerVideoTaskRepositoryFake{
				claimed:              []*VideoTask{{PublicTaskID: "task_due", AccountID: 99, Status: VideoTaskStatusQueued, PollAttempts: 2}},
				enforceLeaseContract: true,
				minimumLeaseValidAt: func(publicTaskID string) time.Time {
					return provider.fetchCompletedAt[publicTaskID]
				},
			}
			poller := NewVideoTaskPoller(repo, &pollerAccountRepositoryFake{accounts: map[int64]*Account{99: {ID: 99}}}, provider)

			err := poller.PollOnce(context.Background(), "worker-1", now)

			if tt.providerErr == nil && err != nil {
				t.Fatalf("PollOnce returned error: %v", err)
			}
			if tt.providerErr != nil && !errors.Is(err, tt.providerErr) {
				t.Fatalf("PollOnce error = %v, want provider error", err)
			}
			if got := repo.releaseTaskIDs(); !reflect.DeepEqual(got, []string{"task_due"}) {
				t.Fatalf("released tasks = %#v, want task_due", got)
			}
		})
	}
}

func TestVideoTaskPollerStartDoesNotStartWhileStopping(t *testing.T) {
	repo := &pollerVideoTaskRepositoryFake{
		claimStarted:    make(chan struct{}),
		blockFirstClaim: make(chan struct{}),
	}
	poller := NewVideoTaskPoller(repo, &pollerAccountRepositoryFake{}, &pollerVideoTaskProviderFake{})
	poller.interval = time.Hour

	poller.Start()
	waitForPollerTestSignal(t, repo.claimStarted, "first claim to start")

	stopDone := make(chan struct{})
	go func() {
		poller.Stop()
		close(stopDone)
	}()

	startDone := make(chan struct{})
	go func() {
		poller.Start()
		close(startDone)
	}()
	time.Sleep(25 * time.Millisecond)
	if got := repo.claimCallCount(); got != 1 {
		t.Fatalf("claim calls while stopping = %d, want 1", got)
	}

	close(repo.blockFirstClaim)
	waitForPollerTestSignal(t, stopDone, "stop to complete")
	waitForPollerTestSignal(t, startDone, "concurrent Start to return")

	poller.Start()
	waitForPollerTestCondition(t, func() bool { return repo.claimCallCount() >= 2 }, "claim after restart")
	poller.Stop()
}

type pollerVideoTaskRepositoryFake struct {
	VideoTaskRepository

	mu sync.Mutex

	claimed         []*VideoTask
	dueTasks        []*VideoTask
	claimErr        error
	claimNow        time.Time
	claimLimit      int
	claimLockOwner  string
	claimLockOwners []string
	claimTTL        time.Duration
	claimCalls      int
	claimHistory    [][]string

	claimStarted       chan struct{}
	firstClaimCanceled chan struct{}
	unblockFirstClaim  chan struct{}
	blockFirstClaim    chan struct{}

	updates              map[string]VideoTaskProviderUpdate
	updateCtxErrs        map[string]error
	updateErrors         map[string]error
	updateCalls          int
	lateProviderUpdates  int
	beforeProviderUpdate func()
	leaseLost            bool
	// The contract mode makes poller tests exercise the repository lease predicates.
	enforceLeaseContract      bool
	minimumLeaseValidAt       func(publicTaskID string) time.Time
	leaseTokens               map[string]string
	leaseExpirations          map[string]time.Time
	renewedLeaseExpirations   map[string]time.Time
	renewals                  []pollerRenewal
	rejectCanceledPersistence bool

	releases               []pollerRelease
	releaseAttempts        []pollerRelease
	releaseCtxErrs         map[string]error
	releaseDeadlines       map[string]time.Time
	releaseWaitForDeadline map[string]bool
	releaseErrors          map[string]error
	events                 []string
}

func (r *pollerVideoTaskRepositoryFake) GetByPublicTaskID(_ context.Context, publicTaskID string) (*VideoTask, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if update, ok := r.updates[publicTaskID]; ok {
		return &VideoTask{PublicTaskID: publicTaskID, Status: update.Status, ErrorMessage: update.ErrorMessage}, nil
	}
	for _, task := range r.claimed {
		if task != nil && task.PublicTaskID == publicTaskID {
			return clonePollerVideoTask(task), nil
		}
	}
	return nil, ErrVideoTaskNotFound
}

func (r *pollerVideoTaskRepositoryFake) ClaimDuePollTasks(ctx context.Context, now time.Time, limit int, lockOwner string, lockTTL time.Duration) ([]*VideoTask, error) {
	r.mu.Lock()
	r.claimCalls++
	call := r.claimCalls
	r.claimNow = now
	r.claimLimit = limit
	r.claimLockOwner = lockOwner
	r.claimLockOwners = append(r.claimLockOwners, lockOwner)
	r.claimTTL = lockTTL
	if call == 1 && r.claimStarted != nil {
		close(r.claimStarted)
	}
	r.mu.Unlock()

	if call == 1 && r.unblockFirstClaim != nil {
		<-ctx.Done()
		if r.firstClaimCanceled != nil {
			close(r.firstClaimCanceled)
		}
		<-r.unblockFirstClaim
	}
	if call == 1 && r.blockFirstClaim != nil {
		<-r.blockFirstClaim
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.claimErr != nil {
		return nil, r.claimErr
	}
	if r.dueTasks != nil {
		out := make([]*VideoTask, 0, limit)
		for _, task := range r.dueTasks {
			if len(out) >= limit {
				break
			}
			if task == nil || task.Status.Terminal() || task.NextPollAt == nil || task.NextPollAt.After(now) {
				continue
			}
			task.PollAttempts++
			out = append(out, clonePollerVideoTask(task))
		}
		r.recordClaimedLeases(out, lockOwner, now.Add(lockTTL))
		r.recordClaimedTaskIDs(out)
		return out, nil
	}
	out := make([]*VideoTask, 0, len(r.claimed))
	for _, task := range r.claimed {
		out = append(out, clonePollerVideoTask(task))
	}
	r.recordClaimedLeases(out, lockOwner, now.Add(lockTTL))
	r.recordClaimedTaskIDs(out)
	return out, nil
}

func (r *pollerVideoTaskRepositoryFake) UpdateFromProvider(ctx context.Context, publicTaskID string, update VideoTaskProviderUpdate) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updates == nil {
		r.updates = map[string]VideoTaskProviderUpdate{}
	}
	if r.updateCtxErrs == nil {
		r.updateCtxErrs = map[string]error{}
	}
	r.updateCtxErrs[publicTaskID] = ctx.Err()
	if r.rejectCanceledPersistence && ctx.Err() != nil {
		return false, ctx.Err()
	}
	r.updates[publicTaskID] = update
	r.events = append(r.events, "update:"+publicTaskID)
	if err := r.updateErrors[publicTaskID]; err != nil {
		return false, err
	}
	for _, task := range r.dueTasks {
		if task == nil || task.PublicTaskID != publicTaskID {
			continue
		}
		if update.Status != "" {
			task.Status = update.Status
		}
		if update.ClearNextPollAt {
			task.NextPollAt = nil
		} else if update.NextPollAt != nil {
			nextPollAt := *update.NextPollAt
			task.NextPollAt = &nextPollAt
		}
		break
	}
	return true, nil
}

func (r *pollerVideoTaskRepositoryFake) UpdateFromProviderWithPollLease(ctx context.Context, publicTaskID, leaseToken string, validAt time.Time, update VideoTaskProviderUpdate) (bool, error) {
	r.mu.Lock()
	r.updateCalls++
	lost := r.leaseLost
	enforceLeaseContract := r.enforceLeaseContract
	claimedLeaseToken, claimed := r.leaseTokens[publicTaskID]
	leaseExpiration := r.leaseExpirations[publicTaskID]
	minimumLeaseValidAt := r.minimumLeaseValidAt
	beforeProviderUpdate := r.beforeProviderUpdate
	r.mu.Unlock()
	if beforeProviderUpdate != nil {
		beforeProviderUpdate()
	}
	if lost {
		return false, nil
	}
	if enforceLeaseContract {
		if !claimed || leaseToken != claimedLeaseToken {
			return false, nil
		}
		if !leaseExpiration.After(validAt) {
			return false, nil
		}
		if minimumLeaseValidAt != nil {
			if fetchCompletedAt := minimumLeaseValidAt(publicTaskID); !fetchCompletedAt.IsZero() && validAt.Before(fetchCompletedAt) {
				return false, fmt.Errorf("poll lease validAt %s precedes fetch completion %s", validAt, fetchCompletedAt)
			}
		}
	}
	return r.UpdateFromProvider(ctx, publicTaskID, update)
}

func (r *pollerVideoTaskRepositoryFake) RenewPollLock(ctx context.Context, publicTaskID, leaseToken string, validAt time.Time, lockTTL time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	claimedLeaseToken, claimed := r.leaseTokens[publicTaskID]
	leaseExpiration := r.leaseExpirations[publicTaskID]
	enforceLeaseContract := r.enforceLeaseContract
	r.renewals = append(r.renewals, pollerRenewal{publicTaskID: publicTaskID, leaseToken: leaseToken, validAt: validAt, lockTTL: lockTTL})
	if enforceLeaseContract && (!claimed || claimedLeaseToken != leaseToken || !leaseExpiration.After(validAt)) {
		return false, nil
	}
	r.leaseExpirations[publicTaskID] = validAt.Add(lockTTL)
	if r.renewedLeaseExpirations == nil {
		r.renewedLeaseExpirations = map[string]time.Time{}
	}
	r.renewedLeaseExpirations[publicTaskID] = validAt.Add(lockTTL)
	return true, nil
}

func (r *pollerVideoTaskRepositoryFake) recordClaimedLeases(tasks []*VideoTask, leaseToken string, leaseExpiration time.Time) {
	if r.leaseTokens == nil {
		r.leaseTokens = map[string]string{}
	}
	if r.leaseExpirations == nil {
		r.leaseExpirations = map[string]time.Time{}
	}
	for _, task := range tasks {
		if task == nil {
			continue
		}
		r.leaseTokens[task.PublicTaskID] = leaseToken
		r.leaseExpirations[task.PublicTaskID] = leaseExpiration
	}
}

func (r *pollerVideoTaskRepositoryFake) recordClaimedTaskIDs(tasks []*VideoTask) {
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		if task != nil {
			ids = append(ids, task.PublicTaskID)
		}
	}
	r.claimHistory = append(r.claimHistory, ids)
}

func (r *pollerVideoTaskRepositoryFake) ReleasePollLock(ctx context.Context, publicTaskID, lockOwner string) (bool, error) {
	r.mu.Lock()
	if r.releaseCtxErrs == nil {
		r.releaseCtxErrs = map[string]error{}
	}
	r.releaseCtxErrs[publicTaskID] = ctx.Err()
	if r.releaseDeadlines == nil {
		r.releaseDeadlines = map[string]time.Time{}
	}
	deadline, _ := ctx.Deadline()
	r.releaseDeadlines[publicTaskID] = deadline
	if r.rejectCanceledPersistence && ctx.Err() != nil {
		r.mu.Unlock()
		return false, ctx.Err()
	}
	if claimedLeaseToken, claimed := r.leaseTokens[publicTaskID]; !claimed || lockOwner != claimedLeaseToken {
		r.mu.Unlock()
		return false, nil
	}
	r.releaseAttempts = append(r.releaseAttempts, pollerRelease{publicTaskID: publicTaskID, lockOwner: lockOwner})
	waitForDeadline := r.releaseWaitForDeadline[publicTaskID]
	r.mu.Unlock()

	if waitForDeadline {
		<-ctx.Done()
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.releaseErrors[publicTaskID]; err != nil {
		return false, err
	}
	r.releases = append(r.releases, pollerRelease{publicTaskID: publicTaskID, lockOwner: lockOwner})
	r.events = append(r.events, "release:"+publicTaskID)
	delete(r.leaseTokens, publicTaskID)
	delete(r.leaseExpirations, publicTaskID)
	return true, nil
}

func (r *pollerVideoTaskRepositoryFake) replaceLease(publicTaskID, leaseToken string, leaseUntil time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.leaseTokens == nil {
		r.leaseTokens = map[string]string{}
	}
	if r.leaseExpirations == nil {
		r.leaseExpirations = map[string]time.Time{}
	}
	r.leaseTokens[publicTaskID] = leaseToken
	r.leaseExpirations[publicTaskID] = leaseUntil
}

func (r *pollerVideoTaskRepositoryFake) leaseFor(publicTaskID string) (string, time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.leaseTokens[publicTaskID], r.leaseExpirations[publicTaskID]
}

func (r *pollerVideoTaskRepositoryFake) renewedLeaseUntil(publicTaskID string) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.renewedLeaseExpirations[publicTaskID]
}

func assertPollErrorBackoffBeforeRelease(t *testing.T, repo *pollerVideoTaskRepositoryFake, publicTaskID string, oldDueAt time.Time, wantNextPollAt time.Time) {
	t.Helper()
	repo.mu.Lock()
	defer repo.mu.Unlock()

	update, ok := repo.updates[publicTaskID]
	if !ok {
		t.Fatalf("missing backoff update for %s", publicTaskID)
	}
	if update.NextPollAt == nil || !update.NextPollAt.Equal(wantNextPollAt) {
		t.Fatalf("next poll = %#v, want %s", update.NextPollAt, wantNextPollAt)
	}
	if !update.NextPollAt.After(oldDueAt) {
		t.Fatalf("next poll = %s, want after original due time %s", update.NextPollAt, oldDueAt)
	}
	if update.Status != "" || update.ProviderStatus != "" || len(update.ResponseBody) != 0 || update.Metadata != nil || update.ErrorMessage != "" || update.CompletedAt != nil || update.ExpiresAt != nil || update.ClearNextPollAt {
		t.Fatalf("backoff update changed task state: %#v", update)
	}
	if err := repo.updateCtxErrs[publicTaskID]; err != nil {
		t.Fatalf("backoff update context error = %v, want nil", err)
	}
	if err := repo.releaseCtxErrs[publicTaskID]; err != nil {
		t.Fatalf("release context error = %v, want nil", err)
	}
	wantEvents := []string{"update:" + publicTaskID, "release:" + publicTaskID}
	if !reflect.DeepEqual(repo.events, wantEvents) {
		t.Fatalf("poll events = %#v, want %#v", repo.events, wantEvents)
	}
}

func (r *pollerVideoTaskRepositoryFake) releaseTaskIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.releases))
	for _, release := range r.releases {
		out = append(out, release.publicTaskID)
	}
	return out
}

func (r *pollerVideoTaskRepositoryFake) updateCallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.updateCalls
}

func (r *pollerVideoTaskRepositoryFake) recordLateProviderUpdate() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lateProviderUpdates++
}

func (r *pollerVideoTaskRepositoryFake) lateProviderUpdateCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lateProviderUpdates
}

func (r *pollerVideoTaskRepositoryFake) claimCallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.claimCalls
}

func requirePollerClaimedIDs(t *testing.T, repo *pollerVideoTaskRepositoryFake, call int, want []string) {
	t.Helper()
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if call >= len(repo.claimHistory) {
		t.Fatalf("claim history has %d calls, want call %d", len(repo.claimHistory), call)
	}
	if !reflect.DeepEqual(repo.claimHistory[call], want) {
		t.Fatalf("claim %d task IDs = %#v, want %#v", call+1, repo.claimHistory[call], want)
	}
}

func pollerFailingTaskIDs() []string {
	ids := make([]string, videoTaskPollLimit)
	for i := range ids {
		ids[i] = fmt.Sprintf("task_failing_%02d", i)
	}
	return ids
}

func pollerTimePtr(value time.Time) *time.Time {
	return &value
}

type pollerRelease struct {
	publicTaskID string
	lockOwner    string
}

func pollerReleaseIDs(releases []pollerRelease) []string {
	ids := make([]string, 0, len(releases))
	for _, release := range releases {
		ids = append(ids, release.publicTaskID)
	}
	return ids
}

func pollerRenewalIDs(renewals []pollerRenewal) []string {
	ids := make([]string, 0, len(renewals))
	for _, renewal := range renewals {
		ids = append(ids, renewal.publicTaskID)
	}
	return ids
}

type pollerRenewal struct {
	publicTaskID string
	leaseToken   string
	validAt      time.Time
	lockTTL      time.Duration
}

type pollerAccountRepositoryFake struct {
	AccountRepository

	accounts   map[int64]*Account
	err        error
	errors     map[int64]error
	getStarted chan struct{}
	blockGet   chan struct{}
}

func (r *pollerAccountRepositoryFake) GetByID(ctx context.Context, id int64) (*Account, error) {
	if r.getStarted != nil {
		r.getStarted <- struct{}{}
	}
	if r.blockGet != nil {
		<-r.blockGet
	}
	if r.err != nil {
		return nil, r.err
	}
	if err := r.errors[id]; err != nil {
		return nil, err
	}
	account := r.accounts[id]
	if account == nil {
		return nil, errors.New("account not found")
	}
	copy := *account
	return &copy, nil
}

type pollerVideoTaskProviderFake struct {
	results               map[string]*VideoProviderFetchResult
	errors                map[string]error
	fetchDelay            time.Duration
	cancelAfterFetch      context.CancelFunc
	afterFetch            func(taskID string)
	requireFetchDeadline  bool
	slowFetch             bool
	fetchCancellationSeen chan struct{}

	fetchAccountIDs  map[string]int64
	fetchCtxErrs     map[string]error
	fetchCompletedAt map[string]time.Time
	fetchDeadlines   map[string]time.Time
	fetchContexts    map[string]context.Context
}

type blockingPollerVideoTaskProvider struct {
	started  chan struct{}
	unblock  chan struct{}
	returned chan struct{}
	result   *VideoProviderFetchResult
}

func (p *blockingPollerVideoTaskProvider) Create(context.Context, *Account, []byte, string, string) (*VideoProviderCreateResult, error) {
	return nil, errors.New("create not implemented")
}

func (p *blockingPollerVideoTaskProvider) Fetch(context.Context, *Account, *VideoTask) (*VideoProviderFetchResult, error) {
	p.started <- struct{}{}
	<-p.unblock
	p.returned <- struct{}{}
	return p.result, nil
}

func (p *blockingPollerVideoTaskProvider) Content(context.Context, *Account, *VideoTask, http.Header) (*VideoContentStream, error) {
	return nil, errors.New("content not implemented")
}

type cooperativeBlockingPollerVideoTaskProvider struct {
	started          chan struct{}
	cancellationSeen chan struct{}
	unblock          chan struct{}
}

func (p *cooperativeBlockingPollerVideoTaskProvider) Create(context.Context, *Account, []byte, string, string) (*VideoProviderCreateResult, error) {
	return nil, errors.New("create not implemented")
}

func (p *cooperativeBlockingPollerVideoTaskProvider) Fetch(ctx context.Context, _ *Account, _ *VideoTask) (*VideoProviderFetchResult, error) {
	p.started <- struct{}{}
	<-ctx.Done()
	p.cancellationSeen <- struct{}{}
	<-p.unblock
	return nil, ctx.Err()
}

func (p *cooperativeBlockingPollerVideoTaskProvider) Content(context.Context, *Account, *VideoTask, http.Header) (*VideoContentStream, error) {
	return nil, errors.New("content not implemented")
}

func (p *pollerVideoTaskProviderFake) Create(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) (*VideoProviderCreateResult, error) {
	return nil, errors.New("create not implemented")
}

func (p *pollerVideoTaskProviderFake) Fetch(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error) {
	if p.fetchCancellationSeen != nil {
		go func() {
			<-ctx.Done()
			p.fetchCancellationSeen <- struct{}{}
		}()
	}
	if p.fetchAccountIDs == nil {
		p.fetchAccountIDs = map[string]int64{}
	}
	if p.fetchCtxErrs == nil {
		p.fetchCtxErrs = map[string]error{}
	}
	if account != nil && task != nil {
		p.fetchAccountIDs[task.PublicTaskID] = account.ID
		p.fetchCtxErrs[task.PublicTaskID] = ctx.Err()
		if p.fetchContexts == nil {
			p.fetchContexts = map[string]context.Context{}
		}
		p.fetchContexts[task.PublicTaskID] = ctx
		if deadline, ok := ctx.Deadline(); ok {
			if p.fetchDeadlines == nil {
				p.fetchDeadlines = map[string]time.Time{}
			}
			p.fetchDeadlines[task.PublicTaskID] = deadline
		} else if p.requireFetchDeadline {
			return nil, errors.New("poller fetch context has no deadline")
		}
	}
	if task == nil {
		return nil, errors.New("task is nil")
	}
	if p.fetchDelay > 0 {
		time.Sleep(p.fetchDelay)
	}
	if p.slowFetch {
		return nil, context.DeadlineExceeded
	}
	if p.fetchCompletedAt == nil {
		p.fetchCompletedAt = map[string]time.Time{}
	}
	p.fetchCompletedAt[task.PublicTaskID] = time.Now()
	if p.cancelAfterFetch != nil {
		p.cancelAfterFetch()
	}
	if p.afterFetch != nil {
		p.afterFetch(task.PublicTaskID)
	}
	if err := p.errors[task.PublicTaskID]; err != nil {
		return nil, err
	}
	result, configured := p.results[task.PublicTaskID]
	if configured && result == nil {
		return nil, nil
	}
	if result == nil {
		result = &VideoProviderFetchResult{Status: VideoTaskStatusQueued, ProviderStatus: "queued"}
	}
	copy := *result
	copy.RawBody = append([]byte(nil), result.RawBody...)
	copy.Metadata = clonePollerMap(result.Metadata)
	return &copy, nil
}

func (p *pollerVideoTaskProviderFake) fetchDeadline(taskID string) (time.Time, bool) {
	deadline, ok := p.fetchDeadlines[taskID]
	return deadline, ok
}

func (p *pollerVideoTaskProviderFake) fetchContext(taskID string) context.Context {
	return p.fetchContexts[taskID]
}

func waitForPollerTestSignal(t *testing.T, ch <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitForPollerTestCondition(t *testing.T, condition func() bool, description string) {
	t.Helper()
	deadline := time.After(time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", description)
		case <-tick.C:
		}
	}
}

func (p *pollerVideoTaskProviderFake) Content(ctx context.Context, account *Account, task *VideoTask, headers http.Header) (*VideoContentStream, error) {
	return nil, errors.New("content not implemented")
}

func clonePollerVideoTask(task *VideoTask) *VideoTask {
	if task == nil {
		return nil
	}
	copy := *task
	copy.RequestBody = append([]byte(nil), task.RequestBody...)
	copy.ResponseBody = append([]byte(nil), task.ResponseBody...)
	copy.Metadata = clonePollerMap(task.Metadata)
	return &copy
}

func clonePollerMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func runUngatedPollerPersistenceForTest(ctx context.Context, persist func(context.Context) (bool, error)) (bool, error) {
	updateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), videoTaskPersistenceTimeout)
	defer cancel()
	return persist(updateCtx)
}
