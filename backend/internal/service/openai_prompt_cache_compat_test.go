package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSanitizeGPTPromptCacheHints(t *testing.T) {
	body := []byte(`{"prompt_cache_options":{"mode":"explicit"},"prompt_cache_breakpoint":true,"prompt_cache_key":"keep"}`)
	for _, model := range []string{"gpt-5.4", "gpt-6-astra", "gpt-6-sol", "openai/GPT_6_LUNA", "gpt-future"} {
		got, changed, err := sanitizeGPTPromptCacheHints(body, model)
		require.NoError(t, err)
		require.True(t, changed, model)
		require.JSONEq(t, `{"prompt_cache_key":"keep"}`, string(got))
	}
	got, changed, err := sanitizeGPTPromptCacheHints(body, "claude-opus-5")
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, body, got)
}

func TestSanitizeGPTPromptCacheHintsPreservesNonProtocolData(t *testing.T) {
	body := []byte(`{
		"prompt_cache_options":{"mode":"explicit"},
		"input":[
			{"type":"message","role":"user","prompt_cache_breakpoint":true,"content":[{"type":"input_text","text":"hello","prompt_cache_breakpoint":true}]},
			{"type":"function_call","arguments":"{\"prompt_cache_breakpoint\":true}","prompt_cache_breakpoint":"keep"}
		],
		"tools":[{"type":"function","parameters":{"properties":{"prompt_cache_breakpoint":{"type":"string"}}}}],
		"metadata":{"prompt_cache_breakpoint":"keep","number":9007199254740993}
	}`)
	got, changed, err := sanitizeGPTPromptCacheHints(body, "gpt-6-sol")
	require.NoError(t, err)
	require.True(t, changed)
	var decoded map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(got, &decoded))
	require.NotContains(t, decoded, "prompt_cache_options")
	require.NotContains(t, string(decoded["input"]), `"prompt_cache_breakpoint":true`)
	require.Contains(t, string(decoded["input"]), `\"prompt_cache_breakpoint\":true`)
	require.Contains(t, string(decoded["input"]), `"prompt_cache_breakpoint":"keep"`)
	require.Contains(t, string(decoded["tools"]), "prompt_cache_breakpoint")
	require.JSONEq(t, `{"prompt_cache_breakpoint":"keep","number":9007199254740993}`, string(decoded["metadata"]))
}
