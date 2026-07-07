//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

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
	if repo.claimLockOwner != "worker-1" {
		t.Fatalf("claim lock owner = %q, want worker-1", repo.claimLockOwner)
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

func TestVideoTaskPollerPollOnceReleasesLockOnProviderError(t *testing.T) {
	now := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	providerErr := errors.New("provider unavailable")
	repo := &pollerVideoTaskRepositoryFake{claimed: []*VideoTask{{PublicTaskID: "task_due", AccountID: 99, Status: VideoTaskStatusQueued}}}
	accountRepo := &pollerAccountRepositoryFake{accounts: map[int64]*Account{99: {ID: 99}}}
	provider := &pollerVideoTaskProviderFake{errors: map[string]error{"task_due": providerErr}}

	poller := NewVideoTaskPoller(repo, accountRepo, provider)
	err := poller.PollOnce(context.Background(), "worker-1", now)

	if !errors.Is(err, providerErr) {
		t.Fatalf("PollOnce error = %v, want provider error", err)
	}
	if !strings.Contains(err.Error(), "task_due") {
		t.Fatalf("PollOnce error = %v, want public task id", err)
	}
	if len(repo.updates) != 0 {
		t.Fatalf("updates = %#v, want none", repo.updates)
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

func TestVideoTaskPollerPollOnceReleasesLockWithNonCanceledContext(t *testing.T) {
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

	if err != nil {
		t.Fatalf("PollOnce returned error: %v", err)
	}
	if got := provider.fetchCtxErrs["task_due"]; !errors.Is(got, context.Canceled) {
		t.Fatalf("provider fetch context err = %v, want context.Canceled", got)
	}
	if got := repo.updateCtxErrs["task_due"]; !errors.Is(got, context.Canceled) {
		t.Fatalf("update context err = %v, want context.Canceled", got)
	}
	if got := repo.releaseCtxErrs["task_due"]; got != nil {
		t.Fatalf("release context err = %v, want nil", got)
	}
}

func TestVideoTaskPollerStartDoesNotStartWhileStopping(t *testing.T) {
	repo := &pollerVideoTaskRepositoryFake{
		claimStarted:       make(chan struct{}),
		firstClaimCanceled: make(chan struct{}),
		unblockFirstClaim:  make(chan struct{}),
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
	waitForPollerTestSignal(t, repo.firstClaimCanceled, "first claim cancellation")

	poller.Start()
	time.Sleep(25 * time.Millisecond)
	if got := repo.claimCallCount(); got != 1 {
		t.Fatalf("claim calls while stopping = %d, want 1", got)
	}

	close(repo.unblockFirstClaim)
	waitForPollerTestSignal(t, stopDone, "stop to complete")

	poller.Start()
	waitForPollerTestCondition(t, func() bool { return repo.claimCallCount() >= 2 }, "claim after restart")
	poller.Stop()
}

type pollerVideoTaskRepositoryFake struct {
	VideoTaskRepository

	mu sync.Mutex

	claimed        []*VideoTask
	claimErr       error
	claimNow       time.Time
	claimLimit     int
	claimLockOwner string
	claimTTL       time.Duration
	claimCalls     int

	claimStarted       chan struct{}
	firstClaimCanceled chan struct{}
	unblockFirstClaim  chan struct{}

	updates       map[string]VideoTaskProviderUpdate
	updateCtxErrs map[string]error

	releases       []pollerRelease
	releaseCtxErrs map[string]error
}

func (r *pollerVideoTaskRepositoryFake) ClaimDuePollTasks(ctx context.Context, now time.Time, limit int, lockOwner string, lockTTL time.Duration) ([]*VideoTask, error) {
	r.mu.Lock()
	r.claimCalls++
	call := r.claimCalls
	r.claimNow = now
	r.claimLimit = limit
	r.claimLockOwner = lockOwner
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

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.claimErr != nil {
		return nil, r.claimErr
	}
	out := make([]*VideoTask, 0, len(r.claimed))
	for _, task := range r.claimed {
		out = append(out, clonePollerVideoTask(task))
	}
	return out, nil
}

func (r *pollerVideoTaskRepositoryFake) UpdateFromProvider(ctx context.Context, publicTaskID string, update VideoTaskProviderUpdate) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updates == nil {
		r.updates = map[string]VideoTaskProviderUpdate{}
	}
	if r.updateCtxErrs == nil {
		r.updateCtxErrs = map[string]error{}
	}
	r.updateCtxErrs[publicTaskID] = ctx.Err()
	r.updates[publicTaskID] = update
	return nil
}

func (r *pollerVideoTaskRepositoryFake) ReleasePollLock(ctx context.Context, publicTaskID string, lockOwner string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.releaseCtxErrs == nil {
		r.releaseCtxErrs = map[string]error{}
	}
	r.releaseCtxErrs[publicTaskID] = ctx.Err()
	r.releases = append(r.releases, pollerRelease{publicTaskID: publicTaskID, lockOwner: lockOwner})
	return nil
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

func (r *pollerVideoTaskRepositoryFake) claimCallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.claimCalls
}

type pollerRelease struct {
	publicTaskID string
	lockOwner    string
}

type pollerAccountRepositoryFake struct {
	AccountRepository

	accounts map[int64]*Account
	err      error
}

func (r *pollerAccountRepositoryFake) GetByID(ctx context.Context, id int64) (*Account, error) {
	if r.err != nil {
		return nil, r.err
	}
	account := r.accounts[id]
	if account == nil {
		return nil, errors.New("account not found")
	}
	copy := *account
	return &copy, nil
}

type pollerVideoTaskProviderFake struct {
	results map[string]*VideoProviderFetchResult
	errors  map[string]error

	fetchAccountIDs map[string]int64
	fetchCtxErrs    map[string]error
}

func (p *pollerVideoTaskProviderFake) Create(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) (*VideoProviderCreateResult, error) {
	return nil, errors.New("create not implemented")
}

func (p *pollerVideoTaskProviderFake) Fetch(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error) {
	if p.fetchAccountIDs == nil {
		p.fetchAccountIDs = map[string]int64{}
	}
	if p.fetchCtxErrs == nil {
		p.fetchCtxErrs = map[string]error{}
	}
	if account != nil && task != nil {
		p.fetchAccountIDs[task.PublicTaskID] = account.ID
		p.fetchCtxErrs[task.PublicTaskID] = ctx.Err()
	}
	if task == nil {
		return nil, errors.New("task is nil")
	}
	if err := p.errors[task.PublicTaskID]; err != nil {
		return nil, err
	}
	result := p.results[task.PublicTaskID]
	if result == nil {
		result = &VideoProviderFetchResult{Status: VideoTaskStatusQueued, ProviderStatus: "queued"}
	}
	copy := *result
	copy.RawBody = append([]byte(nil), result.RawBody...)
	copy.Metadata = clonePollerMap(result.Metadata)
	return &copy, nil
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
