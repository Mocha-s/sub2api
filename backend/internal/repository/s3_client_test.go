//go:build unit

package repository

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"
)

func TestS3ClientSupportsProxyRewrittenAcceptEncoding(t *testing.T) {
	payload := []byte("stored image")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Cloudflare rewrites this header en route to MinIO. It must not be signed.
		signed := strings.Split(strings.Split(r.Header.Get("Authorization"), "SignedHeaders=")[1], ",")[0]
		if strings.Contains(signed, "accept-encoding") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		require.Empty(t, r.Header.Get("Accept-Encoding"), "SDK transport should not request compressed object bodies")
		switch r.Method {
		case http.MethodPut:
			got, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.Equal(t, payload, got)
		case http.MethodGet:
			_, _ = w.Write(payload)
		}
	}))
	defer server.Close()
	client, err := newS3Client(context.Background(), s3ClientParams{
		Endpoint: server.URL, Region: "us-east-1", AccessKeyID: "test-ak", SecretAccessKey: "test-sk", ForcePathStyle: true,
	})
	require.NoError(t, err)
	_, err = client.HeadBucket(t.Context(), &s3.HeadBucketInput{Bucket: aws.String("images")})
	require.NoError(t, err)
	_, err = client.PutObject(t.Context(), &s3.PutObjectInput{Bucket: aws.String("images"), Key: aws.String("image.png"), Body: bytes.NewReader(payload)})
	require.NoError(t, err)
	result, err := client.GetObject(t.Context(), &s3.GetObjectInput{Bucket: aws.String("images"), Key: aws.String("image.png")})
	require.NoError(t, err)
	defer result.Body.Close()
	got, err := io.ReadAll(result.Body)
	require.NoError(t, err)
	require.Equal(t, payload, got)
	presigned, err := s3.NewPresignClient(client).PresignGetObject(t.Context(), &s3.GetObjectInput{
		Bucket: aws.String("images"), Key: aws.String("image.png"),
	})
	require.NoError(t, err)
	require.NotContains(t, presigned.URL, "accept-encoding")
}
