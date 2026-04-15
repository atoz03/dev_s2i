package service

import (
	"encoding/json"
	"strings"
)

const (
	forbiddenTypeValidation = "validation"
	forbiddenTypeViolation  = "violation"
)

func isGoogleProjectConfigError(message string) bool {
	msg := strings.ToLower(strings.TrimSpace(message))
	if msg == "" {
		return false
	}
	return strings.Contains(msg, "project") && (strings.Contains(msg, "missing") || strings.Contains(msg, "config"))
}

func extractAntigravityErrorMessage(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return trimmed
	}
	for _, key := range []string{"message", "error", "detail"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return trimmed
}

func filterEmptyPartsFromGeminiRequest(body []byte) ([]byte, error) {
	return body, nil
}

func classifyForbiddenType(message string) string {
	lower := strings.ToLower(message)
	if strings.Contains(lower, "validation") || strings.Contains(lower, "verify") {
		return forbiddenTypeValidation
	}
	return forbiddenTypeViolation
}

func extractValidationURL(message string) string {
	for _, token := range strings.Fields(message) {
		if strings.HasPrefix(token, "http://") || strings.HasPrefix(token, "https://") {
			return token
		}
	}
	return ""
}
