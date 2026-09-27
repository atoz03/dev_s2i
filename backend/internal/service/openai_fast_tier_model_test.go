package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestSplitOpenAIFastTierModel(t *testing.T) {
	fast := map[string]string{
		"gpt-6-astra-fast": "gpt-6-astra",
		// gpt-6 是 Astra 的公开别名，带 -fast 同样成立
		"gpt-6-fast": "gpt-6",
		// 大小写与 provider 前缀不影响判定，前缀原样保留给上游
		"GPT-6-Astra-Fast":        "GPT-6-Astra",
		"openai/gpt-6-astra-fast": "openai/gpt-6-astra",
		"  gpt-6-astra-fast  ":    "gpt-6-astra",
		// Fast 是服务档位，不是 Astra 专属：同族其他型号一并生效
		"gpt-5.6-sol-fast": "gpt-5.6-sol",
		"gpt-6-sol-fast":   "gpt-6-sol",
		"gpt-6-luna-fast":  "gpt-6-luna",
	}
	for input, expected := range fast {
		base, isFast := splitOpenAIFastTierModel(input)
		require.True(t, isFast, input)
		require.Equal(t, expected, base, input)
	}

	notFast := []string{
		"gpt-6-astra",
		// 档位后缀不是 Fast 档
		"gpt-6-astra-max",
		"gpt-6-astra-2026-09-01",
		// 剥掉 -fast 后不是已知 OpenAI 型号的，一律不碰
		"some-other-fast",
		"gemini-3.6-flash-fast",
		"claude-opus-5-fast",
		"gpt-6-terra-fast",
		"-fast",
		"fast",
		"",
	}
	for _, input := range notFast {
		base, isFast := splitOpenAIFastTierModel(input)
		require.False(t, isFast, input)
		require.Equal(t, input, base, input)
	}
}

func TestApplyOpenAIFastTierModelSuffix(t *testing.T) {
	// `-fast` 折叠成 model + service_tier
	got := applyOpenAIFastTierModelSuffix([]byte(`{"model":"gpt-6-astra-fast","stream":true}`))
	require.Equal(t, "gpt-6-astra", gjson.GetBytes(got, "model").String())
	require.Equal(t, "priority", gjson.GetBytes(got, "service_tier").String())
	require.True(t, gjson.GetBytes(got, "stream").Bool())

	// 客户端显式声明的 service_tier 优先，不被覆盖
	got = applyOpenAIFastTierModelSuffix([]byte(`{"model":"gpt-6-astra-fast","service_tier":"flex"}`))
	require.Equal(t, "gpt-6-astra", gjson.GetBytes(got, "model").String())
	require.Equal(t, "flex", gjson.GetBytes(got, "service_tier").String())

	// 无 `-fast` 后缀的请求体原样返回
	original := []byte(`{"model":"gpt-6-astra"}`)
	require.Equal(t, original, applyOpenAIFastTierModelSuffix(original))
	original = []byte(`{"model":"claude-opus-5"}`)
	require.Equal(t, original, applyOpenAIFastTierModelSuffix(original))
	require.Empty(t, applyOpenAIFastTierModelSuffix(nil))
}

// 折叠后走的是既有 service_tier 计费路径：Fast 档 = 标准档 2 倍，
// 即 2026-09-05 公告里的 36 / 45 / 3.6 / 180（USD per MTok）。
func TestCalculateCostWithServiceTier_GPT6AstraFastMatchesAnnouncedRates(t *testing.T) {
	svc := NewBillingService(nil, nil)
	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 1000, CacheCreationTokens: 1000, CacheReadTokens: 1000}

	base, isFast := splitOpenAIFastTierModel("gpt-6-astra-fast")
	require.True(t, isFast)

	cost, err := svc.CalculateCostWithServiceTier(base, tokens, 1.0, "priority")
	require.NoError(t, err)
	require.InDelta(t, 1000*36e-6, cost.InputCost, 1e-12)
	require.InDelta(t, 1000*180e-6, cost.OutputCost, 1e-12)
	require.InDelta(t, 1000*45e-6, cost.CacheCreationCost, 1e-12)
	require.InDelta(t, 1000*3.6e-6, cost.CacheReadCost, 1e-12)
}
