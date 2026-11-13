//go:build unit

package repository

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestImageStorageSigningRetentionCap(t *testing.T) {
	for _, hours := range []int{0, 24, 72, 168} {
		t.Run(fmt.Sprint(hours), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(200)
			}))
			defer server.Close()
			storage, err := NewS3ImageStorage(context.Background(), &config.ImageStorageConfig{
				Endpoint: server.URL, Region: "us-east-1", Bucket: "images", Prefix: "images/",
				AccessKeyID: "test", SecretAccessKey: "test", ForcePathStyle: true, PresignExpiry: hours,
			})
			require.NoError(t, err)
			raw, err := storage.Save(context.Background(), "images/imgtask_test-0.png", "image/png", []byte("image"))
			require.NoError(t, err)
			u, err := url.Parse(raw)
			require.NoError(t, err)
			want := "259199" // floor to seconds, never extend past the upload's 72h deadline
			if hours == 24 {
				want = "86400"
			}
			require.Equal(t, want, u.Query().Get("X-Amz-Expires"))
		})
	}
}

func TestImageStorageSweepPrivateManagedPrefixAndPagination(t *testing.T) {
	now := time.Now().UTC()
	var deletes []string
	var tokens []string
	failDelete := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			tokens = append(tokens, r.URL.Query().Get("continuation-token"))
			require.Equal(t, "images/", r.URL.Query().Get("prefix"))
			require.Equal(t, "100", r.URL.Query().Get("max-keys"))
			if r.URL.Query().Get("continuation-token") == "" {
				fmt.Fprintf(w, `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>page2</NextContinuationToken><Contents><Key>images/imgtask_00000000000000000000000000000000-0.png</Key><LastModified>%s</LastModified></Contents><Contents><Key>images/imgtask_11111111111111111111111111111111-0.png</Key><LastModified>%s</LastModified></Contents></ListBucketResult>`, now.Add(-72*time.Hour).Format(time.RFC3339), now.Add(-72*time.Hour).Format(time.RFC3339))
			} else {
				fmt.Fprintf(w, `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>images/imgtask_22222222222222222222222222222222-0.png</Key><LastModified>%s</LastModified></Contents><Contents><Key>backups/backup.sql</Key><LastModified>%s</LastModified></Contents><Contents><Key>images/public.png</Key><LastModified>%s</LastModified></Contents><Contents><Key>images/video-reference/task_33333333333333333333333333333333-referenceImages-0.png</Key><LastModified>%s</LastModified></Contents><Contents><Key>images/video-reference/backup.sql</Key><LastModified>%s</LastModified></Contents></ListBucketResult>`, now.Format(time.RFC3339), now.Add(-100*time.Hour).Format(time.RFC3339), now.Add(-100*time.Hour).Format(time.RFC3339), now.Add(-72*time.Hour).Format(time.RFC3339), now.Add(-72*time.Hour).Format(time.RFC3339))
			}
		case http.MethodDelete:
			deletes = append(deletes, r.URL.Path)
			if failDelete && strings.Contains(r.URL.Path, "imgtask_00000000000000000000000000000000") {
				w.WriteHeader(403)
				fmt.Fprint(w, `<Error><Code>AccessDenied</Code></Error>`)
			} else {
				w.WriteHeader(204)
			}
		}
	}))
	defer server.Close()
	for _, prefix := range []string{"", "/", "../", "backups/", "images/../backups/"} {
		storage, err := NewS3ImageStorage(context.Background(), &config.ImageStorageConfig{Endpoint: server.URL, Region: "us-east-1", Bucket: "images", Prefix: prefix, AccessKeyID: "test", SecretAccessKey: "test", ForcePathStyle: true})
		require.NoError(t, err)
		cleaner, ok := any(storage).(interface {
			CleanupExpired(context.Context, time.Time) error
		})
		require.True(t, ok, "private image storage must provide bounded cleanup")
		require.Error(t, cleaner.CleanupExpired(context.Background(), now))
	}
	require.Empty(t, tokens)
	storage, err := NewS3ImageStorage(context.Background(), &config.ImageStorageConfig{Endpoint: server.URL, Region: "us-east-1", Bucket: "images", Prefix: "images/", AccessKeyID: "test", SecretAccessKey: "test", ForcePathStyle: true})
	require.NoError(t, err)
	cleaner := any(storage).(interface {
		CleanupExpired(context.Context, time.Time) error
	})
	require.Error(t, cleaner.CleanupExpired(context.Background(), now))
	require.Error(t, cleaner.CleanupExpired(context.Background(), now))
	require.Equal(t, []string{"", "page2"}, tokens, "a failed object must not starve later pages")
	require.Contains(t, deletes, "/images/images/imgtask_11111111111111111111111111111111-0.png")
	require.Contains(t, deletes, "/images/images/video-reference/task_33333333333333333333333333333333-referenceImages-0.png")
	failDelete = false
	require.NoError(t, cleaner.CleanupExpired(context.Background(), now))
	require.Equal(t, []string{"", "page2", ""}, tokens)
	require.GreaterOrEqual(t, len(deletes), 5, "failed objects are retried without rewinding the listing")
	for _, key := range deletes {
		require.NotContains(t, key, "backup")
		require.NotContains(t, key, "public")
		require.NotContains(t, key, "22222222222222222222222222222222", "fresh objects stay untouched")
	}
}
