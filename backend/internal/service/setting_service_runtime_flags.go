package service

import (
	"context"
	"strconv"
	"strings"
)

// ChannelMonitorRuntime is the lightweight view used by runner/user handler.
type ChannelMonitorRuntime struct {
	Enabled                bool
	DefaultIntervalSeconds int
}

// AvailableChannelsRuntime is the lightweight switch used by user handler.
type AvailableChannelsRuntime struct {
	Enabled bool
}

const (
	channelMonitorIntervalMin      = 15
	channelMonitorIntervalMax      = 3600
	channelMonitorIntervalFallback = 60
)

// GetChannelMonitorRuntime 从设置表读取监控开关与默认间隔。
// 读取失败时采用 fail-open：保持功能可用，间隔回退到默认值。
func (s *SettingService) GetChannelMonitorRuntime(ctx context.Context) ChannelMonitorRuntime {
	vals, err := s.settingRepo.GetMultiple(ctx, []string{
		SettingKeyChannelMonitorEnabled,
		SettingKeyChannelMonitorDefaultIntervalSeconds,
	})
	if err != nil {
		return ChannelMonitorRuntime{Enabled: true, DefaultIntervalSeconds: channelMonitorIntervalFallback}
	}
	return ChannelMonitorRuntime{
		Enabled:                !isFalseSettingValue(vals[SettingKeyChannelMonitorEnabled]),
		DefaultIntervalSeconds: parseChannelMonitorInterval(vals[SettingKeyChannelMonitorDefaultIntervalSeconds]),
	}
}

// GetAvailableChannelsRuntime 从设置表读取可用渠道页面开关。
// 读取失败时采用 fail-closed，避免异常时错误放开入口。
func (s *SettingService) GetAvailableChannelsRuntime(ctx context.Context) AvailableChannelsRuntime {
	vals, err := s.settingRepo.GetMultiple(ctx, []string{SettingKeyAvailableChannelsEnabled})
	if err != nil {
		return AvailableChannelsRuntime{Enabled: false}
	}
	return AvailableChannelsRuntime{Enabled: vals[SettingKeyAvailableChannelsEnabled] == "true"}
}

func parseChannelMonitorInterval(raw string) int {
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return channelMonitorIntervalFallback
	}
	return clampChannelMonitorInterval(v)
}

func clampChannelMonitorInterval(v int) int {
	if v <= 0 {
		return 0
	}
	if v < channelMonitorIntervalMin {
		return channelMonitorIntervalMin
	}
	if v > channelMonitorIntervalMax {
		return channelMonitorIntervalMax
	}
	return v
}
