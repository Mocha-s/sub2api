//go:build unit

package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

func TestVideoTaskRepositoryImplementsServiceInterface(t *testing.T) {
	var _ service.VideoTaskRepository = (*videoTaskRepository)(nil)
}

func TestVideoTaskRepositoryGetByPublicTaskIDForUserEnforcesOwnership(t *testing.T) {
	repo, client := newVideoTaskRepoSQLite(t)
	ctx := context.Background()
	mustCreateVideoTaskListItem(t, ctx, client, "task_owned", 7, "seedance-2.0", service.VideoTaskStatusCompleted, time.Now())

	task, err := repo.GetByPublicTaskIDForUser(ctx, "task_owned", 7)
	if err != nil {
		t.Fatalf("same-user lookup returned error: %v", err)
	}
	if task == nil || task.UserID != 7 {
		t.Fatalf("same-user lookup returned task %#v, want user 7 task", task)
	}

	task, err = repo.GetByPublicTaskIDForUser(ctx, "task_owned", 8)
	if task != nil {
		t.Fatalf("cross-user lookup returned task %#v, want nil", task)
	}
	if !errors.Is(err, errVideoTaskNotFound) {
		t.Fatalf("cross-user lookup error = %v, want %v", err, errVideoTaskNotFound)
	}
}

func TestVideoTaskRepositoryListForUserFiltersOrdersAndLimits(t *testing.T) {
	repo, client := newVideoTaskRepoSQLite(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	mustCreateVideoTaskListItem(t, ctx, client, "task_old", 7, "seedance-2.0", service.VideoTaskStatusCompleted, base.Add(-time.Minute))
	mustCreateVideoTaskListItem(t, ctx, client, "task_new", 7, "seedance-2.0", service.VideoTaskStatusCompleted, base)
	mustCreateVideoTaskListItem(t, ctx, client, "task_other_user", 8, "seedance-2.0", service.VideoTaskStatusCompleted, base.Add(time.Minute))
	mustCreateVideoTaskListItem(t, ctx, client, "task_other_model", 7, "other-model", service.VideoTaskStatusCompleted, base.Add(2*time.Minute))
	mustCreateVideoTaskListItem(t, ctx, client, "task_other_status", 7, "seedance-2.0", service.VideoTaskStatusFailed, base.Add(3*time.Minute))

	items, hasMore, err := repo.ListForUser(ctx, service.VideoTaskListParams{UserID: 7, Status: string(service.VideoTaskStatusCompleted), Model: "seedance-2.0", Limit: 1})

	if err != nil {
		t.Fatalf("ListForUser returned error: %v", err)
	}
	if !hasMore {
		t.Fatal("ListForUser hasMore = false, want true")
	}
	if len(items) != 1 {
		t.Fatalf("ListForUser returned %d items, want 1", len(items))
	}
	if items[0].PublicTaskID != "task_new" {
		t.Fatalf("ListForUser first task = %q, want task_new", items[0].PublicTaskID)
	}
}

func TestVideoTaskRepositoryListForUserAppliesTimeCursors(t *testing.T) {
	repo, client := newVideoTaskRepoSQLite(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	mustCreateVideoTaskListItem(t, ctx, client, "task_before", 7, "seedance-2.0", service.VideoTaskStatusCompleted, base.Add(-time.Minute))
	mustCreateVideoTaskListItem(t, ctx, client, "task_after_boundary", 7, "seedance-2.0", service.VideoTaskStatusCompleted, base)
	mustCreateVideoTaskListItem(t, ctx, client, "task_inside", 7, "seedance-2.0", service.VideoTaskStatusCompleted, base.Add(time.Minute))
	mustCreateVideoTaskListItem(t, ctx, client, "task_before_boundary", 7, "seedance-2.0", service.VideoTaskStatusCompleted, base.Add(2*time.Minute))
	mustCreateVideoTaskListItem(t, ctx, client, "task_after", 7, "seedance-2.0", service.VideoTaskStatusCompleted, base.Add(3*time.Minute))

	items, hasMore, err := repo.ListForUser(ctx, service.VideoTaskListParams{UserID: 7, After: base, Before: base.Add(2 * time.Minute), Limit: 20})

	if err != nil {
		t.Fatalf("ListForUser returned error: %v", err)
	}
	if hasMore {
		t.Fatal("ListForUser hasMore = true, want false")
	}
	assertVideoTaskIDs(t, items, "task_inside")
}

func TestVideoTaskRepositoryListForUserOrdersTiesByIDDesc(t *testing.T) {
	repo, client := newVideoTaskRepoSQLite(t)
	ctx := context.Background()
	createdAt := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	mustCreateVideoTaskListItem(t, ctx, client, "task_tie_low", 7, "seedance-2.0", service.VideoTaskStatusCompleted, createdAt)
	mustCreateVideoTaskListItem(t, ctx, client, "task_tie_high", 7, "seedance-2.0", service.VideoTaskStatusCompleted, createdAt)

	items, hasMore, err := repo.ListForUser(ctx, service.VideoTaskListParams{UserID: 7, Limit: 20})

	if err != nil {
		t.Fatalf("ListForUser returned error: %v", err)
	}
	if hasMore {
		t.Fatal("ListForUser hasMore = true, want false")
	}
	assertVideoTaskIDs(t, items, "task_tie_high", "task_tie_low")
}

func TestVideoTaskEntToServiceMapsLockFields(t *testing.T) {
	lockedBy := "poller-1"
	lockedUntil := time.Date(2026, 7, 2, 12, 30, 0, 0, time.UTC)
	nextPollAt := lockedUntil.Add(5 * time.Second)
	lastPolledAt := lockedUntil.Add(-5 * time.Second)

	task := videoTaskEntToService(&dbent.VideoTask{
		ID:             1,
		PublicTaskID:   "task_public",
		Provider:       service.VideoTaskProviderOpenAICompatible,
		Platform:       service.VideoTaskPlatformOpenAIVideo,
		UserID:         2,
		APIKeyID:       3,
		GroupID:        4,
		AccountID:      5,
		RequestedModel: "sora-2",
		Prompt:         "make a video",
		Status:         string(service.VideoTaskStatusQueued),
		RequestHash:    "request-hash",
		CreatedAt:      lockedUntil,
		UpdatedAt:      lockedUntil,
		NextPollAt:     &nextPollAt,
		LastPolledAt:   &lastPolledAt,
		LockedBy:       &lockedBy,
		LockedUntil:    &lockedUntil,
		PollAttempts:   3,
	})

	if task.LockedBy == nil || *task.LockedBy != lockedBy {
		t.Fatalf("LockedBy = %#v, want %q", task.LockedBy, lockedBy)
	}
	if task.LockedUntil == nil || !task.LockedUntil.Equal(lockedUntil) {
		t.Fatalf("LockedUntil = %#v, want %s", task.LockedUntil, lockedUntil.Format(time.RFC3339))
	}
	if task.NextPollAt == nil || !task.NextPollAt.Equal(nextPollAt) {
		t.Fatalf("NextPollAt = %#v, want %s", task.NextPollAt, nextPollAt.Format(time.RFC3339))
	}
	if task.LastPolledAt == nil || !task.LastPolledAt.Equal(lastPolledAt) {
		t.Fatalf("LastPolledAt = %#v, want %s", task.LastPolledAt, lastPolledAt.Format(time.RFC3339))
	}
	if task.PollAttempts != 3 {
		t.Fatalf("PollAttempts = %d, want 3", task.PollAttempts)
	}
}

func TestVideoTaskEntToServiceMapsSettlementSummaries(t *testing.T) {
	subscriptionID, usageLogID := int64(17), int64(23)
	task := videoTaskEntToService(&dbent.VideoTask{
		PublicTaskID: "task_summary", Provider: "openai_compatible", Platform: "openai_video",
		UserID: 1, APIKeyID: 2, GroupID: 3, AccountID: 4, RequestedModel: "sora", Prompt: "city",
		Status: "queued", RequestHash: "hash", SubscriptionID: &subscriptionID, UsageLogID: &usageLogID,
		UsageMetadata: map[string]any{"request_id": "video:task_summary:charge"}, BilledUsd: 1.25,
	})

	if task.SubscriptionID == nil || *task.SubscriptionID != subscriptionID {
		t.Fatalf("SubscriptionID = %#v, want %d", task.SubscriptionID, subscriptionID)
	}
	if task.UsageLogID == nil || *task.UsageLogID != usageLogID {
		t.Fatalf("UsageLogID = %#v, want %d", task.UsageLogID, usageLogID)
	}
	if task.UsageMetadata["request_id"] != "video:task_summary:charge" || task.BilledUSD != 1.25 {
		t.Fatalf("settlement summary = metadata %#v billed %v", task.UsageMetadata, task.BilledUSD)
	}
}

func TestVideoTaskRepositoryPersistUpstreamFallbackRejectsTerminalTask(t *testing.T) {
	repo, _ := newVideoTaskRepoSQLite(t)
	ctx := context.Background()
	created, err := repo.Create(ctx, service.VideoTaskCreateInput{PublicTaskID: "task_terminal_fallback", Provider: "test", Platform: service.PlatformOpenAI, UserID: 1, APIKeyID: 2, GroupID: 3, AccountID: 4, Model: "sora", Prompt: "city", RequestHash: "hash", Metadata: map[string]any{}})
	if err != nil {
		t.Fatalf("create video task: %v", err)
	}
	_, err = repo.UpdateFromProvider(ctx, created.PublicTaskID, service.VideoTaskProviderUpdate{Status: service.VideoTaskStatusFailed})
	if err != nil {
		t.Fatalf("mark terminal: %v", err)
	}

	err = repo.PersistUpstreamFallback(ctx, created.PublicTaskID, service.VideoTaskUpstreamFallback{Snapshot: service.VideoTaskAcceptedSnapshot{ProviderTaskID: "upstream"}})

	if err == nil {
		t.Fatal("PersistUpstreamFallback returned nil for terminal task")
	}
}

func TestVideoTaskRepositoryPersistUpstreamFallbackRoundTripKeepsNonterminalState(t *testing.T) {
	repo, _ := newVideoTaskRepoSQLite(t)
	ctx := context.Background()
	created, err := repo.Create(ctx, service.VideoTaskCreateInput{PublicTaskID: "task_fallback_roundtrip", Provider: "test", Platform: service.PlatformOpenAI, UserID: 1, APIKeyID: 2, GroupID: 3, AccountID: 4, Model: "sora", Prompt: "city", RequestHash: "hash", Metadata: map[string]any{"existing": "preserved"}})
	if err != nil {
		t.Fatalf("create video task: %v", err)
	}

	err = repo.PersistUpstreamFallback(ctx, created.PublicTaskID, service.VideoTaskUpstreamFallback{Snapshot: service.VideoTaskAcceptedSnapshot{ProviderTaskID: "upstream_fallback", ProviderStatus: "queued"}})
	if err != nil {
		t.Fatalf("PersistUpstreamFallback: %v", err)
	}
	stored, err := repo.GetByPublicTaskID(ctx, created.PublicTaskID)
	if err != nil {
		t.Fatalf("GetByPublicTaskID: %v", err)
	}
	if stored.ProviderTaskID != "" || stored.Status != service.VideoTaskStatusSubmitting {
		t.Fatalf("fallback changed primary state: provider=%q status=%q", stored.ProviderTaskID, stored.Status)
	}
	if stored.Metadata["existing"] != "preserved" || stored.Metadata["reconciliation_upstream_task_id"] != "upstream_fallback" || stored.Metadata["reconciliation_error_code"] != "ATTACH_UPSTREAM_FAILED" {
		t.Fatalf("fallback metadata = %#v", stored.Metadata)
	}
}

func TestVideoTaskEntToServiceMergesRequestAndResultMetadata(t *testing.T) {
	requestMetadata := map[string]any{
		service.VideoAdapterMetadataKey:      service.VideoAdapterJimengOpenAIVideos,
		service.VideoTaskEndpointMetadataKey: service.VideoTaskEndpointVideoGenerations,
		"idempotency_key":                    "idem-request",
		"requested_model":                    "sora-requested",
		"upstream_model":                     "sora-upstream-request",
		"billing_model":                      "sora-billing",
		"upstream_base_url":                  "https://upstream.example",
		"duplicate":                          "request",
	}
	resultMetadata := map[string]any{
		"progress":  float64(37),
		"duplicate": "result",
	}

	task := videoTaskEntToService(&dbent.VideoTask{
		ID:              1,
		PublicTaskID:    "task_public",
		Provider:        service.VideoTaskProviderOpenAICompatible,
		Platform:        service.VideoTaskPlatformOpenAIVideo,
		UserID:          2,
		APIKeyID:        3,
		GroupID:         4,
		AccountID:       5,
		RequestedModel:  "sora-2",
		Prompt:          "make a video",
		Status:          string(service.VideoTaskStatusQueued),
		RequestHash:     "request-hash",
		RequestMetadata: requestMetadata,
		ResultMetadata:  resultMetadata,
	})

	if got := task.Metadata[service.VideoAdapterMetadataKey]; got != service.VideoAdapterJimengOpenAIVideos {
		t.Fatalf("Metadata[video_adapter] = %#v, want %q", got, service.VideoAdapterJimengOpenAIVideos)
	}
	if got := task.Metadata[service.VideoTaskEndpointMetadataKey]; got != service.VideoTaskEndpointVideoGenerations {
		t.Fatalf("Metadata[video_task_endpoint] = %#v, want %q", got, service.VideoTaskEndpointVideoGenerations)
	}
	if got := task.Metadata["idempotency_key"]; got != "idem-request" {
		t.Fatalf("Metadata[idempotency_key] = %#v, want idem-request", got)
	}
	if got := task.Metadata["requested_model"]; got != "sora-requested" {
		t.Fatalf("Metadata[requested_model] = %#v, want sora-requested", got)
	}
	if got := task.Metadata["upstream_model"]; got != "sora-upstream-request" {
		t.Fatalf("Metadata[upstream_model] = %#v, want sora-upstream-request", got)
	}
	if got := task.Metadata["billing_model"]; got != "sora-billing" {
		t.Fatalf("Metadata[billing_model] = %#v, want sora-billing", got)
	}
	if got := task.Metadata["upstream_base_url"]; got != "https://upstream.example" {
		t.Fatalf("Metadata[upstream_base_url] = %#v, want upstream base URL", got)
	}
	if got := task.Metadata["progress"]; got != float64(37) {
		t.Fatalf("Metadata[progress] = %#v, want 37", got)
	}
	if got := task.Metadata["duplicate"]; got != "result" {
		t.Fatalf("Metadata[duplicate] = %#v, want result metadata to override request metadata", got)
	}
}

func TestVideoTaskRepositoryForegroundProviderUpdateDoesNotOverwriteTerminalPollResult(t *testing.T) {
	repo, client := newVideoTaskRepoSQLite(t)
	leaseRepo := requireVideoTaskPollLeaseRepository(t, repo)
	ctx := context.Background()
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	mustCreateDueVideoTask(t, ctx, client, "task_foreground_race", now)

	if _, err := repo.ClaimDuePollTasks(ctx, now, 1, "poller-lease", time.Minute); err != nil {
		t.Fatalf("claim poller lease: %v", err)
	}
	completedAt := now.Add(10 * time.Second)
	applied, err := leaseRepo.UpdateFromProviderWithPollLease(ctx, "task_foreground_race", "poller-lease", completedAt, service.VideoTaskProviderUpdate{
		Status:          service.VideoTaskStatusCompleted,
		ProviderStatus:  "completed-by-poller",
		ResponseBody:    []byte(`{"source":"poller"}`),
		Metadata:        map[string]any{"source": "poller"},
		CompletedAt:     &completedAt,
		ClearNextPollAt: true,
	})
	if err != nil || !applied {
		t.Fatalf("terminal poller update = (%v, %v), want (true, nil)", applied, err)
	}

	staleNextPollAt := now.Add(time.Hour)
	applied, err = repo.UpdateFromProvider(ctx, "task_foreground_race", service.VideoTaskProviderUpdate{
		Status:         service.VideoTaskStatusInProgress,
		ProviderStatus: "stale-foreground",
		ResponseBody:   []byte(`{"source":"foreground"}`),
		Metadata:       map[string]any{"source": "foreground"},
		NextPollAt:     &staleNextPollAt,
	})
	if err != nil || applied {
		t.Fatalf("stale foreground update = (%v, %v), want (false, nil)", applied, err)
	}

	row, err := client.VideoTask.Query().Where().Only(ctx)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	if row.Status != string(service.VideoTaskStatusCompleted) || stringValue(row.ProviderStatus) != "completed-by-poller" || string(cloneBytes(row.UpstreamResponseBody)) != `{"source":"poller"}` || row.ResultMetadata["source"] != "poller" {
		t.Fatalf("stale foreground update overwrote terminal poller result: status=%q provider_status=%q response=%s metadata=%#v", row.Status, stringValue(row.ProviderStatus), string(cloneBytes(row.UpstreamResponseBody)), row.ResultMetadata)
	}
	if row.NextPollAt != nil {
		t.Fatalf("terminal task next_poll_at = %v, want nil", row.NextPollAt)
	}
}

func TestVideoTaskRepositoryForegroundProviderUpdateReportsWhetherItApplied(t *testing.T) {
	repo, client := newVideoTaskRepoSQLite(t)
	updateRepo := requireVideoTaskConditionalProviderUpdateRepository(t, repo)
	ctx := context.Background()
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	id := mustCreateDueVideoTask(t, ctx, client, "task_conditional_update", now)

	applied, err := updateRepo.UpdateFromProvider(ctx, "task_conditional_update", service.VideoTaskProviderUpdate{
		Status:         service.VideoTaskStatusInProgress,
		ProviderStatus: "processing",
	})
	if err != nil || !applied {
		t.Fatalf("nonterminal foreground update = (%v, %v), want (true, nil)", applied, err)
	}
	if err := client.VideoTask.UpdateOneID(id).SetStatus(string(service.VideoTaskStatusCompleted)).Exec(ctx); err != nil {
		t.Fatalf("make task terminal: %v", err)
	}

	applied, err = updateRepo.UpdateFromProvider(ctx, "task_conditional_update", service.VideoTaskProviderUpdate{
		Status:         service.VideoTaskStatusInProgress,
		ProviderStatus: "stale-processing",
	})
	if err != nil || applied {
		t.Fatalf("terminal foreground update = (%v, %v), want (false, nil)", applied, err)
	}
}

func TestVideoTaskRepositoryForegroundProviderUpdateAppliesToNonTerminalTask(t *testing.T) {
	repo, client := newVideoTaskRepoSQLite(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	mustCreateDueVideoTask(t, ctx, client, "task_foreground_nonterminal", now)
	nextPollAt := now.Add(time.Minute)

	applied, err := repo.UpdateFromProvider(ctx, "task_foreground_nonterminal", service.VideoTaskProviderUpdate{
		Status:         service.VideoTaskStatusInProgress,
		ProviderStatus: "processing",
		ResponseBody:   []byte(`{"source":"foreground"}`),
		Metadata:       map[string]any{"source": "foreground"},
		NextPollAt:     &nextPollAt,
	})
	if err != nil || !applied {
		t.Fatalf("foreground update = (%v, %v), want (true, nil)", applied, err)
	}

	row, err := client.VideoTask.Query().Where().Only(ctx)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	if row.Status != string(service.VideoTaskStatusInProgress) || stringValue(row.ProviderStatus) != "processing" || string(cloneBytes(row.UpstreamResponseBody)) != `{"source":"foreground"}` || row.ResultMetadata["source"] != "foreground" {
		t.Fatalf("foreground update did not apply: status=%q provider_status=%q response=%s metadata=%#v", row.Status, stringValue(row.ProviderStatus), string(cloneBytes(row.UpstreamResponseBody)), row.ResultMetadata)
	}
	if row.NextPollAt == nil || !row.NextPollAt.Equal(nextPollAt) {
		t.Fatalf("next_poll_at = %v, want %s", row.NextPollAt, nextPollAt)
	}
}

func TestVideoTaskRepositoryForegroundTerminalProviderUpdateClearsPollLeaseAndRejectsStalePollChanges(t *testing.T) {
	for _, tt := range []struct {
		name           string
		status         service.VideoTaskStatus
		providerStatus string
	}{
		{name: "cancel", status: service.VideoTaskStatusCancelled, providerStatus: "cancelled-by-foreground"},
		{name: "fetch", status: service.VideoTaskStatusCompleted, providerStatus: "completed-by-foreground"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo, client := newVideoTaskRepoSQLite(t)
			leaseRepo := requireVideoTaskPollLeaseRepository(t, repo)
			ctx := context.Background()
			now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
			taskID := "task_foreground_terminal_" + tt.name
			id := mustCreateDueVideoTask(t, ctx, client, taskID, now)
			if _, err := repo.ClaimDuePollTasks(ctx, now, 1, "poller-lease", time.Minute); err != nil {
				t.Fatalf("claim poller lease: %v", err)
			}

			completedAt := now.Add(10 * time.Second)
			applied, err := repo.UpdateFromProvider(ctx, taskID, service.VideoTaskProviderUpdate{
				Status:         tt.status,
				ProviderStatus: tt.providerStatus,
				ResponseBody:   []byte(`{"source":"foreground"}`),
				Metadata:       map[string]any{"source": "foreground"},
				CompletedAt:    &completedAt,
			})
			if err != nil || !applied {
				t.Fatalf("foreground terminal update = (%v, %v), want (true, nil)", applied, err)
			}

			row, err := client.VideoTask.Get(ctx, id)
			if err != nil {
				t.Fatalf("load foreground terminal task: %v", err)
			}
			if row.Status != string(tt.status) || stringValue(row.ProviderStatus) != tt.providerStatus || string(cloneBytes(row.UpstreamResponseBody)) != `{"source":"foreground"}` || row.ResultMetadata["source"] != "foreground" {
				t.Errorf("foreground terminal update did not persist: status=%q provider_status=%q response=%s metadata=%#v", row.Status, stringValue(row.ProviderStatus), string(cloneBytes(row.UpstreamResponseBody)), row.ResultMetadata)
			}
			if row.NextPollAt != nil {
				t.Errorf("foreground terminal task next_poll_at = %v, want nil", row.NextPollAt)
			}
			if row.LockedBy != nil || row.LockedUntil != nil {
				t.Errorf("foreground terminal task lock fields = (%v, %v), want nil", row.LockedBy, row.LockedUntil)
			}

			staleNextPollAt := now.Add(time.Hour)
			applied, err = leaseRepo.UpdateFromProviderWithPollLease(ctx, taskID, "poller-lease", completedAt, service.VideoTaskProviderUpdate{
				Status:         service.VideoTaskStatusInProgress,
				ProviderStatus: "stale-poller",
				ResponseBody:   []byte(`{"source":"stale-poller"}`),
				Metadata:       map[string]any{"source": "stale-poller"},
				NextPollAt:     &staleNextPollAt,
			})
			if err != nil || applied {
				t.Errorf("stale poller update = (%v, %v), want (false, nil)", applied, err)
			}
			released, err := leaseRepo.ReleasePollLock(ctx, taskID, "poller-lease")
			if err != nil || released {
				t.Errorf("stale poller release = (%v, %v), want (false, nil)", released, err)
			}

			row, err = client.VideoTask.Get(ctx, id)
			if err != nil {
				t.Fatalf("reload foreground terminal task: %v", err)
			}
			if row.Status != string(tt.status) || stringValue(row.ProviderStatus) != tt.providerStatus || string(cloneBytes(row.UpstreamResponseBody)) != `{"source":"foreground"}` || row.ResultMetadata["source"] != "foreground" {
				t.Errorf("stale poller operation changed terminal task: status=%q provider_status=%q response=%s metadata=%#v", row.Status, stringValue(row.ProviderStatus), string(cloneBytes(row.UpstreamResponseBody)), row.ResultMetadata)
			}
			if row.NextPollAt != nil || row.LockedBy != nil || row.LockedUntil != nil {
				t.Errorf("stale poller operation changed terminal scheduling metadata: next_poll_at=%v locked_by=%v locked_until=%v", row.NextPollAt, row.LockedBy, row.LockedUntil)
			}
		})
	}
}

func TestVideoTaskRepositoryClaimDuePollTasksRollsBackClaimedLocksOnError(t *testing.T) {
	repo, client := newVideoTaskRepoSQLite(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	firstID := mustCreateDueVideoTask(t, ctx, client, "task_first", now)
	mustCreateDueVideoTask(t, ctx, client, "task_second", now)
	failErr := errors.New("claim update failed")
	updateCalls := 0
	client.VideoTask.Use(func(next dbent.Mutator) dbent.Mutator {
		return dbent.MutateFunc(func(ctx context.Context, m dbent.Mutation) (dbent.Value, error) {
			if m.Op().Is(dbent.OpUpdate) {
				updateCalls++
				if updateCalls == 2 {
					return nil, failErr
				}
			}
			return next.Mutate(ctx, m)
		})
	})

	_, err := repo.ClaimDuePollTasks(ctx, now, 2, "worker-1", time.Minute)

	if !errors.Is(err, failErr) {
		t.Fatalf("ClaimDuePollTasks error = %v, want %v", err, failErr)
	}
	first, err := client.VideoTask.Get(ctx, firstID)
	if err != nil {
		t.Fatalf("get first task: %v", err)
	}
	if first.LockedBy != nil || first.LockedUntil != nil || first.LastPolledAt != nil || first.PollAttempts != 0 {
		t.Fatalf("first task lock fields after failed claim: locked_by=%v locked_until=%v last_polled_at=%v attempts=%d", first.LockedBy, first.LockedUntil, first.LastPolledAt, first.PollAttempts)
	}
}

type videoTaskPollLeaseRepository interface {
	UpdateFromProviderWithPollLease(ctx context.Context, publicTaskID, leaseToken string, validAt time.Time, update service.VideoTaskProviderUpdate) (bool, error)
	ReleasePollLock(ctx context.Context, publicTaskID, leaseToken string) (bool, error)
}

type videoTaskConditionalProviderUpdateRepository interface {
	UpdateFromProvider(ctx context.Context, publicTaskID string, update service.VideoTaskProviderUpdate) (bool, error)
}

type videoTaskPollLockRenewalRepository interface {
	RenewPollLock(ctx context.Context, publicTaskID, leaseToken string, validAt time.Time, lockTTL time.Duration) (bool, error)
}

func TestVideoTaskRepositoryPollLeaseStaleClaimCannotOverwriteOrReleaseReclaimedTask(t *testing.T) {
	repo, client := newVideoTaskRepoSQLite(t)
	leaseRepo := requireVideoTaskPollLeaseRepository(t, repo)
	ctx := context.Background()
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	mustCreateDueVideoTask(t, ctx, client, "task_reclaimed", now)

	if _, err := repo.ClaimDuePollTasks(ctx, now, 1, "lease-A", time.Minute); err != nil {
		t.Fatalf("claim lease A: %v", err)
	}
	reclaimedAt := now.Add(time.Minute)
	if _, err := repo.ClaimDuePollTasks(ctx, reclaimedAt, 1, "lease-B", time.Minute); err != nil {
		t.Fatalf("claim lease B: %v", err)
	}
	bCompletedAt := reclaimedAt.Add(10 * time.Second)
	bLockedUntil := reclaimedAt.Add(time.Minute)
	applied, err := leaseRepo.UpdateFromProviderWithPollLease(ctx, "task_reclaimed", "lease-B", reclaimedAt, service.VideoTaskProviderUpdate{
		Status:          service.VideoTaskStatusCompleted,
		ProviderStatus:  "completed-by-B",
		ResponseBody:    []byte(`{"source":"B"}`),
		Metadata:        map[string]any{"source": "B"},
		CompletedAt:     &bCompletedAt,
		ClearNextPollAt: true,
	})
	if err != nil || !applied {
		t.Fatalf("B guarded update = (%v, %v), want (true, nil)", applied, err)
	}

	aNextPollAt := reclaimedAt.Add(2 * time.Hour)
	applied, err = leaseRepo.UpdateFromProviderWithPollLease(ctx, "task_reclaimed", "lease-A", reclaimedAt, service.VideoTaskProviderUpdate{
		Status:         service.VideoTaskStatusInProgress,
		ProviderStatus: "stale-A",
		ResponseBody:   []byte(`{"source":"A"}`),
		Metadata:       map[string]any{"source": "A"},
		NextPollAt:     &aNextPollAt,
	})
	if err != nil || applied {
		t.Fatalf("A stale guarded update = (%v, %v), want (false, nil)", applied, err)
	}
	released, err := leaseRepo.ReleasePollLock(ctx, "task_reclaimed", "lease-A")
	if err != nil || released {
		t.Fatalf("A stale release = (%v, %v), want (false, nil)", released, err)
	}

	row, err := client.VideoTask.Query().Where().Only(ctx)
	if err != nil {
		t.Fatalf("load reclaimed task: %v", err)
	}
	if row.Status != string(service.VideoTaskStatusCompleted) || stringValue(row.ProviderStatus) != "completed-by-B" || string(cloneBytes(row.UpstreamResponseBody)) != `{"source":"B"}` || row.ResultMetadata["source"] != "B" {
		t.Fatalf("reclaimed task was overwritten by A: status=%q provider_status=%q response=%s", row.Status, stringValue(row.ProviderStatus), string(cloneBytes(row.UpstreamResponseBody)))
	}
	if row.CompletedAt == nil || !row.CompletedAt.Equal(bCompletedAt) {
		t.Fatalf("reclaimed task completed_at = %v, want %s", row.CompletedAt, bCompletedAt)
	}
	if row.NextPollAt != nil {
		t.Fatalf("reclaimed terminal task next_poll_at = %v, want nil", row.NextPollAt)
	}
	if row.LockedBy == nil || *row.LockedBy != "lease-B" || row.LockedUntil == nil || !row.LockedUntil.Equal(bLockedUntil) {
		t.Fatalf("A stale release changed B lease: locked_by=%v locked_until=%v", row.LockedBy, row.LockedUntil)
	}

	released, err = leaseRepo.ReleasePollLock(ctx, "task_reclaimed", "lease-B")
	if err != nil || !released {
		t.Fatalf("B release = (%v, %v), want (true, nil)", released, err)
	}
	row, err = client.VideoTask.Query().Where().Only(ctx)
	if err != nil {
		t.Fatalf("reload released task: %v", err)
	}
	if row.LockedBy != nil || row.LockedUntil != nil {
		t.Fatalf("B release did not clear lock: locked_by=%v locked_until=%v", row.LockedBy, row.LockedUntil)
	}
}

func TestVideoTaskRepositoryPollLeaseExpiredClaimCannotUpdate(t *testing.T) {
	repo, client := newVideoTaskRepoSQLite(t)
	leaseRepo := requireVideoTaskPollLeaseRepository(t, repo)
	ctx := context.Background()
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	mustCreateDueVideoTask(t, ctx, client, "task_expired_lease", now)
	if _, err := repo.ClaimDuePollTasks(ctx, now, 1, "lease-A", time.Minute); err != nil {
		t.Fatalf("claim lease A: %v", err)
	}

	nextPollAt := now.Add(time.Hour)
	applied, err := leaseRepo.UpdateFromProviderWithPollLease(ctx, "task_expired_lease", "lease-A", now.Add(time.Minute), service.VideoTaskProviderUpdate{
		Status:         service.VideoTaskStatusCompleted,
		ProviderStatus: "stale",
		NextPollAt:     &nextPollAt,
	})
	if err != nil || applied {
		t.Fatalf("expired lease update = (%v, %v), want (false, nil)", applied, err)
	}
	row, err := client.VideoTask.Query().Where().Only(ctx)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	if row.Status != string(service.VideoTaskStatusQueued) {
		t.Fatalf("expired lease changed status to %q, want queued", row.Status)
	}
}

func TestVideoTaskRepositoryPollLeaseValidClaimUpdatesAndReleases(t *testing.T) {
	repo, client := newVideoTaskRepoSQLite(t)
	leaseRepo := requireVideoTaskPollLeaseRepository(t, repo)
	ctx := context.Background()
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	mustCreateDueVideoTask(t, ctx, client, "task_valid_lease", now)
	if _, err := repo.ClaimDuePollTasks(ctx, now, 1, "lease-A", time.Minute); err != nil {
		t.Fatalf("claim lease A: %v", err)
	}

	nextPollAt := now.Add(time.Hour)
	applied, err := leaseRepo.UpdateFromProviderWithPollLease(ctx, "task_valid_lease", "lease-A", now.Add(30*time.Second), service.VideoTaskProviderUpdate{
		Status:         service.VideoTaskStatusInProgress,
		ProviderStatus: "processing",
		NextPollAt:     &nextPollAt,
	})
	if err != nil || !applied {
		t.Fatalf("valid lease update = (%v, %v), want (true, nil)", applied, err)
	}
	released, err := leaseRepo.ReleasePollLock(ctx, "task_valid_lease", "lease-A")
	if err != nil || !released {
		t.Fatalf("valid lease release = (%v, %v), want (true, nil)", released, err)
	}
	row, err := client.VideoTask.Query().Where().Only(ctx)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	if row.Status != string(service.VideoTaskStatusInProgress) || row.NextPollAt == nil || !row.NextPollAt.Equal(nextPollAt) {
		t.Fatalf("valid lease did not apply update: status=%q next_poll_at=%v", row.Status, row.NextPollAt)
	}
	if row.LockedBy != nil || row.LockedUntil != nil {
		t.Fatalf("valid lease release did not clear lock: locked_by=%v locked_until=%v", row.LockedBy, row.LockedUntil)
	}
}

func TestVideoTaskRepositoryPollLockRenewalRequiresAValidNonterminalLease(t *testing.T) {
	for _, tt := range []struct {
		name            string
		taskID          string
		leaseToken      string
		validAt         time.Time
		terminal        bool
		wantRenewed     bool
		wantLockedUntil time.Time
	}{
		{
			name:            "valid lease renews then permits update and release",
			taskID:          "task_renew_valid",
			leaseToken:      "lease-A",
			validAt:         time.Date(2026, 7, 3, 12, 0, 30, 0, time.UTC),
			wantRenewed:     true,
			wantLockedUntil: time.Date(2026, 7, 3, 12, 2, 30, 0, time.UTC),
		},
		{
			name:        "wrong token is lease loss",
			taskID:      "task_renew_wrong",
			leaseToken:  "lease-B",
			validAt:     time.Date(2026, 7, 3, 12, 0, 30, 0, time.UTC),
			wantRenewed: false,
		},
		{
			name:        "expired lease is lease loss",
			taskID:      "task_renew_expired",
			leaseToken:  "lease-A",
			validAt:     time.Date(2026, 7, 3, 12, 1, 0, 0, time.UTC),
			wantRenewed: false,
		},
		{
			name:        "terminal task is lease loss",
			taskID:      "task_renew_terminal",
			leaseToken:  "lease-A",
			validAt:     time.Date(2026, 7, 3, 12, 0, 30, 0, time.UTC),
			terminal:    true,
			wantRenewed: false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo, client := newVideoTaskRepoSQLite(t)
			leaseRepo := requireVideoTaskPollLeaseRepository(t, repo)
			renewalRepo := requireVideoTaskPollLockRenewalRepository(t, repo)
			ctx := context.Background()
			now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
			id := mustCreateDueVideoTask(t, ctx, client, tt.taskID, now)
			if _, err := repo.ClaimDuePollTasks(ctx, now, 1, "lease-A", time.Minute); err != nil {
				t.Fatalf("claim lease: %v", err)
			}
			if tt.terminal {
				if err := client.VideoTask.UpdateOneID(id).SetStatus(string(service.VideoTaskStatusCompleted)).Exec(ctx); err != nil {
					t.Fatalf("make task terminal: %v", err)
				}
			}
			before, err := client.VideoTask.Get(ctx, id)
			if err != nil {
				t.Fatalf("load task before renewal: %v", err)
			}

			renewed, err := renewalRepo.RenewPollLock(ctx, tt.taskID, tt.leaseToken, tt.validAt, 2*time.Minute)

			if err != nil || renewed != tt.wantRenewed {
				t.Fatalf("renew = (%v, %v), want (%v, nil)", renewed, err, tt.wantRenewed)
			}
			row, err := client.VideoTask.Get(ctx, id)
			if err != nil {
				t.Fatalf("load task after renewal: %v", err)
			}
			if tt.wantRenewed {
				if row.LockedUntil == nil || !row.LockedUntil.Equal(tt.wantLockedUntil) {
					t.Fatalf("renewed locked_until = %v, want %s", row.LockedUntil, tt.wantLockedUntil)
				}
				nextPollAt := tt.validAt.Add(time.Hour)
				applied, err := leaseRepo.UpdateFromProviderWithPollLease(ctx, tt.taskID, "lease-A", tt.validAt.Add(time.Minute), service.VideoTaskProviderUpdate{Status: service.VideoTaskStatusInProgress, NextPollAt: &nextPollAt})
				if err != nil || !applied {
					t.Fatalf("renewed lease update = (%v, %v), want (true, nil)", applied, err)
				}
				released, err := leaseRepo.ReleasePollLock(ctx, tt.taskID, "lease-A")
				if err != nil || !released {
					t.Fatalf("renewed lease release = (%v, %v), want (true, nil)", released, err)
				}
				return
			}
			if !timePointerEqual(row.LockedUntil, before.LockedUntil) || stringValue(row.LockedBy) != stringValue(before.LockedBy) {
				t.Fatalf("lost lease was modified: before=(%v,%v) after=(%v,%v)", before.LockedBy, before.LockedUntil, row.LockedBy, row.LockedUntil)
			}
		})
	}
}

func TestVideoTaskRepositoryPollLeaseDoesNotOverwriteTerminalTask(t *testing.T) {
	for _, status := range []service.VideoTaskStatus{
		service.VideoTaskStatusCompleted,
		service.VideoTaskStatusFailed,
		service.VideoTaskStatusCancelled,
		service.VideoTaskStatusExpired,
	} {
		t.Run(string(status), func(t *testing.T) {
			repo, client := newVideoTaskRepoSQLite(t)
			leaseRepo := requireVideoTaskPollLeaseRepository(t, repo)
			ctx := context.Background()
			now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
			id := mustCreateDueVideoTask(t, ctx, client, "task_terminal_"+string(status), now)
			if err := client.VideoTask.UpdateOneID(id).
				SetStatus(string(status)).
				SetLockedBy("lease-A").
				SetLockedUntil(now.Add(time.Minute)).
				Exec(ctx); err != nil {
				t.Fatalf("seed terminal locked task: %v", err)
			}

			applied, err := leaseRepo.UpdateFromProviderWithPollLease(ctx, "task_terminal_"+string(status), "lease-A", now, service.VideoTaskProviderUpdate{Status: service.VideoTaskStatusInProgress})
			if err != nil || applied {
				t.Fatalf("terminal %s update = (%v, %v), want (false, nil)", status, applied, err)
			}
		})
	}
}

func requireVideoTaskPollLeaseRepository(t *testing.T, repo *videoTaskRepository) videoTaskPollLeaseRepository {
	t.Helper()
	leaseRepo, ok := any(repo).(videoTaskPollLeaseRepository)
	if !ok {
		t.Fatal("video task repository does not implement poll lease persistence")
	}
	return leaseRepo
}

func requireVideoTaskConditionalProviderUpdateRepository(t *testing.T, repo *videoTaskRepository) videoTaskConditionalProviderUpdateRepository {
	t.Helper()
	updateRepo, ok := any(repo).(videoTaskConditionalProviderUpdateRepository)
	if !ok {
		t.Fatal("video task repository does not report conditional provider update results")
	}
	return updateRepo
}

func requireVideoTaskPollLockRenewalRepository(t *testing.T, repo *videoTaskRepository) videoTaskPollLockRenewalRepository {
	t.Helper()
	renewalRepo, ok := any(repo).(videoTaskPollLockRenewalRepository)
	if !ok {
		t.Fatal("video task repository does not implement poll lock renewal")
	}
	return renewalRepo
}

func timePointerEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func newVideoTaskRepoSQLite(t *testing.T) (*videoTaskRepository, *dbent.Client) {
	t.Helper()

	db, err := sql.Open("sqlite", "file:video_task_repo?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	return &videoTaskRepository{client: client}, client
}

func mustCreateDueVideoTask(t *testing.T, ctx context.Context, client *dbent.Client, publicTaskID string, nextPollAt time.Time) int64 {
	t.Helper()
	task, err := client.VideoTask.Create().
		SetPublicTaskID(publicTaskID).
		SetProvider(service.VideoTaskProviderOpenAICompatible).
		SetPlatform(service.VideoTaskPlatformOpenAIVideo).
		SetUserID(1).
		SetAPIKeyID(2).
		SetGroupID(3).
		SetAccountID(4).
		SetRequestedModel("sora-2").
		SetUpstreamModel("sora-2").
		SetBillingModel("sora-2").
		SetStatus(string(service.VideoTaskStatusQueued)).
		SetPrompt("make a video").
		SetRequestHash(publicTaskID + "-hash").
		SetNextPollAt(nextPollAt).
		Save(ctx)
	if err != nil {
		t.Fatalf("create video task %s: %v", publicTaskID, err)
	}
	return task.ID
}

func mustCreateVideoTaskListItem(t *testing.T, ctx context.Context, client *dbent.Client, publicTaskID string, userID int64, model string, status service.VideoTaskStatus, createdAt time.Time) {
	t.Helper()
	_, err := client.VideoTask.Create().
		SetPublicTaskID(publicTaskID).
		SetProvider(service.VideoTaskProviderOpenAICompatible).
		SetPlatform(service.VideoTaskPlatformOpenAIVideo).
		SetUserID(userID).
		SetAPIKeyID(2).
		SetGroupID(3).
		SetAccountID(4).
		SetRequestedModel(model).
		SetUpstreamModel(model).
		SetBillingModel(model).
		SetStatus(string(status)).
		SetPrompt("make a video").
		SetRequestHash(publicTaskID + "-hash").
		SetCreatedAt(createdAt).
		Save(ctx)
	if err != nil {
		t.Fatalf("create video task %s: %v", publicTaskID, err)
	}
}

func assertVideoTaskIDs(t *testing.T, items []*service.VideoTask, want ...string) {
	t.Helper()
	if len(items) != len(want) {
		t.Fatalf("ListForUser returned %d items, want %d", len(items), len(want))
	}
	for i, item := range items {
		if item.PublicTaskID != want[i] {
			t.Fatalf("ListForUser item %d = %q, want %q", i, item.PublicTaskID, want[i])
		}
	}
}
