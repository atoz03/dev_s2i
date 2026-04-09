package repository

import (
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestBuildSchedulerSlimAccount_PreservesSchedulingMetadataAndDropsSensitivePayload(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	limitReset := now.Add(10 * time.Minute)
	overloadUntil := now.Add(2 * time.Minute)
	tempUnschedUntil := now.Add(3 * time.Minute)
	windowEnd := now.Add(5 * time.Hour)

	account := service.Account{
		ID:          101,
		Name:        "gemini-heavy",
		Platform:    service.PlatformGemini,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 3,
		Priority:    7,
		LastUsedAt:  &now,
		Credentials: map[string]any{
			"api_key":       "gemini-api-key",
			"access_token":  "secret-access-token",
			"project_id":    "proj-1",
			"oauth_type":    "ai_studio",
			"model_mapping": map[string]any{"gemini-2.5-pro": "gemini-2.5-pro"},
			"huge_blob":     strings.Repeat("x", 4096),
		},
		Extra: map[string]any{
			"mixed_scheduling":             true,
			"window_cost_limit":            12.5,
			"window_cost_sticky_reserve":   8.0,
			"max_sessions":                 4,
			"session_idle_timeout_minutes": 11,
			"unused_large_field":           strings.Repeat("y", 4096),
		},
		RateLimitResetAt:       &limitReset,
		OverloadUntil:          &overloadUntil,
		TempUnschedulableUntil: &tempUnschedUntil,
		SessionWindowStart:     &now,
		SessionWindowEnd:       &windowEnd,
		SessionWindowStatus:    "active",
	}

	slim := buildSchedulerSlimAccount(account)

	require.Equal(t, "gemini-api-key", slim.GetCredential("api_key"))
	require.Equal(t, "proj-1", slim.GetCredential("project_id"))
	require.Equal(t, "ai_studio", slim.GetCredential("oauth_type"))
	require.NotEmpty(t, slim.GetModelMapping())
	require.Empty(t, slim.GetCredential("access_token"))
	require.Empty(t, slim.GetCredential("huge_blob"))
	require.Equal(t, true, slim.Extra["mixed_scheduling"])
	require.Equal(t, 12.5, slim.GetWindowCostLimit())
	require.Equal(t, 8.0, slim.GetWindowCostStickyReserve())
	require.Equal(t, 4, slim.GetMaxSessions())
	require.Equal(t, 11, slim.GetSessionIdleTimeoutMinutes())
	require.Nil(t, slim.Extra["unused_large_field"])
}

func TestMarshalSchedulerCachedAccounts_KeepsFullAccountForHydration(t *testing.T) {
	account := service.Account{
		ID:       9,
		Platform: service.PlatformAnthropic,
		Type:     service.AccountTypeOAuth,
		Credentials: map[string]any{
			"api_key":      "anthropic-api-key",
			"access_token": "secret-access-token",
		},
	}

	slimPayload, fullPayload, err := marshalSchedulerCachedAccounts(account)
	require.NoError(t, err)

	slim, err := decodeCachedAccount(slimPayload)
	require.NoError(t, err)
	require.NotNil(t, slim)
	require.Equal(t, "anthropic-api-key", slim.GetCredential("api_key"))
	require.Empty(t, slim.GetCredential("access_token"))

	full, err := decodeCachedAccount(fullPayload)
	require.NoError(t, err)
	require.NotNil(t, full)
	require.Equal(t, "anthropic-api-key", full.GetCredential("api_key"))
	require.Equal(t, "secret-access-token", full.GetCredential("access_token"))
}
