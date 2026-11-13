package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/alitto/pond/v2"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// Bounds apply per request, including SSE final events. Partial previews are never persisted.
const ImageTaskLogMaxResultBytes = 64 << 20
const imageTaskLogMaxItems = 256

type ImageTaskLogResult struct {
	Data        []byte `json:"data,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

type ImageTaskLogEntry struct {
	DeadlineAt                                                      time.Time
	TaskID, RequestID, RequestMode, Platform, Model, Prompt, Status string
	UserID, APIKeyID                                                int64
	ItemCount, SuccessCount                                         int
	ErrorCode, ErrorMessage                                         string
	CreatedAt                                                       time.Time
	FinishedAt                                                      *time.Time
	Results                                                         []ImageTaskLogResult
}

type ImageTaskLogRepository interface {
	Save(context.Context, *ImageTaskLogEntry) error
	Get(ctx context.Context, taskID string, userID int64, index int) (*ImageTaskLogEntry, error)
}

type ImageTaskLogService struct {
	repo      ImageTaskLogRepository
	gateway   *OpenAIGatewayService
	retention pond.Pool
	metadata  pond.Pool
	stopOnce  sync.Once
	workCtx   context.Context
	cancel    context.CancelFunc
}

func NewImageTaskLogService(repo ImageTaskLogRepository, gateway *OpenAIGatewayService) *ImageTaskLogService {
	ctx, cancel := context.WithCancel(context.Background())
	return &ImageTaskLogService{repo: repo, gateway: gateway,
		retention: pond.NewPool(2, pond.WithQueueSize(2)),
		metadata:  pond.NewPool(2, pond.WithQueueSize(128)), workCtx: ctx, cancel: cancel}
}

func (s *ImageTaskLogService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() { s.cancel(); s.retention.StopAndWait(); s.metadata.StopAndWait() })
}

// FinishAsync takes ownership of the bounded body and a value copy of metadata.
// Neither the request context nor the Gin context/writer escapes into the pool.
func (s *ImageTaskLogService) FinishAsync(e ImageTaskLogEntry, status int, body []byte, failed bool) {
	if s == nil {
		return
	}
	now := time.Now().UTC()
	e.FinishedAt = &now
	e.Results = nil
	if _, ok := s.retention.TrySubmit(func() { s.finish(s.workCtx, &e, status, body, failed) }); ok {
		return
	}
	if count := len(gjson.GetBytes(body, "data").Array()); count > 0 {
		e.ItemCount = count
	}
	// Drop large payloads on overload, but retain a small final-state update in a separate queue.
	fallback := []byte(nil)
	if status >= 300 || failed || gjson.GetBytes(body, "error").Exists() {
		failed = true
		fallback = []byte(`{"error":{"code":"image_generation_failed","message":"Image generation failed; retention queue was full"}}`)
	}
	if _, ok := s.metadata.TrySubmit(func() { s.finish(s.workCtx, &e, status, fallback, failed) }); !ok {
		// Both queues are bounded; persisted deadlines also fence interruptions/database outages.
		logger.L().Warn("image_task_log.final_state_queue_full", zap.String("task_id", e.TaskID))
	}
}

func (s *ImageTaskLogService) Start(ctx context.Context, e *ImageTaskLogEntry) {
	if e.TaskID == "" {
		e.TaskID = "imglog_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	if e.DeadlineAt.IsZero() {
		// Sync upstream work is detached and has no common execution timeout.
		// shortcut: mark abandoned sync logs expired after 24h; a late completion still replaces this read-side state.
		e.DeadlineAt = e.CreatedAt.Add(defaultImageTaskTTL)
	}
	if e.RequestMode == "" {
		e.RequestMode = "sync"
	}
	e.Status = ImageTaskStatusProcessing
	e.ItemCount = max(1, e.ItemCount)
	s.Save(ctx, e)
}

// Save is best effort: optional task logging must not alter gateway/billing outcomes.
func (s *ImageTaskLogService) Save(ctx context.Context, e *ImageTaskLogEntry) {
	if s == nil || s.repo == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.repo.Save(ctx, e); err != nil {
		logger.L().Warn("image_task_log.persist_failed", zap.String("task_id", e.TaskID), zap.Error(err))
	}
}

func (s *ImageTaskLogService) Finish(ctx context.Context, e *ImageTaskLogEntry, status int, body []byte, streamFailed bool) {
	s.finish(context.WithoutCancel(ctx), e, status, body, streamFailed)
}
func (s *ImageTaskLogService) finish(ctx context.Context, e *ImageTaskLogEntry, status int, body []byte, streamFailed bool) {
	if s == nil || s.repo == nil {
		return
	}
	now := time.Now().UTC()
	if e.FinishedAt == nil {
		e.FinishedAt = &now
	}
	e.Status = ImageTaskStatusCompleted
	e.SuccessCount = 0
	e.Results = nil
	failed := status < 200 || status >= 300 || streamFailed
	if gjson.ValidBytes(body) && gjson.GetBytes(body, "error").Exists() {
		failed = true
	}
	if failed {
		e.Status = ImageTaskStatusFailed
		e.ErrorCode = firstNonEmpty(gjson.GetBytes(body, "error.code").String(), gjson.GetBytes(body, "error.type").String(), "image_generation_failed")
		e.ErrorMessage = firstNonEmpty(gjson.GetBytes(body, "error.message").String(), "Image generation failed or stream ended before completion")
		e.ErrorCode = boundedImageLogText(e.ErrorCode, 128)
		e.ErrorMessage = boundedImageLogText(redactContentModerationSecrets(e.ErrorMessage), 2048)
		s.Save(ctx, e)
		return
	}
	items := gjson.GetBytes(body, "data").Array()
	if len(items) > 0 {
		e.ItemCount = len(items)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	budget := ImageTaskLogMaxResultBytes
	for i, item := range items {
		if i >= imageTaskLogMaxItems {
			break
		}
		output := ImageTaskLogResult{}
		encoded := strings.TrimSpace(item.Get("b64_json").String())
		rawURL := strings.TrimSpace(item.Get("url").String())
		if encoded == "" && strings.HasPrefix(strings.ToLower(rawURL), "data:") {
			// Reuse the uploader's strict data URL decoder, but still sniff the bytes below.
			uploader := NewImageResultUploader(nil, "", int64(budget), nil)
			data, _, err := uploader.decodeImageDataURL(rawURL)
			if err == nil {
				encoded = base64.StdEncoding.EncodeToString(data)
			}
		} else if encoded == "" && rawURL != "" && s.gateway != nil && ctx.Err() == nil && budget > 0 {
			// Existing outbound allowlist + public-host DNS/redirect checks; no user headers/account credentials.
			encoded, _ = s.gateway.fetchOpenAIImageURLBase64(ctx, &Account{}, rawURL)
		}
		if encoded != "" && len(encoded) <= budget {
			data, err := io.ReadAll(io.LimitReader(base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded)), defaultImageMaxDownloadBytes+1))
			if err == nil && int64(len(data)) <= defaultImageMaxDownloadBytes && isBackfillImageContent(data) {
				output.Data = data
				output.ContentType = detectedImageContentType(data)
				budget -= len(encoded)
				e.SuccessCount++
			}
		}
		if len(output.Data) > 0 {
			e.Results = append(e.Results, output)
		}
	}
	if e.SuccessCount < e.ItemCount {
		e.ErrorCode = "result_unavailable"
		e.ErrorMessage = "Generation succeeded; some results could not be retained (size limit, invalid image, or unavailable/blocked URL)."
	} else {
		e.ErrorCode = ""
		e.ErrorMessage = ""
	}
	s.Save(ctx, e)
}

func boundedImageLogText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return strings.ToValidUTF8(s[:limit], "")
}

// Content only reads retained bytes. Ownership is checked in SQL, before loading a result.
func (s *ImageTaskLogService) Content(ctx context.Context, userID int64, taskID string, index int) ([]byte, string, error) {
	if s == nil || s.repo == nil || index < 0 {
		return nil, "", ErrImageTaskNotFound
	}
	e, err := s.repo.Get(ctx, taskID, userID, index)
	if err != nil {
		return nil, "", err
	}
	if e.Status == ImageTaskStatusProcessing {
		return nil, "", ErrImageTaskResultPending
	}
	if len(e.Results) == 0 || len(e.Results[0].Data) == 0 {
		return nil, "", ErrImageTaskNotFound
	}
	image := e.Results[0]
	if !isBackfillImageContent(image.Data) {
		return nil, "", ErrImageTaskNotFound
	}
	return image.Data, detectedImageContentType(image.Data), nil
}

// ImageTaskLogCapture incrementally observes a response without buffering a whole SSE stream.
type ImageTaskLogCapture struct {
	limit                                                  int
	body, pending, event                                   []byte
	items                                                  []json.RawMessage
	stored                                                 int
	discardLine, discardEvent, overflow, completed, failed bool
	upstream                                               bool
}

const imageTaskLogCaptureKey = "image_task_log_capture"

func SetImageTaskLogCapture(c *gin.Context, capture *ImageTaskLogCapture) {
	c.Set(imageTaskLogCaptureKey, capture)
}

// beginImageTaskLogUpstream switches a streaming attempt to one authoritative
// source before its first write, avoiding downstream/upstream double counting.
func beginImageTaskLogUpstream(c *gin.Context) {
	if value, ok := c.Get(imageTaskLogCaptureKey); ok {
		capture := value.(*ImageTaskLogCapture)
		*capture = ImageTaskLogCapture{limit: capture.limit, upstream: true}
	}
}
func observeImageTaskLogEvent(c *gin.Context, data []byte) {
	if value, ok := c.Get(imageTaskLogCaptureKey); ok {
		capture := value.(*ImageTaskLogCapture)
		if !capture.upstream {
			return
		}
		if len(data) > capture.limit {
			capture.overflow = true
			return
		}
		capture.consume(data)
	}
}

func NewImageTaskLogCapture() *ImageTaskLogCapture {
	return &ImageTaskLogCapture{limit: ImageTaskLogMaxResultBytes}
}

func (c *ImageTaskLogCapture) Write(p []byte, sse bool) {
	if sse && c.upstream {
		return
	}
	if !sse {
		if !c.overflow && len(p) <= c.limit-len(c.body) {
			c.body = append(c.body, p...)
		} else {
			c.body = nil
			c.overflow = true
		}
		return
	}
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		lineEnd := end >= 0
		if !lineEnd {
			end = len(p)
		}
		if !c.discardLine {
			if end <= c.limit-len(c.pending) {
				c.pending = append(c.pending, p[:end]...)
			} else {
				c.pending = nil
				c.discardLine = true
				c.discardEvent = true
				c.overflow = true
			}
		}
		p = p[end:]
		if !lineEnd {
			break
		}
		p = p[1:]
		if !c.discardLine {
			c.line(bytes.TrimSuffix(c.pending, []byte{'\r'}))
		}
		c.pending = c.pending[:0]
		c.discardLine = false
	}
}
func (c *ImageTaskLogCapture) line(line []byte) {
	if len(line) == 0 {
		if !c.discardEvent {
			c.consume(c.event)
		}
		c.event = c.event[:0]
		c.discardEvent = false
		return
	}
	if !bytes.HasPrefix(line, []byte("data:")) || c.discardEvent {
		return
	}
	data := bytes.TrimPrefix(line[5:], []byte(" "))
	if len(data)+1 > c.limit-len(c.event) {
		c.event = nil
		c.discardEvent = true
		c.overflow = true
		return
	}
	if len(c.event) > 0 {
		c.event = append(c.event, '\n')
	}
	c.event = append(c.event, data...)
}
func (c *ImageTaskLogCapture) consume(data []byte) {
	if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
		return
	}
	if !gjson.ValidBytes(data) {
		return
	}
	event := gjson.ParseBytes(data)
	kind := event.Get("type").String()
	if event.Get("error").Exists() || kind == "error" || kind == "response.failed" {
		c.failed = true
		c.body = append([]byte(nil), data...)
		return
	}
	if strings.HasSuffix(kind, ".partial_image") {
		return
	}
	if strings.HasSuffix(kind, ".completed") {
		c.completed = true
	}
	if event.Get("data").IsArray() {
		c.completed = true
		for _, item := range event.Get("data").Array() {
			c.add(item)
		}
	} else if kind == "image_generation.completed" || kind == "image_edit.completed" || kind == "image_editing.completed" {
		c.add(event)
	}
}
func (c *ImageTaskLogCapture) add(item gjson.Result) {
	if len(c.items) >= imageTaskLogMaxItems {
		return
	}
	raw, _ := json.Marshal(map[string]string{"b64_json": item.Get("b64_json").String(), "url": item.Get("url").String()})
	if len(raw) > c.limit-c.stored {
		c.items = append(c.items, json.RawMessage(`{}`))
		return
	}
	c.stored += len(raw)
	c.items = append(c.items, raw)
}
func (c *ImageTaskLogCapture) Result(sse bool) ([]byte, bool) {
	if !sse {
		return c.body, false
	}
	if len(c.pending) > 0 && !c.discardLine {
		c.line(c.pending)
	}
	if len(c.event) > 0 && !c.discardEvent {
		c.consume(c.event)
		c.event = nil
	}
	if c.failed {
		return c.body, true
	}
	out, _ := json.Marshal(map[string]any{"data": c.items})
	return out, !c.completed && !c.overflow
}
