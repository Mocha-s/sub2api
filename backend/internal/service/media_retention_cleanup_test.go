//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type retentionRepoStub struct {
	imageLogMemoryRepo
	calls int
	err   error
}

func (r *retentionRepoStub) CleanupExpired(ctx context.Context, now time.Time, limit int) error {
	r.calls++
	if limit != 100 {
		return errors.New("cleanup must be bounded to 100 rows")
	}
	return r.err
}

type retentionStorageStub struct {
	recordingStorage
	calls int
}

func (s *retentionStorageStub) CleanupExpired(context.Context, time.Time) error {
	s.calls++
	return nil
}

func TestMediaRetentionCleanupRetriesAndStillSweepsOnDatabaseFailure(t *testing.T) {
	r := &retentionRepoStub{err: errors.New("database unavailable")}
	logs := NewImageTaskLogService(r, nil)
	defer logs.Stop()
	settings, _, _ := newImageStorageFixture(t, config.ImageStorageConfig{
		Bucket: "images", Prefix: "images/", AccessKeyID: "key", SecretAccessKey: "secret",
	})
	storage := &retentionStorageStub{}
	settings.factory = func(context.Context, *config.ImageStorageConfig) (ImageStorage, error) { return storage, nil }
	cleaner, ok := any(logs).(interface {
		cleanupExpired(context.Context, time.Time, *ImageStorageSettingService) error
	})
	require.True(t, ok, "existing image log lifecycle must also clean expired media")
	require.Error(t, cleaner.cleanupExpired(context.Background(), time.Now(), settings))
	require.Equal(t, 1, storage.calls)
	r.err = nil
	require.NoError(t, cleaner.cleanupExpired(context.Background(), time.Now(), settings))
	require.Equal(t, 11, r.calls, "one failed batch plus a bounded ten-batch drain")
	require.Equal(t, 2, storage.calls)
}

func TestMediaRetentionStorageRejectsBackupPrefixOverlap(t *testing.T) {
	settings, repo, _ := newImageStorageFixture(t, config.ImageStorageConfig{})
	seedBackupS3(t, repo, BackupS3Config{Bucket: "shared", Prefix: "images/", AccessKeyID: "key", SecretAccessKey: "secret"})
	_, err := settings.Update(context.Background(), ImageStorageSettings{ReuseBackupS3: true, Prefix: "images/"})
	require.NoError(t, err)
	storage := &retentionStorageStub{}
	settings.factory = func(context.Context, *config.ImageStorageConfig) (ImageStorage, error) { return storage, nil }
	cleaner, ok := any(settings).(interface {
		CleanupExpired(context.Context, time.Time) error
	})
	require.True(t, ok)
	require.Error(t, cleaner.CleanupExpired(context.Background(), time.Now()))
	require.Zero(t, storage.calls)
	_, err = settings.Update(context.Background(), ImageStorageSettings{ReuseBackupS3: true, Prefix: "generated-images/"})
	require.NoError(t, err)
	require.NoError(t, cleaner.CleanupExpired(context.Background(), time.Now()))
	require.Equal(t, 1, storage.calls, "sharing credentials/bucket is safe only with disjoint prefixes")
}

func TestMediaRetentionBatchDownloadIgnoresExtendedOutputExpiry(t *testing.T) {
	svc, repo, limiter := newTestBatchImageDownloadService()
	old, later := time.Now().Add(-72*time.Hour), time.Now().Add(7*24*time.Hour)
	repo.jobs["imgbatch_download"].FinishedAt = &old
	repo.jobs["imgbatch_download"].OutputExpiresAt = &later
	_, err := svc.OpenItemContent(context.Background(), testBatchImageOwner(), "imgbatch_download", "cover/../001", 0)
	require.ErrorIs(t, err, ErrBatchImageOutputDeleted)
	require.Zero(t, limiter.acquireCount)
}

func TestMediaRetentionBatchPublicHistoryExpiresBeforeSettlement(t *testing.T) {
	for _, route := range []string{"get", "list", "items"} {
		t.Run(route, func(t *testing.T) {
			svc, repo, _, _, _ := newTestBatchImagePublicService(true)
			owner := testBatchImageOwner()
			old := time.Now().Add(-72 * time.Hour)
			repo.jobs["expired"] = &BatchImageJob{BatchID: "expired", UserID: owner.UserID, APIKeyID: &owner.APIKeyID,
				Status: BatchImageJobStatusCompleted, TaskName: "private prompt", FinishedAt: &old, UpdatedAt: time.Now()}
			switch route {
			case "get":
				_, err := svc.Get(context.Background(), owner, "expired")
				require.ErrorIs(t, err, ErrBatchImageJobNotFound)
			case "list":
				got, err := svc.List(context.Background(), owner, BatchImageJobsQuery{})
				require.NoError(t, err)
				require.Empty(t, got.Data)
			case "items":
				_, err := svc.ListItems(context.Background(), owner, "expired", BatchImageItemsQuery{})
				require.ErrorIs(t, err, ErrBatchImageJobNotFound)
			}
		})
	}
}
