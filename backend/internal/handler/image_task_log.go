package handler

import (
	"net/http"
	"strings"
	"time"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const asyncImageExecutionKey = "image_task_async_execution"
const imageTaskLogEntryKey = "image_task_log_entry"

type imageTaskLogWriter struct {
	gin.ResponseWriter
	capture *service.ImageTaskLogCapture
	sse     bool
}

func (w *imageTaskLogWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.sse = strings.Contains(w.Header().Get("Content-Type"), "text/event-stream")
	// The upstream may finish after the caller disconnects; retain its output even if delivery failed.
	w.capture.Write(p, w.sse)
	return n, err
}
func (w *imageTaskLogWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

func (h *OpenAIGatewayHandler) startImageTaskLog(c *gin.Context) func() {
	if h == nil {
		return func() {}
	}
	return startImageTaskLog(c, h.imageLogs)
}

func (h *GatewayHandler) startGeminiImageTaskLog(c *gin.Context, model string, body []byte) func() {
	if h.imageLogs == nil || (!service.IsGeminiImageGenerationModel(model) && !service.IsGeminiBananaBridgeModel(model)) {
		return func() {}
	}
	finish := startImageTaskLog(c, h.imageLogs)
	if raw, ok := c.Get(imageTaskLogEntryKey); ok {
		entry := raw.(*service.ImageTaskLogEntry)
		entry.Model = model
		var texts []string
		for _, content := range gjson.GetBytes(body, "contents").Array() {
			for _, part := range content.Get("parts").Array() {
				if text := part.Get("text").String(); text != "" {
					texts = append(texts, text)
				}
			}
		}
		entry.Prompt = strings.Join(texts, "\n")
		h.imageLogs.Save(c.Request.Context(), entry)
	}
	return finish
}

func startImageTaskLog(c *gin.Context, logs *service.ImageTaskLogService) func() {
	if logs == nil || c.GetBool(asyncImageExecutionKey) {
		return func() {}
	}
	key, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || key == nil || key.UserID <= 0 {
		return func() {}
	}
	entry := &service.ImageTaskLogEntry{UserID: key.UserID, APIKeyID: key.ID, Platform: effectiveAPIKeyPlatform(c, key),
		RequestID: service.ResolveUsageBillingRequestID(c.Request.Context(), "")}
	logs.Start(c.Request.Context(), entry)
	c.Set(imageTaskLogEntryKey, entry)
	writer := &imageTaskLogWriter{ResponseWriter: c.Writer, capture: service.NewImageTaskLogCapture()}
	service.SetImageTaskLogCapture(c, writer.capture)
	c.Writer = writer
	return func() {
		body, failed := writer.capture.Result(writer.sse)
		status := writer.Status()
		if c.Request.Context().Err() != nil && len(body) == 0 {
			status = http.StatusGatewayTimeout
		}
		c.Writer = writer.ResponseWriter
		logs.FinishAsync(*entry, status, body, failed)
	}
}

// Accepted async submissions are logged under their public task ID by Submit/run.
// Rejected authenticated requests have no public task ID, but still need failure history.
func (h *OpenAIGatewayHandler) rejectedAsyncImageTaskLog(c *gin.Context) func() {
	if h == nil || h.imageLogs == nil {
		return func() {}
	}
	key, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || key == nil {
		return func() {}
	}
	e := &service.ImageTaskLogEntry{UserID: key.UserID, APIKeyID: key.ID, Platform: effectiveAPIKeyPlatform(c, key),
		RequestMode: "async", CreatedAt: time.Now().UTC(), RequestID: service.ResolveUsageBillingRequestID(c.Request.Context(), "")}
	c.Set(imageTaskLogEntryKey, e)
	writer := &imageTaskLogWriter{ResponseWriter: c.Writer, capture: service.NewImageTaskLogCapture()}
	c.Writer = writer
	return func() {
		c.Writer = writer.ResponseWriter
		if writer.Status() < 400 {
			return
		}
		h.imageLogs.Start(c.Request.Context(), e)
		body, _ := writer.capture.Result(false)
		h.imageLogs.Finish(c.Request.Context(), e, writer.Status(), body, false)
	}
}

func (h *OpenAIGatewayHandler) imageTaskLogMetadata(c *gin.Context, model, prompt string, count int) {
	raw, ok := c.Get(imageTaskLogEntryKey)
	if !ok || h.imageLogs == nil {
		return
	}
	entry := raw.(*service.ImageTaskLogEntry)
	entry.Model = model
	entry.Prompt = prompt
	entry.ItemCount = max(1, count)
	if key, ok := middleware2.GetAPIKeyFromContext(c); ok {
		entry.Platform = effectiveAPIKeyPlatform(c, key)
	}
	if entry.TaskID != "" {
		h.imageLogs.Save(c.Request.Context(), entry)
	}
}
func imageTaskLogUsage(c *gin.Context, result *service.OpenAIForwardResult) {
	if result == nil {
		return
	}
	if raw, ok := c.Get(imageTaskLogEntryKey); ok {
		raw.(*service.ImageTaskLogEntry).RequestID = service.ResolveUsageBillingRequestID(c.Request.Context(), result.RequestID)
	}
}
