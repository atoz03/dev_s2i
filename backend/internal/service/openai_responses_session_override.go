package service

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var openAIResponsesSessionConflictFields = []string{
	"previous_response_id",
	"prompt_cache_retention",
	"safety_identifier",
}

// ResolveSessionIDWithFallback 按统一优先级解析原始会话键：
// session_id > conversation_id > body.prompt_cache_key > fallback。
func (s *OpenAIGatewayService) ResolveSessionIDWithFallback(c *gin.Context, body []byte, fallback string) string {
	if sessionID := s.ExtractSessionID(c, body); sessionID != "" {
		return sessionID
	}
	return strings.TrimSpace(fallback)
}

// GenerateSessionHashWithKeyFallback 在常规信号缺失时，使用 fallbackKey 生成稳定粘性会话哈希。
func (s *OpenAIGatewayService) GenerateSessionHashWithKeyFallback(c *gin.Context, body []byte, fallbackKey string) string {
	sessionHash := s.GenerateSessionHash(c, body)
	if sessionHash != "" {
		return sessionHash
	}

	key := strings.TrimSpace(fallbackKey)
	if key == "" {
		return ""
	}

	currentHash, legacyHash := deriveOpenAISessionHashes(key)
	attachOpenAILegacySessionHashToGin(c, legacyHash)
	return currentHash
}

func normalizeOpenAIResponsesPromptCacheBody(body []byte, promptCacheKey string) ([]byte, bool, error) {
	key := strings.TrimSpace(promptCacheKey)
	if len(body) == 0 || key == "" {
		return body, false, nil
	}

	normalized := body
	changed := false

	if existing := strings.TrimSpace(gjson.GetBytes(normalized, "prompt_cache_key").String()); existing != key {
		next, err := sjson.SetBytes(normalized, "prompt_cache_key", key)
		if err != nil {
			return body, false, fmt.Errorf("set prompt_cache_key: %w", err)
		}
		normalized = next
		changed = true
	}

	for _, field := range openAIResponsesSessionConflictFields {
		if !gjson.GetBytes(normalized, field).Exists() {
			continue
		}
		next, err := sjson.DeleteBytes(normalized, field)
		if err != nil {
			return body, false, fmt.Errorf("delete %s: %w", field, err)
		}
		normalized = next
		changed = true
	}

	return normalized, changed, nil
}

func buildOpenAIUpstreamSessionKey(c *gin.Context, rawSessionKey string) string {
	return isolateOpenAISessionID(getAPIKeyIDFromContext(c), rawSessionKey)
}

func applyOpenAIResponsesSessionHeaders(req *http.Request, c *gin.Context, upstreamSessionKey string, compact bool) {
	if req == nil {
		return
	}

	req.Header.Del("session_id")
	req.Header.Del("conversation_id")

	sessionKey := strings.TrimSpace(upstreamSessionKey)
	if sessionKey == "" && compact {
		sessionKey = buildOpenAIUpstreamSessionKey(c, resolveOpenAICompactSessionID(c))
	}
	if sessionKey == "" {
		return
	}

	req.Header.Set("session_id", sessionKey)
	if compact {
		return
	}
	req.Header.Set("conversation_id", sessionKey)
}
