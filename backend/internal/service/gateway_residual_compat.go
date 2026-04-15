package service

import (
	"context"
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
