package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeGeminiModelID(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want string
	}{
		{name: "catalog name", in: " models/gemini-3.1-flash-image ", want: "gemini-3.1-flash-image"},
		{name: "bare name", in: "gemini-3.1-flash-image", want: "gemini-3.1-flash-image"},
		{name: "unsafe provider prefix remains unsafe", in: "google/gemini-3.1-flash-image", want: "google/gemini-3.1-flash-image"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeGeminiModelID(tt.in)
			require.Equal(t, tt.want, got)
			if strings.Contains(got, "/") {
				require.False(t, IsSafeGeminiModelPathSegment(got))
			}
		})
	}
}

func TestBuildGeminiAIStudioModelActionURL(t *testing.T) {
	const base = "https://generativelanguage.googleapis.com"

	got, err := buildGeminiAIStudioModelActionURL(base, "gemini-2.5-pro", "generateContent", false)
	require.NoError(t, err)
	require.Equal(t, base+"/v1beta/models/gemini-2.5-pro:generateContent", got)

	got, err = buildGeminiAIStudioModelActionURL(base+"/", " gemini-2.5-flash ", "streamGenerateContent", true)
	require.NoError(t, err)
	require.Equal(t, base+"/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse", got)

	got, err = buildGeminiAIStudioModelActionURL(base, "gemini-2.5-pro", "countTokens", false)
	require.NoError(t, err)
	require.Equal(t, base+"/v1beta/models/gemini-2.5-pro:countTokens", got)

	got, err = buildGeminiAIStudioModelActionURL(base, "models/gemini-3.1-flash-image", "generateContent", false)
	require.NoError(t, err)
	require.Equal(t, base+"/v1beta/models/gemini-3.1-flash-image:generateContent", got)

	got, err = buildGeminiAIStudioModelActionURL(base, "[yu]gemini-3.1-flash-image-preview", "generateContent", false)
	require.NoError(t, err)
	require.Equal(t, base+"/v1beta/models/[yu]gemini-3.1-flash-image-preview:generateContent", got)
}

// TestBuildGeminiAIStudioModelActionURLRejectsNonConformingModel 锁定不变式：
// 模型名来自客户端（native 路由的 URL 片段 / compat 路由的请求体），
// 只有合规的路径片段才允许拼进上游 URL。
func TestBuildGeminiAIStudioModelActionURLRejectsNonConformingModel(t *testing.T) {
	const base = "https://generativelanguage.googleapis.com"

	for _, model := range []string{
		"../../x/y",
		"..",
		".",
		"gemini-2.5-pro/../../x",
		`..\..\x`,
		"gemini-2.5-pro?a=b",
		"gemini-2.5-pro#frag",
		"gemini-2.5-pro%2f..",
		"gemini 2.5 pro",
		"gemini\x00pro",
		"gemini-2.5-pro@001",
		"gemini~pro",
		"models/models/gemini-2.5-pro",
		"...",
		"",
		"   ",
	} {
		t.Run("model_"+model, func(t *testing.T) {
			_, err := buildGeminiAIStudioModelActionURL(base, model, "generateContent", false)
			require.Error(t, err, "model %q must be rejected", model)
			require.False(t, IsSafeGeminiModelPathSegment(model))
		})
	}

	// action 只允许已知取值，避免未来把可变字符串拼进 path。
	_, err := buildGeminiAIStudioModelActionURL(base, "gemini-2.5-pro", "deleteModel", false)
	require.Error(t, err)

	_, err = buildGeminiAIStudioModelActionURL("", "gemini-2.5-pro", "generateContent", false)
	require.Error(t, err)
}

func TestGeminiModelRouteTagsAreStrictlyValidated(t *testing.T) {
	for _, model := range []string{
		"[yu]gemini-3.1-flash-image-preview",
		"[vendor_1]gemini-3.1-flash-image-preview",
	} {
		require.True(t, IsSafeGeminiModelPathSegment(model), model)
	}
	for _, model := range []string{
		"[]gemini-3.1-flash-image-preview",
		"[../]gemini-3.1-flash-image-preview",
		"[yu/vendor]gemini-3.1-flash-image-preview",
		"[yu]gemini-3.1-flash-image-preview/../x",
		"[yu]gemini-3.1-flash-image-preview?x=1",
	} {
		require.False(t, IsSafeGeminiModelPathSegment(model), model)
	}
}
