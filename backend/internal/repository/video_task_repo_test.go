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
