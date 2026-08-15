package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

type videoCompatPayload struct {
	Raw                 map[string]json.RawMessage
	Model               string
	Prompt              string
	Duration            string
	Aspect              string
	Resolution          string
	Size                string
	ImageRefs           []string
	ImageRefCount       int
	BaseImageRefCount   int
	ReferenceImageRefs  []string
	ReferenceImageCount int
	StyleRefCount       int
	ElementRefCount     int
	VideoRefs           []string
	VideoRefCount       int
	AudioRefs           []string
	AudioRefCount       int
	StartFrame          string
	StartFrameCount     int
	EndFrame            string
	EndFrameCount       int
	InputVideo          string
	InputVideoCount     int
	ReferenceMode       string
}

func parseVideoCompatPayload(body []byte) (*videoCompatPayload, error) {
	var raw map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, errors.New("video create JSON body must be an object")
	}

	baseImageRefs := collectVideoCompatRefs(raw, "image", "images", "images_base64", "imagesBase64", "image_url", "image_urls", "imageUrls", "image_reference", "reference_image", "referenceImage", "reference_image_url", "referenceImageUrl", "input_reference", "inputReference")
	referenceImageRefs := collectVideoCompatRefs(raw, "reference_images", "referenceImages", "reference_image_urls", "referenceImageUrls")
	styleRefs := collectVideoCompatRefs(raw, "style_references", "styleReferences")
	elementRefs := collectVideoCompatRefs(raw, "element_references", "elementReferences")
	imageRefs := collectVideoCompatRefs(raw, "image", "images", "images_base64", "imagesBase64", "image_url", "image_urls", "imageUrls", "image_reference", "reference_image", "referenceImage", "reference_images", "referenceImages", "reference_image_url", "referenceImageUrl", "reference_image_urls", "referenceImageUrls", "first_frame_image", "style_references", "styleReferences", "element_references", "elementReferences", "input_reference", "inputReference")
	videoRefs := collectVideoCompatRefs(raw, "video", "videos", "video_url", "videoUrl", "video_urls", "videoUrls", "reference_video", "referenceVideo", "reference_videos", "referenceVideos", "reference_video_url", "referenceVideoUrl", "reference_video_urls", "referenceVideoUrls")
	audioRefs := collectVideoCompatRefs(raw, "audio_reference", "audioReference", "audio_references", "audioReferences", "audio_url", "audioUrl", "audio_urls", "audioUrls", "reference_audio", "referenceAudio", "reference_audios", "referenceAudios", "reference_audio_url", "referenceAudioUrl", "reference_audio_urls", "referenceAudioUrls", "audio", "audios", "audios_base64", "audiosBase64")
	startFrames := collectVideoCompatRefs(raw, "start_frame")
	endFrames := collectVideoCompatRefs(raw, "end_frame")
	inputVideos := collectVideoCompatRefs(raw, "input_video", "inputVideo")

	return &videoCompatPayload{
		Raw:                 raw,
		Model:               rawStringField(raw, "model"),
		Prompt:              rawStringField(raw, "prompt"),
		Duration:            firstVideoCompatString(raw, "duration", "seconds", "duration_seconds"),
		Aspect:              firstVideoCompatString(raw, "aspect_ratio", "ratio"),
		Resolution:          firstVideoCompatString(raw, "resolution"),
		Size:                firstVideoCompatString(raw, "size"),
		ImageRefs:           imageRefs.refs,
		ImageRefCount:       imageRefs.count,
		BaseImageRefCount:   baseImageRefs.count,
		ReferenceImageRefs:  referenceImageRefs.refs,
		ReferenceImageCount: referenceImageRefs.count,
		StyleRefCount:       styleRefs.count,
		ElementRefCount:     elementRefs.count,
		VideoRefs:           videoRefs.refs,
		VideoRefCount:       videoRefs.count,
		AudioRefs:           audioRefs.refs,
		AudioRefCount:       audioRefs.count,
		StartFrame:          firstVideoCompatRef(startFrames.refs),
		StartFrameCount:     startFrames.count,
		EndFrame:            firstVideoCompatRef(endFrames.refs),
		EndFrameCount:       endFrames.count,
		InputVideo:          firstVideoCompatRef(inputVideos.refs),
		InputVideoCount:     inputVideos.count,
		ReferenceMode:       firstVideoCompatString(raw, "reference_mode"),
	}, nil
}

type videoCompatRefs struct {
	refs  []string
	count int
}

func firstVideoCompatString(raw map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		if v := rawStringField(raw, key); v != "" {
			return v
		}
		if b := raw[key]; len(b) > 0 {
			var n json.Number
			dec := json.NewDecoder(strings.NewReader(string(b)))
			dec.UseNumber()
			if err := dec.Decode(&n); err == nil {
				return n.String()
			}
		}
	}
	return ""
}

func firstVideoCompatRef(refs []string) string {
	if len(refs) == 0 {
		return ""
	}
	return refs[0]
}

func collectVideoCompatRefs(raw map[string]json.RawMessage, keys ...string) videoCompatRefs {
	seen := map[string]struct{}{}
	refs := []string{}
	add := func(v string) bool {
		v = strings.TrimSpace(v)
		if v == "" {
			return false
		}
		if _, ok := seen[v]; !ok {
			seen[v] = struct{}{}
			refs = append(refs, v)
		}
		return true
	}
	addMap := func(v map[string]any) bool {
		for _, nested := range []string{"url", "image_url", "video_url", "audio_url"} {
			if sv, ok := v[nested].(string); ok && add(sv) {
				return true
			}
		}
		return false
	}

	count := 0
	for _, key := range keys {
		b := raw[key]
		if len(b) == 0 {
			continue
		}
		var s string
		if err := json.Unmarshal(b, &s); err == nil {
			if add(s) {
				count++
			}
			continue
		}
		var arr []any
		if err := json.Unmarshal(b, &arr); err == nil {
			for _, item := range arr {
				switch v := item.(type) {
				case string:
					if add(v) {
						count++
					}
				case map[string]any:
					if addMap(v) {
						count++
					}
				}
			}
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(b, &obj); err == nil && addMap(obj) {
			count++
		}
	}
	return videoCompatRefs{refs: refs, count: count}
}

func validateVideoCompatCreateBody(body []byte, upstreamModel ...string) error {
	p, err := parseVideoCompatPayload(body)
	if err != nil {
		return invalidVideoTaskRequest("invalid video create JSON: %v", err)
	}

	model := strings.ToLower(strings.TrimSpace(p.Model))
	if len(upstreamModel) > 0 {
		if mapped := strings.ToLower(strings.TrimSpace(upstreamModel[0])); mapped != "" {
			model = mapped
		}
	}
	switch {
	case strings.Contains(model, "omni"):
		if p.AudioRefCount > 0 || hasVideoCompatField(p.Raw, "audio", "audios", "audio_url", "audioUrl", "audio_urls", "audioUrls", "audio_reference", "audioReference", "audio_references", "audioReferences", "reference_audio", "referenceAudio", "reference_audios", "referenceAudios", "reference_audio_url", "referenceAudioUrl", "reference_audio_urls", "referenceAudioUrls", "generate_audio") {
			return invalidVideoTaskRequest("audio is not supported by %s", p.Model)
		}
		if p.BaseImageRefCount > 1 {
			return invalidVideoTaskRequest("images cannot exceed 1")
		}
		styleRefCount := p.StyleRefCount
		elementRefCount := p.ElementRefCount
		switch strings.ToLower(strings.TrimSpace(p.ReferenceMode)) {
		case "style":
			styleRefCount += p.ReferenceImageCount
		case "frame":
			if p.ReferenceImageCount > 1 {
				return invalidVideoTaskRequest("frame references cannot exceed 1")
			}
		default:
			if p.ReferenceImageCount > 3 {
				return invalidVideoTaskRequest("reference_images cannot exceed 3")
			}
			elementRefCount += p.ReferenceImageCount
		}
		if elementRefCount > 3 {
			return invalidVideoTaskRequest("element references cannot exceed 3")
		}
		if styleRefCount > 4 {
			return invalidVideoTaskRequest("style references cannot exceed 4")
		}
		if p.InputVideoCount > 1 {
			return invalidVideoTaskRequest("input_video cannot exceed 1")
		}
		if p.InputVideoCount+p.VideoRefCount > 1 {
			return invalidVideoTaskRequest("source video cannot exceed 1")
		}
		if p.BaseImageRefCount+p.ReferenceImageCount+p.ElementRefCount+p.StyleRefCount+p.InputVideoCount+p.VideoRefCount > 4 {
			return invalidVideoTaskRequest("total references cannot exceed 4")
		}
	case strings.HasPrefix(model, "kling-v3"):
		if err := validateKlingFrameRefs(p); err != nil {
			return err
		}
		if p.ImageRefCount > 0 {
			return invalidVideoTaskRequest("image references are not supported by %s", p.Model)
		}
		if p.AudioRefCount > 0 {
			return invalidVideoTaskRequest("audio is not supported by %s", p.Model)
		}
		if p.InputVideoCount > 0 {
			return invalidVideoTaskRequest("input_video is not supported by %s", p.Model)
		}
		if p.VideoRefCount > 0 {
			return invalidVideoTaskRequest("video references are not supported by %s", p.Model)
		}
	case strings.HasPrefix(model, "kling-o3"):
		if p.AudioRefCount > 0 {
			return invalidVideoTaskRequest("audio is not supported by %s", p.Model)
		}
		if err := validateKlingFrameRefs(p); err != nil {
			return err
		}
		frameMode := p.StartFrameCount > 0 || p.EndFrameCount > 0
		sourceVideos := p.VideoRefCount + p.InputVideoCount
		if frameMode && p.ImageRefCount > 0 {
			return invalidVideoTaskRequest("frame references cannot be combined with image references")
		}
		if frameMode && sourceVideos > 0 {
			return invalidVideoTaskRequest("frame references cannot be combined with source video")
		}
		if sourceVideos > 1 {
			return invalidVideoTaskRequest("source video cannot exceed 1")
		}
		if sourceVideos > 0 && klingSourceVideoUses4K(p) {
			return invalidVideoTaskRequest("source video cannot use 4K")
		}
		if p.ImageRefCount > 7 {
			return invalidVideoTaskRequest("image references cannot exceed 7")
		}
		if sourceVideos > 0 && p.ImageRefCount > 4 {
			return invalidVideoTaskRequest("image references cannot exceed 4 when source video is used")
		}
	case strings.Contains(model, "h3") || strings.Contains(model, "hailuo-03"):
		if p.ReferenceImageCount > 5 {
			return invalidVideoTaskRequest("reference_images cannot exceed 5")
		}
		if p.AudioRefCount > 3 {
			return invalidVideoTaskRequest("audio references cannot exceed 3")
		}
		if p.ReferenceImageCount > 0 && (p.StartFrame != "" || p.EndFrame != "") {
			return invalidVideoTaskRequest("reference_images cannot be combined with start_frame or end_frame")
		}
		if p.AudioRefCount > 0 && p.ReferenceImageCount == 0 {
			return invalidVideoTaskRequest("audio references require reference_images")
		}
		if p.VideoRefCount > 0 {
			return invalidVideoTaskRequest("video references are not supported by %s", p.Model)
		}
		if p.InputVideoCount > 0 {
			return invalidVideoTaskRequest("input_video is not supported by %s", p.Model)
		}
	case model == "grok-imagine-video-1.5":
		if p.ImageRefCount != 1 {
			return invalidVideoTaskRequest("grok-imagine-video-1.5 requires one image")
		}
	}
	return nil
}

func klingSourceVideoUses4K(p *videoCompatPayload) bool {
	resolution := strings.ToLower(strings.TrimSpace(p.Resolution))
	size := strings.ToLower(strings.TrimSpace(p.Size))
	return strings.Contains(resolution, "2160") || strings.Contains(resolution, "4k") || strings.Contains(size, "2160") || strings.Contains(size, "3840") || strings.Contains(size, "4k")
}

func validateKlingFrameRefs(p *videoCompatPayload) error {
	if p.StartFrameCount > 1 {
		return invalidVideoTaskRequest("start_frame cannot exceed 1")
	}
	if p.EndFrameCount > 1 {
		return invalidVideoTaskRequest("end_frame cannot exceed 1")
	}
	if p.EndFrameCount > 0 && p.StartFrameCount == 0 {
		return invalidVideoTaskRequest("end_frame requires start_frame")
	}
	return nil
}

func hasVideoCompatField(raw map[string]json.RawMessage, keys ...string) bool {
	for _, key := range keys {
		if len(raw[key]) > 0 {
			return true
		}
	}
	return false
}
