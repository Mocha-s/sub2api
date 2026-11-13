package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestImageTaskStoreRoundTripAndTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := NewImageTaskStore(rdb)
	task := &service.ImageTaskRecord{
		ID:        "imgtask_123",
		UserID:    7,
		APIKeyID:  9,
		Status:    service.ImageTaskStatusProcessing,
		CreatedAt: 100,
		ExpiresAt: 200,
	}

	require.NoError(t, store.Save(context.Background(), task, 24*time.Hour))
	got, err := store.Get(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, task, got)
	require.Equal(t, 24*time.Hour, mr.TTL(imageTaskKey(task.ID)))
}

func TestImageTaskStoreMissing(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := NewImageTaskStore(rdb)

	_, err := store.Get(context.Background(), "imgtask_missing")
	require.ErrorIs(t, err, service.ErrImageTaskNotFound)
}

func TestImageTaskStoreCompletionCannotRefreshOrExceedRetention(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := NewImageTaskStore(rdb)
	done := time.Now().Add(-time.Hour).Unix()
	task := &service.ImageTaskRecord{ID: "imgtask_retention", Status: service.ImageTaskStatusCompleted, CompletedAt: &done, ExpiresAt: done + int64((72*time.Hour)/time.Second)}
	require.NoError(t, store.Save(context.Background(), task, 7*24*time.Hour))
	require.LessOrEqual(t, mr.TTL(imageTaskKey(task.ID)), 71*time.Hour)
	before := mr.TTL(imageTaskKey(task.ID))
	task.ExpiresAt = time.Now().Add(72 * time.Hour).Unix()
	task.Status = service.ImageTaskStatusFailed
	require.NoError(t, store.Save(context.Background(), task, 72*time.Hour))
	got, err := store.Get(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, service.ImageTaskStatusCompleted, got.Status)
	require.LessOrEqual(t, mr.TTL(imageTaskKey(task.ID)), before)
}
