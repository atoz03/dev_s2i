//go:build unit

package service

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// TestCodexVersionConstants_Consistency：UA 与 version 头是同一个版本声明的两个出口，
// 各自硬编码会漂移成互相矛盾的身份。
func TestCodexVersionConstants_Consistency(t *testing.T) {
	require.Equal(t, codexCLIVersion, openAICodexProbeVersion,
		"codexCLIVersion 与 openAICodexProbeVersion 必须保持一致")
	require.Contains(t, codexCLIUserAgent, "codex_cli_rs/"+codexCLIVersion,
		"codexCLIUserAgent 必须内嵌 codexCLIVersion")
	require.Contains(t, codexCLIUserAgent, codexCLIUserAgentSuffix,
		"codexCLIUserAgent 必须带 OS/架构/终端后缀，裸 originator/version 易被判为非官方客户端")
}

// TestCodexCLIVersionMeetsUpstreamGate：上游对低于 codexUpstreamMinVersion 的 version 头直接 404。
func TestCodexCLIVersionMeetsUpstreamGate(t *testing.T) {
	require.GreaterOrEqual(t, CompareVersions(codexCLIVersion, codexUpstreamMinVersion), 0,
		"codexCLIVersion 必须不低于上游 version 门槛 %s，否则 /backend-api/codex 一律 404", codexUpstreamMinVersion)
}

// withCodexNormalization 临时切换归一化开关，测试结束后恢复，避免污染同包其他用例。
func withCodexNormalization(t *testing.T, enabled bool) {
	t.Helper()
	prev := codexOriginatorNormalization.Load()
	SetCodexOriginatorNormalizationEnabled(enabled)
	t.Cleanup(func() { SetCodexOriginatorNormalizationEnabled(prev) })
}

// TestCodexOriginatorNormalizationDefaultsOn 反义命名保证零值 Config 仍开启保护。
func TestCodexOriginatorNormalizationDefaultsOn(t *testing.T) {
	cfg := &config.Config{}
	require.False(t, cfg.Gateway.DisableCodexOriginatorNormalization,
		"零值 Config 必须表示「归一化开启」，否则手工构造的 Config 会静默关掉全局保护")
	require.True(t, !cfg.Gateway.DisableCodexOriginatorNormalization)
}

func TestEnforceCodexIdentityHeaders_NormalizationOn(t *testing.T) {
	withCodexNormalization(t, true)

	t.Run("降载桶身份 codex-tui 被归一化为 codex_cli_rs", func(t *testing.T) {
		// 上游按 originator 分桶降载：codex-tui 落在降载桶，请求会 HTTP 200 后
		// 立刻以 server_is_overloaded 收尾，客户端表现为 stream closed before response.completed。
		h := http.Header{}
		h.Set("originator", "codex-tui")
		h.Set("user-agent", "codex-tui/0.146.0 (Ubuntu 22.4.0; x86_64) xterm-256color")

		enforceCodexIdentityHeaders(h)

		require.Equal(t, "codex_cli_rs", h.Get("originator"))
		require.Equal(t, codexCLIUserAgent, h.Get("user-agent"))
		require.Equal(t, codexCLIVersion, h.Get("version"))
	})

	t.Run("第三方 UA 整体回退默认 Codex CLI 身份", func(t *testing.T) {
		h := http.Header{}
		h.Set("originator", "cccc")
		h.Set("user-agent", "cccc/1.0.0")

		enforceCodexIdentityHeaders(h)

		require.Equal(t, "codex_cli_rs", h.Get("originator"))
		require.Equal(t, codexCLIUserAgent, h.Get("user-agent"))
	})

	t.Run("陈旧 version 被提升到内置版本", func(t *testing.T) {
		h := http.Header{}
		h.Set("originator", "codex_cli_rs")
		h.Set("user-agent", "codex_cli_rs/0.125.0")
		h.Set("version", "0.125.0")

		enforceCodexIdentityHeaders(h)

		require.Equal(t, codexCLIVersion, h.Get("version"))
		require.GreaterOrEqual(t, CompareVersions(h.Get("version"), codexUpstreamMinVersion), 0)
	})

	t.Run("归一化后 originator 与 UA 首段仍然配套且幂等", func(t *testing.T) {
		h := http.Header{}
		h.Set("originator", "codex-tui")
		h.Set("user-agent", "codex-tui/0.146.0 (Ubuntu 22.4.0; x86_64) xterm-256color")

		enforceCodexIdentityHeaders(h)
		first := h.Clone()
		enforceCodexIdentityHeaders(h)

		require.Equal(t, first, h, "改写必须幂等")
		require.True(t, len(h.Get("user-agent")) > len(h.Get("originator")) &&
			h.Get("user-agent")[:len(h.Get("originator"))+1] == h.Get("originator")+"/",
			"originator %q 必须与 UA %q 首段配套", h.Get("originator"), h.Get("user-agent"))
	})

	t.Run("缺失 originator 时 no-op 保护 messages bridge", func(t *testing.T) {
		h := http.Header{}
		h.Set("user-agent", "anything/1.0")

		enforceCodexIdentityHeaders(h)

		require.Empty(t, h.Get("originator"))
		require.Equal(t, "anything/1.0", h.Get("user-agent"))
		require.Empty(t, h.Get("version"))
	})

	t.Run("nil header 不 panic", func(t *testing.T) {
		require.NotPanics(t, func() { enforceCodexIdentityHeaders(nil) })
	})
}

// TestEnforceCodexIdentityHeaders_NormalizationOff 关闭归一化后退回配对语义（回滚路径）。
func TestEnforceCodexIdentityHeaders_NormalizationOff(t *testing.T) {
	withCodexNormalization(t, false)

	t.Run("保留客户端真实身份并保证配对", func(t *testing.T) {
		h := http.Header{}
		h.Set("originator", "codex-tui")
		h.Set("user-agent", "codex-tui/0.146.0 (Ubuntu 22.4.0; x86_64) xterm-256color")

		enforceCodexIdentityHeaders(h)

		require.Equal(t, "codex-tui", h.Get("originator"))
		require.Equal(t, "codex-tui/0.146.0 (Ubuntu 22.4.0; x86_64) xterm-256color", h.Get("user-agent"))
	})

	t.Run("错配的 originator 按最终 UA 重配", func(t *testing.T) {
		h := http.Header{}
		h.Set("originator", "codex-tui")
		h.Set("user-agent", codexCLIUserAgent)

		enforceCodexIdentityHeaders(h)

		require.Equal(t, "codex_cli_rs", h.Get("originator"))
		require.Equal(t, codexCLIUserAgent, h.Get("user-agent"))
	})

	t.Run("低于门槛的 version 被提升，不携带时不补写", func(t *testing.T) {
		h := http.Header{}
		h.Set("originator", "codex_cli_rs")
		h.Set("user-agent", codexCLIUserAgent)
		h.Set("version", "0.125.0")
		enforceCodexIdentityHeaders(h)
		require.Equal(t, codexCLIVersion, h.Get("version"))

		h2 := http.Header{}
		h2.Set("originator", "codex_cli_rs")
		h2.Set("user-agent", codexCLIUserAgent)
		enforceCodexIdentityHeaders(h2)
		require.Empty(t, h2.Get("version"))
	})
}
