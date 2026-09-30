//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestNormalizeVideoTaskMultipartBody(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "video-ds-2.0"))
	require.NoError(t, writer.WriteField("prompt", "city at sunset"))
	require.NoError(t, writer.WriteField("seconds", "6"))
	require.NoError(t, writer.WriteField("size", "1280x720"))
	require.NoError(t, writer.WriteField("resolution_name", "720p"))
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="input_reference[]"; filename="frame.png"`)
	header.Set("Content-Type", "image/png")
	part, err := writer.CreatePart(header)
	require.NoError(t, err)
	_, err = part.Write([]byte("PNG"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	normalized, contentType, err := NormalizeVideoTaskRequestBody(body.Bytes(), writer.FormDataContentType())
	require.NoError(t, err)
	require.Equal(t, "application/json", contentType)
	envelope, err := ParseVideoTaskCreateEnvelope(normalized)
	require.NoError(t, err)
	require.Equal(t, "video-ds-2.0", envelope.Model)
	require.Equal(t, "city at sunset", envelope.Prompt)
	require.Equal(t, "6", envelope.Metadata["seconds"])
	require.Equal(t, 1, envelope.Metadata["image_count"])
	require.Contains(t, string(normalized), "data:image/png;base64,UE5H")
}

type namedVideoAdapter struct {
	name string
	VideoTaskProvider
}

func (a *namedVideoAdapter) Name() string { return a.name }

func (a *namedVideoAdapter) Create(ctx context.Context, account *Account, body []byte, contentType string, upstreamModel string) (*VideoProviderCreateResult, error) {
	result, err := a.VideoTaskProvider.Create(ctx, account, body, contentType, upstreamModel)
	if result != nil {
		result.Metadata = stampVideoAdapterMetadata(result.Metadata, a.name)
	}
	return result, err
}

func (a *namedVideoAdapter) Fetch(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error) {
	result, err := a.VideoTaskProvider.Fetch(ctx, account, task)
	if result != nil {
		result.Metadata = stampVideoAdapterMetadata(result.Metadata, a.name)
	}
	return result, err
}

type fakeNamedVideoProvider struct {
	createResult *VideoProviderCreateResult
	fetchResult  *VideoProviderFetchResult
	createBody   []byte
	createCalls  int
}

func (p *fakeNamedVideoProvider) Create(_ context.Context, _ *Account, body []byte, _ string, _ string) (*VideoProviderCreateResult, error) {
	p.createCalls++
	p.createBody = append([]byte(nil), body...)
	return p.createResult, nil
}

func (p *fakeNamedVideoProvider) Fetch(context.Context, *Account, *VideoTask) (*VideoProviderFetchResult, error) {
	return p.fetchResult, nil
}

func (p *fakeNamedVideoProvider) Content(context.Context, *Account, *VideoTask, http.Header) (*VideoContentStream, error) {
	return nil, nil
}

type fakeRefreshVideoAdapter struct {
	*fakeNamedVideoProvider
	refreshCalls int
}

func (a *fakeRefreshVideoAdapter) Name() string { return VideoAdapterJimengOpenAIVideos }

func (a *fakeRefreshVideoAdapter) Refresh(ctx context.Context, account *Account, task *VideoTask) (*VideoProviderFetchResult, error) {
	a.refreshCalls++
	return &VideoProviderFetchResult{
		ProviderTaskID: task.ProviderTaskID,
		Status:         VideoTaskStatusCompleted,
		ProviderStatus: "completed",
		RawBody:        []byte(`{"id":"upstream_refresh","status":"completed"}`),
	}, nil
}

func TestParseVideoTaskCreateEnvelopeAcceptsSeedanceDurationBody(t *testing.T) {
	body := []byte(`{"model":"seedance-2.0-720p","prompt":"rain city","duration":8,"aspect_ratio":"16:9","resolution":"720p"}`)

	envelope, err := ParseVideoTaskCreateEnvelope(body)

	require.NoError(t, err)
	require.Equal(t, "seedance-2.0-720p", envelope.Model)
	require.Equal(t, "rain city", envelope.Prompt)
	require.NotEmpty(t, envelope.RequestHash)
	require.NotEmpty(t, envelope.PromptHash)
	require.Equal(t, body, envelope.RawBody)
	require.Equal(t, "8", envelope.Metadata["duration"])
	require.Equal(t, "16:9", envelope.Metadata["aspect_ratio"])
	require.Equal(t, "720p", envelope.Metadata["resolution"])
}

func TestParseVideoTaskCreateEnvelopeRejectsMissingPrompt(t *testing.T) {
	_, err := ParseVideoTaskCreateEnvelope([]byte(`{"model":"seedance-2.0-720p"}`))

	require.Error(t, err)
	require.Contains(t, err.Error(), "prompt is required")
}

func TestParseVideoTaskCreateEnvelopeRejectsNonObject(t *testing.T) {
	_, err := ParseVideoTaskCreateEnvelope([]byte(`null`))

	require.Error(t, err)
	require.Contains(t, err.Error(), "video create JSON body must be an object")
}

func TestResolveVideoAdapterNameDefaultsMissingToJimeng(t *testing.T) {
	name, err := resolveVideoAdapterName(&Account{Credentials: map[string]any{}}, nil)

	require.NoError(t, err)
	require.Equal(t, VideoAdapterJimengOpenAIVideos, name)
}

func TestResolveVideoAdapterNameAcceptsNewAPIVideoGenerations(t *testing.T) {
	name, err := resolveVideoAdapterName(&Account{Credentials: map[string]any{VideoAdapterMetadataKey: VideoAdapterNewAPIVideoGenerations}}, nil)

	require.NoError(t, err)
	require.Equal(t, VideoAdapterNewAPIVideoGenerations, name)
}

func TestResolveVideoAdapterNameAcceptsAutoVideoGenerations(t *testing.T) {
	name, err := resolveVideoAdapterName(&Account{Credentials: map[string]any{VideoAdapterMetadataKey: VideoAdapterAutoVideoGenerations}}, nil)

	require.NoError(t, err)
	require.Equal(t, VideoAdapterAutoVideoGenerations, name)
}

func TestResolveVideoAdapterNameRejectsUnknown(t *testing.T) {
	_, err := resolveVideoAdapterName(&Account{Credentials: map[string]any{"video_adapter": "not-real"}}, nil)

	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown video_adapter")
}

func TestResolveVideoAdapterNamePrefersTaskMetadata(t *testing.T) {
	name, err := resolveVideoAdapterName(
		&Account{Credentials: map[string]any{"video_adapter": VideoAdapterOpenAIVideosDuration}},
		&VideoTask{Metadata: map[string]any{"video_adapter": VideoAdapterSeedanceAPIV1}},
	)

	require.NoError(t, err)
	require.Equal(t, VideoAdapterSeedanceAPIV1, name)
}

func TestAccountVideoTaskProviderCancelUnsupportedReturnsTypedError(t *testing.T) {
	provider := &accountVideoTaskProvider{registry: videoAdapterRegistry{
		VideoAdapterJimengOpenAIVideos: &jimengOpenAIVideosAdapter{provider: &fakeNamedVideoProvider{}},
	}}
	account := &Account{Credentials: map[string]any{VideoAdapterMetadataKey: VideoAdapterJimengOpenAIVideos}}
	task := &VideoTask{PublicTaskID: "task_cancel", Metadata: map[string]any{VideoAdapterMetadataKey: VideoAdapterJimengOpenAIVideos}}

	result, err := provider.Cancel(context.Background(), account, task)

	require.Nil(t, result)
	require.ErrorIs(t, err, ErrVideoTaskActionUnsupported)
	require.Equal(t, http.StatusNotImplemented, infraerrors.Code(err))
	require.Equal(t, "VIDEO_TASK_ACTION_UNSUPPORTED", infraerrors.Reason(err))
}

func TestAccountVideoTaskProviderRefreshDelegatesToAdapter(t *testing.T) {
	adapter := &fakeRefreshVideoAdapter{fakeNamedVideoProvider: &fakeNamedVideoProvider{}}
	provider := &accountVideoTaskProvider{registry: videoAdapterRegistry{VideoAdapterJimengOpenAIVideos: adapter}}
	account := &Account{Credentials: map[string]any{VideoAdapterMetadataKey: VideoAdapterJimengOpenAIVideos}}
	task := &VideoTask{ProviderTaskID: "upstream_refresh", Metadata: map[string]any{VideoAdapterMetadataKey: VideoAdapterJimengOpenAIVideos}}

	result, err := provider.Refresh(context.Background(), account, task)

	require.NoError(t, err)
	require.Equal(t, 1, adapter.refreshCalls)
	require.Equal(t, VideoTaskStatusCompleted, result.Status)
}

func TestNamedVideoAdapterStampsCreateMetadata(t *testing.T) {
	provider := &fakeNamedVideoProvider{createResult: &VideoProviderCreateResult{}}
	adapter := &namedVideoAdapter{name: VideoAdapterSeedanceAPIV1, VideoTaskProvider: provider}

	result, err := adapter.Create(context.Background(), nil, nil, "", "")

	require.NoError(t, err)
	require.NotNil(t, result.Metadata)
	require.Equal(t, VideoAdapterSeedanceAPIV1, result.Metadata[VideoAdapterMetadataKey])
}

func TestNamedVideoAdapterStampsFetchMetadata(t *testing.T) {
	provider := &fakeNamedVideoProvider{fetchResult: &VideoProviderFetchResult{Metadata: map[string]any{"existing": "kept"}}}
	adapter := &namedVideoAdapter{name: VideoAdapterOpenAIVideosDuration, VideoTaskProvider: provider}

	result, err := adapter.Fetch(context.Background(), nil, &VideoTask{})

	require.NoError(t, err)
	require.Equal(t, "kept", result.Metadata["existing"])
	require.Equal(t, VideoAdapterOpenAIVideosDuration, result.Metadata[VideoAdapterMetadataKey])
}

func TestJimengOpenAIVideosAdapterVideoGenerationsRejectsUnsupportedFieldsOnCreate(t *testing.T) {
	provider := &fakeNamedVideoProvider{createResult: &VideoProviderCreateResult{Metadata: map[string]any{"existing": "kept"}}}
	adapter := &jimengOpenAIVideosAdapter{provider: provider}
	body := []byte(`{"model":"video-ds-2.0-fast","prompt":"city","duration":8,"aspect_ratio":"9:16","resolution":"720p","generate_audio":true,"generation_mode":"reference"}`)

	result, err := adapter.Create(withVideoTaskEndpoint(context.Background(), VideoTaskEndpointVideoGenerations), nil, body, "application/json", "video-ds-2.0-fast")

	require.Nil(t, result)
	require.ErrorContains(t, err, "resolution is not supported")
	require.Zero(t, provider.createCalls)
}

func TestAdaptJimengVideoGenerationsCompatBodyNormalizesDurationAndRatio(t *testing.T) {
	adapted, err := adaptJimengVideoGenerationsCompatBody([]byte(`{"model":"video-ds-2.0-fast","prompt":"city","duration":8,"ratio":"9:16"}`), true)

	require.NoError(t, err)
	require.JSONEq(t, `{"model":"video-ds-2.0-fast","prompt":"city","seconds":"8","aspect_ratio":"9:16"}`, string(adapted))
}

func TestVideoAdaptersNormalizeDurationSecondsAlias(t *testing.T) {
	for _, tt := range []struct {
		name string
		fn   func([]byte) ([]byte, error)
	}{
		{name: "seedance", fn: func(body []byte) ([]byte, error) { return seedanceCreateBody(body, "seedance-2.0") }},
		{name: "openai duration", fn: func(body []byte) ([]byte, error) { return openAIDurationCreateBody(body, "video-ds-2.0") }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			adapted, err := tt.fn([]byte(`{"model":"requested","prompt":"city","duration_seconds":8}`))

			require.NoError(t, err)
			require.JSONEq(t, `{"model":"`+map[string]string{"seedance": "seedance-2.0", "openai duration": "video-ds-2.0"}[tt.name]+`","prompt":"city","duration":8}`, string(adapted))
		})
	}
}

func TestJimengOpenAIVideosAdapterPreservesSeedance2VideoFields(t *testing.T) {
	provider := &fakeNamedVideoProvider{createResult: &VideoProviderCreateResult{}}
	adapter := &jimengOpenAIVideosAdapter{provider: provider}
	body := []byte(`{"model":"seedance2.0-413-mini","prompt":"classroom","duration":11,"aspect_ratio":"3:4","resolution":"480p","generate_audio":true,"generation_mode":"reference"}`)

	_, err := adapter.Create(withVideoTaskEndpoint(context.Background(), VideoTaskEndpointVideoGenerations), nil, body, "application/json", "seedance2.0-413-mini")

	require.NoError(t, err)
	require.JSONEq(t, `{"model":"seedance2.0-413-mini","prompt":"classroom","duration":11,"aspect_ratio":"3:4","resolution":"480p","generate_audio":true}`, string(provider.createBody))
}

func TestIsSeedance2VideoModelRecognizesUnhyphenatedVersions(t *testing.T) {
	for _, tt := range []struct {
		model string
		want  bool
	}{
		{model: "seedance2.0-413-mini", want: true},
		{model: "seedance2.5-480p", want: true},
		{model: "seedance2.5-720p", want: true},
		{model: "video-ds-2.0-fast", want: false},
		{model: "seedance-2.5", want: false},
	} {
		t.Run(tt.model, func(t *testing.T) {
			require.Equal(t, tt.want, isSeedance2VideoModel(tt.model))
		})
	}
}

func TestJimengOpenAIVideosAdapterVideoGenerationsConvertsDurationSecondsCompatibly(t *testing.T) {
	provider := &fakeNamedVideoProvider{createResult: &VideoProviderCreateResult{}}
	adapter := &jimengOpenAIVideosAdapter{provider: provider}
	body := []byte(`{"model":"video-ds-2.0-fast","prompt":"city","duration_seconds":8,"aspect_ratio":"9:16","seed":123}`)

	_, err := adapter.Create(withVideoTaskEndpoint(context.Background(), VideoTaskEndpointVideoGenerations), nil, body, "application/json", "video-ds-2.0-fast")

	require.NoError(t, err)
	require.JSONEq(t, `{"model":"video-ds-2.0-fast","prompt":"city","seconds":"8","aspect_ratio":"9:16"}`, string(provider.createBody))
}

func TestJimengOpenAIVideosAdapterVideoGenerationsNormalizesBeforeMappedModelReplacement(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/v1/videos", r.URL.Path)
		gotBody, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.JSONEq(t, `{"model":"upstream-video","prompt":"city","seconds":"8","aspect_ratio":"9:16"}`, string(gotBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"video_jimeng_mapped","status":"queued"}`)
	}))
	defer server.Close()

	adapter := &jimengOpenAIVideosAdapter{provider: NewOpenAICompatibleVideoProvider(server.Client())}
	account := &Account{Credentials: map[string]any{"base_url": server.URL, "api_key": "sk-video"}}
	body := []byte(`{"model":"video-ds-2.0-fast","prompt":"city","duration_seconds":8,"aspect_ratio":"9:16","seed":123}`)

	result, err := adapter.Create(withVideoTaskEndpoint(context.Background(), VideoTaskEndpointVideoGenerations), account, body, "application/json", "upstream-video")

	require.NoError(t, err)
	require.Equal(t, "video_jimeng_mapped", result.ProviderTaskID)
}

func TestJimengOpenAIVideosAdapterValidateCreateUsesMappedUpstreamModel(t *testing.T) {
	adapter := &jimengOpenAIVideosAdapter{provider: &fakeNamedVideoProvider{}}
	body := []byte(`{"model":"seedance-2.0","prompt":"city","duration":5,"aspect_ratio":"16:9"}`)

	err := adapter.ValidateCreate(withVideoTaskEndpoint(context.Background(), VideoTaskEndpointVideoGenerations), nil, body, "application/json", "video-ds-2.0")

	require.NoError(t, err)
}

func TestJimengOpenAIVideosAdapterEstimateReturnsLocalMetadata(t *testing.T) {
	adapter := &jimengOpenAIVideosAdapter{provider: &fakeNamedVideoProvider{}}
	body := []byte(`{"model":"seedance-2.0","prompt":"city","duration":5,"aspect_ratio":"16:9"}`)

	result, err := adapter.Estimate(withVideoTaskEndpoint(context.Background(), VideoTaskEndpointVideoGenerations), nil, body, "application/json", "video-ds-2.0")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.JSONEq(t, `{"object":"video.estimate","model":"seedance-2.0","upstream_model":"video-ds-2.0","adapter":"jimeng_openai_videos","endpoint":"video_generations","metadata":{"seconds":"5","aspect_ratio":"16:9"}}`, string(result.ResponseBody))
}

func TestJimengOpenAIVideosAdapterValidateCreateAllowsMappedNonJimengUpstreamModel(t *testing.T) {
	adapter := &jimengOpenAIVideosAdapter{provider: &fakeNamedVideoProvider{}}
	body := []byte(`{"model":"seedance-2.0","prompt":"city","duration":5,"aspect_ratio":"16:9"}`)

	err := adapter.ValidateCreate(withVideoTaskEndpoint(context.Background(), VideoTaskEndpointVideoGenerations), nil, body, "application/json", "seedance-2.0")

	require.NoError(t, err)
}

func TestJimengOpenAIVideosAdapterRejectsUnsupportedUnifiedField(t *testing.T) {
	adapter := &jimengOpenAIVideosAdapter{provider: &fakeNamedVideoProvider{}}

	err := adapter.ValidateCreate(
		withVideoTaskEndpoint(context.Background(), VideoTaskEndpointVideoGenerations),
		nil,
		[]byte(`{"model":"video-ds-2.0-fast","prompt":"city","return_last_frame":true}`),
		"application/json",
		"video-ds-2.0-fast",
	)

	require.Error(t, err)
	require.ErrorContains(t, err, "return_last_frame is not supported")
}

func TestJimengOpenAIVideosAdapterVideosPreservesDurationSeconds(t *testing.T) {
	provider := &fakeNamedVideoProvider{createResult: &VideoProviderCreateResult{}}
	adapter := &jimengOpenAIVideosAdapter{provider: provider}
	body := []byte(`{"model":"video-ds-2.0-fast","prompt":"city","duration_seconds":8,"aspect_ratio":"9:16"}`)

	_, err := adapter.Create(withVideoTaskEndpoint(context.Background(), VideoTaskEndpointVideos), nil, body, "application/json", "video-ds-2.0-fast")

	require.NoError(t, err)
	require.JSONEq(t, `{"model":"video-ds-2.0-fast","prompt":"city","duration_seconds":8,"aspect_ratio":"9:16"}`, string(provider.createBody))
}

func TestJimengOpenAIVideosAdapterVideoGenerationsRejectsInvalidDuration(t *testing.T) {
	provider := &fakeNamedVideoProvider{createResult: &VideoProviderCreateResult{}}
	adapter := &jimengOpenAIVideosAdapter{provider: provider}
	body := []byte(`{"model":"video-ds-2.0-fast","prompt":"city","duration":{"bad":true}}`)

	result, err := adapter.Create(withVideoTaskEndpoint(context.Background(), VideoTaskEndpointVideoGenerations), nil, body, "application/json", "video-ds-2.0-fast")

	require.Nil(t, result)
	require.Error(t, err)
	require.Contains(t, err.Error(), "duration must be a number or string")
	require.Zero(t, provider.createCalls)
}

func TestJimengOpenAIVideosAdapterInvalidJSONReturnsError(t *testing.T) {
	provider := &fakeNamedVideoProvider{createResult: &VideoProviderCreateResult{}}
	adapter := &jimengOpenAIVideosAdapter{provider: provider}

	result, err := adapter.Create(withVideoTaskEndpoint(context.Background(), VideoTaskEndpointVideoGenerations), nil, []byte(`{"model":`), "application/json", "video-ds-2.0-fast")

	require.Nil(t, result)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid jimeng OpenAI video create JSON")
	require.Zero(t, provider.createCalls)
}

func TestJimengOpenAIVideosAdapterStampsFetchMetadata(t *testing.T) {
	provider := &fakeNamedVideoProvider{fetchResult: &VideoProviderFetchResult{
		Metadata: map[string]any{"existing": "kept"},
		RawBody:  []byte(`{"status":"completed","data":{"url":"https://cdn.example/jimeng.mp4"}}`),
	}}
	adapter := &jimengOpenAIVideosAdapter{provider: provider}

	result, err := adapter.Fetch(context.Background(), nil, &VideoTask{})

	require.NoError(t, err)
	require.Equal(t, "kept", result.Metadata["existing"])
	require.Equal(t, "https://cdn.example/jimeng.mp4", result.Metadata["result_url"])
	require.Equal(t, VideoAdapterJimengOpenAIVideos, result.Metadata[VideoAdapterMetadataKey])
}
