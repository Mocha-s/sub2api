package service

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// MegaAi accepts remote reference images only; never download caller-supplied URLs here.
func (a *megaAiVideoAdapter) prepareInlineImages(ctx context.Context, body []byte) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var uploader *ImageResultUploader
	var uploadID string
	for _, key := range []string{"images", "referenceImages"} {
		raw, exists := payload[key]
		if !exists {
			continue
		}
		var images []string
		if err := json.Unmarshal(raw, &images); err != nil {
			return nil, invalidVideoTaskRequest("%s must be an array of image URLs", key)
		}
		changed := false
		for i, image := range images {
			if !strings.HasPrefix(strings.ToLower(image), "data:") {
				if !absoluteVideoImageURL(image) {
					return nil, invalidVideoTaskRequest("%s[%d] must be an absolute http/https URL or an image data URL", key, i)
				}
				continue
			}
			if uploader == nil {
				var enabled bool
				if a.imageStorage != nil {
					uploader, enabled = a.imageStorage()
				}
				if !enabled || uploader == nil || uploader.storage == nil {
					return nil, infraerrors.ServiceUnavailable("VIDEO_REFERENCE_STORAGE_UNAVAILABLE", "image storage is required for inline video references; use an HTTPS image URL")
				}
				var err error
				uploadID, err = GenerateVideoPublicTaskID()
				if err != nil {
					return nil, err
				}
			}
			data, contentType, err := uploader.decodeImageDataURL(image)
			if err != nil || len(data) == 0 || detectedImageContentType(data) == "" {
				return nil, invalidVideoTaskRequest("%s[%d] contains invalid or oversized image data", key, i)
			}
			objectKey := uploader.buildKey("video-reference/"+uploadID+"-"+key, i, contentType)
			storedURL, err := uploader.storage.Save(ctx, objectKey, contentType, data)
			if err != nil || !absoluteVideoImageURL(storedURL) {
				return nil, infraerrors.ServiceUnavailable("VIDEO_REFERENCE_UPLOAD_FAILED", "video reference image upload failed")
			}
			images[i] = storedURL
			changed = true
		}
		if changed {
			var err error
			payload[key], err = json.Marshal(images)
			if err != nil {
				return nil, err
			}
		}
	}
	return json.Marshal(payload)
}

func absoluteVideoImageURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Hostname() != "" && u.User == nil && (u.Scheme == "http" || u.Scheme == "https")
}
