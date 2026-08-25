package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// newOAuthPassthroughStreamTest 组装一个 OAuth 透传流式请求，返回客户端实际收到的字节。
func newOAuthPassthroughStreamTest(t *testing.T, upstreamSSE string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", codexCLIUserAgent)

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamSSE)),
	}}

	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{
		ID:             777,
		Name:           "acc-passthrough",
		Platform:       PlatformOpenAI,
		Type:           AccountTypeOAuth,
		Concurrency:    1,
		Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Extra:          map[string]any{"openai_passthrough": true},
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}

	_, err := svc.Forward(context.Background(), c, account,
		[]byte(`{"model":"gpt-5.6-sol","stream":true,"instructions":"answer","input":"hi"}`))
	require.NoError(t, err)
	return rec
}

// SSE 事件由 data 行之后的空行分帧；缺少分帧空行时解析器不会派发该事件。
// 透传路径原本只在「本行是客户端输出」时 flush，终止事件的空行、[DONE] 及其空行
// 都留在 bufio 缓冲里随函数返回被丢弃，客户端因此永远等不到 response.completed。
func TestOpenAIGatewayService_OAuthPassthrough_StreamTailReachesClient(t *testing.T) {
	upstreamSSE := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1"}}`,
		"",
		`data: {"type":"response.output_text.delta","delta":"hi"}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":1,"output_tokens":1}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")

	body := newOAuthPassthroughStreamTest(t, upstreamSSE).Body.String()

	require.Contains(t, body, `"type":"response.completed"`)
	require.Contains(t, body, "data: [DONE]",
		"[DONE] 不是客户端输出事件，只按 lineStartsClientOutput flush 时会被留在缓冲里丢掉")

	// 终止事件必须带分帧空行，否则客户端 SSE 解析器不会把它交给应用层。
	idx := strings.Index(body, `"type":"response.completed"`)
	require.GreaterOrEqual(t, idx, 0)
	require.Contains(t, body[idx:], "\n\n",
		"response.completed 之后必须出现分帧空行，否则客户端报 stream closed before response.completed")
}

// 非客户端输出事件（reasoning / in_progress 等）在首个输出之后也必须即时出站，
// 否则长推理阶段客户端与中间代理长时间收不到任何字节。
func TestOpenAIGatewayService_OAuthPassthrough_StreamFlushesNonOutputEventsAfterFirstOutput(t *testing.T) {
	upstreamSSE := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_2"}}`,
		"",
		`data: {"type":"response.output_item.added","item":{"id":"item_1"}}`,
		"",
		"event: response.reasoning_summary_part.added",
		`data: {"type":"response.reasoning_summary_part.added","part":{"text":"thinking"}}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_2"}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")

	body := newOAuthPassthroughStreamTest(t, upstreamSSE).Body.String()

	require.Contains(t, body, "event: response.reasoning_summary_part.added",
		"event: 行不是客户端输出，但首个输出之后必须随流出站")
	require.Contains(t, body, `"type":"response.reasoning_summary_part.added"`)
	require.Contains(t, body, "data: [DONE]")
}

// 对照组：首个客户端输出之前不得 flush——pre-output failover 依赖客户端尚未收到任何字节，
// 否则换号重试会把第二份 response.created 追加到同一条流上。
func TestOpenAIGatewayService_OAuthPassthrough_NoFlushBeforeFirstClientOutput(t *testing.T) {
	upstreamSSE := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_3"}}`,
		"",
		"event: error",
		`data: {"type":"error","error":{"code":"server_is_overloaded","message":"at capacity"}}`,
		"",
		"event: response.failed",
		`data: {"type":"response.failed","response":{"id":"resp_3","error":{"code":"server_is_overloaded","message":"at capacity"}}}`,
		"",
	}, "\n")

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", codexCLIUserAgent)

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamSSE)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{
		ID:             778,
		Name:           "acc-passthrough-failover",
		Platform:       PlatformOpenAI,
		Type:           AccountTypeOAuth,
		Concurrency:    1,
		Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Extra:          map[string]any{"openai_passthrough": true, "pool_mode": true},
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}

	_, err := svc.Forward(context.Background(), c, account,
		[]byte(`{"model":"gpt-5.6-sol","stream":true,"instructions":"answer","input":"hi"}`))

	require.Error(t, err)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Empty(t, rec.Body.String(),
		"pre-output failover 前不得向客户端写出任何字节，否则换号重试会写出第二份 response.created")
}

// 客户端请求 stream=false 时必须拿到 JSON：ChatGPT 非 compact 端点只接受流式，
// 网关向上游强制 stream=true 取数，但响应侧必须把 SSE 折叠回 JSON，
// 否则请求 JSON 的客户端会收到裸 SSE。
func TestOpenAIGatewayService_OAuthPassthrough_NonStreamingClientGetsJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", codexCLIUserAgent)

	upstreamSSE := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_ns"}}`,
		"",
		`data: {"type":"response.output_text.delta","delta":"hi"}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_ns","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamSSE)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{
		ID:             779,
		Name:           "acc-passthrough-nonstream",
		Platform:       PlatformOpenAI,
		Type:           AccountTypeOAuth,
		Concurrency:    1,
		Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Extra:          map[string]any{"openai_passthrough": true},
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}

	_, err := svc.Forward(context.Background(), c, account,
		[]byte(`{"model":"gpt-5.6-sol","stream":false,"instructions":"answer","input":"hi"}`))
	require.NoError(t, err)

	// 向上游仍然强制流式取数。
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool(),
		"非 compact 端点只接受流式，上游请求体必须保持 stream=true")

	// 但返回给客户端的必须是 JSON，而不是 SSE 帧。
	body := rec.Body.String()
	require.NotContains(t, body, "data: ", "客户端请求非流式时不得收到 SSE 帧")
	require.Equal(t, "resp_ns", gjson.Get(body, "id").String())
	require.Equal(t, "hi", gjson.Get(body, "output.0.content.0.text").String())
}
