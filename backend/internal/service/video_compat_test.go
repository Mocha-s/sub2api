//go:build unit

package service

import (
	"strings"
	"testing"
)

func TestNormalizeOpenAIVideoStatusIncludesNotStart(t *testing.T) {
	if got := NormalizeOpenAIVideoStatus("NOT_START"); got != VideoTaskStatusQueued {
		t.Fatalf("NormalizeOpenAIVideoStatus(NOT_START) = %q, want %q", got, VideoTaskStatusQueued)
	}
}

func TestOpenAIDurationResultURLDocumentedShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"top result_url", `{"result_url":"https://cdn.test/r.mp4"}`, "https://cdn.test/r.mp4"},
		{"top content_url", `{"content_url":"https://cdn.test/c.mp4"}`, "https://cdn.test/c.mp4"},
		{"metadata result_urls", `{"metadata":{"result_urls":["https://cdn.test/m.mp4"]}}`, "https://cdn.test/m.mp4"},
		{"data url", `{"data":[{"url":"https://cdn.test/d.mp4"}]}`, "https://cdn.test/d.mp4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := openAIDurationResultURL([]byte(tt.body)); got != tt.want {
				t.Fatalf("openAIDurationResultURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseVideoCompatPayloadCollectsAliases(t *testing.T) {
	p, err := parseVideoCompatPayload([]byte(`{
		"model":"kling-o3",
		"prompt":"city",
		"seconds":"10",
		"image_url":"https://cdn.test/a.png",
		"reference_images":["https://cdn.test/b.png"],
		"start_frame":"https://cdn.test/start.png",
		"audio_reference":"https://cdn.test/a.wav"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.Duration != "10" {
		t.Fatalf("Duration = %q, want 10", p.Duration)
	}
	if len(p.ImageRefs) != 2 {
		t.Fatalf("ImageRefs len = %d, want 2", len(p.ImageRefs))
	}
	if p.StartFrame == "" {
		t.Fatal("StartFrame empty")
	}
	if len(p.AudioRefs) != 1 {
		t.Fatalf("AudioRefs len = %d, want 1", len(p.AudioRefs))
	}
}

func TestParseVideoCompatPayloadCollectsReviewedAliases(t *testing.T) {
	p, err := parseVideoCompatPayload([]byte(`{
		"model":"kling-o3",
		"prompt":"city",
		"image_reference":"https://cdn.test/image-reference.png",
		"imageUrls":["https://cdn.test/image-url-camel.png"],
		"referenceImage":"https://cdn.test/reference-image.png",
		"referenceImages":["https://cdn.test/reference.png"],
		"referenceImageUrl":"https://cdn.test/reference-image-url.png",
		"referenceImageUrls":["https://cdn.test/reference-url.png"],
		"style_references":[{"url":"https://cdn.test/style-a.png"}],
		"styleReferences":["https://cdn.test/style-b.png"],
		"element_references":[{"image_url":"https://cdn.test/element-a.png"}],
		"elementReferences":["https://cdn.test/element-b.png"],
		"inputReference":"https://cdn.test/input-reference.png",
		"images_base64":["image-b64-a"],
		"imagesBase64":["image-b64-b"],
		"reference_video":"https://cdn.test/video-a.mp4",
		"videoUrl":"https://cdn.test/video-url-camel.mp4",
		"videoUrls":["https://cdn.test/video-urls-camel.mp4"],
		"referenceVideos":["https://cdn.test/video-b.mp4"],
		"reference_video_url":"https://cdn.test/video-c.mp4",
		"referenceVideoUrls":["https://cdn.test/video-d.mp4"],
		"reference_audio_url":"https://cdn.test/audio-a.wav",
		"referenceAudioUrls":["https://cdn.test/audio-b.wav"],
		"audios_base64":["audio-b64-a"],
		"audiosBase64":["audio-b64-b"],
		"inputVideo":"https://cdn.test/input.mp4"
	}`))
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"https://cdn.test/image-reference.png",
		"https://cdn.test/image-url-camel.png",
		"https://cdn.test/reference-image.png",
		"https://cdn.test/reference.png",
		"https://cdn.test/reference-image-url.png",
		"https://cdn.test/reference-url.png",
		"https://cdn.test/style-a.png",
		"https://cdn.test/style-b.png",
		"https://cdn.test/element-a.png",
		"https://cdn.test/element-b.png",
		"https://cdn.test/input-reference.png",
		"image-b64-a",
		"image-b64-b",
	} {
		t.Run(want, func(t *testing.T) {
			requireVideoCompatRef(t, p.ImageRefs, want)
		})
	}
	for _, want := range []string{
		"https://cdn.test/video-a.mp4",
		"https://cdn.test/video-url-camel.mp4",
		"https://cdn.test/video-urls-camel.mp4",
		"https://cdn.test/video-b.mp4",
		"https://cdn.test/video-c.mp4",
		"https://cdn.test/video-d.mp4",
	} {
		t.Run(want, func(t *testing.T) {
			requireVideoCompatRef(t, p.VideoRefs, want)
		})
	}
	for _, want := range []string{"https://cdn.test/audio-a.wav", "https://cdn.test/audio-b.wav", "audio-b64-a", "audio-b64-b"} {
		t.Run(want, func(t *testing.T) {
			requireVideoCompatRef(t, p.AudioRefs, want)
		})
	}
	if p.InputVideo != "https://cdn.test/input.mp4" {
		t.Fatalf("InputVideo = %q, want camelCase alias", p.InputVideo)
	}
}

func TestParseVideoCompatPayloadCollectsObjectRefs(t *testing.T) {
	p, err := parseVideoCompatPayload([]byte(`{
		"model":"kling-o3",
		"prompt":"city",
		"image":{"image_url":"https://cdn.test/image.png"},
		"referenceImages":{"url":"https://cdn.test/reference.png"},
		"video":{"video_url":"https://cdn.test/video.mp4"},
		"audio_reference":{"audio_url":"https://cdn.test/audio.wav"}
	}`))
	if err != nil {
		t.Fatal(err)
	}

	requireVideoCompatRef(t, p.ImageRefs, "https://cdn.test/image.png")
	requireVideoCompatRef(t, p.ReferenceImageRefs, "https://cdn.test/reference.png")
	requireVideoCompatRef(t, p.VideoRefs, "https://cdn.test/video.mp4")
	requireVideoCompatRef(t, p.AudioRefs, "https://cdn.test/audio.wav")
}

func TestValidateVideoCompatPayloadModelConstraints(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"omni rejects audio", `{"model":"gemini-omni-flash","prompt":"x","duration":5,"audio":"https://cdn.test/a.wav"}`, "audio is not supported"},
		{"omni images max one", `{"model":"gemini-omni-flash","prompt":"x","duration":5,"images":["https://cdn.test/1.png","https://cdn.test/2.png"]}`, "images cannot exceed 1"},
		{"omni reference images max three", `{"model":"gemini-omni-flash","prompt":"x","duration":5,"referenceImages":["https://cdn.test/1.png","https://cdn.test/2.png","https://cdn.test/3.png","https://cdn.test/4.png"]}`, "reference_images cannot exceed 3"},
		{"omni frame mode reference images max one", `{"model":"gemini-omni-flash","prompt":"x","duration":5,"reference_mode":"frame","referenceImages":["https://cdn.test/1.png","https://cdn.test/2.png"]}`, "frame references cannot exceed 1"},
		{"omni element refs max three", `{"model":"gemini-omni-flash","prompt":"x","duration":5,"elementReferences":["https://cdn.test/1.png","https://cdn.test/2.png","https://cdn.test/3.png","https://cdn.test/4.png"]}`, "element references cannot exceed 3"},
		{"omni style refs max four", `{"model":"gemini-omni-flash","prompt":"x","duration":5,"styleReferences":["https://cdn.test/1.png","https://cdn.test/2.png","https://cdn.test/3.png","https://cdn.test/4.png","https://cdn.test/5.png"]}`, "style references cannot exceed 4"},
		{"omni input video max one", `{"model":"gemini-omni-flash","prompt":"x","duration":5,"input_video":"https://cdn.test/1.mp4","inputVideo":"https://cdn.test/2.mp4"}`, "input_video cannot exceed 1"},
		{"omni videos max one", `{"model":"gemini-omni-flash","prompt":"x","duration":5,"videos":["https://cdn.test/1.mp4","https://cdn.test/2.mp4"]}`, "source video cannot exceed 1"},
		{"omni video urls max one", `{"model":"gemini-omni-flash","prompt":"x","duration":5,"video_urls":["https://cdn.test/1.mp4","https://cdn.test/2.mp4"]}`, "source video cannot exceed 1"},
		{"omni total refs max four", `{"model":"gemini-omni-flash","prompt":"x","duration":5,"image":"https://cdn.test/1.png","referenceImages":["https://cdn.test/2.png","https://cdn.test/3.png","https://cdn.test/4.png"],"styleReferences":["https://cdn.test/5.png"]}`, "total references cannot exceed 4"},
		{"kling v3 rejects ordinary refs", `{"model":"kling-v3","prompt":"x","duration":5,"image_url":"https://cdn.test/a.png"}`, "image references are not supported"},
		{"kling v3 rejects audio refs", `{"model":"kling-v3","prompt":"x","duration":5,"audio_reference":"https://cdn.test/a.wav"}`, "audio is not supported"},
		{"kling v3 rejects source video", `{"model":"kling-v3","prompt":"x","duration":5,"input_video":"https://cdn.test/in.mp4"}`, "input_video is not supported"},
		{"kling v3 rejects video refs", `{"model":"kling-v3","prompt":"x","duration":5,"video_url":"https://cdn.test/in.mp4"}`, "video references are not supported"},
		{"kling end needs start", `{"model":"kling-v3","prompt":"x","duration":5,"end_frame":"https://cdn.test/end.png"}`, "end_frame requires start_frame"},
		{"kling array end needs start", `{"model":"kling-o3","prompt":"x","duration":5,"end_frame":["https://cdn.test/end.png"]}`, "end_frame requires start_frame"},
		{"kling start frame max one", `{"model":"kling-v3","prompt":"x","duration":5,"start_frame":["https://cdn.test/1.png","https://cdn.test/2.png"]}`, "start_frame cannot exceed 1"},
		{"kling end frame max one", `{"model":"kling-o3","prompt":"x","duration":5,"start_frame":"https://cdn.test/start.png","end_frame":["https://cdn.test/1.png","https://cdn.test/2.png"]}`, "end_frame cannot exceed 1"},
		{"kling o3 rejects audio refs", `{"model":"kling-o3","prompt":"x","duration":5,"audio_reference":"https://cdn.test/a.wav"}`, "audio is not supported"},
		{"kling o3 frames exclude ordinary refs", `{"model":"kling-o3","prompt":"x","duration":5,"start_frame":"https://cdn.test/start.png","image_url":"https://cdn.test/a.png"}`, "frame references cannot be combined"},
		{"kling o3 frames exclude video refs", `{"model":"kling-o3","prompt":"x","duration":5,"start_frame":"https://cdn.test/start.png","video_url":"https://cdn.test/in.mp4"}`, "frame references cannot be combined"},
		{"kling input video excludes frame refs", `{"model":"kling-o3","prompt":"x","duration":5,"input_video":"https://cdn.test/in.mp4","start_frame":"https://cdn.test/start.png"}`, "frame references"},
		{"kling o3 source video max one", `{"model":"kling-o3","prompt":"x","duration":5,"videos":["https://cdn.test/1.mp4","https://cdn.test/2.mp4"]}`, "source video cannot exceed 1"},
		{"kling o3 source rejects 2160p", `{"model":"kling-o3","prompt":"x","duration":5,"input_video":"https://cdn.test/in.mp4","resolution":"2160P"}`, "source video cannot use 4K"},
		{"kling o3 source rejects 4k size", `{"model":"kling-o3","prompt":"x","duration":5,"video_url":"https://cdn.test/in.mp4","size":"3840x2160"}`, "source video cannot use 4K"},
		{"kling o3 image refs max seven", `{"model":"kling-o3","prompt":"x","duration":5,"image_urls":["https://cdn.test/a.png","https://cdn.test/a.png","https://cdn.test/a.png","https://cdn.test/a.png","https://cdn.test/a.png","https://cdn.test/a.png","https://cdn.test/a.png","https://cdn.test/a.png"]}`, "image references cannot exceed 7"},
		{"kling o3 source image refs max four", `{"model":"kling-o3","prompt":"x","duration":5,"input_video":"https://cdn.test/in.mp4","image_urls":["https://cdn.test/1.png","https://cdn.test/2.png","https://cdn.test/3.png","https://cdn.test/4.png","https://cdn.test/5.png"]}`, "image references cannot exceed 4"},
		{"h3 refs conflict", `{"model":"minimax-h3-2k","prompt":"x","duration":8,"reference_images":["https://cdn.test/a.png"],"start_frame":"https://cdn.test/s.png"}`, "reference_images cannot be combined"},
		{"h3 reference images max five", `{"model":"minimax-h3-2k","prompt":"x","duration":8,"referenceImages":["https://cdn.test/a.png","https://cdn.test/a.png","https://cdn.test/a.png","https://cdn.test/a.png","https://cdn.test/a.png","https://cdn.test/a.png"]}`, "reference_images cannot exceed 5"},
		{"h3 reference image urls conflict", `{"model":"minimax-h3-2k","prompt":"x","duration":8,"referenceImageUrls":["https://cdn.test/a.png"],"start_frame":"https://cdn.test/s.png"}`, "reference_images cannot be combined"},
		{"h3 audio needs refs", `{"model":"minimax-h3-2k","prompt":"x","duration":8,"audio_reference":"https://cdn.test/a.wav"}`, "audio references require reference_images"},
		{"h3 audio max three", `{"model":"minimax-h3-2k","prompt":"x","duration":8,"referenceImages":["https://cdn.test/a.png"],"audio_references":["https://cdn.test/a.wav","https://cdn.test/a.wav","https://cdn.test/a.wav","https://cdn.test/a.wav"]}`, "audio references cannot exceed 3"},
		{"h3 audio needs reference images not generic image", `{"model":"minimax-h3-2k","prompt":"x","duration":8,"audio_reference":"https://cdn.test/a.wav","image_url":"https://cdn.test/a.png"}`, "audio references require reference_images"},
		{"h3 audio needs reference images not generic images", `{"model":"minimax-h3-2k","prompt":"x","duration":8,"audio_reference":"https://cdn.test/a.wav","images":["https://cdn.test/a.png"]}`, "audio references require reference_images"},
		{"h3 rejects video", `{"model":"minimax-h3-2k","prompt":"x","duration":8,"video":"https://cdn.test/in.mp4"}`, "video references are not supported"},
		{"h3 rejects videos", `{"model":"minimax-h3-2k","prompt":"x","duration":8,"videos":["https://cdn.test/in.mp4"]}`, "video references are not supported"},
		{"h3 rejects video url", `{"model":"minimax-h3-2k","prompt":"x","duration":8,"video_url":"https://cdn.test/in.mp4"}`, "video references are not supported"},
		{"h3 rejects camel video url", `{"model":"minimax-h3-2k","prompt":"x","duration":8,"videoUrl":"https://cdn.test/in.mp4"}`, "video references are not supported"},
		{"h3 rejects video urls", `{"model":"minimax-h3-2k","prompt":"x","duration":8,"video_urls":["https://cdn.test/in.mp4"]}`, "video references are not supported"},
		{"h3 rejects camel video urls", `{"model":"minimax-h3-2k","prompt":"x","duration":8,"videoUrls":["https://cdn.test/in.mp4"]}`, "video references are not supported"},
		{"h3 rejects reference video url", `{"model":"minimax-h3-2k","prompt":"x","duration":8,"reference_video_url":"https://cdn.test/in.mp4"}`, "video references are not supported"},
		{"h3 rejects reference video urls", `{"model":"minimax-h3-2k","prompt":"x","duration":8,"reference_video_urls":["https://cdn.test/in.mp4"]}`, "video references are not supported"},
		{"h3 rejects reference videos", `{"model":"minimax-h3-2k","prompt":"x","duration":8,"referenceVideoUrls":["https://cdn.test/in.mp4"]}`, "video references are not supported"},
		{"h3 rejects input video", `{"model":"minimax-h3-2k","prompt":"x","duration":8,"input_video":"https://cdn.test/in.mp4"}`, "input_video is not supported"},
		{"h3 rejects camel input video", `{"model":"minimax-h3-2k","prompt":"x","duration":8,"inputVideo":"https://cdn.test/in.mp4"}`, "input_video is not supported"},
		{"hailuo 03 rejects input video", `{"model":"hailuo-03-fast","prompt":"x","duration":8,"input_video":"https://cdn.test/in.mp4"}`, "input_video is not supported"},
		{"omni rejects reference audio", `{"model":"gemini-omni-flash","prompt":"x","duration":5,"referenceAudioUrl":"https://cdn.test/a.wav"}`, "audio is not supported"},
		{"grok single image required", `{"model":"grok-imagine-video-1.5","prompt":"x","seconds":6}`, "requires one image"},
		{"grok duplicate images count", `{"model":"grok-imagine-video-1.5","prompt":"x","seconds":6,"image_urls":["https://cdn.test/a.png","https://cdn.test/a.png"]}`, "requires one image"},
		{"grok rejects multiple images", `{"model":"grok-imagine-video-1.5","prompt":"x","seconds":6,"image_urls":["https://cdn.test/a.png","https://cdn.test/b.png"]}`, "requires one image"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateVideoCompatCreateBody([]byte(tt.body))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateVideoCompatPayloadUsesMappedUpstreamModelConstraints(t *testing.T) {
	tests := []struct {
		name           string
		requestedModel string
		upstreamModel  string
		body           string
		want           string
	}{
		{
			name:           "kling o3 alias",
			requestedModel: "public-kling",
			upstreamModel:  "kling-o3",
			body:           `{"model":"public-kling","prompt":"x","duration":5,"end_frame":"https://cdn.test/end.png"}`,
			want:           "end_frame requires start_frame",
		},
		{
			name:           "omni alias",
			requestedModel: "public-omni",
			upstreamModel:  "gemini-omni-flash",
			body:           `{"model":"public-omni","prompt":"x","duration":5,"referenceAudioUrl":"https://cdn.test/a.wav"}`,
			want:           "audio is not supported",
		},
		{
			name:           "h3 alias",
			requestedModel: "public-h3",
			upstreamModel:  "minimax-h3-2k",
			body:           `{"model":"public-h3","prompt":"x","duration":8,"audio_reference":"https://cdn.test/a.wav"}`,
			want:           "audio references require reference_images",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := &Account{Credentials: map[string]any{"model_mapping": map[string]any{tt.requestedModel: tt.upstreamModel}}}
			err := validateVideoCompatCreateBody([]byte(tt.body), account.GetMappedModel(tt.requestedModel))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateVideoCompatPayloadAcceptsReviewedImageAliases(t *testing.T) {
	tests := []struct {
		name string
		body string
		ref  string
	}{
		{"grok referenceImages", `{"model":"grok-imagine-video-1.5","prompt":"x","seconds":6,"referenceImages":["https://cdn.test/a.png"]}`, "https://cdn.test/a.png"},
		{"grok imageUrls", `{"model":"grok-imagine-video-1.5","prompt":"x","seconds":6,"imageUrls":["https://cdn.test/a.png"]}`, "https://cdn.test/a.png"},
		{"grok referenceImage", `{"model":"grok-imagine-video-1.5","prompt":"x","seconds":6,"referenceImage":"https://cdn.test/a.png"}`, "https://cdn.test/a.png"},
		{"grok referenceImageUrl", `{"model":"grok-imagine-video-1.5","prompt":"x","seconds":6,"referenceImageUrl":"https://cdn.test/a.png"}`, "https://cdn.test/a.png"},
		{"kling input video with image_url", `{"model":"kling-o3","prompt":"x","duration":5,"input_video":"https://cdn.test/in.mp4","image_url":"https://cdn.test/a.png"}`, "https://cdn.test/a.png"},
		{"kling input video with image_reference", `{"model":"kling-o3","prompt":"x","duration":5,"input_video":"https://cdn.test/in.mp4","image_reference":"https://cdn.test/a.png"}`, "https://cdn.test/a.png"},
		{"kling input video with inputReference", `{"model":"kling-o3","prompt":"x","duration":5,"input_video":"https://cdn.test/in.mp4","inputReference":"https://cdn.test/a.png"}`, "https://cdn.test/a.png"},
		{"kling input video with four image refs", `{"model":"kling-o3","prompt":"x","duration":5,"input_video":"https://cdn.test/in.mp4","image_urls":["https://cdn.test/1.png","https://cdn.test/2.png","https://cdn.test/3.png","https://cdn.test/4.png"]}`, "https://cdn.test/4.png"},
		{"h3 audio with referenceImages", `{"model":"minimax-h3-2k","prompt":"x","duration":8,"audio_reference":"https://cdn.test/a.wav","referenceImages":["https://cdn.test/a.png"]}`, "https://cdn.test/a.png"},
		{"h3 audio with reference_image_urls", `{"model":"minimax-h3-2k","prompt":"x","duration":8,"audio_reference":"https://cdn.test/a.wav","reference_image_urls":["https://cdn.test/a.png"]}`, "https://cdn.test/a.png"},
		{"h3 audio with referenceImageUrls", `{"model":"minimax-h3-2k","prompt":"x","duration":8,"audio_reference":"https://cdn.test/a.wav","referenceImageUrls":["https://cdn.test/a.png"]}`, "https://cdn.test/a.png"},
		{"omni style mode referenceImages", `{"model":"gemini-omni-flash","prompt":"x","duration":5,"reference_mode":"style","referenceImages":["https://cdn.test/1.png","https://cdn.test/2.png","https://cdn.test/3.png","https://cdn.test/4.png"]}`, "https://cdn.test/4.png"},
		{"omni inputReference", `{"model":"gemini-omni-flash","prompt":"x","duration":5,"inputReference":"https://cdn.test/ref.png"}`, "https://cdn.test/ref.png"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := parseVideoCompatPayload([]byte(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			requireVideoCompatRef(t, p.ImageRefs, tt.ref)

			if err := validateVideoCompatCreateBody([]byte(tt.body)); err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
		})
	}
}

func requireVideoCompatRef(t *testing.T, got []string, want string) {
	t.Helper()
	for _, v := range got {
		if v == want {
			return
		}
	}
	t.Fatalf("%q not found in %v", want, got)
}
