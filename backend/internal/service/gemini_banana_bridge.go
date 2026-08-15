package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func IsGeminiBananaBridgeModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(model, "models/")))
	return strings.HasPrefix(model, "nano-banana2-") || strings.HasPrefix(model, "nano-banana-pro-")
}

func BuildOpenAIImagesBodyFromGeminiBanana(model string, body []byte) ([]byte, error) {
	model = strings.TrimSpace(strings.TrimPrefix(model, "models/"))
	if !IsGeminiBananaBridgeModel(model) {
		return nil, fmt.Errorf("unsupported gemini banana model: %s", model)
	}
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return nil, fmt.Errorf("invalid gemini request body")
	}
	prompt := geminiBananaPrompt(body)
	if prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}

	out := []byte(`{"model":"","prompt":"","response_format":"b64_json"}`)
	out, _ = sjson.SetBytes(out, "model", model)
	out, _ = sjson.SetBytes(out, "prompt", prompt)
	if size := geminiBananaImageSize(body); size != "" {
		out, _ = sjson.SetBytes(out, "size", size)
	}
	if images := geminiBananaInputImages(body); len(images) > 0 {
		out, _ = sjson.SetRawBytes(out, "images", []byte(`[]`))
		for _, imageURL := range images {
			item := []byte(`{"image_url":""}`)
			item, _ = sjson.SetBytes(item, "image_url", imageURL)
			out, _ = sjson.SetRawBytes(out, "images.-1", item)
		}
	}
	return out, nil
}

func BuildGeminiBananaResponseFromOpenAIImages(model string, body []byte) ([]byte, ClaudeUsage, int, error) {
	data := gjson.GetBytes(body, "data")
	if !data.IsArray() || len(data.Array()) == 0 {
		return nil, ClaudeUsage{}, 0, fmt.Errorf("upstream did not return image output")
	}
	parts := make([]map[string]any, 0, len(data.Array())*2)
	imageCount := 0
	for _, item := range data.Array() {
		b64 := strings.TrimSpace(item.Get("b64_json").String())
		mimeType := "image/" + strings.Trim(strings.TrimSpace(gjson.GetBytes(body, "output_format").String()), ".")
		if mimeType == "image/" {
			mimeType = "image/png"
		}
		if b64 == "" {
			url := strings.TrimSpace(item.Get("url").String())
			if strings.HasPrefix(url, "data:") {
				mimeType, b64 = splitImageDataURL(url)
			}
		}
		if b64 == "" {
			continue
		}
		imageCount++
		parts = append(parts, map[string]any{"inlineData": map[string]any{"mimeType": mimeType, "data": b64}})
		if text := strings.TrimSpace(item.Get("revised_prompt").String()); text != "" {
			parts = append(parts, map[string]any{"text": text})
		}
	}
	if len(parts) == 0 {
		return nil, ClaudeUsage{}, 0, fmt.Errorf("upstream did not return image output")
	}

	usage := openAIImagesJSONUsage(body)
	resp := map[string]any{
		"candidates": []map[string]any{{
			"content":      map[string]any{"role": "model", "parts": parts},
			"finishReason": "STOP",
			"index":        0,
		}},
		"modelVersion": strings.TrimSpace(model),
	}
	if usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.ImageOutputTokens > 0 || usage.CacheReadInputTokens > 0 || usage.CacheCreationInputTokens > 0 {
		resp["usageMetadata"] = geminiUsageMetadataFromClaudeUsage(usage)
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return nil, ClaudeUsage{}, 0, err
	}
	return out, usage, imageCount, nil
}

func geminiBananaPrompt(body []byte) string {
	texts := make([]string, 0, 4)
	gjson.GetBytes(body, "contents").ForEach(func(_, content gjson.Result) bool {
		content.Get("parts").ForEach(func(_, part gjson.Result) bool {
			if text := strings.TrimSpace(part.Get("text").String()); text != "" {
				texts = append(texts, text)
			}
			return true
		})
		return true
	})
	return strings.Join(texts, "\n")
}

func geminiBananaImageSize(body []byte) string {
	if size := strings.TrimSpace(gjson.GetBytes(body, "generationConfig.imageConfig.imageSize").String()); size != "" {
		return size
	}
	return strings.TrimSpace(gjson.GetBytes(body, "generationConfig.imageConfig.aspectRatio").String())
}

func geminiBananaInputImages(body []byte) []string {
	images := make([]string, 0, 2)
	gjson.GetBytes(body, "contents").ForEach(func(_, content gjson.Result) bool {
		content.Get("parts").ForEach(func(_, part gjson.Result) bool {
			if inline := part.Get("inlineData"); inline.Exists() {
				mimeType := strings.TrimSpace(inline.Get("mimeType").String())
				data := strings.TrimSpace(inline.Get("data").String())
				if mimeType != "" && data != "" {
					images = append(images, "data:"+mimeType+";base64,"+data)
				}
			}
			if fileData := part.Get("fileData"); fileData.Exists() {
				if uri := strings.TrimSpace(fileData.Get("fileUri").String()); uri != "" {
					images = append(images, uri)
				}
			}
			return true
		})
		return true
	})
	return images
}

func splitImageDataURL(url string) (string, string) {
	meta, data, ok := strings.Cut(strings.TrimSpace(url), ",")
	if !ok || data == "" || !strings.HasPrefix(meta, "data:") {
		return "image/png", ""
	}
	mimeType := strings.TrimPrefix(meta, "data:")
	if before, _, ok := strings.Cut(mimeType, ";"); ok {
		mimeType = before
	}
	if strings.TrimSpace(mimeType) == "" {
		mimeType = "image/png"
	}
	return mimeType, data
}

func openAIImagesJSONUsage(body []byte) ClaudeUsage {
	usage := gjson.GetBytes(body, "usage")
	if !usage.Exists() {
		return ClaudeUsage{}
	}
	return ClaudeUsage{
		InputTokens:              int(usage.Get("input_tokens").Int()),
		OutputTokens:             int(usage.Get("output_tokens").Int()),
		CacheCreationInputTokens: int(usage.Get("cache_creation_input_tokens").Int()),
		CacheReadInputTokens:     int(usage.Get("cache_read_input_tokens").Int()),
		ImageOutputTokens:        int(usage.Get("output_tokens_details.image_tokens").Int()),
	}
}

func geminiUsageMetadataFromClaudeUsage(usage ClaudeUsage) map[string]any {
	out := map[string]any{
		"promptTokenCount":     usage.InputTokens,
		"candidatesTokenCount": usage.OutputTokens,
		"totalTokenCount":      usage.InputTokens + usage.OutputTokens,
	}
	if usage.CacheReadInputTokens > 0 {
		out["cachedContentTokenCount"] = usage.CacheReadInputTokens
	}
	if usage.ImageOutputTokens > 0 {
		out["candidatesTokensDetails"] = []map[string]any{{"modality": "IMAGE", "tokenCount": usage.ImageOutputTokens}}
	}
	return out
}
