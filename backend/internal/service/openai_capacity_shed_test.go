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

// 上游容量降载的真实流内序列是 `event: error` → `event: response.failed`，
// 两个事件的错误码位置不同，判定必须同时覆盖。
func TestIsOpenAICapacityShedPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{"裸 error 帧 server_is_overloaded", `{"error":{"code":"server_is_overloaded","message":"boom"}}`, true},
		{"裸 error 帧 slow_down", `{"error":{"code":"slow_down","message":"boom"}}`, true},
		{"response.failed 内嵌 server_is_overloaded", `{"type":"response.failed","response":{"error":{"code":"server_is_overloaded"}}}`, true},
		{"大小写与空白不敏感", `{"error":{"code":"  Server_Is_Overloaded "}}`, true},
		{"限流不是降载", `{"error":{"code":"rate_limit_exceeded"}}`, false},
		{"内容策略不是降载", `{"error":{"code":"content_policy_violation"}}`, false},
		{"空载荷", ``, false},
		{"非法 JSON", `{not json`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isOpenAICapacityShedPayload([]byte(tt.payload)))
		})
	}
}

// 无法再改走 failover 时，降载码必须改写为致命集之外的 server_error，
// 否则 Codex CLI 收到即终止会话，客户端内置退避重试不会发生。
func TestRewriteOpenAICapacityShedErrorCodeForClient(t *testing.T) {
	t.Run("裸 error 帧改写码并保留消息", func(t *testing.T) {
		payload := []byte(`{"error":{"code":"server_is_overloaded","message":"Selected model is at capacity."}}`)

		got, rewritten := rewriteOpenAICapacityShedErrorCodeForClient(payload, "error")

		require.True(t, rewritten)
		require.Equal(t, "server_error", gjson.GetBytes(got, "error.code").String())
		require.Equal(t, "Selected model is at capacity.", gjson.GetBytes(got, "error.message").String(),
			"错误消息必须原样保留，改写的只是客户端据以判定致命性的码")
	})

	t.Run("response.failed 改写嵌套码", func(t *testing.T) {
		payload := []byte(`{"type":"response.failed","response":{"id":"resp_1","error":{"code":"slow_down","message":"slow"}}}`)

		got, rewritten := rewriteOpenAICapacityShedErrorCodeForClient(payload, "response.failed")

		require.True(t, rewritten)
		require.Equal(t, "server_error", gjson.GetBytes(got, "response.error.code").String())
		require.Equal(t, "resp_1", gjson.GetBytes(got, "response.id").String(), "其余字段不得改动")
	})

	t.Run("其他错误码一律不动", func(t *testing.T) {
		payload := []byte(`{"error":{"code":"rate_limit_exceeded","message":"slow down"}}`)

		got, rewritten := rewriteOpenAICapacityShedErrorCodeForClient(payload, "error")

		require.False(t, rewritten)
		require.Equal(t, payload, got)
	})

	t.Run("非错误类事件不动", func(t *testing.T) {
		payload := []byte(`{"type":"response.output_text.delta","error":{"code":"server_is_overloaded"}}`)

		_, rewritten := rewriteOpenAICapacityShedErrorCodeForClient(payload, "response.output_text.delta")

		require.False(t, rewritten)
	})
}

// 完整降载序列（response.created → error → response.failed）必须走 pre-output failover：
// 裸 error 帧不得被当作首个客户端输出而固化 clientOutputStarted，
// 否则随后的 response.failed 永远进不了 failover 分支，降载错误被原样转发，
// Codex CLI 按致命集判定后直接终止会话。
func TestOpenAIGatewayService_OAuthPassthrough_CapacityShedTriggersPreOutputFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", codexCLIUserAgent)

	upstreamSSE := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_shed"}}`,
		"",
		"event: error",
		`data: {"type":"error","error":{"code":"server_is_overloaded","message":"Selected model is at capacity."}}`,
		"",
		"event: response.failed",
		`data: {"type":"response.failed","response":{"id":"resp_shed","error":{"code":"server_is_overloaded","message":"Selected model is at capacity."}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamSSE)),
	}}

	svc := &OpenAIGatewayService{
		cfg:          &config.Config{},
		httpUpstream: upstream,
	}
	account := &Account{
		ID:             321,
		Name:           "acc-shed",
		Platform:       PlatformOpenAI,
		Type:           AccountTypeOAuth,
		Concurrency:    1,
		Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Extra:          map[string]any{"openai_passthrough": true, "pool_mode": true},
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}

	_, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.6-sol","stream":true,"input":"hi"}`))

	require.Error(t, err)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover, "降载必须转成 failover 错误，让网关换账号重试")

	// 走了 failover 就不得有任何降载帧漏给客户端。
	require.NotContains(t, rec.Body.String(), "server_is_overloaded")
}
