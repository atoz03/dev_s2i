package service

import (
	"context"
	"strings"
	"time"
)

// IsSchedulableForModelWithContext 返回账号在当前上下文下是否仍可用于指定模型。
func (a *Account) IsSchedulableForModelWithContext(_ context.Context, requestedModel string) bool {
	return a.IsSchedulable() && a.IsModelSupported(requestedModel)
}

// GetRateLimitRemainingTimeWithContext 返回账号当前的剩余限流时间。
// 已移除 antigravity 专用限流窗口后，默认返回 0。
func (a *Account) GetRateLimitRemainingTimeWithContext(_ context.Context, _ string) time.Duration {
	return 0
}

// tempUnscheduleGoogleConfigError 在移除 antigravity/gemini 混合链路后不再做额外处理。
func tempUnscheduleGoogleConfigError(_ context.Context, _ AccountRepository, _ int64, _ string) {}

// tempUnscheduleEmptyResponse 在移除 antigravity/gemini 混合链路后不再做额外处理。
func tempUnscheduleEmptyResponse(_ context.Context, _ AccountRepository, _ int64, _ string) {}

// mapAntigravityModel 退化为账号级映射解析；未命中时返回原模型名。
func mapAntigravityModel(account *Account, requestedModel string) string {
	if account == nil {
		return strings.TrimSpace(requestedModel)
	}
	mapped, _ := account.ResolveMappedModel(requestedModel)
	return strings.TrimSpace(mapped)
}

// applyThinkingModelSuffix 在移除 antigravity 专用后缀规则后直接返回映射模型。
func applyThinkingModelSuffix(mappedModel string, _ bool) string {
	return strings.TrimSpace(mappedModel)
}
