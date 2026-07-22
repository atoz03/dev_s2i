//go:build unit

package repository

import (
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestBuildSchedulerSlimAccount_KeepsOpenAIWSFlags(t *testing.T) {
	account := service.Account{
		ID:       42,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
		Extra: map[string]any{
			"openai_oauth_responses_websockets_v2_enabled": true,
			"openai_oauth_responses_websockets_v2_mode":    service.OpenAIWSIngressModePassthrough,
			"openai_ws_force_http":                         true,
			"openai_responses_mode":                        "force_chat_completions",
			"openai_responses_supported":                   false,
			"mixed_scheduling":                             true,
		},
	}

	got := buildSchedulerSlimAccount(account)

	require.Equal(t, true, got.Extra["openai_oauth_responses_websockets_v2_enabled"])
	require.Equal(t, service.OpenAIWSIngressModePassthrough, got.Extra["openai_oauth_responses_websockets_v2_mode"])
	require.Equal(t, true, got.Extra["openai_ws_force_http"])
	require.Equal(t, "force_chat_completions", got.Extra["openai_responses_mode"])
	require.Equal(t, false, got.Extra["openai_responses_supported"])
	require.Equal(t, true, got.Extra["mixed_scheduling"])
}

func TestBuildSchedulerMetadataAccount_KeepsSlimGroupMembership(t *testing.T) {
	account := service.Account{
		ID:       42,
		Platform: service.PlatformAnthropic,
		GroupIDs: []int64{7, 9, 7, 0},
		AccountGroups: []service.AccountGroup{
			{
				AccountID: 42,
				GroupID:   7,
				Priority:  2,
				Account:   &service.Account{ID: 42, Name: "drop-from-metadata"},
				Group:     &service.Group{ID: 7, Name: "drop-from-metadata"},
			},
			{
				AccountID: 42,
				GroupID:   11,
				Priority:  3,
				Group:     &service.Group{ID: 11, Name: "drop-from-metadata"},
			},
			{
				AccountID: 42,
				GroupID:   0,
				Priority:  4,
			},
		},
	}

	got := buildSchedulerMetadataAccount(account)

	require.Equal(t, []int64{7, 9, 11}, got.GroupIDs)
	require.Len(t, got.AccountGroups, 2)
	require.Equal(t, int64(42), got.AccountGroups[0].AccountID)
	require.Equal(t, int64(7), got.AccountGroups[0].GroupID)
	require.Equal(t, 2, got.AccountGroups[0].Priority)
	require.Nil(t, got.AccountGroups[0].Account)
	require.Nil(t, got.AccountGroups[0].Group)
	require.Equal(t, int64(11), got.AccountGroups[1].GroupID)
	require.Nil(t, got.Groups)
}

func TestBuildSchedulerMetadataAccount_KeepsQuotaState(t *testing.T) {
	now := time.Now().UTC()
	account := service.Account{
		ID:       46690,
		Platform: service.PlatformGemini,
		Type:     service.AccountTypeAPIKey,
		Extra: map[string]any{
			"quota_daily_limit":      20.0,
			"quota_daily_used":       20.0,
			"quota_daily_start":      now.Add(-time.Hour).Format(time.RFC3339),
			"quota_daily_reset_mode": "rolling",
			"unrelated":              "drop me",
		},
	}

	got := buildSchedulerMetadataAccount(account)

	require.Equal(t, 20.0, got.Extra["quota_daily_limit"])
	require.Equal(t, 20.0, got.Extra["quota_daily_used"])
	require.Equal(t, "rolling", got.Extra["quota_daily_reset_mode"])
	require.NotContains(t, got.Extra, "unrelated")
	require.True(t, got.IsQuotaExceeded())
}

func TestApplySchedulerLastUsedKeepsNewestTimestamp(t *testing.T) {
	newer := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	account := &service.Account{LastUsedAt: &newer}

	require.NoError(t, applySchedulerLastUsed(account, newer.Add(-time.Minute).UnixMilli()))
	require.Equal(t, newer, *account.LastUsedAt)

	latest := newer.Add(time.Minute)
	require.NoError(t, applySchedulerLastUsed(account, latest.UnixMilli()))
	require.Equal(t, latest, *account.LastUsedAt)
}

func TestDecodeSchedulerLastUsedSupportsLegacyNanosecondsAndMilliseconds(t *testing.T) {
	want := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

	legacy, err := decodeSchedulerLastUsed(strconv.FormatInt(want.UnixNano(), 10))
	require.NoError(t, err)
	require.Equal(t, want, *legacy)

	current, err := decodeSchedulerLastUsed(strconv.FormatInt(want.UnixMilli(), 10))
	require.NoError(t, err)
	require.Equal(t, want, *current)
}
