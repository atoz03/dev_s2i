package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsReasoningModelCoversLaterGenerations(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  bool
	}{
		{"gpt-6-astra", true}, {"gpt-6-sol", true}, {"gpt-6-luna", true},
		{"gpt-7-whatever", true}, {"gpt-5.5", true}, {"GPT-6-Astra", true},
		{"openai/gpt-6-astra", true}, {"OPENAI/GPT_6_SOL", true},
		{"gpt-4o", false}, {"gpt-4.1", false}, {"gpt-image-1", false},
		{"claude-opus-5", false}, {"", false},
	} {
		require.Equal(t, tc.want, isReasoningModel(tc.model), tc.model)
	}
}

func TestAnthropicToResponsesStripsSamplingForGPT6(t *testing.T) {
	temp := 0.7
	for _, model := range []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna"} {
		resp, err := AnthropicToResponses(&AnthropicRequest{
			Model: model, MaxTokens: 1024, Temperature: &temp, TopP: &temp,
			Messages: []AnthropicMessage{{Role: "user", Content: json.RawMessage(`"Hello"`)}},
		})
		require.NoError(t, err)
		require.Nil(t, resp.Temperature, model)
		require.Nil(t, resp.TopP, model)
	}
}
