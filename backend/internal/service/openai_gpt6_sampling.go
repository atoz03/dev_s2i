package service

import (
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// sanitizeGPT6ResponsesSampling 删除 Sol/Luna 推理档不接受的采样参数。
// reasoning.effort=none 支持采样，因此保持原样。
func sanitizeGPT6ResponsesSampling(reqBody map[string]any, model string) bool {
	if !openai.IsGPT6SolOrLunaModelSpelling(model) || reqBody == nil {
		return false
	}
	effort := ""
	if reasoning, ok := reqBody["reasoning"].(map[string]any); ok {
		effort, _ = reasoning["effort"].(string)
	}
	if strings.EqualFold(strings.TrimSpace(effort), "none") {
		return false
	}
	changed := false
	for _, field := range []string{"temperature", "top_p", "top_logprobs", "logprobs"} {
		if _, exists := reqBody[field]; exists {
			delete(reqBody, field)
			changed = true
		}
	}
	return changed
}

// sanitizeGPT6ResponsesSamplingBytes 为 OAuth 透传链路保留原始 JSON，只删除不兼容字段。
func sanitizeGPT6ResponsesSamplingBytes(body []byte, model string) ([]byte, bool, error) {
	if len(body) == 0 || !openai.IsGPT6SolOrLunaModelSpelling(model) {
		return body, false, nil
	}
	if strings.EqualFold(strings.TrimSpace(gjson.GetBytes(body, "reasoning.effort").String()), "none") {
		return body, false, nil
	}

	normalized := body
	changed := false
	for _, field := range []string{"temperature", "top_p", "top_logprobs", "logprobs"} {
		if !gjson.GetBytes(normalized, field).Exists() {
			continue
		}
		next, err := sjson.DeleteBytes(normalized, field)
		if err != nil {
			return body, false, fmt.Errorf("sanitize GPT-6 sampling field %s: %w", field, err)
		}
		normalized = next
		changed = true
	}
	return normalized, changed, nil
}
