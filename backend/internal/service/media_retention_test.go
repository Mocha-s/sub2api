package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestImageTaskRetentionCapsTTLAndDoesNotRefreshCompletion(t *testing.T) {
	store := &imageTaskMemoryStore{}
	svc := NewImageTaskServiceWithOptions(store, 7*24*time.Hour, time.Minute)
	created, err := svc.Create(context.Background(), ImageTaskOwner{UserID: 7, APIKeyID: 9})
	require.NoError(t, err)
	require.LessOrEqual(t, store.ttl, 72*time.Hour)
	require.NoError(t, svc.Complete(context.Background(), created.ID, http.StatusOK, []byte(`{"data":[]}`)))
	completed := time.Now().Add(-time.Hour).Unix()
	store.task.CompletedAt = &completed
	store.task.ExpiresAt = completed + int64((72*time.Hour)/time.Second)
	require.NoError(t, svc.Fail(context.Background(), created.ID, 502, []byte(`{"message":"late retry"}`)))
	require.Equal(t, completed, *store.task.CompletedAt)
	require.Equal(t, ImageTaskStatusCompleted, store.task.Status)
}

func TestImageTaskRetentionReadFence(t *testing.T) {
	now := time.Now().Unix()
	for _, delta := range []int64{-1, 0, 1} {
		store := &imageTaskMemoryStore{task: &ImageTaskRecord{ID: "task", UserID: 7, APIKeyID: 9, Status: ImageTaskStatusCompleted, CompletedAt: &now, ExpiresAt: now + delta}}
		_, err := NewImageTaskService(store).Get(context.Background(), ImageTaskOwner{UserID: 7, APIKeyID: 9}, "task")
		if delta <= 0 {
			require.ErrorIs(t, err, ErrImageTaskNotFound)
		} else {
			require.NoError(t, err)
		}
	}
}

func TestVideoTaskRetentionFencesBothContentRoles(t *testing.T) {
	for _, admin := range []bool{false, true} {
		repo := newFakeVideoTaskRepository(nil)
		end := time.Now().Add(-72 * time.Hour)
		repo.tasks["old"] = &VideoTask{PublicTaskID: "old", UserID: 7, AccountID: 99, Status: VideoTaskStatusCompleted, CompletedAt: &end}
		svc := newVideoTaskServiceForTest(repo, nil, nil, nil, nil)
		params := VideoTaskContentParams{PublicTaskID: "old", UserID: 7}
		var err error
		if admin {
			_, err = svc.AdminContent(context.Background(), params)
		} else {
			_, err = svc.Content(context.Background(), params)
		}
		require.ErrorIs(t, err, ErrVideoTaskNotFound)
	}
}

func TestVideoTaskRetentionExactDeadlineAndLegacyAnchor(t *testing.T) {
	done := time.Now()
	for _, legacy := range []bool{false, true} {
		task := &VideoTask{Status: VideoTaskStatusCompleted, CreatedAt: done.Add(-time.Hour), UpdatedAt: done}
		if !legacy {
			task.CompletedAt = &done
		}
		require.False(t, VideoTaskLogExpired(task, done.Add(72*time.Hour-time.Nanosecond)))
		require.True(t, VideoTaskLogExpired(task, done.Add(72*time.Hour)))
		require.True(t, VideoTaskLogExpired(task, done.Add(72*time.Hour+time.Nanosecond)))
	}
}

func TestVideoTaskRetentionAbandonmentUsesLocalPollDeadline(t *testing.T) {
	submitted := time.Now()
	future := submitted.Add(365 * 24 * time.Hour)
	task := &VideoTask{Status: VideoTaskStatusInProgress, CreatedAt: submitted.Add(-time.Hour), SubmittedAt: &submitted, ExpiresAt: &future}
	require.False(t, VideoTaskLogExpired(task, submitted.Add(96*time.Hour-time.Nanosecond)))
	require.True(t, VideoTaskLogExpired(task, submitted.Add(96*time.Hour)))
}
