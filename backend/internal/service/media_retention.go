package service

import (
	"context"
	"errors"
	"path"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

const MediaRetention = 72 * time.Hour

func (s *ImageTaskLogService) startRetentionCleanup(storage *ImageStorageSettingService) {
	s.cleanupWorkers.Add(1)
	go func() {
		defer s.cleanupWorkers.Done()
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			ctx, cancel := context.WithTimeout(s.workCtx, 2*time.Minute)
			err := s.cleanupExpired(ctx, time.Now(), storage)
			cancel()
			if err != nil && s.workCtx.Err() == nil {
				logger.L().Warn("media_retention.cleanup_failed", zap.Error(err))
			}
			select {
			case <-s.workCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (s *ImageTaskLogService) cleanupExpired(ctx context.Context, now time.Time, storage *ImageStorageSettingService) error {
	var dbErr error
	if repo, ok := s.repo.(interface {
		CleanupExpired(context.Context, time.Time, int) error
	}); ok {
		for batch := 0; batch < 10 && ctx.Err() == nil; batch++ {
			if dbErr = repo.CleanupExpired(ctx, now, 100); dbErr != nil {
				break
			}
		}
	}
	// A database outage must not prevent independent object cleanup (and vice versa).
	return errors.Join(dbErr, storage.CleanupExpired(ctx, now))
}

func SafeImageRetentionPrefix(prefix string) bool {
	return prefix != "" && strings.HasSuffix(prefix, "/") && !strings.HasPrefix(prefix, "/") &&
		!strings.HasPrefix(prefix, "../") && path.Clean(prefix)+"/" == prefix && !strings.ContainsAny(prefix, "\\\x00") &&
		!strings.EqualFold(strings.Split(prefix, "/")[0], "backups") &&
		!strings.EqualFold(strings.Split(prefix, "/")[0], "backup")
}

// A terminal task's first completion is authoritative; updated_at is only a legacy fallback.
func VideoTaskLogExpired(task *VideoTask, now time.Time) bool {
	if task == nil {
		return true
	}
	if expired, _ := task.Metadata["retention_expired"].(bool); expired {
		return true
	}
	if task.Status.Terminal() {
		end := task.UpdatedAt
		if task.CompletedAt != nil {
			end = *task.CompletedAt
		}
		if end.IsZero() {
			end = task.CreatedAt
		}
		if deadline := videoTaskPollDeadline(task); task.ProviderStatus == "local_timeout" && !deadline.IsZero() && deadline.Before(end) {
			end = deadline
		}
		return !end.IsZero() && !now.Before(end.Add(MediaRetention))
	}
	end := videoTaskPollDeadline(task)
	return !end.IsZero() && !now.Before(end.Add(MediaRetention))
}

func batchImageLogExpired(job *BatchImageJob, now time.Time) bool {
	if job == nil {
		return true
	}
	if !IsTerminalBatchImageJobStatus(job.Status) {
		return false
	}
	end := job.UpdatedAt
	if job.FinishedAt != nil {
		end = *job.FinishedAt
	}
	if end.IsZero() {
		end = job.CreatedAt
	}
	return !end.IsZero() && !now.Before(end.Add(MediaRetention))
}
