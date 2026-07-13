//go:build unit

package service

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestNormalizeOpenAIVideoStatus(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   VideoTaskStatus
	}{
		{name: "queued", status: "queued", want: VideoTaskStatusQueued},
		{name: "pending", status: "pending", want: VideoTaskStatusQueued},
		{name: "submitted", status: "submitted", want: VideoTaskStatusQueued},
		{name: "processing", status: "processing", want: VideoTaskStatusInProgress},
		{name: "running", status: "running", want: VideoTaskStatusInProgress},
		{name: "in_progress", status: "in_progress", want: VideoTaskStatusInProgress},
		{name: "completed", status: "completed", want: VideoTaskStatusCompleted},
		{name: "complete", status: "complete", want: VideoTaskStatusCompleted},
		{name: "success", status: "success", want: VideoTaskStatusCompleted},
		{name: "succeeded", status: "succeeded", want: VideoTaskStatusCompleted},
		{name: "failed", status: "failed", want: VideoTaskStatusFailed},
		{name: "failure", status: "failure", want: VideoTaskStatusFailed},
		{name: "error", status: "error", want: VideoTaskStatusFailed},
		{name: "cancelled", status: "cancelled", want: VideoTaskStatusCancelled},
		{name: "canceled", status: "canceled", want: VideoTaskStatusCancelled},
		{name: "expired", status: "expired", want: VideoTaskStatusExpired},
		{name: "unknown provider string", status: "provider-custom", want: VideoTaskStatusUnknown},
		{name: "trims and lowercases", status: " In_Progress ", want: VideoTaskStatusInProgress},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeOpenAIVideoStatus(tt.status); got != tt.want {
				t.Fatalf("NormalizeOpenAIVideoStatus(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestParseOpenAIVideoCreateRequest_DocumentedFields(t *testing.T) {
	originalBody := []byte(`{"model":"  video-ds-2.0-fast  ","prompt":"  slow dolly over city lights  ","seconds":"15","aspect_ratio":" 16:9 ","images":[{"url":"https://example.test/image.png"}],"videos":[{"url":"https://example.test/input.mp4"}],"audios":[{"url":"https://example.test/input.wav"}],"provider_extra":"kept out of typed fields"}`)
	body := append([]byte(nil), originalBody...)

	req, err := ParseOpenAIVideoCreateRequest(body)
	if err != nil {
		t.Fatalf("ParseOpenAIVideoCreateRequest returned error: %v", err)
	}

	if req.Model != "video-ds-2.0-fast" {
		t.Fatalf("Model = %q, want %q", req.Model, "video-ds-2.0-fast")
	}
	if req.Prompt != "slow dolly over city lights" {
		t.Fatalf("Prompt = %q, want %q", req.Prompt, "slow dolly over city lights")
	}
	if req.Seconds != "15" {
		t.Fatalf("Seconds = %q, want 15", req.Seconds)
	}
	if req.AspectRatio != "16:9" {
		t.Fatalf("AspectRatio = %q, want %q", req.AspectRatio, "16:9")
	}
	if len(req.Images) != 1 || len(req.Videos) != 1 || len(req.Audios) != 1 {
		t.Fatalf("media counts = images:%d videos:%d audios:%d, want 1 each", len(req.Images), len(req.Videos), len(req.Audios))
	}
	if req.RequestHash == "" {
		t.Fatal("RequestHash is empty")
	}
	if req.PromptHash == "" {
		t.Fatal("PromptHash is empty")
	}
	if !bytes.Equal(req.RawBody, originalBody) {
		t.Fatalf("RawBody = %q, want original body %q", req.RawBody, originalBody)
	}
	body[0] = ' '
	if !bytes.Equal(req.RawBody, originalBody) {
		t.Fatalf("RawBody changed after source body mutation: %q", req.RawBody)
	}

	wantMetadata := map[string]any{
		"seconds":      "15",
		"aspect_ratio": "16:9",
		"image_count":  1,
		"video_count":  1,
		"audio_count":  1,
	}
	for key, want := range wantMetadata {
		if got := req.Metadata[key]; got != want {
			t.Fatalf("Metadata[%q] = %#v, want %#v", key, got, want)
		}
	}

	reqAgain, err := ParseOpenAIVideoCreateRequest(originalBody)
	if err != nil {
		t.Fatalf("ParseOpenAIVideoCreateRequest returned error on second parse: %v", err)
	}
	if reqAgain.RequestHash != req.RequestHash {
		t.Fatalf("RequestHash is not stable: %q then %q", req.RequestHash, reqAgain.RequestHash)
	}
	if reqAgain.PromptHash != req.PromptHash {
		t.Fatalf("PromptHash is not stable: %q then %q", req.PromptHash, reqAgain.PromptHash)
	}
}

func TestParseOpenAIVideoCreateRequest_RequiresModelAndPrompt(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{name: "missing model", body: []byte(`{"prompt":"make a video"}`)},
		{name: "blank model", body: []byte(`{"model":" \t ","prompt":"make a video"}`)},
		{name: "missing prompt", body: []byte(`{"model":"video-ds-2.0"}`)},
		{name: "blank prompt", body: []byte(`{"model":"video-ds-2.0","prompt":" \n "}`)},
		{name: "unsupported model", body: []byte(`{"model":"sora-2","prompt":"make a video"}`)},
		{name: "numeric seconds", body: []byte(`{"model":"video-ds-2.0-fast","prompt":"make a video","seconds":15}`)},
		{name: "forbidden duration field", body: []byte(`{"model":"video-ds-2.0-fast","prompt":"make a video","seconds":"15","duration":"15"}`)},
		{name: "forbidden width field", body: []byte(`{"model":"video-ds-2.0-fast","prompt":"make a video","seconds":"15","width":1080}`)},
		{name: "forbidden height field", body: []byte(`{"model":"video-ds-2.0-fast","prompt":"make a video","seconds":"15","height":1920}`)},
		{name: "forbidden size field", body: []byte(`{"model":"video-ds-2.0-fast","prompt":"make a video","seconds":"15","size":"9:16"}`)},
		{name: "forbidden mode field", body: []byte(`{"model":"video-ds-2.0-fast","prompt":"make a video","seconds":"15","mode":"fast"}`)},
		{name: "forbidden model_name field", body: []byte(`{"model":"video-ds-2.0-fast","prompt":"make a video","seconds":"15","model_name":"video-ds-2.0-fast"}`)},
		{name: "forbidden req_key field", body: []byte(`{"model":"video-ds-2.0-fast","prompt":"make a video","seconds":"15","req_key":"abc"}`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseOpenAIVideoCreateRequest(tt.body); err == nil {
				t.Fatal("ParseOpenAIVideoCreateRequest returned nil error")
			}
		})
	}
}

func TestRewriteOpenAIVideoTaskID(t *testing.T) {
	body := []byte(`{"id":"provider-id","task_id":"provider-task-id","status":"processing","unknown":123,"nested":{"id":"nested-provider-id"}}`)

	rewritten, err := RewriteOpenAIVideoTaskID(body, "task_0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("RewriteOpenAIVideoTaskID returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(rewritten, &got); err != nil {
		t.Fatalf("rewritten body is not JSON: %v", err)
	}
	if got["id"] != "task_0123456789abcdef0123456789abcdef" {
		t.Fatalf("id = %#v, want rewritten public task id", got["id"])
	}
	if got["task_id"] != "task_0123456789abcdef0123456789abcdef" {
		t.Fatalf("task_id = %#v, want rewritten public task id", got["task_id"])
	}
	if got["status"] != "processing" {
		t.Fatalf("status = %#v, want preserved provider status", got["status"])
	}
	if got["unknown"] != float64(123) {
		t.Fatalf("unknown = %#v, want preserved unknown field", got["unknown"])
	}
	nested, ok := got["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested = %#v, want object", got["nested"])
	}
	if nested["id"] != "nested-provider-id" {
		t.Fatalf("nested id = %#v, want preserved nested provider id", nested["id"])
	}
}

func TestRewriteOpenAIVideoTaskIDAddsCanonicalIDWhenUpstreamOmitsIt(t *testing.T) {
	rewritten, err := RewriteOpenAIVideoTaskID([]byte(`{"status":"processing"}`), "task_public")
	if err != nil {
		t.Fatalf("RewriteOpenAIVideoTaskID returned error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(rewritten, &got); err != nil {
		t.Fatalf("unmarshal rewritten body: %v", err)
	}
	if got["id"] != "task_public" || got["status"] != "processing" {
		t.Fatalf("rewritten body = %#v", got)
	}
}
