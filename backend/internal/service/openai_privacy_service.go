package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/imroc/req/v3"
)

// PrivacyClientFactory creates an HTTP client for privacy API calls.
// Injected from repository layer to avoid import cycles.
type PrivacyClientFactory func(proxyURL string) (*req.Client, error)

const (
	openAISettingsURL = "https://chatgpt.com/backend-api/settings/account_user_setting"

	PrivacyModeTrainingOff = "training_off"
	PrivacyModeFailed      = "training_set_failed"
	PrivacyModeCFBlocked   = "training_set_cf_blocked"
)

func shouldSkipOpenAIPrivacyEnsure(extra map[string]any) bool {
	if extra == nil {
		return false
	}
	raw, ok := extra["privacy_mode"]
	if !ok {
		return false
	}
	mode, _ := raw.(string)
	mode = strings.TrimSpace(mode)
	return mode != PrivacyModeFailed && mode != PrivacyModeCFBlocked
}

// disableOpenAITraining calls ChatGPT settings API to turn off "Improve the model for everyone".
// Returns privacy_mode value: "training_off" on success, "cf_blocked" / "failed" on failure.
func disableOpenAITraining(ctx context.Context, clientFactory PrivacyClientFactory, accessToken, proxyURL string) string {
	if accessToken == "" || clientFactory == nil {
		return ""
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	client, err := clientFactory(proxyURL)
	if err != nil {
		slog.Warn("openai_privacy_client_error", "error", err.Error())
		return PrivacyModeFailed
	}

	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+accessToken).
		SetHeader("Origin", "https://chatgpt.com").
		SetHeader("Referer", "https://chatgpt.com/").
		SetHeader("Accept", "application/json").
		SetHeader("sec-fetch-mode", "cors").
		SetHeader("sec-fetch-site", "same-origin").
		SetHeader("sec-fetch-dest", "empty").
		SetQueryParam("feature", "training_allowed").
		SetQueryParam("value", "false").
		Patch(openAISettingsURL)

	if err != nil {
		slog.Warn("openai_privacy_request_error", "error", err.Error())
		return PrivacyModeFailed
	}

	if resp.StatusCode == 403 || resp.StatusCode == 503 {
		body := resp.String()
		if strings.Contains(body, "cloudflare") || strings.Contains(body, "cf-") || strings.Contains(body, "Just a moment") {
			slog.Warn("openai_privacy_cf_blocked", "status", resp.StatusCode)
			return PrivacyModeCFBlocked
		}
	}

	if !resp.IsSuccessState() {
		slog.Warn("openai_privacy_failed", "status", resp.StatusCode, "body", truncate(resp.String(), 200))
		return PrivacyModeFailed
	}

	slog.Info("openai_privacy_training_disabled")
	return PrivacyModeTrainingOff
}

// ChatGPTAccountInfo 从 chatgpt.com/backend-api/accounts/check 获取的账号信息
type ChatGPTAccountInfo struct {
	PlanType              string
	Email                 string
	SubscriptionExpiresAt string // entitlement.expires_at (RFC3339)
}

type chatGPTAccountCandidate struct {
	info      ChatGPTAccountInfo
	isDefault bool
	isPaid    bool
	isExpired bool
}

const chatGPTAccountsCheckURL = "https://chatgpt.com/backend-api/accounts/check/v4-2023-04-27"

// fetchChatGPTAccountInfo calls ChatGPT backend-api to get account info (plan_type, etc.).
// Used as fallback when id_token doesn't contain these fields (e.g., Mobile RT).
// orgID is used to match the correct account when multiple accounts exist (e.g., personal + team).
// Returns nil on any failure (best-effort, non-blocking).
func fetchChatGPTAccountInfo(ctx context.Context, clientFactory PrivacyClientFactory, accessToken, proxyURL, orgID string) *ChatGPTAccountInfo {
	if accessToken == "" || clientFactory == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	client, err := clientFactory(proxyURL)
	if err != nil {
		slog.Debug("chatgpt_account_check_client_error", "error", err.Error())
		return nil
	}

	var result map[string]any
	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+accessToken).
		SetHeader("Origin", "https://chatgpt.com").
		SetHeader("Referer", "https://chatgpt.com/").
		SetHeader("Accept", "application/json").
		SetSuccessResult(&result).
		Get(chatGPTAccountsCheckURL)

	if err != nil {
		slog.Debug("chatgpt_account_check_request_error", "error", err.Error())
		return nil
	}

	if !resp.IsSuccessState() {
		slog.Debug("chatgpt_account_check_failed", "status", resp.StatusCode, "body", truncate(resp.String(), 200))
		return nil
	}

	info := &ChatGPTAccountInfo{}

	accounts, ok := result["accounts"].(map[string]any)
	if !ok {
		slog.Debug("chatgpt_account_check_no_accounts", "body", truncate(resp.String(), 300))
		return nil
	}

	selected := selectChatGPTAccountInfo(accounts, orgID, time.Now())
	if selected != nil {
		*info = *selected
	}

	if info.PlanType == "" {
		slog.Debug("chatgpt_account_check_no_plan_type", "body", truncate(resp.String(), 300))
		return nil
	}

	slog.Info("chatgpt_account_check_success", "plan_type", info.PlanType, "subscription_expires_at", info.SubscriptionExpiresAt, "org_id", orgID)
	return info
}

// fillAccountInfo 从单个 account 对象中提取 plan_type 和 subscription_expires_at
func fillAccountInfo(info *ChatGPTAccountInfo, acct map[string]any) {
	info.PlanType = extractPlanType(acct)
	info.SubscriptionExpiresAt = extractEntitlementExpiresAt(acct)
}

// selectChatGPTAccountInfo 在多个账号中选择最适合展示的套餐信息。
// 选择原则：
// 1. 如果 orgID 对应账号仍有效，优先使用该账号；
// 2. 如果 orgID 对应的是已过期付费计划，则回退到其他仍有效的账号；
// 3. 未命中 orgID 时，有效账号的优先级为：默认有效付费账号 > 其他有效付费账号 > 默认有效账号 > 其他有效账号；
// 4. 如果所有账号都无有效套餐，则回退到旧规则，至少返回一个可展示结果。
func selectChatGPTAccountInfo(accounts map[string]any, orgID string, now time.Time) *ChatGPTAccountInfo {
	var (
		matchedCandidate  *chatGPTAccountCandidate
		defaultPaidActive *chatGPTAccountCandidate
		defaultActive     *chatGPTAccountCandidate
		paidActive        *chatGPTAccountCandidate
		anyActive         *chatGPTAccountCandidate
		defaultAny        *chatGPTAccountCandidate
		paidAny           *chatGPTAccountCandidate
		anyCandidate      *chatGPTAccountCandidate
	)

	for accountID, acctRaw := range accounts {
		acct, ok := acctRaw.(map[string]any)
		if !ok {
			continue
		}

		candidate := buildChatGPTAccountCandidate(acct, now)
		if candidate == nil {
			continue
		}

		if anyCandidate == nil {
			anyCandidate = candidate
		}
		if candidate.isDefault && defaultAny == nil {
			defaultAny = candidate
		}
		if candidate.isPaid && paidAny == nil {
			paidAny = candidate
		}
		if !candidate.isExpired {
			if anyActive == nil {
				anyActive = candidate
			}
			if candidate.isDefault && candidate.isPaid && defaultPaidActive == nil {
				defaultPaidActive = candidate
			}
			if candidate.isDefault && defaultActive == nil {
				defaultActive = candidate
			}
			if candidate.isPaid && paidActive == nil {
				paidActive = candidate
			}
		}
		if orgID != "" && accountID == orgID {
			matchedCandidate = candidate
		}
	}

	if matchedCandidate != nil && !matchedCandidate.isExpired {
		return &matchedCandidate.info
	}

	switch {
	case defaultPaidActive != nil:
		return &defaultPaidActive.info
	case paidActive != nil:
		return &paidActive.info
	case defaultActive != nil:
		return &defaultActive.info
	case anyActive != nil:
		return &anyActive.info
	case matchedCandidate != nil:
		return &matchedCandidate.info
	case defaultAny != nil:
		return &defaultAny.info
	case paidAny != nil:
		return &paidAny.info
	case anyCandidate != nil:
		return &anyCandidate.info
	default:
		return nil
	}
}

func buildChatGPTAccountCandidate(acct map[string]any, now time.Time) *chatGPTAccountCandidate {
	info := ChatGPTAccountInfo{}
	fillAccountInfo(&info, acct)
	if info.PlanType == "" {
		return nil
	}

	candidate := &chatGPTAccountCandidate{
		info:      info,
		isPaid:    !strings.EqualFold(info.PlanType, "free"),
		isExpired: isExpiredPaidSubscription(info.PlanType, info.SubscriptionExpiresAt, now),
	}
	if account, ok := acct["account"].(map[string]any); ok {
		candidate.isDefault, _ = account["is_default"].(bool)
	}

	return candidate
}

func isExpiredPaidSubscription(planType, expiresAt string, now time.Time) bool {
	if strings.EqualFold(strings.TrimSpace(planType), "free") {
		return false
	}
	expiresAt = strings.TrimSpace(expiresAt)
	if expiresAt == "" {
		return false
	}

	parsed, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return false
	}
	return !parsed.After(now)
}

// extractPlanType 从单个 account 对象中提取 plan_type
func extractPlanType(acct map[string]any) string {
	if account, ok := acct["account"].(map[string]any); ok {
		if planType, ok := account["plan_type"].(string); ok && planType != "" {
			return planType
		}
	}
	if entitlement, ok := acct["entitlement"].(map[string]any); ok {
		if subPlan, ok := entitlement["subscription_plan"].(string); ok && subPlan != "" {
			return subPlan
		}
	}
	return ""
}

// extractEntitlementExpiresAt 从 entitlement 中提取 expires_at。
// 预期为 RFC3339 字符串格式，如 "2026-05-02T20:32:12+00:00"。
func extractEntitlementExpiresAt(acct map[string]any) string {
	entitlement, ok := acct["entitlement"].(map[string]any)
	if !ok {
		return ""
	}
	ea, _ := entitlement["expires_at"].(string)
	return ea
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("...(%d more)", len(s)-n)
}
