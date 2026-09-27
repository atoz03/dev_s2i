package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestSanitizeGPT6ResponsesSampling(t *testing.T) {
	for _, model := range []string{"gpt-6-sol", "openai/gpt-6-luna-max"} {
		body := map[string]any{
			"reasoning":   map[string]any{"effort": "high"},
			"temperature": 0.7, "top_p": 0.8, "top_logprobs": 3, "logprobs": true,
		}
		require.True(t, sanitizeGPT6ResponsesSampling(body, model))
		for _, field := range []string{"temperature", "top_p", "top_logprobs", "logprobs"} {
			require.NotContains(t, body, field)
		}
	}

	none := map[string]any{"reasoning": map[string]any{"effort": "none"}, "temperature": 0.7}
	require.False(t, sanitizeGPT6ResponsesSampling(none, "gpt-6-sol"))
	require.Contains(t, none, "temperature")

	unrelated := map[string]any{"temperature": 0.7}
	require.False(t, sanitizeGPT6ResponsesSampling(unrelated, "gpt-6-astra"))
	require.Contains(t, unrelated, "temperature")
}

func TestSanitizeGPT6ResponsesSamplingBytes(t *testing.T) {
	body := []byte(`{"model":"gpt-6-luna","reasoning":{"effort":"high"},"temperature":0.7,"top_p":0.8,"input":[{"json":{"temperature":1}}]}`)
	normalized, changed, err := sanitizeGPT6ResponsesSamplingBytes(body, "gpt-6-luna")
	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(normalized, "temperature").Exists())
	require.False(t, gjson.GetBytes(normalized, "top_p").Exists())
	require.Equal(t, float64(1), gjson.GetBytes(normalized, "input.0.json.temperature").Float())

	noneBody := []byte(`{"reasoning":{"effort":"none"},"temperature":0.7}`)
	unchanged, changed, err := sanitizeGPT6ResponsesSamplingBytes(noneBody, "gpt-6-sol")
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, string(noneBody), string(unchanged))
}
