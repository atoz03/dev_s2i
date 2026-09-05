package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsOpenAIGPT6AstraModel(t *testing.T) {
	t.Parallel()

	accepted := []string{
		"gpt-6",
		"gpt-6-astra",
		"openai/gpt-6",
		"openai/gpt-6-astra",
		"GPT-6_ASTRA",
		"provider/gpt-6-astra",
		// 日期与推理档位后缀属于同一型号
		"gpt-6-astra-2026-09-01",
		"gpt-6-astra-max",
		"gpt-6-astra-xhigh",
		// 公开别名同样带档位下发（OpenCode 变体）
		"gpt-6-max",
		"gpt-6-high",
		"gpt-6-2026-09-01",
	}
	for _, model := range accepted {
		require.True(t, isOpenAIGPT6AstraModel(model), model)
	}

	rejected := []string{
		"",
		"gpt-6-terra",
		"gpt-6.1",
		"gpt-61",
		"gpt-5.6-sol",
		"claude-sonnet-4-5",
		"gemini-3.1-pro",
	}
	for _, model := range rejected {
		require.False(t, isOpenAIGPT6AstraModel(model), model)
	}
}

func TestNormalizeKnownOpenAICodexModelGPT6Astra(t *testing.T) {
	t.Parallel()

	for _, model := range []string{
		"gpt-6-astra",
		"openai/gpt-6-astra",
		"gpt-6",
		"openai/gpt-6",
		"gpt-6-astra-2026-09-01",
		"gpt-6-astra-max",
		"gpt-6-max",
		"gpt-6-high",
	} {
		require.Equal(t, "gpt-6-astra", normalizeKnownOpenAICodexModel(model), model)
	}

	// 其他 GPT-6 家族没有定价与能力口径，必须保持"未知"，
	// 不能被 gpt-5 兜底或 Astra 吞掉。
	for _, model := range []string{"gpt-6-terra", "gpt-6.1"} {
		require.Equal(t, "", normalizeKnownOpenAICodexModel(model), model)
	}
}
