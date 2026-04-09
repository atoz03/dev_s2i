package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSelectChatGPTAccountInfo_FallsBackFromExpiredMatchedOrgToActivePaidPlan(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)
	accounts := map[string]any{
		"team-org": map[string]any{
			"account": map[string]any{
				"plan_type":  "team",
				"is_default": true,
			},
			"entitlement": map[string]any{
				"expires_at": "2026-02-18T00:00:00Z",
			},
		},
		"personal-org": map[string]any{
			"account": map[string]any{
				"plan_type": "plus",
			},
			"entitlement": map[string]any{
				"expires_at": "2026-06-18T00:00:00Z",
			},
		},
	}

	info := selectChatGPTAccountInfo(accounts, "team-org", now)
	require.NotNil(t, info)
	require.Equal(t, "plus", info.PlanType)
	require.Equal(t, "2026-06-18T00:00:00Z", info.SubscriptionExpiresAt)
}

func TestSelectChatGPTAccountInfo_PrefersMatchedOrgWhenStillActive(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)
	accounts := map[string]any{
		"team-org": map[string]any{
			"account": map[string]any{
				"plan_type":  "team",
				"is_default": true,
			},
			"entitlement": map[string]any{
				"expires_at": "2026-07-18T00:00:00Z",
			},
		},
		"personal-org": map[string]any{
			"account": map[string]any{
				"plan_type": "plus",
			},
			"entitlement": map[string]any{
				"expires_at": "2026-06-18T00:00:00Z",
			},
		},
	}

	info := selectChatGPTAccountInfo(accounts, "team-org", now)
	require.NotNil(t, info)
	require.Equal(t, "team", info.PlanType)
	require.Equal(t, "2026-07-18T00:00:00Z", info.SubscriptionExpiresAt)
}

func TestSelectChatGPTAccountInfo_PrefersActivePaidPlanOverDefaultFreeWhenNoMatchedOrg(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)
	accounts := map[string]any{
		"default-free": map[string]any{
			"account": map[string]any{
				"plan_type":  "free",
				"is_default": true,
			},
		},
		"paid-plus": map[string]any{
			"account": map[string]any{
				"plan_type": "plus",
			},
			"entitlement": map[string]any{
				"expires_at": "2026-06-18T00:00:00Z",
			},
		},
	}

	info := selectChatGPTAccountInfo(accounts, "", now)
	require.NotNil(t, info)
	require.Equal(t, "plus", info.PlanType)
	require.Equal(t, "2026-06-18T00:00:00Z", info.SubscriptionExpiresAt)
}

func TestIsExpiredPaidSubscription(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)

	require.True(t, isExpiredPaidSubscription("team", "2026-02-18T00:00:00Z", now))
	require.False(t, isExpiredPaidSubscription("plus", "2026-06-18T00:00:00Z", now))
	require.False(t, isExpiredPaidSubscription("free", "", now))
	require.False(t, isExpiredPaidSubscription("team", "", now))
	require.False(t, isExpiredPaidSubscription("team", "invalid-time", now))
}
