package service

import (
	"context"
	"strings"
)

// GetUpstreamUserAgentSettings 返回上游请求默认 User-Agent 与是否强制统一。
func (s *SettingService) GetUpstreamUserAgentSettings(ctx context.Context) (string, bool) {
	if s == nil || s.settingRepo == nil {
		return "", false
	}
	values, err := s.settingRepo.GetMultiple(ctx, []string{SettingKeyDefaultUpstreamUserAgent, SettingKeyForceUnifiedUpstreamUserAgent})
	if err != nil {
		return "", false
	}
	defaultUA := strings.TrimSpace(values[SettingKeyDefaultUpstreamUserAgent])
	forceUnified := strings.EqualFold(strings.TrimSpace(values[SettingKeyForceUnifiedUpstreamUserAgent]), "true")
	return defaultUA, forceUnified
}
