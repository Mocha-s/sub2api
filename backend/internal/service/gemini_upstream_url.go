package service

import (
	"errors"
	"fmt"
	"strings"
)

// geminiAIStudioActions 是允许出现在上游 URL 里的 action 集合，与 ForwardNative
// 的入站校验保持一致，同时避免 compat 侧把可变字符串直接拼进 path。
var geminiAIStudioActions = map[string]struct{}{
	"generateContent":       {},
	"streamGenerateContent": {},
	"countTokens":           {},
}

// NormalizeGeminiModelID converts a Gemini models-list resource name to the
// bare model ID used in /v1beta/models/{model}:{action} paths.
func NormalizeGeminiModelID(model string) string {
	return strings.TrimPrefix(strings.TrimSpace(model), "models/")
}

// buildGeminiAIStudioModelActionURL 组装 AI Studio 的
// /v1beta/models/{model}:{action} 上游 URL。
//
// model 是客户端可控的（native 路由取自 URL 片段，compat 路由取自请求体的 model
// 字段，之后可能再经渠道映射），因此必须先过路径片段护栏才能拼进 path，
// 见 upstream_path_guard.go。新增 AI Studio 端点请一律走本函数。
func buildGeminiAIStudioModelActionURL(baseURL, model, action string, stream bool) (string, error) {
	trimmedBase := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmedBase == "" {
		return "", errors.New("gemini base url is required")
	}
	trimmedModel := NormalizeGeminiModelID(model)
	if trimmedModel == "" {
		return "", errors.New("gemini model is required")
	}
	if err := validateGeminiModelPathSegment(trimmedModel); err != nil {
		return "", err
	}
	trimmedAction := strings.TrimSpace(action)
	if _, ok := geminiAIStudioActions[trimmedAction]; !ok {
		return "", fmt.Errorf("unsupported gemini action: %s", trimmedAction)
	}

	fullURL := fmt.Sprintf("%s/v1beta/models/%s:%s", trimmedBase, trimmedModel, trimmedAction)
	if stream {
		fullURL += "?alt=sse"
	}
	return fullURL, nil
}

// IsSafeGeminiModelPathSegment 供 handler 层在解析出 URL 里的模型名后立刻校验，
// 让客户端拿到明确的 400，而不是等到构造上游请求时才报错。
func IsSafeGeminiModelPathSegment(model string) bool {
	return isSafeGeminiModelPathSegment(strings.TrimSpace(model))
}

func validateGeminiModelPathSegment(model string) error {
	if !isSafeGeminiModelPathSegment(strings.TrimSpace(model)) {
		return errors.New("invalid gemini model for upstream url path")
	}
	return nil
}

// isSafeGeminiModelPathSegment permits a provider-specific route tag such as
// [yu] before an otherwise safe model ID. The tag is intentionally narrow so
// brackets cannot introduce path, query, or fragment syntax.
func isSafeGeminiModelPathSegment(model string) bool {
	if isSafeUpstreamPathSegment(model) {
		return true
	}
	if len(model) > maxUpstreamPathSegmentLen || !strings.HasPrefix(model, "[") {
		return false
	}
	close := strings.IndexByte(model, ']')
	if close <= 1 || close >= len(model)-1 {
		return false
	}
	for i := 1; i < close; i++ {
		b := model[i]
		if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '-') {
			return false
		}
	}
	return isSafeUpstreamPathSegment(model[close+1:])
}
