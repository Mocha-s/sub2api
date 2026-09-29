package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// BuildOpenAIImagesRequestFromGeminiBanana chooses the upstream Images endpoint
// and body format. Inline Gemini references become multipart edit uploads because
// common OpenAI-compatible image providers ignore JSON data-URL edit inputs.
func BuildOpenAIImagesRequestFromGeminiBanana(model string, body []byte) ([]byte, string, string, error) {
	converted, err := BuildOpenAIImagesBodyFromGeminiBanana(model, body)
	if err != nil {
		return nil, "", "", err
	}
	inlineImages := geminiBananaInlineImages(body)
	if len(inlineImages) == 0 {
		return converted, "application/json", "/v1/images/generations", nil
	}

	var output bytes.Buffer
	writer := multipart.NewWriter(&output)
	for _, field := range []string{"model", "prompt", "size", "aspect_ratio"} {
		if value := strings.TrimSpace(gjson.GetBytes(converted, field).String()); value != "" {
			if err := writer.WriteField(field, value); err != nil {
				return nil, "", "", fmt.Errorf("write image field %s: %w", field, err)
			}
		}
	}
	for index, image := range inlineImages {
		data, err := decodeGeminiBananaImageData(image.data)
		if err != nil {
			return nil, "", "", fmt.Errorf("decode reference image %d: %w", index+1, err)
		}
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="image"; filename="reference-%d"`, index+1))
		header.Set("Content-Type", image.mimeType)
		part, err := writer.CreatePart(header)
		if err != nil {
			return nil, "", "", fmt.Errorf("create reference image %d: %w", index+1, err)
		}
		if _, err := part.Write(data); err != nil {
			return nil, "", "", fmt.Errorf("write reference image %d: %w", index+1, err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", "", fmt.Errorf("finalize reference image upload: %w", err)
	}
	return output.Bytes(), writer.FormDataContentType(), "/v1/images/edits", nil
}

type geminiBananaInlineImage struct {
	mimeType string
	data     string
}

func geminiBananaInlineImages(body []byte) []geminiBananaInlineImage {
	images := make([]geminiBananaInlineImage, 0, 2)
	gjson.GetBytes(body, "contents").ForEach(func(_, content gjson.Result) bool {
		content.Get("parts").ForEach(func(_, part gjson.Result) bool {
			inline := part.Get("inlineData")
			if !inline.Exists() {
				inline = part.Get("inline_data")
			}
			mimeType := strings.TrimSpace(inline.Get("mimeType").String())
			data := strings.TrimSpace(inline.Get("data").String())
			if strings.HasPrefix(strings.ToLower(mimeType), "image/") && data != "" {
				images = append(images, geminiBananaInlineImage{mimeType: mimeType, data: data})
			}
			return true
		})
		return true
	})
	return images
}

func decodeGeminiBananaImageData(data string) ([]byte, error) {
	data = strings.TrimSpace(data)
	if decoded, err := base64.StdEncoding.DecodeString(data); err == nil {
		return decoded, nil
	}
	return base64.RawStdEncoding.DecodeString(data)
}

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
	if aspectRatio := geminiBananaAspectRatio(body); aspectRatio != "" {
		out, _ = sjson.SetBytes(out, "aspect_ratio", aspectRatio)
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
	return BuildGeminiBananaResponseFromOpenAIImagesContext(context.Background(), model, body)
}

func BuildGeminiBananaResponseFromOpenAIImagesContext(ctx context.Context, model string, body []byte) ([]byte, ClaudeUsage, int, error) {
	return buildGeminiBananaResponseFromOpenAIImagesContext(ctx, model, body, nil)
}

func (s *OpenAIGatewayService) BuildGeminiBananaResponseFromOpenAIImages(ctx context.Context, account *Account, model string, body []byte) ([]byte, ClaudeUsage, int, error) {
	if s == nil || s.httpUpstream == nil {
		return nil, ClaudeUsage{}, 0, fmt.Errorf("safe upstream client is not configured")
	}
	return buildGeminiBananaResponseFromOpenAIImagesContext(ctx, model, body, func(fetchCtx context.Context, imageURL string) ([]byte, string, error) {
		return s.fetchGeminiBananaImage(fetchCtx, account, imageURL)
	})
}

type geminiBananaImageFetcher func(context.Context, string) ([]byte, string, error)

func buildGeminiBananaResponseFromOpenAIImagesContext(ctx context.Context, model string, body []byte, fetchImage geminiBananaImageFetcher) ([]byte, ClaudeUsage, int, error) {
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
			} else if strings.HasPrefix(strings.ToLower(url), "https://") {
				if fetchImage == nil {
					return nil, ClaudeUsage{}, 0, fmt.Errorf("safe upstream client is required to download image URLs")
				}
				data, fetchedMIME, err := fetchImage(ctx, url)
				if err != nil {
					return nil, ClaudeUsage{}, 0, fmt.Errorf("download upstream image: %w", err)
				}
				if fetchedMIME != "" {
					mimeType = fetchedMIME
				}
				b64 = base64.StdEncoding.EncodeToString(data)
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

func (s *OpenAIGatewayService) fetchGeminiBananaImage(ctx context.Context, account *Account, imageURL string) ([]byte, string, error) {
	u, err := parseGeminiBananaImageURL(imageURL)
	if err != nil {
		return nil, "", err
	}
	ctx = WithHTTPUpstreamPublicHostsOnly(ctx)
	ctx = WithHTTPRedirectValidator(ctx, func(target *url.URL) error {
		_, err := parseGeminiBananaImageURL(target.String())
		return err
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "image/*")
	proxyURL := ""
	accountID := int64(0)
	concurrency := 0
	if account != nil {
		accountID = account.ID
		concurrency = account.Concurrency
		if account.ProxyID != nil && account.Proxy != nil {
			proxyURL = account.Proxy.URL()
		}
	}
	resp, err := s.httpUpstream.Do(req, proxyURL, accountID, concurrency)
	if err != nil {
		return nil, "", err
	}
	return readGeminiBananaImageResponse(resp)
}

func parseGeminiBananaImageURL(raw string) (*url.URL, error) {
	u, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil || u == nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("invalid image URL")
	}
	return u, nil
}

func readGeminiBananaImageResponse(resp *http.Response) ([]byte, string, error) {
	if resp == nil {
		return nil, "", fmt.Errorf("empty image response")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, "", err
	}
	if len(data) == 20<<20 {
		return nil, "", fmt.Errorf("response exceeds 20 MiB")
	}
	mimeType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if strings.Contains(mimeType, ";") {
		mimeType = strings.TrimSpace(strings.SplitN(mimeType, ";", 2)[0])
	}
	if !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		mimeType = http.DetectContentType(data)
	}
	return data, mimeType, nil
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
	return strings.TrimSpace(gjson.GetBytes(body, "generationConfig.imageConfig.imageSize").String())
}

func geminiBananaAspectRatio(body []byte) string {
	return strings.TrimSpace(gjson.GetBytes(body, "generationConfig.imageConfig.aspectRatio").String())
}

func geminiBananaInputImages(body []byte) []string {
	images := make([]string, 0, 2)
	gjson.GetBytes(body, "contents").ForEach(func(_, content gjson.Result) bool {
		content.Get("parts").ForEach(func(_, part gjson.Result) bool {
			inline := part.Get("inlineData")
			if !inline.Exists() {
				inline = part.Get("inline_data")
			}
			if inline.Exists() {
				mimeType := strings.TrimSpace(inline.Get("mimeType").String())
				data := strings.TrimSpace(inline.Get("data").String())
				if mimeType != "" && data != "" {
					images = append(images, "data:"+mimeType+";base64,"+data)
				}
			}
			fileData := part.Get("fileData")
			if !fileData.Exists() {
				fileData = part.Get("file_data")
			}
			if fileData.Exists() {
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
