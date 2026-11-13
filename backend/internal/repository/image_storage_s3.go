package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/servertiming"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// S3ImageStorage 用 S3 兼容对象存储实现 service.ImageStorage。
type S3ImageStorage struct {
	client        *s3.Client
	bucket        string
	publicBaseURL string
	presignExpiry time.Duration
	prefix        string
	cursor        *string
	retryKeys     map[string]struct{}
}

var _ service.ImageStorage = (*S3ImageStorage)(nil)

var retainedImageKey = regexp.MustCompile(`^(imgtask_[0-9a-f]{32}|video-reference/task_[0-9a-f]{32}-(images|referenceImages))-[0-9]+\.(png|jpg|webp|gif)$`)

// NewS3ImageStorage 依据配置构造 S3 图片存储（调用方应先确认 cfg.Active()）。
func NewS3ImageStorage(ctx context.Context, cfg *config.ImageStorageConfig) (*S3ImageStorage, error) {
	client, err := newS3Client(ctx, s3ClientParams{
		Endpoint:        cfg.Endpoint,
		Region:          cfg.Region,
		AccessKeyID:     cfg.AccessKeyID,
		SecretAccessKey: cfg.SecretAccessKey,
		ForcePathStyle:  cfg.ForcePathStyle,
	})
	if err != nil {
		return nil, err
	}

	expiry := time.Duration(cfg.PresignExpiry) * time.Hour
	if cfg.PresignExpiry <= 0 || cfg.PresignExpiry > 72 {
		expiry = service.MediaRetention
	}

	return &S3ImageStorage{
		client:        client,
		bucket:        cfg.Bucket,
		publicBaseURL: strings.TrimRight(cfg.PublicBaseURL, "/"),
		presignExpiry: expiry,
		prefix:        cfg.Prefix,
	}, nil
}

// Save 上传图片字节，返回可访问 URL：配了 public_base_url 则返回公开直链，否则返回 presigned 临时链接。
func (s *S3ImageStorage) Save(ctx context.Context, key, contentType string, data []byte) (string, error) {
	// Anchor before upload: signing latency must not renew an object's retention window.
	deadline := time.Now().Add(service.MediaRetention)
	finish := servertiming.ObserveDependency(ctx, "s3")
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &s.bucket,
		Key:         &key,
		Body:        bytes.NewReader(data),
		ContentType: &contentType,
	})
	finish()
	if err != nil {
		return "", fmt.Errorf("S3 PutObject: %w", err)
	}

	if s.publicBaseURL != "" {
		return s.publicBaseURL + "/" + strings.TrimLeft(key, "/"), nil
	}

	presignClient := s3.NewPresignClient(s.client)
	expiry := min(s.presignExpiry, time.Until(deadline).Truncate(time.Second))
	if expiry <= 0 {
		return "", fmt.Errorf("image retention deadline elapsed")
	}
	result, err := presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	}, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", fmt.Errorf("presign url: %w", err)
	}
	return result.URL, nil
}

// CleanupExpired advances one page per cycle despite individual failures.
// At most100 failed keys are retried per pass; others are revisited when traversal wraps.
func (s *S3ImageStorage) CleanupExpired(ctx context.Context, now time.Time) error {
	if s.publicBaseURL != "" || !service.SafeImageRetentionPrefix(s.prefix) {
		return fmt.Errorf("image retention requires a private store and a safe dedicated image prefix")
	}
	maxKeys := int32(100)
	page, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: &s.bucket, Prefix: &s.prefix, MaxKeys: &maxKeys, ContinuationToken: s.cursor,
	})
	if err != nil {
		return fmt.Errorf("list expired images: %w", err)
	}
	var deleteErr error
	attempted := make(map[string]struct{}, len(s.retryKeys))
	remove := func(key string) {
		attempted[key] = struct{}{}
		if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &key}); err != nil {
			deleteErr = errors.Join(deleteErr, fmt.Errorf("delete expired image: %w", err))
			if len(s.retryKeys) < 100 {
				if s.retryKeys == nil {
					s.retryKeys = make(map[string]struct{})
				}
				s.retryKeys[key] = struct{}{}
			}
		} else {
			delete(s.retryKeys, key)
		}
	}
	for key := range s.retryKeys {
		remove(key)
	}
	for _, object := range page.Contents {
		if object.Key == nil || object.LastModified == nil || object.LastModified.After(now.Add(-service.MediaRetention)) {
			continue
		}
		key := *object.Key
		if _, ok := attempted[key]; ok {
			continue
		}
		relative := strings.TrimPrefix(key, s.prefix)
		// Only gateway-generated objects, not arbitrary public/backup objects under a misconfigured prefix.
		if !strings.HasPrefix(key, s.prefix) || !retainedImageKey.MatchString(relative) {
			continue
		}
		remove(key)
	}
	s.cursor = nil
	if page.IsTruncated != nil && *page.IsTruncated {
		if page.NextContinuationToken == nil || *page.NextContinuationToken == "" {
			return fmt.Errorf("image listing truncated without continuation token")
		}
		s.cursor = page.NextContinuationToken
	}
	return deleteErr
}
