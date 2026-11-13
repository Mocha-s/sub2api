package service

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type imageLogObservingWriter struct {
	gin.ResponseWriter
	capture *ImageTaskLogCapture
}

func (w *imageLogObservingWriter) Write(p []byte) (int, error) {
	w.capture.Write(p, true)
	return w.ResponseWriter.Write(p)
}
func (w *imageLogObservingWriter) WriteString(p string) (int, error) { return w.Write([]byte(p)) }

func TestImageTaskLogObservesFinalOutputAfterPartialDisconnect(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(b64BackfillPNGBytes)
	for _, oauth := range []bool{false, true} {
		for _, disconnect := range []bool{false, true} {
			name := "passthrough"
			if oauth {
				name = "oauth"
			}
			if disconnect {
				name += "/disconnect"
			}
			t.Run(name, func(t *testing.T) {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest("POST", "/v1/images/generations", nil)
				if disconnect {
					c.Writer = &failingOpenAIImageWriter{ResponseWriter: c.Writer, failAfter: 1}
				}
				capture := NewImageTaskLogCapture()
				SetImageTaskLogCapture(c, capture)
				c.Writer = &imageLogObservingWriter{ResponseWriter: c.Writer, capture: capture}
				stream := "data: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"partial\"}\n\n" +
					"data: {\"type\":\"image_generation.completed\",\"b64_json\":\"" + encoded + "\"}\n\n" +
					"data: [DONE]\n\n"
				if oauth {
					stream = "data: {\"type\":\"response.image_generation_call.partial_image\",\"partial_image_b64\":\"partial\"}\n\n" +
						"data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"ig_1\",\"type\":\"image_generation_call\",\"result\":\"" + encoded + "\"}}\n\n" +
						"data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"id\":\"ig_1\",\"type\":\"image_generation_call\",\"result\":\"" + encoded + "\"}]}}\n\n"
				}
				resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}
				gateway := &OpenAIGatewayService{}
				if oauth {
					_, count, _, _, err := gateway.handleOpenAIImagesOAuthStreamingResponse(resp, c, time.Now(), "b64_json", "image_generation", "gpt-image-1")
					require.NoError(t, err)
					require.Equal(t, 1, count)
				} else {
					_, count, _, _, err := gateway.handleOpenAIImagesStreamingResponse(resp, c, time.Now(), nil)
					require.NoError(t, err)
					require.Equal(t, 1, count)
				}
				body, failed := capture.Result(true)
				require.False(t, failed)
				repo := &imageLogMemoryRepo{}
				logs := NewImageTaskLogService(repo, nil)
				defer logs.Stop()
				entry := &ImageTaskLogEntry{TaskID: "stream", UserID: 7}
				logs.Start(context.Background(), entry)
				logs.Finish(context.Background(), entry, 200, body, false)
				require.Equal(t, 1, repo.entry.SuccessCount, "connected delivery and upstream observation must not duplicate outputs")
				require.Len(t, repo.entry.Results, 1)
				require.Equal(t, b64BackfillPNGBytes, repo.entry.Results[0].Data)
			})
		}
	}
}
