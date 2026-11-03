package service

import (
	"bytes"
	"encoding/json"
	"strings"
)

// The cangyuan_reference_urls adapter targets relays that document their reference
// fields under the cangyuan names (reference_image_urls / first_image_url /
// last_image_url / reference_videos / reference_audios) instead of the unified
// image(s)/video(s)/audio(s) set. Create/Fetch/Content stay identical to
// openai_videos_duration; only the create body is renamed.
func NewCangyuanReferenceURLsVideoAdapter(openai *OpenAIGatewayService) VideoTaskAdapter {
	return newOpenAIVideosDurationAdapter(openai, VideoAdapterCangyuanReferenceURLs, cangyuanReferenceCreateBody)
}

// cangyuanReferenceCreateBody starts from the openai_videos_duration body and
// renames reference fields to the cangyuan contract, splitting role-tagged
// first/last frames out of `images`.
// shortcut: data URIs pass through although cangyuan documents HTTPS URLs only; upgrade if clients start uploading files.
func cangyuanReferenceCreateBody(body []byte, upstreamModel string) ([]byte, error) {
	base, err := openAIDurationCreateBody(body, upstreamModel)
	if err != nil {
		return nil, err
	}
	payload, err := openAIDurationRawPayload(body)
	if err != nil {
		return nil, err
	}

	first := firstReferenceURL(payload, "first_image_url", "first_frame_image", "first_frame", "start_frame")
	last := firstReferenceURL(payload, "last_image_url", "last_frame_image", "last_frame", "end_frame")
	if first == "" {
		// A lone image alias is the frame to animate under the unified contract.
		first = firstReferenceURL(payload, "image", "image_url", "input_reference")
	}

	var references, videos, audios []string
	for _, key := range []string{"images", "reference_images", "reference_image_urls", "image_urls", "image_reference"} {
		urls, frameFirst, frameLast := splitReferenceFrames(payload[key])
		references = append(references, urls...)
		if first == "" {
			first = frameFirst
		}
		if last == "" {
			last = frameLast
		}
	}
	for _, key := range []string{"videos", "reference_videos", "video", "video_url", "input_video"} {
		videos = append(videos, referenceURLList(payload[key])...)
	}
	for _, key := range []string{"audios", "reference_audios", "audio", "audio_url"} {
		audios = append(audios, referenceURLList(payload[key])...)
	}

	out := map[string]json.RawMessage{}
	if err := json.Unmarshal(base, &out); err != nil {
		return nil, err
	}
	for _, key := range []string{"images", "videos", "audios"} {
		delete(out, key)
	}
	renamed := map[string][]string{
		"reference_image_urls": references,
		"reference_videos":     videos,
		"reference_audios":     audios,
	}
	for key, values := range renamed {
		if len(values) == 0 {
			continue
		}
		encoded, err := json.Marshal(values)
		if err != nil {
			return nil, err
		}
		out[key] = encoded
	}
	for key, value := range map[string]string{"first_image_url": first, "last_image_url": last} {
		if value == "" {
			continue
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		out[key] = encoded
	}
	return json.Marshal(out)
}

func firstReferenceURL(payload map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		if urls := referenceURLList(payload[key]); len(urls) > 0 {
			return urls[0]
		}
	}
	return ""
}

// referenceURLList accepts a bare URL string, a {url: ...} object or an array of either.
func referenceURLList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil
	}
	switch v := value.(type) {
	case string:
		if url := strings.TrimSpace(v); url != "" {
			return []string{url}
		}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			out = append(out, referenceURLValue(item)...)
		}
		return out
	case map[string]any:
		return referenceURLValue(v)
	}
	return nil
}

func referenceURLValue(value any) []string {
	switch v := value.(type) {
	case string:
		if url := strings.TrimSpace(v); url != "" {
			return []string{url}
		}
	case map[string]any:
		for _, key := range []string{"url", "image_url", "video_url", "audio_url", "file_url", "uri"} {
			if url, ok := v[key].(string); ok {
				if url = strings.TrimSpace(url); url != "" {
					return []string{url}
				}
			}
		}
	}
	return nil
}

// splitReferenceFrames separates role-tagged first/last frames from plain reference images.
func splitReferenceFrames(raw json.RawMessage) (references []string, first string, last string) {
	if len(raw) == 0 {
		return nil, "", ""
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, "", ""
	}
	items, ok := value.([]any)
	if !ok {
		return referenceURLList(raw), "", ""
	}
	for _, item := range items {
		urls := referenceURLValue(item)
		if len(urls) == 0 {
			continue
		}
		role := ""
		if object, ok := item.(map[string]any); ok {
			role, _ = object["role"].(string)
		}
		switch frameRole(role) {
		case "first":
			if first == "" {
				first = urls[0]
				continue
			}
		case "last":
			if last == "" {
				last = urls[0]
				continue
			}
		}
		references = append(references, urls...)
	}
	return references, first, last
}

func frameRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "first_frame", "first", "start_frame", "start":
		return "first"
	case "last_frame", "last", "end_frame", "end":
		return "last"
	default:
		return ""
	}
}
