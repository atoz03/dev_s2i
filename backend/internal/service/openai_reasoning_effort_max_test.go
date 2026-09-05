package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 网关不改写出站 reasoning.effort，因此用量记录必须原样保留 max，
// 否则 GPT-5.6 / GPT-6 Astra 的最高档在使用记录里会丢失（显示为空）。
func TestNormalizeOpenAIReasoningEffortKeepsMax(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"max":       "max",
		"MAX":       "max",
		" max ":     "max",
		"xhigh":     "xhigh",
		"x-high":    "xhigh",
		"extrahigh": "xhigh",
		"high":      "high",
		"medium":    "medium",
		"low":       "low",
		"none":      "",
		"minimal":   "",
		"ultra":     "",
		"":          "",
	}
	for raw, want := range cases {
		require.Equal(t, want, normalizeOpenAIReasoningEffort(raw), raw)
	}
}

func TestExtractOpenAIReasoningEffortKeepsMaxFromBody(t *testing.T) {
	t.Parallel()

	nested := map[string]any{"reasoning": map[string]any{"effort": "max"}}
	got := extractOpenAIReasoningEffort(nested, "gpt-6-astra")
	require.NotNil(t, got)
	require.Equal(t, "max", *got)

	flat := map[string]any{"reasoning_effort": "max"}
	got = extractOpenAIReasoningEffortFromBody([]byte(`{"reasoning_effort":"max"}`), "gpt-6-astra")
	require.NotNil(t, got)
	require.Equal(t, "max", *got)
	require.NotNil(t, extractOpenAIReasoningEffort(flat, "gpt-6-astra"))
}

func TestDeriveOpenAIReasoningEffortFromModel_MaxSuffix(t *testing.T) {
	t.Parallel()

	// OpenCode 之类的客户端把档位拼进模型名下发。
	require.Equal(t, "max", deriveOpenAIReasoningEffortFromModel("gpt-6-astra-max"))
	require.Equal(t, "max", deriveOpenAIReasoningEffortFromModel("openai/gpt-6-astra-max"))
	require.Equal(t, "xhigh", deriveOpenAIReasoningEffortFromModel("gpt-6-astra-xhigh"))

	// 回归：gpt-5.1-codex-max 的 "-max" 是型号的一部分，不是推理档位，
	// 不得因此在用量记录里凭空写出 max。
	require.Equal(t, "", deriveOpenAIReasoningEffortFromModel("gpt-5.1-codex-max"))
	require.Equal(t, "", deriveOpenAIReasoningEffortFromModel("openai/gpt-5.1-codex-max"))

	require.Equal(t, "", deriveOpenAIReasoningEffortFromModel("gpt-6-astra"))
	require.Equal(t, "", deriveOpenAIReasoningEffortFromModel(""))
}
