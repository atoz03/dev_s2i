package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type codexModelsHTTPUpstreamStub struct {
	do func(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error)
}

func (s *codexModelsHTTPUpstreamStub) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	return s.do(req, proxyURL, accountID, accountConcurrency)
}

func (s *codexModelsHTTPUpstreamStub) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return s.Do(req, proxyURL, accountID, accountConcurrency)
}

type codexModelsBlockingBody struct {
	ctx         context.Context
	readStarted chan struct{}
	startedOnce *sync.Once
	release     <-chan struct{}
	body        *strings.Reader
}

func (b *codexModelsBlockingBody) Read(p []byte) (int, error) {
	b.startedOnce.Do(func() { close(b.readStarted) })
	select {
	case <-b.release:
		return b.body.Read(p)
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	}
}

func (b *codexModelsBlockingBody) Close() error { return nil }

func newCodexModelsAPIKeyTestService(upstream HTTPUpstream) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
			Enabled: false,
		}}},
		httpUpstream: upstream,
	}
}

func newCodexModelsAPIKeyTestAccount(baseURL string) *Account {
	credentials := map[string]any{"api_key": "sk-upstream"}
	if baseURL != "" {
		credentials["base_url"] = baseURL
	}
	return &Account{
		ID:          2,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: credentials,
		Concurrency: 3,
	}
}

func newCodexModelsTestAccount() *Account {
	return &Account{
		ID:       1,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":       "test-access-token",
			"chatgpt_account_id": "acc-123",
		},
	}
}

func TestFetchCodexModelsManifestPassthrough(t *testing.T) {
	manifestBody := `{"models":[{"slug":"gpt-5.5","display_name":"GPT-5.5"}]}`

	var gotAuth, gotAccountID, gotOriginator, gotClientVersion string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccountID = r.Header.Get("chatgpt-account-id")
		gotOriginator = r.Header.Get("Originator")
		gotClientVersion = r.URL.Query().Get("client_version")
		w.Header().Set("ETag", `W/"abc123"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(manifestBody))
	}))
	defer server.Close()

	original := chatgptCodexModelsURL
	chatgptCodexModelsURL = server.URL
	defer func() { chatgptCodexModelsURL = original }()

	s := &OpenAIGatewayService{}
	manifest, err := s.FetchCodexModelsManifest(context.Background(), newCodexModelsTestAccount(), "0.137.0", "")
	if err != nil {
		t.Fatalf("FetchCodexModelsManifest returned error: %v", err)
	}

	if string(manifest.Body) != manifestBody {
		t.Errorf("body not passed through verbatim: got %q", manifest.Body)
	}
	if manifest.ETag != `W/"abc123"` {
		t.Errorf("etag not passed through: got %q", manifest.ETag)
	}
	if gotAuth != "Bearer test-access-token" {
		t.Errorf("authorization header: got %q", gotAuth)
	}
	if gotAccountID != "acc-123" {
		t.Errorf("chatgpt-account-id header: got %q", gotAccountID)
	}
	if gotOriginator != openai.CodexDefaultOriginator {
		t.Errorf("originator header: got %q", gotOriginator)
	}
	if gotClientVersion != "0.137.0" {
		t.Errorf("client_version query: got %q", gotClientVersion)
	}
}

func TestFetchCodexModelsManifestDefaultClientVersion(t *testing.T) {
	var gotClientVersion string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClientVersion = r.URL.Query().Get("client_version")
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer server.Close()

	original := chatgptCodexModelsURL
	chatgptCodexModelsURL = server.URL
	defer func() { chatgptCodexModelsURL = original }()

	s := &OpenAIGatewayService{}
	if _, err := s.FetchCodexModelsManifest(context.Background(), newCodexModelsTestAccount(), "", ""); err != nil {
		t.Fatalf("FetchCodexModelsManifest returned error: %v", err)
	}
	if gotClientVersion != openAICodexProbeVersion {
		t.Errorf("default client_version: got %q, want %q", gotClientVersion, openAICodexProbeVersion)
	}
}

func TestFetchCodexModelsManifestNotModified(t *testing.T) {
	var gotIfNoneMatch string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIfNoneMatch = r.Header.Get("If-None-Match")
		w.Header().Set("ETag", `W/"abc123"`)
		w.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()

	original := chatgptCodexModelsURL
	chatgptCodexModelsURL = server.URL
	defer func() { chatgptCodexModelsURL = original }()

	s := &OpenAIGatewayService{}
	manifest, err := s.FetchCodexModelsManifest(context.Background(), newCodexModelsTestAccount(), "0.137.0", `W/"abc123"`)
	if err != nil {
		t.Fatalf("FetchCodexModelsManifest returned error: %v", err)
	}
	if !manifest.NotModified {
		t.Error("expected NotModified to be true")
	}
	if gotIfNoneMatch != `W/"abc123"` {
		t.Errorf("if-none-match header: got %q", gotIfNoneMatch)
	}
}

func TestFetchCodexModelsManifestUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"detail":"boom"}`, http.StatusInternalServerError)
	}))
	defer server.Close()

	original := chatgptCodexModelsURL
	chatgptCodexModelsURL = server.URL
	defer func() { chatgptCodexModelsURL = original }()

	s := &OpenAIGatewayService{}
	if _, err := s.FetchCodexModelsManifest(context.Background(), newCodexModelsTestAccount(), "0.137.0", ""); err == nil {
		t.Fatal("expected error for upstream 500, got nil")
	}
}

func TestFetchCodexModelsManifestMissingToken(t *testing.T) {
	account := newCodexModelsTestAccount()
	delete(account.Credentials, "access_token")

	s := &OpenAIGatewayService{}
	if _, err := s.FetchCodexModelsManifest(context.Background(), account, "0.137.0", ""); err == nil {
		t.Fatal("expected error for missing access token, got nil")
	}
}

func TestFetchCodexModelsManifestOAuth401IsRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"token_revoked"}}`))
	}))
	defer server.Close()

	original := chatgptCodexModelsURL
	chatgptCodexModelsURL = server.URL
	defer func() { chatgptCodexModelsURL = original }()

	_, err := (&OpenAIGatewayService{}).FetchCodexModelsManifest(context.Background(), newCodexModelsTestAccount(), "0.137.0", "")
	require.Error(t, err)
	require.True(t, IsRetryableCodexModelsManifestError(err))
}

func TestFetchCodexModelsManifestAPIKeyConvertsStandardModelList(t *testing.T) {
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Etag": []string{`W/"upstream-list"`}},
		Body: io.NopCloser(strings.NewReader(
			`{"object":"list","data":[{"id":"gpt-5.6-sol"},{"id":"gpt-5.6-codex"}]}`,
		)),
	}}
	svc := newCodexModelsAPIKeyTestService(upstream)

	manifest, err := svc.FetchCodexModelsManifest(
		context.Background(),
		newCodexModelsAPIKeyTestAccount("https://upstream.example/v1"),
		"0.145.0",
		"",
	)
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "https://upstream.example/v1/models?client_version=0.145.0", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer sk-upstream", upstream.lastReq.Header.Get("Authorization"))
	require.Empty(t, upstream.lastReq.Header.Get("chatgpt-account-id"))
	require.JSONEq(t, `{"models":[{"slug":"gpt-5.6-sol","display_name":"gpt-5.6-sol"},{"slug":"gpt-5.6-codex","display_name":"gpt-5.6-codex"}]}`, string(manifest.Body))
	require.Equal(t, codexModelsManifestBodyETag(manifest.Body), manifest.ETag)
	require.Equal(t, `W/"upstream-list"`, manifest.upstreamETag)
}

func TestConvertOpenAIModelListProducesCodexRequiredFields(t *testing.T) {
	converted := convertOpenAIModelListToCodexManifest([]byte(`{"object":"list","data":[{"id":"gpt-5.6-sol"}]}`))

	var manifest struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
		} `json:"models"`
	}
	require.NoError(t, json.Unmarshal(converted, &manifest))
	require.Len(t, manifest.Models, 1)
	require.Equal(t, "gpt-5.6-sol", manifest.Models[0].Slug)
	require.Equal(t, "gpt-5.6-sol", manifest.Models[0].DisplayName)
}

func TestFetchCodexModelsManifestAPIKeyUsesFreshCacheAndClientETag(t *testing.T) {
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Etag": []string{`"manifest-v1"`}},
		Body:       io.NopCloser(strings.NewReader(`{"models":[{"slug":"gpt-5.6"}]}`)),
	}}
	svc := newCodexModelsAPIKeyTestService(upstream)
	account := newCodexModelsAPIKeyTestAccount("https://upstream.example")

	manifest, err := svc.FetchCodexModelsManifest(context.Background(), account, "0.145.0", "")
	require.NoError(t, err)
	require.Len(t, upstream.requests, 1)

	notModified, err := svc.FetchCodexModelsManifest(context.Background(), account, "0.145.0", manifest.ETag)
	require.NoError(t, err)
	require.True(t, notModified.NotModified)
	require.Equal(t, manifest.ETag, notModified.ETag)
	require.Len(t, upstream.requests, 1, "fresh cache must avoid a duplicate upstream request")
}

func TestFetchCodexModelsManifestAPIKeySharedRefreshSurvivesCallerCancellation(t *testing.T) {
	const manifestBody = `{"models":[{"slug":"gpt-5.6"}]}`
	var calls atomic.Int32
	var readStartedOnce sync.Once
	readStarted := make(chan struct{})
	release := make(chan struct{})
	upstream := &codexModelsHTTPUpstreamStub{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Etag": []string{`W/"shared"`}},
			Body: &codexModelsBlockingBody{
				ctx:         req.Context(),
				readStarted: readStarted,
				startedOnce: &readStartedOnce,
				release:     release,
				body:        strings.NewReader(manifestBody),
			},
		}, nil
	}}

	svc := newCodexModelsAPIKeyTestService(upstream)
	account := newCodexModelsAPIKeyTestAccount("https://upstream.example")
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstErr := make(chan error, 1)
	go func() {
		_, err := svc.FetchCodexModelsManifest(firstCtx, account, "0.145.0", "")
		firstErr <- err
	}()

	select {
	case <-readStarted:
	case <-time.After(time.Second):
		t.Fatal("上游响应体读取未启动")
	}
	cancelFirst()
	select {
	case err := <-firstErr:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("已取消的首个调用方未及时返回")
	}

	secondResult := make(chan struct {
		manifest *CodexModelsManifest
		err      error
	}, 1)
	go func() {
		manifest, err := svc.FetchCodexModelsManifest(context.Background(), account, "0.145.0", "")
		secondResult <- struct {
			manifest *CodexModelsManifest
			err      error
		}{manifest: manifest, err: err}
	}()

	time.Sleep(50 * time.Millisecond)
	require.Equal(t, int32(1), calls.Load(), "调用方取消不应终止共享刷新或触发第二次上游请求")
	close(release)
	select {
	case result := <-secondResult:
		require.NoError(t, result.err)
		require.Equal(t, manifestBody, string(result.manifest.Body))
	case <-time.After(time.Second):
		t.Fatal("第二个调用方未收到共享刷新结果")
	}
	require.Equal(t, int32(1), calls.Load())
}

func TestFetchCodexModelsManifestAPIKeyConcurrentRequestsShareRefresh(t *testing.T) {
	const callers = 8
	var calls atomic.Int32
	var startedOnce sync.Once
	started := make(chan struct{})
	release := make(chan struct{})
	upstream := &codexModelsHTTPUpstreamStub{do: func(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		calls.Add(1)
		startedOnce.Do(func() { close(started) })
		<-release
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"models":[]}`)),
		}, nil
	}}

	svc := newCodexModelsAPIKeyTestService(upstream)
	account := newCodexModelsAPIKeyTestAccount("https://upstream.example")
	begin := make(chan struct{})
	errs := make(chan error, callers)
	for range callers {
		go func() {
			<-begin
			_, err := svc.FetchCodexModelsManifest(context.Background(), account, "0.145.0", "")
			errs <- err
		}()
	}
	close(begin)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("上游请求未启动")
	}
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, int32(1), calls.Load(), "并发冷缓存请求必须合并为一次上游刷新")
	close(release)
	for range callers {
		require.NoError(t, <-errs)
	}
}

func TestAdjustAPIKeyCodexModelsManifestDisablesResponsesLiteOnlyForTargetedModels(t *testing.T) {
	body := []byte(`{"models":[{"slug":"gpt-5.6-sol","use_responses_lite":true},{"slug":"gpt-5.6-terra","use_responses_lite":false},{"slug":"gpt-5.6-codex","use_responses_lite":true}]}`)

	adjusted, err := adjustAPIKeyCodexModelsManifest(body)
	require.NoError(t, err)
	require.JSONEq(t, `{"models":[{"slug":"gpt-5.6-sol","use_responses_lite":false},{"slug":"gpt-5.6-terra","use_responses_lite":false},{"slug":"gpt-5.6-codex","use_responses_lite":true}]}`, string(adjusted))
}

func TestAdjustAPIKeyCodexModelsManifestDisablesResponsesLiteForGPT6Astra(t *testing.T) {
	body := []byte(`{"models":[{"slug":"gpt-6-astra","use_responses_lite":true},{"slug":"gpt-6","use_responses_lite":true}]}`)

	adjusted, err := adjustAPIKeyCodexModelsManifest(body)
	require.NoError(t, err)

	models := decodeCodexManifestModelsForTest(t, adjusted)
	require.Len(t, models, 2)
	for _, model := range models {
		require.Equal(t, false, model["use_responses_lite"], model["slug"])
	}
}

// API Key 账号的 /v1/models 转换清单不带档位信息，Codex 会退化成
// reasoning.effort=none，而 gpt-6-astra 对 none 直接 400。
func TestAdjustAPIKeyCodexModelsManifestFillsGPT6AstraReasoningLevels(t *testing.T) {
	body := []byte(`{"models":[{"slug":"gpt-6-astra","display_name":"gpt-6-astra"},{"slug":"gpt-5.4","display_name":"gpt-5.4"}]}`)

	adjusted, err := adjustAPIKeyCodexModelsManifest(body)
	require.NoError(t, err)

	models := decodeCodexManifestModelsForTest(t, adjusted)
	require.Len(t, models, 2)
	require.Equal(t, "medium", models[0]["default_reasoning_level"])
	require.Equal(t,
		[]string{"low", "medium", "high", "xhigh", "max"},
		effortsFromCodexManifestModel(t, models[0]),
	)
	// 其他型号沿用上游清单，不被网关补写
	require.NotContains(t, models[1], "supported_reasoning_levels")
	require.NotContains(t, models[1], "default_reasoning_level")
}

// 复现 upstream #6622：上游清单把 Astra 声明成仅支持 none。
func TestAdjustAPIKeyCodexModelsManifestReplacesUnsupportedGPT6AstraLevels(t *testing.T) {
	body := []byte(`{"models":[{"slug":"gpt-6-astra","default_reasoning_level":"none","supported_reasoning_levels":[{"effort":"none","description":"Use the model's default behavior"}]}]}`)

	adjusted, err := adjustAPIKeyCodexModelsManifest(body)
	require.NoError(t, err)

	models := decodeCodexManifestModelsForTest(t, adjusted)
	require.Len(t, models, 1)
	require.Equal(t, "medium", models[0]["default_reasoning_level"])
	require.Equal(t,
		[]string{"low", "medium", "high", "xhigh", "max"},
		effortsFromCodexManifestModel(t, models[0]),
	)
}

// 默认档不在自己声明的档位表里时，整条声明一起改写为网关口径——
// 只补默认档会写出 default_reasoning_level ∉ supported_reasoning_levels。
func TestAdjustAPIKeyCodexModelsManifestRewritesWhenDefaultOutsideDeclaredLevels(t *testing.T) {
	cases := map[string]string{
		"默认档越界":     `{"models":[{"slug":"gpt-6-astra","default_reasoning_level":"none","supported_reasoning_levels":[{"effort":"high","description":"deep"},{"effort":"max","description":"deepest"}]}]}`,
		"默认档合法但未声明": `{"models":[{"slug":"gpt-6-astra","default_reasoning_level":"low","supported_reasoning_levels":[{"effort":"high","description":"deep"},{"effort":"max","description":"deepest"}]}]}`,
		"缺默认档":      `{"models":[{"slug":"gpt-6-astra","supported_reasoning_levels":[{"effort":"high","description":"deep"}]}]}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			adjusted, err := adjustAPIKeyCodexModelsManifest([]byte(body))
			require.NoError(t, err)

			models := decodeCodexManifestModelsForTest(t, adjusted)
			require.Len(t, models, 1)
			efforts := effortsFromCodexManifestModel(t, models[0])
			require.Equal(t, []string{"low", "medium", "high", "xhigh", "max"}, efforts)
			require.Contains(t, efforts, models[0]["default_reasoning_level"])
			require.Equal(t, "medium", models[0]["default_reasoning_level"])
		})
	}
}

func TestAdjustAPIKeyCodexModelsManifestLeavesCompliantGPT6AstraUntouched(t *testing.T) {
	body := []byte(`{"models":[{"slug":"gpt-6-astra","default_reasoning_level":"high","supported_reasoning_levels":[{"effort":"high","description":"deep"}]}]}`)

	adjusted, err := adjustAPIKeyCodexModelsManifest(body)
	require.NoError(t, err)
	require.JSONEq(t, string(body), string(adjusted))
}

func decodeCodexManifestModelsForTest(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var envelope struct {
		Models []map[string]any `json:"models"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope))
	return envelope.Models
}

func effortsFromCodexManifestModel(t *testing.T, model map[string]any) []string {
	t.Helper()
	raw, ok := model["supported_reasoning_levels"].([]any)
	require.True(t, ok, "supported_reasoning_levels missing: %v", model)
	efforts := make([]string, 0, len(raw))
	for _, item := range raw {
		level, ok := item.(map[string]any)
		require.True(t, ok)
		effort, ok := level["effort"].(string)
		require.True(t, ok)
		efforts = append(efforts, effort)
	}
	return efforts
}

func TestFetchCodexModelsManifestAPIKeyInvalidEnvelopeIsRetryable(t *testing.T) {
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"object":"list","data":[]}`)),
	}}
	svc := newCodexModelsAPIKeyTestService(upstream)

	_, err := svc.FetchCodexModelsManifest(
		context.Background(),
		newCodexModelsAPIKeyTestAccount("https://upstream.example"),
		"0.145.0",
		"",
	)
	require.Error(t, err)
	require.Equal(t, "OPENAI_CODEX_MODELS_UPSTREAM_INVALID_MANIFEST", infraerrors.Reason(err))
	require.True(t, IsRetryableCodexModelsManifestError(err))
}

func TestFetchCodexModelsManifestAPIKeyRequiresCustomBaseURL(t *testing.T) {
	for _, baseURL := range []string{"", "https://api.openai.com/v1"} {
		t.Run(baseURL, func(t *testing.T) {
			svc := newCodexModelsAPIKeyTestService(&httpUpstreamRecorder{})
			_, err := svc.FetchCodexModelsManifest(
				context.Background(),
				newCodexModelsAPIKeyTestAccount(baseURL),
				"0.145.0",
				"",
			)
			require.Error(t, err)
			require.Equal(t, "OPENAI_CODEX_MODELS_API_KEY_UPSTREAM_UNSUPPORTED", infraerrors.Reason(err))
		})
	}
}
