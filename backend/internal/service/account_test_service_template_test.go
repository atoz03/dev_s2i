//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreateTestPayload_UsesPongTemplate(t *testing.T) {
	t.Parallel()

	payload, err := createTestPayload("claude-sonnet-4-5")
	require.NoError(t, err)

	body, err := json.Marshal(payload)
	require.NoError(t, err)

	var parsed struct {
		Messages []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
		System []struct {
			Text string `json:"text"`
		} `json:"system"`
		MaxTokens int  `json:"max_tokens"`
		Stream    bool `json:"stream"`
	}
	require.NoError(t, json.Unmarshal(body, &parsed))
	require.Len(t, parsed.Messages, 1)
	require.Len(t, parsed.Messages[0].Content, 1)
	require.Equal(t, defaultClaudeTextTestPrompt, parsed.Messages[0].Content[0].Text)
	require.Len(t, parsed.System, 2)
	require.Equal(t, claudeCodeSystemPrompt, parsed.System[0].Text)
	require.Equal(t, defaultEchoBotInstruction, parsed.System[1].Text)
	require.Equal(t, defaultTextTestMaxTokens, parsed.MaxTokens)
	require.True(t, parsed.Stream)
}

func TestCreateOpenAITestPayload_UsesPongTemplate(t *testing.T) {
	t.Parallel()

	payload := createOpenAITestPayload("gpt-5.4", true)

	body, err := json.Marshal(payload)
	require.NoError(t, err)

	var parsed struct {
		Input []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
		Instructions string `json:"instructions"`
		Store        bool   `json:"store"`
		Stream       bool   `json:"stream"`
	}
	require.NoError(t, json.Unmarshal(body, &parsed))
	require.Len(t, parsed.Input, 1)
	require.Len(t, parsed.Input[0].Content, 1)
	require.Equal(t, "input_text", parsed.Input[0].Content[0].Type)
	require.Equal(t, defaultOpenAITextTestPrompt, parsed.Input[0].Content[0].Text)
	require.Contains(t, parsed.Instructions, "You are Codex, based on GPT-5.")
	require.Contains(t, parsed.Instructions, defaultEchoBotInstruction)
	require.False(t, parsed.Store)
	require.True(t, parsed.Stream)
}
