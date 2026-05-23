package admin

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// semverPattern 预编译 semver 格式校验正则
var semverPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// menuItemIDPattern validates custom menu item IDs: alphanumeric, hyphens, underscores only.
var menuItemIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

var allowedHiddenAdminMenuItemKeys = map[string]struct{}{
	"ops":              {},
	"users":            {},
	"groups":           {},
	"channels":         {},
	"subscriptions":    {},
	"accounts":         {},
	"announcements":    {},
	"proxies":          {},
	"redeem":           {},
	"promoCodes":       {},
	"paymentDashboard": {},
	"paymentOrders":    {},
	"paymentPlans":     {},
	"usage":            {},
}

// generateMenuItemID generates a short random hex ID for a custom menu item.
func generateMenuItemID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate menu item ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func scopesContainOpenID(scopes string) bool {
	for _, scope := range strings.Fields(strings.ToLower(strings.TrimSpace(scopes))) {
		if scope == "openid" {
			return true
		}
	}
	return false
}

// SettingHandler 系统设置处理器
type SettingHandler struct {
	settingService           *service.SettingService
	emailService             *service.EmailService
	turnstileService         *service.TurnstileService
	opsService               *service.OpsService
	endpointProbeService     *service.EndpointProbeService
	endpointProbePlanService *service.EndpointProbePlanService
	paymentConfigService     *service.PaymentConfigService
	paymentService           *service.PaymentService
}

// NewSettingHandler 创建系统设置处理器
func NewSettingHandler(
	settingService *service.SettingService,
	emailService *service.EmailService,
	turnstileService *service.TurnstileService,
	opsService *service.OpsService,
	optionalDeps ...any,
) *SettingHandler {
	endpointProbeService, endpointProbePlanService, paymentConfigService, paymentService := parseSettingHandlerOptionalDeps(optionalDeps...)
	return &SettingHandler{
		settingService:           settingService,
		emailService:             emailService,
		turnstileService:         turnstileService,
		opsService:               opsService,
		endpointProbeService:     endpointProbeService,
		endpointProbePlanService: endpointProbePlanService,
		paymentConfigService:     paymentConfigService,
		paymentService:           paymentService,
	}
}

func parseSettingHandlerOptionalDeps(optionalDeps ...any) (
	endpointProbeService *service.EndpointProbeService,
	endpointProbePlanService *service.EndpointProbePlanService,
	paymentConfigService *service.PaymentConfigService,
	paymentService *service.PaymentService,
) {
	castEndpointProbeService := func(v any) *service.EndpointProbeService {
		svc, _ := v.(*service.EndpointProbeService)
		return svc
	}
	castEndpointProbePlanService := func(v any) *service.EndpointProbePlanService {
		svc, _ := v.(*service.EndpointProbePlanService)
		return svc
	}
	castPaymentConfigService := func(v any) *service.PaymentConfigService {
		svc, _ := v.(*service.PaymentConfigService)
		return svc
	}
	castPaymentService := func(v any) *service.PaymentService {
		svc, _ := v.(*service.PaymentService)
		return svc
	}

	switch len(optionalDeps) {
	case 2:
		// 兼容旧调用：NewSettingHandler(setting, email, turnstile, ops, paymentConfig, payment)
		paymentConfigService = castPaymentConfigService(optionalDeps[0])
		paymentService = castPaymentService(optionalDeps[1])
		return
	case 4:
		// 当前调用：NewSettingHandler(setting, email, turnstile, ops, endpointProbe, endpointProbePlan, paymentConfig, payment)
		endpointProbeService = castEndpointProbeService(optionalDeps[0])
		endpointProbePlanService = castEndpointProbePlanService(optionalDeps[1])
		paymentConfigService = castPaymentConfigService(optionalDeps[2])
		paymentService = castPaymentService(optionalDeps[3])
		return
	case 5:
		// 兼容历史调用（包含已移除的 soraS3Storage 占位参数）。
		endpointProbeService = castEndpointProbeService(optionalDeps[1])
		endpointProbePlanService = castEndpointProbePlanService(optionalDeps[2])
		paymentConfigService = castPaymentConfigService(optionalDeps[3])
		paymentService = castPaymentService(optionalDeps[4])
		return
	}

	// 兜底：按类型识别，避免未来可选参数顺序变化导致崩溃。
	for _, dep := range optionalDeps {
		switch typed := dep.(type) {
		case *service.EndpointProbeService:
			if endpointProbeService == nil {
				endpointProbeService = typed
			}
		case *service.EndpointProbePlanService:
			if endpointProbePlanService == nil {
				endpointProbePlanService = typed
			}
		case *service.PaymentConfigService:
			if paymentConfigService == nil {
				paymentConfigService = typed
			}
		case *service.PaymentService:
			if paymentService == nil {
				paymentService = typed
			}
		}
	}
	return
}

// GetSettings 获取所有系统设置
// GET /api/v1/admin/settings
func (h *SettingHandler) GetSettings(c *gin.Context) {
	settings, err := h.settingService.GetAllSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	// Check if ops monitoring is enabled (respects config.ops.enabled)
	opsEnabled := h.opsService != nil && h.opsService.IsMonitoringEnabled(c.Request.Context())
	defaultSubscriptions := make([]dto.DefaultSubscriptionSetting, 0, len(settings.DefaultSubscriptions))
	for _, sub := range settings.DefaultSubscriptions {
		defaultSubscriptions = append(defaultSubscriptions, dto.DefaultSubscriptionSetting{
			GroupID:      sub.GroupID,
			ValidityDays: sub.ValidityDays,
		})
	}
	authSourceDefaults, err := h.settingService.GetAuthSourceDefaultSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	authSourceEmailSubscriptions := dtoDefaultSubscriptionsFromService(authSourceDefaults.Email.Subscriptions)
	authSourceLinuxDoSubscriptions := dtoDefaultSubscriptionsFromService(authSourceDefaults.LinuxDo.Subscriptions)
	authSourceOIDCSubscriptions := dtoDefaultSubscriptionsFromService(authSourceDefaults.OIDC.Subscriptions)
	authSourceWeChatSubscriptions := dtoDefaultSubscriptionsFromService(authSourceDefaults.WeChat.Subscriptions)
	authSourceGitHubSubscriptions := dtoDefaultSubscriptionsFromService(authSourceDefaults.GitHub.Subscriptions)
	authSourceGoogleSubscriptions := dtoDefaultSubscriptionsFromService(authSourceDefaults.Google.Subscriptions)
	authSourceDingTalkSubscriptions := dtoDefaultSubscriptionsFromService(authSourceDefaults.DingTalk.Subscriptions)

	// Load payment config
	var paymentCfg *service.PaymentConfig
	if h.paymentConfigService != nil {
		paymentCfg, _ = h.paymentConfigService.GetPaymentConfig(c.Request.Context())
	}
	if paymentCfg == nil {
		paymentCfg = &service.PaymentConfig{}
	}

	response.Success(c, dto.SystemSettings{
		RegistrationEnabled:                       settings.RegistrationEnabled,
		EmailVerifyEnabled:                        settings.EmailVerifyEnabled,
		RegistrationEmailSuffixWhitelist:          settings.RegistrationEmailSuffixWhitelist,
		PromoCodeEnabled:                          settings.PromoCodeEnabled,
		PasswordResetEnabled:                      settings.PasswordResetEnabled,
		FrontendURL:                               settings.FrontendURL,
		InvitationCodeEnabled:                     settings.InvitationCodeEnabled,
		TotpEnabled:                               settings.TotpEnabled,
		TotpEncryptionKeyConfigured:               h.settingService.IsTotpEncryptionKeyConfigured(),
		LoginAgreementEnabled:                     settings.LoginAgreementEnabled,
		LoginAgreementMode:                        settings.LoginAgreementMode,
		LoginAgreementUpdatedAt:                   settings.LoginAgreementUpdatedAt,
		LoginAgreementDocuments:                   loginAgreementDocumentsToDTO(settings.LoginAgreementDocuments),
		SMTPHost:                                  settings.SMTPHost,
		SMTPPort:                                  settings.SMTPPort,
		SMTPUsername:                              settings.SMTPUsername,
		SMTPPasswordConfigured:                    settings.SMTPPasswordConfigured,
		SMTPFrom:                                  settings.SMTPFrom,
		SMTPFromName:                              settings.SMTPFromName,
		SMTPUseTLS:                                settings.SMTPUseTLS,
		TurnstileEnabled:                          settings.TurnstileEnabled,
		TurnstileSiteKey:                          settings.TurnstileSiteKey,
		TurnstileSecretKeyConfigured:              settings.TurnstileSecretKeyConfigured,
		LinuxDoConnectEnabled:                     settings.LinuxDoConnectEnabled,
		LinuxDoConnectClientID:                    settings.LinuxDoConnectClientID,
		LinuxDoConnectClientSecretConfigured:      settings.LinuxDoConnectClientSecretConfigured,
		LinuxDoConnectRedirectURL:                 settings.LinuxDoConnectRedirectURL,
		DingTalkConnectEnabled:                    settings.DingTalkConnectEnabled,
		DingTalkConnectClientID:                   settings.DingTalkConnectClientID,
		DingTalkConnectClientSecretConfigured:     settings.DingTalkConnectClientSecretConfigured,
		DingTalkConnectRedirectURL:                settings.DingTalkConnectRedirectURL,
		DingTalkConnectCorpRestrictionPolicy:      settings.DingTalkConnectCorpRestrictionPolicy,
		DingTalkConnectInternalCorpID:             settings.DingTalkConnectInternalCorpID,
		DingTalkConnectBypassRegistration:         settings.DingTalkConnectBypassRegistration,
		DingTalkConnectSyncCorpEmail:              settings.DingTalkConnectSyncCorpEmail,
		DingTalkConnectSyncDisplayName:            settings.DingTalkConnectSyncDisplayName,
		DingTalkConnectSyncDept:                   settings.DingTalkConnectSyncDept,
		DingTalkConnectSyncCorpEmailAttrKey:       settings.DingTalkConnectSyncCorpEmailAttrKey,
		DingTalkConnectSyncDisplayNameAttrKey:     settings.DingTalkConnectSyncDisplayNameAttrKey,
		DingTalkConnectSyncDeptAttrKey:            settings.DingTalkConnectSyncDeptAttrKey,
		DingTalkConnectSyncCorpEmailAttrName:      settings.DingTalkConnectSyncCorpEmailAttrName,
		DingTalkConnectSyncDisplayNameAttrName:    settings.DingTalkConnectSyncDisplayNameAttrName,
		DingTalkConnectSyncDeptAttrName:           settings.DingTalkConnectSyncDeptAttrName,
		WeChatConnectEnabled:                      settings.WeChatConnectEnabled,
		WeChatConnectAppID:                        settings.WeChatConnectAppID,
		WeChatConnectAppSecretConfigured:          settings.WeChatConnectAppSecretConfigured,
		WeChatConnectOpenAppID:                    settings.WeChatConnectOpenAppID,
		WeChatConnectOpenAppSecretConfigured:      settings.WeChatConnectOpenAppSecretConfigured,
		WeChatConnectMPAppID:                      settings.WeChatConnectMPAppID,
		WeChatConnectMPAppSecretConfigured:        settings.WeChatConnectMPAppSecretConfigured,
		WeChatConnectMobileAppID:                  settings.WeChatConnectMobileAppID,
		WeChatConnectMobileAppSecretConfigured:    settings.WeChatConnectMobileAppSecretConfigured,
		WeChatConnectOpenEnabled:                  settings.WeChatConnectOpenEnabled,
		WeChatConnectMPEnabled:                    settings.WeChatConnectMPEnabled,
		WeChatConnectMobileEnabled:                settings.WeChatConnectMobileEnabled,
		WeChatConnectMode:                         settings.WeChatConnectMode,
		WeChatConnectScopes:                       settings.WeChatConnectScopes,
		WeChatConnectRedirectURL:                  settings.WeChatConnectRedirectURL,
		WeChatConnectFrontendRedirectURL:          settings.WeChatConnectFrontendRedirectURL,
		OIDCConnectEnabled:                        settings.OIDCConnectEnabled,
		OIDCConnectProviderName:                   settings.OIDCConnectProviderName,
		OIDCConnectClientID:                       settings.OIDCConnectClientID,
		OIDCConnectClientSecretConfigured:         settings.OIDCConnectClientSecretConfigured,
		OIDCConnectIssuerURL:                      settings.OIDCConnectIssuerURL,
		OIDCConnectDiscoveryURL:                   settings.OIDCConnectDiscoveryURL,
		OIDCConnectAuthorizeURL:                   settings.OIDCConnectAuthorizeURL,
		OIDCConnectTokenURL:                       settings.OIDCConnectTokenURL,
		OIDCConnectUserInfoURL:                    settings.OIDCConnectUserInfoURL,
		OIDCConnectJWKSURL:                        settings.OIDCConnectJWKSURL,
		OIDCConnectScopes:                         settings.OIDCConnectScopes,
		OIDCConnectRedirectURL:                    settings.OIDCConnectRedirectURL,
		OIDCConnectFrontendRedirectURL:            settings.OIDCConnectFrontendRedirectURL,
		OIDCConnectTokenAuthMethod:                settings.OIDCConnectTokenAuthMethod,
		OIDCConnectUsePKCE:                        settings.OIDCConnectUsePKCE,
		OIDCConnectValidateIDToken:                settings.OIDCConnectValidateIDToken,
		OIDCConnectAllowedSigningAlgs:             settings.OIDCConnectAllowedSigningAlgs,
		OIDCConnectClockSkewSeconds:               settings.OIDCConnectClockSkewSeconds,
		OIDCConnectRequireEmailVerified:           settings.OIDCConnectRequireEmailVerified,
		OIDCConnectUserInfoEmailPath:              settings.OIDCConnectUserInfoEmailPath,
		OIDCConnectUserInfoIDPath:                 settings.OIDCConnectUserInfoIDPath,
		OIDCConnectUserInfoUsernamePath:           settings.OIDCConnectUserInfoUsernamePath,
		GitHubOAuthEnabled:                        settings.GitHubOAuthEnabled,
		GitHubOAuthClientID:                       settings.GitHubOAuthClientID,
		GitHubOAuthClientSecretConfigured:         settings.GitHubOAuthClientSecretConfigured,
		GitHubOAuthRedirectURL:                    settings.GitHubOAuthRedirectURL,
		GitHubOAuthFrontendRedirectURL:            settings.GitHubOAuthFrontendRedirectURL,
		GoogleOAuthEnabled:                        settings.GoogleOAuthEnabled,
		GoogleOAuthClientID:                       settings.GoogleOAuthClientID,
		GoogleOAuthClientSecretConfigured:         settings.GoogleOAuthClientSecretConfigured,
		GoogleOAuthRedirectURL:                    settings.GoogleOAuthRedirectURL,
		GoogleOAuthFrontendRedirectURL:            settings.GoogleOAuthFrontendRedirectURL,
		SiteName:                                  settings.SiteName,
		SiteLogo:                                  settings.SiteLogo,
		SiteSubtitle:                              settings.SiteSubtitle,
		APIBaseURL:                                settings.APIBaseURL,
		ContactInfo:                               settings.ContactInfo,
		DocURL:                                    settings.DocURL,
		HomeContent:                               settings.HomeContent,
		HideCcsImportButton:                       settings.HideCcsImportButton,
		PurchaseSubscriptionEnabled:               settings.PurchaseSubscriptionEnabled,
		PurchaseSubscriptionURL:                   settings.PurchaseSubscriptionURL,
		TableDefaultPageSize:                      settings.TableDefaultPageSize,
		TablePageSizeOptions:                      settings.TablePageSizeOptions,
		HiddenAdminMenuItems:                      dto.ParseHiddenAdminMenuItems(settings.HiddenAdminMenuItems),
		CustomMenuItems:                           dto.ParseCustomMenuItems(settings.CustomMenuItems),
		CustomEndpoints:                           dto.ParseCustomEndpoints(settings.CustomEndpoints),
		DefaultConcurrency:                        settings.DefaultConcurrency,
		DefaultBalance:                            settings.DefaultBalance,
		AffiliateEnabled:                          settings.AffiliateEnabled,
		AffiliateRebateRate:                       settings.AffiliateRebateRate,
		AffiliateRebateFreezeHours:                settings.AffiliateRebateFreezeHours,
		AffiliateRebateDurationDays:               settings.AffiliateRebateDurationDays,
		AffiliateRebatePerInviteeCap:              settings.AffiliateRebatePerInviteeCap,
		DefaultUserRPMLimit:                       settings.DefaultUserRPMLimit,
		DefaultSubscriptions:                      defaultSubscriptions,
		AuthSourceDefaultEmailBalance:             authSourceDefaults.Email.Balance,
		AuthSourceDefaultEmailConcurrency:         authSourceDefaults.Email.Concurrency,
		AuthSourceDefaultEmailSubscriptions:       authSourceEmailSubscriptions,
		AuthSourceDefaultEmailGrantOnSignup:       authSourceDefaults.Email.GrantOnSignup,
		AuthSourceDefaultEmailGrantOnFirstBind:    authSourceDefaults.Email.GrantOnFirstBind,
		AuthSourceDefaultLinuxDoBalance:           authSourceDefaults.LinuxDo.Balance,
		AuthSourceDefaultLinuxDoConcurrency:       authSourceDefaults.LinuxDo.Concurrency,
		AuthSourceDefaultLinuxDoSubscriptions:     authSourceLinuxDoSubscriptions,
		AuthSourceDefaultLinuxDoGrantOnSignup:     authSourceDefaults.LinuxDo.GrantOnSignup,
		AuthSourceDefaultLinuxDoGrantOnFirstBind:  authSourceDefaults.LinuxDo.GrantOnFirstBind,
		AuthSourceDefaultOIDCBalance:              authSourceDefaults.OIDC.Balance,
		AuthSourceDefaultOIDCConcurrency:          authSourceDefaults.OIDC.Concurrency,
		AuthSourceDefaultOIDCSubscriptions:        authSourceOIDCSubscriptions,
		AuthSourceDefaultOIDCGrantOnSignup:        authSourceDefaults.OIDC.GrantOnSignup,
		AuthSourceDefaultOIDCGrantOnFirstBind:     authSourceDefaults.OIDC.GrantOnFirstBind,
		AuthSourceDefaultWeChatBalance:            authSourceDefaults.WeChat.Balance,
		AuthSourceDefaultWeChatConcurrency:        authSourceDefaults.WeChat.Concurrency,
		AuthSourceDefaultWeChatSubscriptions:      authSourceWeChatSubscriptions,
		AuthSourceDefaultWeChatGrantOnSignup:      authSourceDefaults.WeChat.GrantOnSignup,
		AuthSourceDefaultWeChatGrantOnFirstBind:   authSourceDefaults.WeChat.GrantOnFirstBind,
		AuthSourceDefaultGitHubBalance:            authSourceDefaults.GitHub.Balance,
		AuthSourceDefaultGitHubConcurrency:        authSourceDefaults.GitHub.Concurrency,
		AuthSourceDefaultGitHubSubscriptions:      authSourceGitHubSubscriptions,
		AuthSourceDefaultGitHubGrantOnSignup:      authSourceDefaults.GitHub.GrantOnSignup,
		AuthSourceDefaultGitHubGrantOnFirstBind:   authSourceDefaults.GitHub.GrantOnFirstBind,
		AuthSourceDefaultGoogleBalance:            authSourceDefaults.Google.Balance,
		AuthSourceDefaultGoogleConcurrency:        authSourceDefaults.Google.Concurrency,
		AuthSourceDefaultGoogleSubscriptions:      authSourceGoogleSubscriptions,
		AuthSourceDefaultGoogleGrantOnSignup:      authSourceDefaults.Google.GrantOnSignup,
		AuthSourceDefaultGoogleGrantOnFirstBind:   authSourceDefaults.Google.GrantOnFirstBind,
		AuthSourceDefaultDingTalkBalance:          authSourceDefaults.DingTalk.Balance,
		AuthSourceDefaultDingTalkConcurrency:      authSourceDefaults.DingTalk.Concurrency,
		AuthSourceDefaultDingTalkSubscriptions:    authSourceDingTalkSubscriptions,
		AuthSourceDefaultDingTalkGrantOnSignup:    authSourceDefaults.DingTalk.GrantOnSignup,
		AuthSourceDefaultDingTalkGrantOnFirstBind: authSourceDefaults.DingTalk.GrantOnFirstBind,
		ForceEmailOnThirdPartySignup:              authSourceDefaults.ForceEmailOnThirdPartySignup,
		EnableModelFallback:                       settings.EnableModelFallback,
		FallbackModelAnthropic:                    settings.FallbackModelAnthropic,
		FallbackModelOpenAI:                       settings.FallbackModelOpenAI,
		FallbackModelGemini:                       settings.FallbackModelGemini,
		EnableIdentityPatch:                       settings.EnableIdentityPatch,
		IdentityPatchPrompt:                       settings.IdentityPatchPrompt,
		OpsMonitoringEnabled:                      opsEnabled && settings.OpsMonitoringEnabled,
		OpsRealtimeMonitoringEnabled:              settings.OpsRealtimeMonitoringEnabled,
		OpsQueryModeDefault:                       settings.OpsQueryModeDefault,
		OpsMetricsIntervalSeconds:                 settings.OpsMetricsIntervalSeconds,
		MinClaudeCodeVersion:                      settings.MinClaudeCodeVersion,
		MaxClaudeCodeVersion:                      settings.MaxClaudeCodeVersion,
		AllowUngroupedKeyScheduling:               settings.AllowUngroupedKeyScheduling,
		BackendModeEnabled:                        settings.BackendModeEnabled,
		EnableFingerprintUnification:              settings.EnableFingerprintUnification,
		EnableMetadataPassthrough:                 settings.EnableMetadataPassthrough,
		DefaultUpstreamUserAgent:                  settings.DefaultUpstreamUserAgent,
		ForceUnifiedUpstreamUserAgent:             settings.ForceUnifiedUpstreamUserAgent,
		UpdateGitHubRepo:                          settings.UpdateGitHubRepo,
		EnableCCHSigning:                          settings.EnableCCHSigning,
		AntigravityUserAgentVersion:               settings.AntigravityUserAgentVersion,
		WebSearchEmulationEnabled:                 settings.WebSearchEmulationEnabled,
		PaymentVisibleMethodAlipaySource:          settings.PaymentVisibleMethodAlipaySource,
		PaymentVisibleMethodWxpaySource:           settings.PaymentVisibleMethodWxpaySource,
		PaymentVisibleMethodAlipayEnabled:         settings.PaymentVisibleMethodAlipayEnabled,
		PaymentVisibleMethodWxpayEnabled:          settings.PaymentVisibleMethodWxpayEnabled,
		OpenAIAdvancedSchedulerEnabled:            settings.OpenAIAdvancedSchedulerEnabled,
		BalanceLowNotifyEnabled:                   settings.BalanceLowNotifyEnabled,
		BalanceLowNotifyThreshold:                 settings.BalanceLowNotifyThreshold,
		BalanceLowNotifyRechargeURL:               settings.BalanceLowNotifyRechargeURL,
		AccountQuotaNotifyEnabled:                 settings.AccountQuotaNotifyEnabled,
		AccountQuotaNotifyEmails:                  dto.NotifyEmailEntriesFromService(settings.AccountQuotaNotifyEmails),
		PaymentEnabled:                            paymentCfg.Enabled,
		PaymentMinAmount:                          paymentCfg.MinAmount,
		PaymentMaxAmount:                          paymentCfg.MaxAmount,
		PaymentDailyLimit:                         paymentCfg.DailyLimit,
		PaymentOrderTimeoutMin:                    paymentCfg.OrderTimeoutMin,
		PaymentMaxPendingOrders:                   paymentCfg.MaxPendingOrders,
		PaymentEnabledTypes:                       paymentCfg.EnabledTypes,
		PaymentBalanceDisabled:                    paymentCfg.BalanceDisabled,
		PaymentBalanceRechargeMultiplier:          paymentCfg.BalanceRechargeMultiplier,
		PaymentRechargeFeeRate:                    paymentCfg.RechargeFeeRate,
		PaymentLoadBalanceStrat:                   paymentCfg.LoadBalanceStrategy,
		PaymentProductNamePrefix:                  paymentCfg.ProductNamePrefix,
		PaymentProductNameSuffix:                  paymentCfg.ProductNameSuffix,
		PaymentHelpImageURL:                       paymentCfg.HelpImageURL,
		PaymentHelpText:                           paymentCfg.HelpText,
		PaymentCancelRateLimitEnabled:             paymentCfg.CancelRateLimitEnabled,
		PaymentCancelRateLimitMax:                 paymentCfg.CancelRateLimitMax,
		PaymentCancelRateLimitWindow:              paymentCfg.CancelRateLimitWindow,
		PaymentCancelRateLimitUnit:                paymentCfg.CancelRateLimitUnit,
		PaymentCancelRateLimitMode:                paymentCfg.CancelRateLimitMode,
		ChannelMonitorEnabled:                     settings.ChannelMonitorEnabled,
		ChannelMonitorDefaultIntervalSeconds:      settings.ChannelMonitorDefaultIntervalSeconds,
		AvailableChannelsEnabled:                  settings.AvailableChannelsEnabled,
		RiskControlEnabled:                        settings.RiskControlEnabled,
	})
}

func loginAgreementDocumentsToDTO(items []service.LoginAgreementDocument) []dto.LoginAgreementDocument {
	result := make([]dto.LoginAgreementDocument, 0, len(items))
	for _, item := range items {
		result = append(result, dto.LoginAgreementDocument{
			ID:        item.ID,
			Title:     item.Title,
			ContentMD: item.ContentMD,
		})
	}
	return result
}

func loginAgreementDocumentsToService(items []dto.LoginAgreementDocument) []service.LoginAgreementDocument {
	result := make([]service.LoginAgreementDocument, 0, len(items))
	for _, item := range items {
		title := strings.TrimSpace(item.Title)
		content := strings.TrimSpace(item.ContentMD)
		if title == "" && content == "" {
			continue
		}
		result = append(result, service.LoginAgreementDocument{
			ID:        strings.TrimSpace(item.ID),
			Title:     title,
			ContentMD: content,
		})
	}
	return result
}

// UpdateSettingsRequest 更新设置请求
type UpdateSettingsRequest struct {
	// 注册设置
	RegistrationEnabled              bool                         `json:"registration_enabled"`
	EmailVerifyEnabled               bool                         `json:"email_verify_enabled"`
	RegistrationEmailSuffixWhitelist []string                     `json:"registration_email_suffix_whitelist"`
	PromoCodeEnabled                 bool                         `json:"promo_code_enabled"`
	PasswordResetEnabled             bool                         `json:"password_reset_enabled"`
	FrontendURL                      string                       `json:"frontend_url"`
	InvitationCodeEnabled            bool                         `json:"invitation_code_enabled"`
	TotpEnabled                      bool                         `json:"totp_enabled"` // TOTP 双因素认证
	LoginAgreementEnabled            bool                         `json:"login_agreement_enabled"`
	LoginAgreementMode               string                       `json:"login_agreement_mode"`
	LoginAgreementUpdatedAt          string                       `json:"login_agreement_updated_at"`
	LoginAgreementDocuments          []dto.LoginAgreementDocument `json:"login_agreement_documents"`

	// 邮件服务设置
	SMTPHost     string `json:"smtp_host"`
	SMTPPort     int    `json:"smtp_port"`
	SMTPUsername string `json:"smtp_username"`
	SMTPPassword string `json:"smtp_password"`
	SMTPFrom     string `json:"smtp_from_email"`
	SMTPFromName string `json:"smtp_from_name"`
	SMTPUseTLS   bool   `json:"smtp_use_tls"`

	// Cloudflare Turnstile 设置
	TurnstileEnabled   bool   `json:"turnstile_enabled"`
	TurnstileSiteKey   string `json:"turnstile_site_key"`
	TurnstileSecretKey string `json:"turnstile_secret_key"`

	// API Key IP 访问控制设置
	APIKeyACLTrustForwardedIP *bool `json:"api_key_acl_trust_forwarded_ip"`

	// LinuxDo Connect OAuth 登录
	LinuxDoConnectEnabled      bool   `json:"linuxdo_connect_enabled"`
	LinuxDoConnectClientID     string `json:"linuxdo_connect_client_id"`
	LinuxDoConnectClientSecret string `json:"linuxdo_connect_client_secret"`
	LinuxDoConnectRedirectURL  string `json:"linuxdo_connect_redirect_url"`

	// DingTalk Connect OAuth 登录
	DingTalkConnectEnabled                 bool   `json:"dingtalk_connect_enabled"`
	DingTalkConnectClientID                string `json:"dingtalk_connect_client_id"`
	DingTalkConnectClientSecret            string `json:"dingtalk_connect_client_secret"`
	DingTalkConnectRedirectURL             string `json:"dingtalk_connect_redirect_url"`
	DingTalkConnectCorpRestrictionPolicy   string `json:"dingtalk_connect_corp_restriction_policy"`
	DingTalkConnectInternalCorpID          string `json:"dingtalk_connect_internal_corp_id"`
	DingTalkConnectBypassRegistration      bool   `json:"dingtalk_connect_bypass_registration"`
	DingTalkConnectSyncCorpEmail           bool   `json:"dingtalk_connect_sync_corp_email"`
	DingTalkConnectSyncDisplayName         bool   `json:"dingtalk_connect_sync_display_name"`
	DingTalkConnectSyncDept                bool   `json:"dingtalk_connect_sync_dept"`
	DingTalkConnectSyncCorpEmailAttrKey    string `json:"dingtalk_connect_sync_corp_email_attr_key"`
	DingTalkConnectSyncDisplayNameAttrKey  string `json:"dingtalk_connect_sync_display_name_attr_key"`
	DingTalkConnectSyncDeptAttrKey         string `json:"dingtalk_connect_sync_dept_attr_key"`
	DingTalkConnectSyncCorpEmailAttrName   string `json:"dingtalk_connect_sync_corp_email_attr_name"`
	DingTalkConnectSyncDisplayNameAttrName string `json:"dingtalk_connect_sync_display_name_attr_name"`
	DingTalkConnectSyncDeptAttrName        string `json:"dingtalk_connect_sync_dept_attr_name"`

	// WeChat Connect OAuth 登录
	WeChatConnectEnabled             bool   `json:"wechat_connect_enabled"`
	WeChatConnectAppID               string `json:"wechat_connect_app_id"`
	WeChatConnectAppSecret           string `json:"wechat_connect_app_secret"`
	WeChatConnectOpenEnabled         bool   `json:"wechat_connect_open_enabled"`
	WeChatConnectOpenAppID           string `json:"wechat_connect_open_app_id"`
	WeChatConnectOpenAppSecret       string `json:"wechat_connect_open_app_secret"`
	WeChatConnectMPEnabled           bool   `json:"wechat_connect_mp_enabled"`
	WeChatConnectMPAppID             string `json:"wechat_connect_mp_app_id"`
	WeChatConnectMPAppSecret         string `json:"wechat_connect_mp_app_secret"`
	WeChatConnectMobileEnabled       bool   `json:"wechat_connect_mobile_enabled"`
	WeChatConnectMobileAppID         string `json:"wechat_connect_mobile_app_id"`
	WeChatConnectMobileAppSecret     string `json:"wechat_connect_mobile_app_secret"`
	WeChatConnectMode                string `json:"wechat_connect_mode"`
	WeChatConnectScopes              string `json:"wechat_connect_scopes"`
	WeChatConnectRedirectURL         string `json:"wechat_connect_redirect_url"`
	WeChatConnectFrontendRedirectURL string `json:"wechat_connect_frontend_redirect_url"`

	// Generic OIDC OAuth 登录
	OIDCConnectEnabled              bool   `json:"oidc_connect_enabled"`
	OIDCConnectProviderName         string `json:"oidc_connect_provider_name"`
	OIDCConnectClientID             string `json:"oidc_connect_client_id"`
	OIDCConnectClientSecret         string `json:"oidc_connect_client_secret"`
	OIDCConnectIssuerURL            string `json:"oidc_connect_issuer_url"`
	OIDCConnectDiscoveryURL         string `json:"oidc_connect_discovery_url"`
	OIDCConnectAuthorizeURL         string `json:"oidc_connect_authorize_url"`
	OIDCConnectTokenURL             string `json:"oidc_connect_token_url"`
	OIDCConnectUserInfoURL          string `json:"oidc_connect_userinfo_url"`
	OIDCConnectJWKSURL              string `json:"oidc_connect_jwks_url"`
	OIDCConnectScopes               string `json:"oidc_connect_scopes"`
	OIDCConnectRedirectURL          string `json:"oidc_connect_redirect_url"`
	OIDCConnectFrontendRedirectURL  string `json:"oidc_connect_frontend_redirect_url"`
	OIDCConnectTokenAuthMethod      string `json:"oidc_connect_token_auth_method"`
	OIDCConnectUsePKCE              *bool  `json:"oidc_connect_use_pkce"`
	OIDCConnectValidateIDToken      *bool  `json:"oidc_connect_validate_id_token"`
	OIDCConnectAllowedSigningAlgs   string `json:"oidc_connect_allowed_signing_algs"`
	OIDCConnectClockSkewSeconds     *int   `json:"oidc_connect_clock_skew_seconds"`
	OIDCConnectRequireEmailVerified bool   `json:"oidc_connect_require_email_verified"`
	OIDCConnectUserInfoEmailPath    string `json:"oidc_connect_userinfo_email_path"`
	OIDCConnectUserInfoIDPath       string `json:"oidc_connect_userinfo_id_path"`
	OIDCConnectUserInfoUsernamePath string `json:"oidc_connect_userinfo_username_path"`

	GitHubOAuthEnabled             bool   `json:"github_oauth_enabled"`
	GitHubOAuthClientID            string `json:"github_oauth_client_id"`
	GitHubOAuthClientSecret        string `json:"github_oauth_client_secret"`
	GitHubOAuthRedirectURL         string `json:"github_oauth_redirect_url"`
	GitHubOAuthFrontendRedirectURL string `json:"github_oauth_frontend_redirect_url"`
	GoogleOAuthEnabled             bool   `json:"google_oauth_enabled"`
	GoogleOAuthClientID            string `json:"google_oauth_client_id"`
	GoogleOAuthClientSecret        string `json:"google_oauth_client_secret"`
	GoogleOAuthRedirectURL         string `json:"google_oauth_redirect_url"`
	GoogleOAuthFrontendRedirectURL string `json:"google_oauth_frontend_redirect_url"`

	// OEM设置
	SiteName                    string                `json:"site_name"`
	SiteLogo                    string                `json:"site_logo"`
	SiteSubtitle                string                `json:"site_subtitle"`
	APIBaseURL                  string                `json:"api_base_url"`
	ContactInfo                 string                `json:"contact_info"`
	DocURL                      string                `json:"doc_url"`
	HomeContent                 string                `json:"home_content"`
	HideCcsImportButton         bool                  `json:"hide_ccs_import_button"`
	PurchaseSubscriptionEnabled *bool                 `json:"purchase_subscription_enabled"`
	PurchaseSubscriptionURL     *string               `json:"purchase_subscription_url"`
	TableDefaultPageSize        int                   `json:"table_default_page_size"`
	TablePageSizeOptions        []int                 `json:"table_page_size_options"`
	HiddenAdminMenuItems        *[]string             `json:"hidden_admin_menu_items"`
	CustomMenuItems             *[]dto.CustomMenuItem `json:"custom_menu_items"`
	CustomEndpoints             *[]dto.CustomEndpoint `json:"custom_endpoints"`

	// 默认配置
	DefaultConcurrency           int                              `json:"default_concurrency"`
	DefaultBalance               float64                          `json:"default_balance"`
	AffiliateEnabled             *bool                            `json:"affiliate_enabled"`
	AffiliateRebateRate          *float64                         `json:"affiliate_rebate_rate"`
	AffiliateRebateFreezeHours   *int                             `json:"affiliate_rebate_freeze_hours"`
	AffiliateRebateDurationDays  *int                             `json:"affiliate_rebate_duration_days"`
	AffiliateRebatePerInviteeCap *float64                         `json:"affiliate_rebate_per_invitee_cap"`
	DefaultUserRPMLimit          int                              `json:"default_user_rpm_limit"`
	DefaultSubscriptions         []dto.DefaultSubscriptionSetting `json:"default_subscriptions"`
	// 第三方登录默认赠送配置（Email 来源）
	AuthSourceDefaultEmailBalance             *float64                          `json:"auth_source_default_email_balance"`
	AuthSourceDefaultEmailConcurrency         *int                              `json:"auth_source_default_email_concurrency"`
	AuthSourceDefaultEmailSubscriptions       *[]dto.DefaultSubscriptionSetting `json:"auth_source_default_email_subscriptions"`
	AuthSourceDefaultEmailGrantOnSignup       *bool                             `json:"auth_source_default_email_grant_on_signup"`
	AuthSourceDefaultEmailGrantOnFirstBind    *bool                             `json:"auth_source_default_email_grant_on_first_bind"`
	AuthSourceDefaultLinuxDoBalance           *float64                          `json:"auth_source_default_linuxdo_balance"`
	AuthSourceDefaultLinuxDoConcurrency       *int                              `json:"auth_source_default_linuxdo_concurrency"`
	AuthSourceDefaultLinuxDoSubscriptions     *[]dto.DefaultSubscriptionSetting `json:"auth_source_default_linuxdo_subscriptions"`
	AuthSourceDefaultLinuxDoGrantOnSignup     *bool                             `json:"auth_source_default_linuxdo_grant_on_signup"`
	AuthSourceDefaultLinuxDoGrantOnFirstBind  *bool                             `json:"auth_source_default_linuxdo_grant_on_first_bind"`
	AuthSourceDefaultOIDCBalance              *float64                          `json:"auth_source_default_oidc_balance"`
	AuthSourceDefaultOIDCConcurrency          *int                              `json:"auth_source_default_oidc_concurrency"`
	AuthSourceDefaultOIDCSubscriptions        *[]dto.DefaultSubscriptionSetting `json:"auth_source_default_oidc_subscriptions"`
	AuthSourceDefaultOIDCGrantOnSignup        *bool                             `json:"auth_source_default_oidc_grant_on_signup"`
	AuthSourceDefaultOIDCGrantOnFirstBind     *bool                             `json:"auth_source_default_oidc_grant_on_first_bind"`
	AuthSourceDefaultWeChatBalance            *float64                          `json:"auth_source_default_wechat_balance"`
	AuthSourceDefaultWeChatConcurrency        *int                              `json:"auth_source_default_wechat_concurrency"`
	AuthSourceDefaultWeChatSubscriptions      *[]dto.DefaultSubscriptionSetting `json:"auth_source_default_wechat_subscriptions"`
	AuthSourceDefaultWeChatGrantOnSignup      *bool                             `json:"auth_source_default_wechat_grant_on_signup"`
	AuthSourceDefaultWeChatGrantOnFirstBind   *bool                             `json:"auth_source_default_wechat_grant_on_first_bind"`
	AuthSourceDefaultGitHubBalance            *float64                          `json:"auth_source_default_github_balance"`
	AuthSourceDefaultGitHubConcurrency        *int                              `json:"auth_source_default_github_concurrency"`
	AuthSourceDefaultGitHubSubscriptions      *[]dto.DefaultSubscriptionSetting `json:"auth_source_default_github_subscriptions"`
	AuthSourceDefaultGitHubGrantOnSignup      *bool                             `json:"auth_source_default_github_grant_on_signup"`
	AuthSourceDefaultGitHubGrantOnFirstBind   *bool                             `json:"auth_source_default_github_grant_on_first_bind"`
	AuthSourceDefaultGoogleBalance            *float64                          `json:"auth_source_default_google_balance"`
	AuthSourceDefaultGoogleConcurrency        *int                              `json:"auth_source_default_google_concurrency"`
	AuthSourceDefaultGoogleSubscriptions      *[]dto.DefaultSubscriptionSetting `json:"auth_source_default_google_subscriptions"`
	AuthSourceDefaultGoogleGrantOnSignup      *bool                             `json:"auth_source_default_google_grant_on_signup"`
	AuthSourceDefaultGoogleGrantOnFirstBind   *bool                             `json:"auth_source_default_google_grant_on_first_bind"`
	AuthSourceDefaultDingTalkBalance          *float64                          `json:"auth_source_default_dingtalk_balance"`
	AuthSourceDefaultDingTalkConcurrency      *int                              `json:"auth_source_default_dingtalk_concurrency"`
	AuthSourceDefaultDingTalkSubscriptions    *[]dto.DefaultSubscriptionSetting `json:"auth_source_default_dingtalk_subscriptions"`
	AuthSourceDefaultDingTalkGrantOnSignup    *bool                             `json:"auth_source_default_dingtalk_grant_on_signup"`
	AuthSourceDefaultDingTalkGrantOnFirstBind *bool                             `json:"auth_source_default_dingtalk_grant_on_first_bind"`
	ForceEmailOnThirdPartySignup              *bool                             `json:"force_email_on_third_party_signup"`

	// Model fallback configuration
	EnableModelFallback    bool   `json:"enable_model_fallback"`
	FallbackModelAnthropic string `json:"fallback_model_anthropic"`
	FallbackModelOpenAI    string `json:"fallback_model_openai"`
	FallbackModelGemini    string `json:"fallback_model_gemini"`

	// Identity patch configuration (Claude -> Gemini)
	EnableIdentityPatch bool   `json:"enable_identity_patch"`
	IdentityPatchPrompt string `json:"identity_patch_prompt"`

	// Ops monitoring (vNext)
	OpsMonitoringEnabled         *bool   `json:"ops_monitoring_enabled"`
	OpsRealtimeMonitoringEnabled *bool   `json:"ops_realtime_monitoring_enabled"`
	OpsQueryModeDefault          *string `json:"ops_query_mode_default"`
	OpsMetricsIntervalSeconds    *int    `json:"ops_metrics_interval_seconds"`

	MinClaudeCodeVersion string `json:"min_claude_code_version"`
	MaxClaudeCodeVersion string `json:"max_claude_code_version"`

	// 分组隔离
	AllowUngroupedKeyScheduling bool `json:"allow_ungrouped_key_scheduling"`

	// Backend Mode
	BackendModeEnabled bool `json:"backend_mode_enabled"`

	// Gateway forwarding behavior
	EnableFingerprintUnification      *bool   `json:"enable_fingerprint_unification"`
	EnableMetadataPassthrough         *bool   `json:"enable_metadata_passthrough"`
	DefaultUpstreamUserAgent          *string `json:"default_upstream_user_agent"`
	ForceUnifiedUpstreamUserAgent     *bool   `json:"force_unified_upstream_user_agent"`
	UpdateGitHubRepo                  *string `json:"update_github_repo"`
	EnableCCHSigning                  *bool   `json:"enable_cch_signing"`
	AntigravityUserAgentVersion       *string `json:"antigravity_user_agent_version"`
	PaymentVisibleMethodAlipaySource  *string `json:"payment_visible_method_alipay_source"`
	PaymentVisibleMethodWxpaySource   *string `json:"payment_visible_method_wxpay_source"`
	PaymentVisibleMethodAlipayEnabled *bool   `json:"payment_visible_method_alipay_enabled"`
	PaymentVisibleMethodWxpayEnabled  *bool   `json:"payment_visible_method_wxpay_enabled"`
	OpenAIAdvancedSchedulerEnabled    *bool   `json:"openai_advanced_scheduler_enabled"`

	// 余额不足提醒
	BalanceLowNotifyEnabled         *bool                   `json:"balance_low_notify_enabled"`
	BalanceLowNotifyThreshold       *float64                `json:"balance_low_notify_threshold"`
	BalanceLowNotifyRechargeURL     *string                 `json:"balance_low_notify_recharge_url"`
	SubscriptionExpiryNotifyEnabled *bool                   `json:"subscription_expiry_notify_enabled"`
	AccountQuotaNotifyEnabled       *bool                   `json:"account_quota_notify_enabled"`
	AccountQuotaNotifyEmails        *[]dto.NotifyEmailEntry `json:"account_quota_notify_emails"`

	// Payment configuration (integrated into settings, full replace)
	PaymentEnabled                   *bool    `json:"payment_enabled"`
	PaymentMinAmount                 *float64 `json:"payment_min_amount"`
	PaymentMaxAmount                 *float64 `json:"payment_max_amount"`
	PaymentDailyLimit                *float64 `json:"payment_daily_limit"`
	PaymentOrderTimeoutMin           *int     `json:"payment_order_timeout_minutes"`
	PaymentMaxPendingOrders          *int     `json:"payment_max_pending_orders"`
	PaymentEnabledTypes              []string `json:"payment_enabled_types"`
	PaymentBalanceDisabled           *bool    `json:"payment_balance_disabled"`
	PaymentBalanceRechargeMultiplier *float64 `json:"payment_balance_recharge_multiplier"`
	PaymentRechargeFeeRate           *float64 `json:"payment_recharge_fee_rate"`
	PaymentLoadBalanceStrat          *string  `json:"payment_load_balance_strategy"`
	PaymentProductNamePrefix         *string  `json:"payment_product_name_prefix"`
	PaymentProductNameSuffix         *string  `json:"payment_product_name_suffix"`
	PaymentHelpImageURL              *string  `json:"payment_help_image_url"`
	PaymentHelpText                  *string  `json:"payment_help_text"`

	// Cancel rate limit
	PaymentCancelRateLimitEnabled *bool   `json:"payment_cancel_rate_limit_enabled"`
	PaymentCancelRateLimitMax     *int    `json:"payment_cancel_rate_limit_max"`
	PaymentCancelRateLimitWindow  *int    `json:"payment_cancel_rate_limit_window"`
	PaymentCancelRateLimitUnit    *string `json:"payment_cancel_rate_limit_unit"`
	PaymentCancelRateLimitMode    *string `json:"payment_cancel_rate_limit_window_mode"`

	// Channel Monitor 开关
	ChannelMonitorEnabled                *bool `json:"channel_monitor_enabled"`
	ChannelMonitorDefaultIntervalSeconds *int  `json:"channel_monitor_default_interval_seconds"`

	// Available Channels 开关
	AvailableChannelsEnabled *bool `json:"available_channels_enabled"`
}

// UpdateSettings 更新系统设置
// PUT /api/v1/admin/settings
func (h *SettingHandler) UpdateSettings(c *gin.Context) {
	var req UpdateSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	previousSettings, err := h.settingService.GetAllSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	previousAuthDefaults, err := h.settingService.GetAuthSourceDefaultSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	// 验证参数
	if req.DefaultConcurrency < 1 {
		req.DefaultConcurrency = 1
	}
	if req.DefaultBalance < 0 {
		req.DefaultBalance = 0
	}
	if req.DefaultUserRPMLimit < 0 {
		req.DefaultUserRPMLimit = 0
	}
	affiliateEnabled := previousSettings.AffiliateEnabled
	if req.AffiliateEnabled != nil {
		affiliateEnabled = *req.AffiliateEnabled
	}
	affiliateRebateRate := previousSettings.AffiliateRebateRate
	if req.AffiliateRebateRate != nil {
		affiliateRebateRate = *req.AffiliateRebateRate
	}
	if affiliateRebateRate < service.AffiliateRebateRateMin {
		affiliateRebateRate = service.AffiliateRebateRateMin
	}
	if affiliateRebateRate > service.AffiliateRebateRateMax {
		affiliateRebateRate = service.AffiliateRebateRateMax
	}
	affiliateRebateFreezeHours := previousSettings.AffiliateRebateFreezeHours
	if req.AffiliateRebateFreezeHours != nil {
		affiliateRebateFreezeHours = *req.AffiliateRebateFreezeHours
	}
	if affiliateRebateFreezeHours < 0 {
		affiliateRebateFreezeHours = service.AffiliateRebateFreezeHoursDefault
	}
	if affiliateRebateFreezeHours > service.AffiliateRebateFreezeHoursMax {
		affiliateRebateFreezeHours = service.AffiliateRebateFreezeHoursMax
	}
	affiliateRebateDurationDays := previousSettings.AffiliateRebateDurationDays
	if req.AffiliateRebateDurationDays != nil {
		affiliateRebateDurationDays = *req.AffiliateRebateDurationDays
	}
	if affiliateRebateDurationDays < 0 {
		affiliateRebateDurationDays = service.AffiliateRebateDurationDaysDefault
	}
	if affiliateRebateDurationDays > service.AffiliateRebateDurationDaysMax {
		affiliateRebateDurationDays = service.AffiliateRebateDurationDaysMax
	}
	affiliateRebatePerInviteeCap := previousSettings.AffiliateRebatePerInviteeCap
	if req.AffiliateRebatePerInviteeCap != nil {
		affiliateRebatePerInviteeCap = *req.AffiliateRebatePerInviteeCap
	}
	if affiliateRebatePerInviteeCap < 0 {
		affiliateRebatePerInviteeCap = service.AffiliateRebatePerInviteeCapDefault
	}
	// 通用表格配置：兼容旧客户端未传字段时保留当前值。
	if req.TableDefaultPageSize <= 0 {
		req.TableDefaultPageSize = previousSettings.TableDefaultPageSize
	}
	if req.TablePageSizeOptions == nil {
		req.TablePageSizeOptions = previousSettings.TablePageSizeOptions
	}
	req.SMTPHost = strings.TrimSpace(req.SMTPHost)
	req.SMTPUsername = strings.TrimSpace(req.SMTPUsername)
	req.SMTPPassword = strings.TrimSpace(req.SMTPPassword)
	req.SMTPFrom = strings.TrimSpace(req.SMTPFrom)
	req.SMTPFromName = strings.TrimSpace(req.SMTPFromName)
	if req.SMTPPort <= 0 {
		req.SMTPPort = 587
	}
	req.DefaultSubscriptions = normalizeDefaultSubscriptions(req.DefaultSubscriptions)

	// SMTP 配置保护：如果请求中 smtp_host 为空但数据库中已有配置，则保留已有 SMTP 配置
	// 防止前端加载设置失败时空表单覆盖已保存的 SMTP 配置
	if req.SMTPHost == "" && previousSettings.SMTPHost != "" {
		req.SMTPHost = previousSettings.SMTPHost
		req.SMTPPort = previousSettings.SMTPPort
		req.SMTPUsername = previousSettings.SMTPUsername
		req.SMTPFrom = previousSettings.SMTPFrom
		req.SMTPFromName = previousSettings.SMTPFromName
		req.SMTPUseTLS = previousSettings.SMTPUseTLS
	}

	// Turnstile 参数验证
	if req.TurnstileEnabled {
		// 检查必填字段
		if req.TurnstileSiteKey == "" {
			response.BadRequest(c, "Turnstile Site Key is required when enabled")
			return
		}
		// 如果未提供 secret key，使用已保存的值（留空保留当前值）
		if req.TurnstileSecretKey == "" {
			if previousSettings.TurnstileSecretKey == "" {
				response.BadRequest(c, "Turnstile Secret Key is required when enabled")
				return
			}
			req.TurnstileSecretKey = previousSettings.TurnstileSecretKey
		}

		// 当 site_key 或 secret_key 任一变化时验证（避免配置错误导致无法登录）
		siteKeyChanged := previousSettings.TurnstileSiteKey != req.TurnstileSiteKey
		secretKeyChanged := previousSettings.TurnstileSecretKey != req.TurnstileSecretKey
		if siteKeyChanged || secretKeyChanged {
			if err := h.turnstileService.ValidateSecretKey(c.Request.Context(), req.TurnstileSecretKey); err != nil {
				response.ErrorFrom(c, err)
				return
			}
		}
	}

	// TOTP 双因素认证参数验证
	// 只有手动配置了加密密钥才允许启用 TOTP 功能
	if req.TotpEnabled && !previousSettings.TotpEnabled {
		// 尝试启用 TOTP，检查加密密钥是否已手动配置
		if !h.settingService.IsTotpEncryptionKeyConfigured() {
			response.BadRequest(c, "Cannot enable TOTP: TOTP_ENCRYPTION_KEY environment variable must be configured first. Generate a key with 'openssl rand -hex 32' and set it in your environment.")
			return
		}
	}
	loginAgreementMode := strings.ToLower(strings.TrimSpace(req.LoginAgreementMode))
	if loginAgreementMode == "" {
		loginAgreementMode = strings.ToLower(strings.TrimSpace(previousSettings.LoginAgreementMode))
	}
	switch loginAgreementMode {
	case "", "modal":
		loginAgreementMode = "modal"
	case "checkbox":
	default:
		response.BadRequest(c, "Login agreement mode must be modal or checkbox")
		return
	}
	loginAgreementUpdatedAt := strings.TrimSpace(req.LoginAgreementUpdatedAt)
	if loginAgreementUpdatedAt == "" {
		loginAgreementUpdatedAt = strings.TrimSpace(previousSettings.LoginAgreementUpdatedAt)
	}
	loginAgreementDocuments := loginAgreementDocumentsToService(req.LoginAgreementDocuments)
	if len(loginAgreementDocuments) == 0 {
		loginAgreementDocuments = previousSettings.LoginAgreementDocuments
	}
	for _, doc := range loginAgreementDocuments {
		if strings.TrimSpace(doc.Title) == "" {
			response.BadRequest(c, "Login agreement document title is required")
			return
		}
		if len(doc.Title) > 80 {
			response.BadRequest(c, "Login agreement document title is too long (max 80 characters)")
			return
		}
		if len(doc.ContentMD) > 200*1024 {
			response.BadRequest(c, "Login agreement document content is too large (max 200KB)")
			return
		}
	}
	if req.LoginAgreementEnabled && len(loginAgreementDocuments) == 0 {
		response.BadRequest(c, "Login agreement documents are required when enabled")
		return
	}

	// LinuxDo Connect 参数验证
	if req.LinuxDoConnectEnabled {
		req.LinuxDoConnectClientID = strings.TrimSpace(req.LinuxDoConnectClientID)
		req.LinuxDoConnectClientSecret = strings.TrimSpace(req.LinuxDoConnectClientSecret)
		req.LinuxDoConnectRedirectURL = strings.TrimSpace(req.LinuxDoConnectRedirectURL)

		if req.LinuxDoConnectClientID == "" {
			response.BadRequest(c, "LinuxDo Client ID is required when enabled")
			return
		}
		if req.LinuxDoConnectRedirectURL == "" {
			response.BadRequest(c, "LinuxDo Redirect URL is required when enabled")
			return
		}
		if err := config.ValidateAbsoluteHTTPURL(req.LinuxDoConnectRedirectURL); err != nil {
			response.BadRequest(c, "LinuxDo Redirect URL must be an absolute http(s) URL")
			return
		}

		// 如果未提供 client_secret，则保留现有值（如有）。
		if req.LinuxDoConnectClientSecret == "" {
			if previousSettings.LinuxDoConnectClientSecret == "" {
				response.BadRequest(c, "LinuxDo Client Secret is required when enabled")
				return
			}
			req.LinuxDoConnectClientSecret = previousSettings.LinuxDoConnectClientSecret
		}
	}

	// DingTalk Connect 参数验证
	// 防御性：任何写入路径上把已废弃的 corp_restriction_policy=whitelist 入参 coerce 为 none，
	// 避免任何直连 admin API 的客户端把死值写回 DB（前端 UI 已无此选项）。
	req.DingTalkConnectCorpRestrictionPolicy = service.CoerceDingTalkCorpPolicyForWrite(req.DingTalkConnectCorpRestrictionPolicy)
	if req.DingTalkConnectEnabled {
		req.DingTalkConnectClientID = strings.TrimSpace(req.DingTalkConnectClientID)
		req.DingTalkConnectClientSecret = strings.TrimSpace(req.DingTalkConnectClientSecret)
		req.DingTalkConnectRedirectURL = strings.TrimSpace(req.DingTalkConnectRedirectURL)
		req.DingTalkConnectCorpRestrictionPolicy = strings.TrimSpace(req.DingTalkConnectCorpRestrictionPolicy)
		req.DingTalkConnectInternalCorpID = strings.TrimSpace(req.DingTalkConnectInternalCorpID)

		if req.DingTalkConnectClientID == "" {
			response.BadRequest(c, "DingTalk Client ID is required when enabled")
			return
		}
		if req.DingTalkConnectRedirectURL == "" {
			response.BadRequest(c, "DingTalk Redirect URL is required when enabled")
			return
		}
		if err := config.ValidateAbsoluteHTTPURL(req.DingTalkConnectRedirectURL); err != nil {
			response.BadRequest(c, "DingTalk Redirect URL must be an absolute http(s) URL")
			return
		}
		if req.DingTalkConnectClientSecret == "" {
			if previousSettings.DingTalkConnectClientSecret == "" {
				response.BadRequest(c, "DingTalk Client Secret is required when enabled")
				return
			}
			req.DingTalkConnectClientSecret = previousSettings.DingTalkConnectClientSecret
		}

		dingTalkCfg := config.DingTalkConnectConfig{
			Enabled:               true,
			DingTalkAppKind:       "internal_app",
			AppType:               "internal",
			CorpRestrictionPolicy: req.DingTalkConnectCorpRestrictionPolicy,
			InternalCorpID:        req.DingTalkConnectInternalCorpID,
		}
		if dingTalkCfg.CorpRestrictionPolicy == "" {
			dingTalkCfg.CorpRestrictionPolicy = previousSettings.DingTalkConnectCorpRestrictionPolicy
		}
		if dingTalkCfg.CorpRestrictionPolicy == "internal_only" {
			dingTalkCfg.AppType = "internal"
		} else {
			dingTalkCfg.AppType = "public"
		}
		if err := config.ValidateDingTalkConfig(dingTalkCfg); err != nil {
			response.ErrorWithDetails(c, http.StatusBadRequest, err.Error(), mapDingTalkValidateError(err), nil)
			return
		}

		if dingTalkCfg.CorpRestrictionPolicy != "internal_only" {
			req.DingTalkConnectBypassRegistration = false
			req.DingTalkConnectSyncCorpEmail = false
			req.DingTalkConnectSyncDisplayName = false
			req.DingTalkConnectSyncDept = false
		}

		req.DingTalkConnectSyncCorpEmailAttrKey = strings.TrimSpace(req.DingTalkConnectSyncCorpEmailAttrKey)
		if req.DingTalkConnectSyncCorpEmailAttrKey == "" {
			req.DingTalkConnectSyncCorpEmailAttrKey = "dingtalk_email"
		}
		req.DingTalkConnectSyncDisplayNameAttrKey = strings.TrimSpace(req.DingTalkConnectSyncDisplayNameAttrKey)
		if req.DingTalkConnectSyncDisplayNameAttrKey == "" {
			req.DingTalkConnectSyncDisplayNameAttrKey = "dingtalk_name"
		}
		req.DingTalkConnectSyncDeptAttrKey = strings.TrimSpace(req.DingTalkConnectSyncDeptAttrKey)
		if req.DingTalkConnectSyncDeptAttrKey == "" {
			req.DingTalkConnectSyncDeptAttrKey = "dingtalk_department"
		}
		req.DingTalkConnectSyncCorpEmailAttrName = strings.TrimSpace(req.DingTalkConnectSyncCorpEmailAttrName)
		if req.DingTalkConnectSyncCorpEmailAttrName == "" {
			req.DingTalkConnectSyncCorpEmailAttrName = "钉钉企业邮箱"
		}
		req.DingTalkConnectSyncDisplayNameAttrName = strings.TrimSpace(req.DingTalkConnectSyncDisplayNameAttrName)
		if req.DingTalkConnectSyncDisplayNameAttrName == "" {
			req.DingTalkConnectSyncDisplayNameAttrName = "钉钉姓名"
		}
		req.DingTalkConnectSyncDeptAttrName = strings.TrimSpace(req.DingTalkConnectSyncDeptAttrName)
		if req.DingTalkConnectSyncDeptAttrName == "" {
			req.DingTalkConnectSyncDeptAttrName = "钉钉部门"
		}
	}

	oidcUsePKCE := previousSettings.OIDCConnectUsePKCE
	if req.OIDCConnectUsePKCE != nil {
		oidcUsePKCE = *req.OIDCConnectUsePKCE
	}
	oidcValidateIDToken := previousSettings.OIDCConnectValidateIDToken
	if req.OIDCConnectValidateIDToken != nil {
		oidcValidateIDToken = *req.OIDCConnectValidateIDToken
	}
	if req.OIDCConnectUsePKCE == nil || req.OIDCConnectValidateIDToken == nil {
		defaultUsePKCE, defaultValidateIDToken := h.settingService.OIDCSecurityWriteDefaults(c.Request.Context())
		if req.OIDCConnectUsePKCE == nil {
			oidcUsePKCE = defaultUsePKCE
		}
		if req.OIDCConnectValidateIDToken == nil {
			oidcValidateIDToken = defaultValidateIDToken
		}
	}
	oidcClockSkewSeconds := previousSettings.OIDCConnectClockSkewSeconds
	if req.OIDCConnectClockSkewSeconds != nil {
		oidcClockSkewSeconds = *req.OIDCConnectClockSkewSeconds
	}

	// Generic OIDC 参数验证
	if req.OIDCConnectEnabled {
		req.OIDCConnectProviderName = strings.TrimSpace(req.OIDCConnectProviderName)
		req.OIDCConnectClientID = strings.TrimSpace(req.OIDCConnectClientID)
		req.OIDCConnectClientSecret = strings.TrimSpace(req.OIDCConnectClientSecret)
		req.OIDCConnectIssuerURL = strings.TrimSpace(req.OIDCConnectIssuerURL)
		req.OIDCConnectDiscoveryURL = strings.TrimSpace(req.OIDCConnectDiscoveryURL)
		req.OIDCConnectAuthorizeURL = strings.TrimSpace(req.OIDCConnectAuthorizeURL)
		req.OIDCConnectTokenURL = strings.TrimSpace(req.OIDCConnectTokenURL)
		req.OIDCConnectUserInfoURL = strings.TrimSpace(req.OIDCConnectUserInfoURL)
		req.OIDCConnectJWKSURL = strings.TrimSpace(req.OIDCConnectJWKSURL)
		req.OIDCConnectScopes = strings.TrimSpace(req.OIDCConnectScopes)
		req.OIDCConnectRedirectURL = strings.TrimSpace(req.OIDCConnectRedirectURL)
		req.OIDCConnectFrontendRedirectURL = strings.TrimSpace(req.OIDCConnectFrontendRedirectURL)
		req.OIDCConnectTokenAuthMethod = strings.ToLower(strings.TrimSpace(req.OIDCConnectTokenAuthMethod))
		req.OIDCConnectAllowedSigningAlgs = strings.TrimSpace(req.OIDCConnectAllowedSigningAlgs)
		req.OIDCConnectUserInfoEmailPath = strings.TrimSpace(req.OIDCConnectUserInfoEmailPath)
		req.OIDCConnectUserInfoIDPath = strings.TrimSpace(req.OIDCConnectUserInfoIDPath)
		req.OIDCConnectUserInfoUsernamePath = strings.TrimSpace(req.OIDCConnectUserInfoUsernamePath)

		if req.OIDCConnectProviderName == "" {
			req.OIDCConnectProviderName = strings.TrimSpace(previousSettings.OIDCConnectProviderName)
		}
		if req.OIDCConnectClientID == "" {
			req.OIDCConnectClientID = strings.TrimSpace(previousSettings.OIDCConnectClientID)
		}
		if req.OIDCConnectIssuerURL == "" {
			req.OIDCConnectIssuerURL = strings.TrimSpace(previousSettings.OIDCConnectIssuerURL)
		}
		if req.OIDCConnectDiscoveryURL == "" {
			req.OIDCConnectDiscoveryURL = strings.TrimSpace(previousSettings.OIDCConnectDiscoveryURL)
		}
		if req.OIDCConnectAuthorizeURL == "" {
			req.OIDCConnectAuthorizeURL = strings.TrimSpace(previousSettings.OIDCConnectAuthorizeURL)
		}
		if req.OIDCConnectTokenURL == "" {
			req.OIDCConnectTokenURL = strings.TrimSpace(previousSettings.OIDCConnectTokenURL)
		}
		if req.OIDCConnectUserInfoURL == "" {
			req.OIDCConnectUserInfoURL = strings.TrimSpace(previousSettings.OIDCConnectUserInfoURL)
		}
		if req.OIDCConnectJWKSURL == "" {
			req.OIDCConnectJWKSURL = strings.TrimSpace(previousSettings.OIDCConnectJWKSURL)
		}
		if req.OIDCConnectScopes == "" {
			req.OIDCConnectScopes = strings.TrimSpace(previousSettings.OIDCConnectScopes)
		}
		if req.OIDCConnectRedirectURL == "" {
			req.OIDCConnectRedirectURL = strings.TrimSpace(previousSettings.OIDCConnectRedirectURL)
		}
		if req.OIDCConnectFrontendRedirectURL == "" {
			req.OIDCConnectFrontendRedirectURL = strings.TrimSpace(previousSettings.OIDCConnectFrontendRedirectURL)
		}
		if req.OIDCConnectTokenAuthMethod == "" {
			req.OIDCConnectTokenAuthMethod = strings.ToLower(strings.TrimSpace(previousSettings.OIDCConnectTokenAuthMethod))
		}
		if req.OIDCConnectAllowedSigningAlgs == "" {
			req.OIDCConnectAllowedSigningAlgs = strings.TrimSpace(previousSettings.OIDCConnectAllowedSigningAlgs)
		}
		if req.OIDCConnectUserInfoEmailPath == "" {
			req.OIDCConnectUserInfoEmailPath = strings.TrimSpace(previousSettings.OIDCConnectUserInfoEmailPath)
		}
		if req.OIDCConnectUserInfoIDPath == "" {
			req.OIDCConnectUserInfoIDPath = strings.TrimSpace(previousSettings.OIDCConnectUserInfoIDPath)
		}
		if req.OIDCConnectUserInfoUsernamePath == "" {
			req.OIDCConnectUserInfoUsernamePath = strings.TrimSpace(previousSettings.OIDCConnectUserInfoUsernamePath)
		}

		if req.OIDCConnectProviderName == "" {
			req.OIDCConnectProviderName = "OIDC"
		}
		if req.OIDCConnectClientID == "" {
			response.BadRequest(c, "OIDC Client ID is required when enabled")
			return
		}
		if req.OIDCConnectIssuerURL == "" {
			response.BadRequest(c, "OIDC Issuer URL is required when enabled")
			return
		}
		if err := config.ValidateAbsoluteHTTPURL(req.OIDCConnectIssuerURL); err != nil {
			response.BadRequest(c, "OIDC Issuer URL must be an absolute http(s) URL")
			return
		}
		if req.OIDCConnectDiscoveryURL != "" {
			if err := config.ValidateAbsoluteHTTPURL(req.OIDCConnectDiscoveryURL); err != nil {
				response.BadRequest(c, "OIDC Discovery URL must be an absolute http(s) URL")
				return
			}
		}
		if req.OIDCConnectAuthorizeURL != "" {
			if err := config.ValidateAbsoluteHTTPURL(req.OIDCConnectAuthorizeURL); err != nil {
				response.BadRequest(c, "OIDC Authorize URL must be an absolute http(s) URL")
				return
			}
		}
		if req.OIDCConnectTokenURL != "" {
			if err := config.ValidateAbsoluteHTTPURL(req.OIDCConnectTokenURL); err != nil {
				response.BadRequest(c, "OIDC Token URL must be an absolute http(s) URL")
				return
			}
		}
		if req.OIDCConnectUserInfoURL != "" {
			if err := config.ValidateAbsoluteHTTPURL(req.OIDCConnectUserInfoURL); err != nil {
				response.BadRequest(c, "OIDC UserInfo URL must be an absolute http(s) URL")
				return
			}
		}
		if req.OIDCConnectRedirectURL == "" {
			response.BadRequest(c, "OIDC Redirect URL is required when enabled")
			return
		}
		if err := config.ValidateAbsoluteHTTPURL(req.OIDCConnectRedirectURL); err != nil {
			response.BadRequest(c, "OIDC Redirect URL must be an absolute http(s) URL")
			return
		}
		if req.OIDCConnectFrontendRedirectURL == "" {
			response.BadRequest(c, "OIDC Frontend Redirect URL is required when enabled")
			return
		}
		if err := config.ValidateFrontendRedirectURL(req.OIDCConnectFrontendRedirectURL); err != nil {
			response.BadRequest(c, "OIDC Frontend Redirect URL is invalid")
			return
		}
		if !scopesContainOpenID(req.OIDCConnectScopes) {
			response.BadRequest(c, "OIDC scopes must contain openid")
			return
		}
		switch req.OIDCConnectTokenAuthMethod {
		case "", "client_secret_post", "client_secret_basic", "none":
		default:
			response.BadRequest(c, "OIDC Token Auth Method must be one of client_secret_post/client_secret_basic/none")
			return
		}
		if req.OIDCConnectTokenAuthMethod == "none" && !oidcUsePKCE {
			response.BadRequest(c, "OIDC PKCE must be enabled when token_auth_method=none")
			return
		}
		if oidcClockSkewSeconds < 0 || oidcClockSkewSeconds > 600 {
			response.BadRequest(c, "OIDC clock skew seconds must be between 0 and 600")
			return
		}
		if oidcValidateIDToken {
			if req.OIDCConnectAllowedSigningAlgs == "" {
				response.BadRequest(c, "OIDC Allowed Signing Algs is required when validate_id_token=true")
				return
			}
		}
		if req.OIDCConnectJWKSURL != "" {
			if err := config.ValidateAbsoluteHTTPURL(req.OIDCConnectJWKSURL); err != nil {
				response.BadRequest(c, "OIDC JWKS URL must be an absolute http(s) URL")
				return
			}
		}
		if req.OIDCConnectTokenAuthMethod == "" || req.OIDCConnectTokenAuthMethod == "client_secret_post" || req.OIDCConnectTokenAuthMethod == "client_secret_basic" {
			if req.OIDCConnectClientSecret == "" {
				if previousSettings.OIDCConnectClientSecret == "" {
					response.BadRequest(c, "OIDC Client Secret is required when enabled")
					return
				}
				req.OIDCConnectClientSecret = previousSettings.OIDCConnectClientSecret
			}
		}
	}

	// “购买订阅”页面配置验证
	purchaseEnabled := previousSettings.PurchaseSubscriptionEnabled
	if req.PurchaseSubscriptionEnabled != nil {
		purchaseEnabled = *req.PurchaseSubscriptionEnabled
	}
	purchaseURL := previousSettings.PurchaseSubscriptionURL
	if req.PurchaseSubscriptionURL != nil {
		purchaseURL = strings.TrimSpace(*req.PurchaseSubscriptionURL)
	}

	// - 启用时要求 URL 合法且非空
	// - 禁用时允许为空；若提供了 URL 也做基本校验，避免误配置
	if purchaseEnabled {
		if purchaseURL == "" {
			response.BadRequest(c, "Purchase Subscription URL is required when enabled")
			return
		}
		if err := config.ValidateAbsoluteHTTPURL(purchaseURL); err != nil {
			response.BadRequest(c, "Purchase Subscription URL must be an absolute http(s) URL")
			return
		}
	} else if purchaseURL != "" {
		if err := config.ValidateAbsoluteHTTPURL(purchaseURL); err != nil {
			response.BadRequest(c, "Purchase Subscription URL must be an absolute http(s) URL")
			return
		}
	}

	// Frontend URL 验证
	req.FrontendURL = strings.TrimSpace(req.FrontendURL)
	if req.FrontendURL != "" {
		if err := config.ValidateAbsoluteHTTPURL(req.FrontendURL); err != nil {
			response.BadRequest(c, "Frontend URL must be an absolute http(s) URL")
			return
		}
	}

	// 自定义菜单项验证
	const (
		maxCustomMenuItems    = 20
		maxMenuItemLabelLen   = 50
		maxMenuItemURLLen     = 2048
		maxMenuItemIconSVGLen = 10 * 1024 // 10KB
		maxMenuItemIDLen      = 32
	)

	customMenuJSON := previousSettings.CustomMenuItems
	hiddenAdminMenuItemsJSON := previousSettings.HiddenAdminMenuItems
	if req.HiddenAdminMenuItems != nil {
		items := *req.HiddenAdminMenuItems
		seen := make(map[string]struct{}, len(items))
		normalized := make([]string, 0, len(items))
		for _, item := range items {
			key := strings.TrimSpace(item)
			if key == "" {
				continue
			}
			if _, ok := allowedHiddenAdminMenuItemKeys[key]; !ok {
				response.BadRequest(c, "Invalid hidden admin menu item key: "+key)
				return
			}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			normalized = append(normalized, key)
		}
		hiddenMenuBytes, err := json.Marshal(normalized)
		if err != nil {
			response.BadRequest(c, "Failed to serialize hidden admin menu items")
			return
		}
		hiddenAdminMenuItemsJSON = string(hiddenMenuBytes)
	}

	if req.CustomMenuItems != nil {
		items := *req.CustomMenuItems
		if len(items) > maxCustomMenuItems {
			response.BadRequest(c, "Too many custom menu items (max 20)")
			return
		}
		for i, item := range items {
			if strings.TrimSpace(item.Label) == "" {
				response.BadRequest(c, "Custom menu item label is required")
				return
			}
			if len(item.Label) > maxMenuItemLabelLen {
				response.BadRequest(c, "Custom menu item label is too long (max 50 characters)")
				return
			}
			urlTrimmed := strings.TrimSpace(item.URL)
			if strings.HasPrefix(urlTrimmed, "md:") {
				// Markdown page mode: URL = "md:<slug>"
				slug := strings.TrimPrefix(urlTrimmed, "md:")
				if slug == "" {
					response.BadRequest(c, "Custom menu item markdown slug cannot be empty (use md:slug format)")
					return
				}
			} else {
				if urlTrimmed == "" {
					response.BadRequest(c, "Custom menu item URL is required (use md:slug for markdown pages)")
					return
				}
				if len(item.URL) > maxMenuItemURLLen {
					response.BadRequest(c, "Custom menu item URL is too long (max 2048 characters)")
					return
				}
				if err := config.ValidateAbsoluteHTTPURL(urlTrimmed); err != nil {
					response.BadRequest(c, "Custom menu item URL must be an absolute http(s) URL or md:<slug>")
					return
				}
			}
			if item.Visibility != "user" && item.Visibility != "admin" {
				response.BadRequest(c, "Custom menu item visibility must be 'user' or 'admin'")
				return
			}
			if len(item.IconSVG) > maxMenuItemIconSVGLen {
				response.BadRequest(c, "Custom menu item icon SVG is too large (max 10KB)")
				return
			}
			// Auto-generate ID if missing
			if strings.TrimSpace(item.ID) == "" {
				id, err := generateMenuItemID()
				if err != nil {
					response.Error(c, http.StatusInternalServerError, "Failed to generate menu item ID")
					return
				}
				items[i].ID = id
			} else if len(item.ID) > maxMenuItemIDLen {
				response.BadRequest(c, "Custom menu item ID is too long (max 32 characters)")
				return
			} else if !menuItemIDPattern.MatchString(item.ID) {
				response.BadRequest(c, "Custom menu item ID contains invalid characters (only a-z, A-Z, 0-9, - and _ are allowed)")
				return
			}
		}
		// ID uniqueness check
		seen := make(map[string]struct{}, len(items))
		for _, item := range items {
			if _, exists := seen[item.ID]; exists {
				response.BadRequest(c, "Duplicate custom menu item ID: "+item.ID)
				return
			}
			seen[item.ID] = struct{}{}
		}
		menuBytes, err := json.Marshal(items)
		if err != nil {
			response.BadRequest(c, "Failed to serialize custom menu items")
			return
		}
		customMenuJSON = string(menuBytes)
	}

	// 自定义端点验证
	const (
		maxCustomEndpoints        = 10
		maxEndpointNameLen        = 50
		maxEndpointURLLen         = 2048
		maxEndpointDescriptionLen = 200
	)

	customEndpointsJSON := previousSettings.CustomEndpoints
	if req.CustomEndpoints != nil {
		endpoints := *req.CustomEndpoints
		if len(endpoints) > maxCustomEndpoints {
			response.BadRequest(c, "Too many custom endpoints (max 10)")
			return
		}
		for _, ep := range endpoints {
			if strings.TrimSpace(ep.Name) == "" {
				response.BadRequest(c, "Custom endpoint name is required")
				return
			}
			if len(ep.Name) > maxEndpointNameLen {
				response.BadRequest(c, "Custom endpoint name is too long (max 50 characters)")
				return
			}
			if strings.TrimSpace(ep.Endpoint) == "" {
				response.BadRequest(c, "Custom endpoint URL is required")
				return
			}
			if len(ep.Endpoint) > maxEndpointURLLen {
				response.BadRequest(c, "Custom endpoint URL is too long (max 2048 characters)")
				return
			}
			if err := config.ValidateAbsoluteHTTPURL(strings.TrimSpace(ep.Endpoint)); err != nil {
				response.BadRequest(c, "Custom endpoint URL must be an absolute http(s) URL")
				return
			}
			if len(ep.Description) > maxEndpointDescriptionLen {
				response.BadRequest(c, "Custom endpoint description is too long (max 200 characters)")
				return
			}
		}
		endpointBytes, err := json.Marshal(endpoints)
		if err != nil {
			response.BadRequest(c, "Failed to serialize custom endpoints")
			return
		}
		customEndpointsJSON = string(endpointBytes)
	}

	// Ops metrics collector interval validation (seconds).
	if req.OpsMetricsIntervalSeconds != nil {
		v := *req.OpsMetricsIntervalSeconds
		if v < 60 {
			v = 60
		}
		if v > 3600 {
			v = 3600
		}
		req.OpsMetricsIntervalSeconds = &v
	}
	defaultSubscriptions := make([]service.DefaultSubscriptionSetting, 0, len(req.DefaultSubscriptions))
	for _, sub := range req.DefaultSubscriptions {
		defaultSubscriptions = append(defaultSubscriptions, service.DefaultSubscriptionSetting{
			GroupID:      sub.GroupID,
			ValidityDays: sub.ValidityDays,
		})
	}
	nextAuthDefaults := cloneAuthSourceDefaultSettings(previousAuthDefaults)
	if req.AuthSourceDefaultEmailBalance != nil {
		nextAuthDefaults.Email.Balance = *req.AuthSourceDefaultEmailBalance
	}
	if req.AuthSourceDefaultEmailConcurrency != nil {
		nextAuthDefaults.Email.Concurrency = *req.AuthSourceDefaultEmailConcurrency
	}
	if req.AuthSourceDefaultEmailSubscriptions != nil {
		normalized := normalizeDefaultSubscriptions(*req.AuthSourceDefaultEmailSubscriptions)
		nextAuthDefaults.Email.Subscriptions = serviceDefaultSubscriptionsFromDTO(normalized)
	}
	if req.AuthSourceDefaultEmailGrantOnSignup != nil {
		nextAuthDefaults.Email.GrantOnSignup = *req.AuthSourceDefaultEmailGrantOnSignup
	}
	if req.AuthSourceDefaultEmailGrantOnFirstBind != nil {
		nextAuthDefaults.Email.GrantOnFirstBind = *req.AuthSourceDefaultEmailGrantOnFirstBind
	}
	if req.AuthSourceDefaultLinuxDoBalance != nil {
		nextAuthDefaults.LinuxDo.Balance = *req.AuthSourceDefaultLinuxDoBalance
	}
	if req.AuthSourceDefaultLinuxDoConcurrency != nil {
		nextAuthDefaults.LinuxDo.Concurrency = *req.AuthSourceDefaultLinuxDoConcurrency
	}
	if req.AuthSourceDefaultLinuxDoSubscriptions != nil {
		normalized := normalizeDefaultSubscriptions(*req.AuthSourceDefaultLinuxDoSubscriptions)
		nextAuthDefaults.LinuxDo.Subscriptions = serviceDefaultSubscriptionsFromDTO(normalized)
	}
	if req.AuthSourceDefaultLinuxDoGrantOnSignup != nil {
		nextAuthDefaults.LinuxDo.GrantOnSignup = *req.AuthSourceDefaultLinuxDoGrantOnSignup
	}
	if req.AuthSourceDefaultLinuxDoGrantOnFirstBind != nil {
		nextAuthDefaults.LinuxDo.GrantOnFirstBind = *req.AuthSourceDefaultLinuxDoGrantOnFirstBind
	}
	if req.AuthSourceDefaultOIDCBalance != nil {
		nextAuthDefaults.OIDC.Balance = *req.AuthSourceDefaultOIDCBalance
	}
	if req.AuthSourceDefaultOIDCConcurrency != nil {
		nextAuthDefaults.OIDC.Concurrency = *req.AuthSourceDefaultOIDCConcurrency
	}
	if req.AuthSourceDefaultOIDCSubscriptions != nil {
		normalized := normalizeDefaultSubscriptions(*req.AuthSourceDefaultOIDCSubscriptions)
		nextAuthDefaults.OIDC.Subscriptions = serviceDefaultSubscriptionsFromDTO(normalized)
	}
	if req.AuthSourceDefaultOIDCGrantOnSignup != nil {
		nextAuthDefaults.OIDC.GrantOnSignup = *req.AuthSourceDefaultOIDCGrantOnSignup
	}
	if req.AuthSourceDefaultOIDCGrantOnFirstBind != nil {
		nextAuthDefaults.OIDC.GrantOnFirstBind = *req.AuthSourceDefaultOIDCGrantOnFirstBind
	}
	if req.AuthSourceDefaultWeChatBalance != nil {
		nextAuthDefaults.WeChat.Balance = *req.AuthSourceDefaultWeChatBalance
	}
	if req.AuthSourceDefaultWeChatConcurrency != nil {
		nextAuthDefaults.WeChat.Concurrency = *req.AuthSourceDefaultWeChatConcurrency
	}
	if req.AuthSourceDefaultWeChatSubscriptions != nil {
		normalized := normalizeDefaultSubscriptions(*req.AuthSourceDefaultWeChatSubscriptions)
		nextAuthDefaults.WeChat.Subscriptions = serviceDefaultSubscriptionsFromDTO(normalized)
	}
	if req.AuthSourceDefaultWeChatGrantOnSignup != nil {
		nextAuthDefaults.WeChat.GrantOnSignup = *req.AuthSourceDefaultWeChatGrantOnSignup
	}
	if req.AuthSourceDefaultWeChatGrantOnFirstBind != nil {
		nextAuthDefaults.WeChat.GrantOnFirstBind = *req.AuthSourceDefaultWeChatGrantOnFirstBind
	}
	if req.AuthSourceDefaultGitHubBalance != nil {
		nextAuthDefaults.GitHub.Balance = *req.AuthSourceDefaultGitHubBalance
	}
	if req.AuthSourceDefaultGitHubConcurrency != nil {
		nextAuthDefaults.GitHub.Concurrency = *req.AuthSourceDefaultGitHubConcurrency
	}
	if req.AuthSourceDefaultGitHubSubscriptions != nil {
		normalized := normalizeDefaultSubscriptions(*req.AuthSourceDefaultGitHubSubscriptions)
		nextAuthDefaults.GitHub.Subscriptions = serviceDefaultSubscriptionsFromDTO(normalized)
	}
	if req.AuthSourceDefaultGitHubGrantOnSignup != nil {
		nextAuthDefaults.GitHub.GrantOnSignup = *req.AuthSourceDefaultGitHubGrantOnSignup
	}
	if req.AuthSourceDefaultGitHubGrantOnFirstBind != nil {
		nextAuthDefaults.GitHub.GrantOnFirstBind = *req.AuthSourceDefaultGitHubGrantOnFirstBind
	}
	if req.AuthSourceDefaultGoogleBalance != nil {
		nextAuthDefaults.Google.Balance = *req.AuthSourceDefaultGoogleBalance
	}
	if req.AuthSourceDefaultGoogleConcurrency != nil {
		nextAuthDefaults.Google.Concurrency = *req.AuthSourceDefaultGoogleConcurrency
	}
	if req.AuthSourceDefaultGoogleSubscriptions != nil {
		normalized := normalizeDefaultSubscriptions(*req.AuthSourceDefaultGoogleSubscriptions)
		nextAuthDefaults.Google.Subscriptions = serviceDefaultSubscriptionsFromDTO(normalized)
	}
	if req.AuthSourceDefaultGoogleGrantOnSignup != nil {
		nextAuthDefaults.Google.GrantOnSignup = *req.AuthSourceDefaultGoogleGrantOnSignup
	}
	if req.AuthSourceDefaultGoogleGrantOnFirstBind != nil {
		nextAuthDefaults.Google.GrantOnFirstBind = *req.AuthSourceDefaultGoogleGrantOnFirstBind
	}
	if req.AuthSourceDefaultDingTalkBalance != nil {
		nextAuthDefaults.DingTalk.Balance = *req.AuthSourceDefaultDingTalkBalance
	}
	if req.AuthSourceDefaultDingTalkConcurrency != nil {
		nextAuthDefaults.DingTalk.Concurrency = *req.AuthSourceDefaultDingTalkConcurrency
	}
	if req.AuthSourceDefaultDingTalkSubscriptions != nil {
		normalized := normalizeDefaultSubscriptions(*req.AuthSourceDefaultDingTalkSubscriptions)
		nextAuthDefaults.DingTalk.Subscriptions = serviceDefaultSubscriptionsFromDTO(normalized)
	}
	if req.AuthSourceDefaultDingTalkGrantOnSignup != nil {
		nextAuthDefaults.DingTalk.GrantOnSignup = *req.AuthSourceDefaultDingTalkGrantOnSignup
	}
	if req.AuthSourceDefaultDingTalkGrantOnFirstBind != nil {
		nextAuthDefaults.DingTalk.GrantOnFirstBind = *req.AuthSourceDefaultDingTalkGrantOnFirstBind
	}
	if req.ForceEmailOnThirdPartySignup != nil {
		nextAuthDefaults.ForceEmailOnThirdPartySignup = *req.ForceEmailOnThirdPartySignup
	}

	// 验证最低版本号格式（空字符串=禁用，或合法 semver）
	if req.MinClaudeCodeVersion != "" {
		if !semverPattern.MatchString(req.MinClaudeCodeVersion) {
			response.Error(c, http.StatusBadRequest, "min_claude_code_version must be empty or a valid semver (e.g. 2.1.63)")
			return
		}
	}

	// 验证最高版本号格式（空字符串=禁用，或合法 semver）
	if req.MaxClaudeCodeVersion != "" {
		if !semverPattern.MatchString(req.MaxClaudeCodeVersion) {
			response.Error(c, http.StatusBadRequest, "max_claude_code_version must be empty or a valid semver (e.g. 3.0.0)")
			return
		}
	}
	if req.AntigravityUserAgentVersion != nil {
		normalized := strings.TrimSpace(*req.AntigravityUserAgentVersion)
		req.AntigravityUserAgentVersion = &normalized
		if normalized != "" && !semverPattern.MatchString(normalized) {
			response.Error(c, http.StatusBadRequest, "antigravity_user_agent_version must be empty or a valid semver (e.g. 1.23.2)")
			return
		}
	}

	// 交叉验证：如果同时设置了最低和最高版本号，最高版本号必须 >= 最低版本号
	if req.MinClaudeCodeVersion != "" && req.MaxClaudeCodeVersion != "" {
		if service.CompareVersions(req.MaxClaudeCodeVersion, req.MinClaudeCodeVersion) < 0 {
			response.Error(c, http.StatusBadRequest, "max_claude_code_version must be greater than or equal to min_claude_code_version")
			return
		}
	}

	settings := &service.SystemSettings{
		RegistrationEnabled:              req.RegistrationEnabled,
		EmailVerifyEnabled:               req.EmailVerifyEnabled,
		RegistrationEmailSuffixWhitelist: req.RegistrationEmailSuffixWhitelist,
		PromoCodeEnabled:                 req.PromoCodeEnabled,
		PasswordResetEnabled:             req.PasswordResetEnabled,
		FrontendURL:                      req.FrontendURL,
		InvitationCodeEnabled:            req.InvitationCodeEnabled,
		TotpEnabled:                      req.TotpEnabled,
		LoginAgreementEnabled:            req.LoginAgreementEnabled,
		LoginAgreementMode:               loginAgreementMode,
		LoginAgreementUpdatedAt:          loginAgreementUpdatedAt,
		LoginAgreementDocuments:          loginAgreementDocuments,
		SMTPHost:                         req.SMTPHost,
		SMTPPort:                         req.SMTPPort,
		SMTPUsername:                     req.SMTPUsername,
		SMTPPassword:                     req.SMTPPassword,
		SMTPFrom:                         req.SMTPFrom,
		SMTPFromName:                     req.SMTPFromName,
		SMTPUseTLS:                       req.SMTPUseTLS,
		TurnstileEnabled:                 req.TurnstileEnabled,
		TurnstileSiteKey:                 req.TurnstileSiteKey,
		TurnstileSecretKey:               req.TurnstileSecretKey,
		APIKeyACLTrustForwardedIP: func() bool {
			if req.APIKeyACLTrustForwardedIP != nil {
				return *req.APIKeyACLTrustForwardedIP
			}
			return previousSettings.APIKeyACLTrustForwardedIP
		}(),
		LinuxDoConnectEnabled:                  req.LinuxDoConnectEnabled,
		LinuxDoConnectClientID:                 req.LinuxDoConnectClientID,
		LinuxDoConnectClientSecret:             req.LinuxDoConnectClientSecret,
		LinuxDoConnectRedirectURL:              req.LinuxDoConnectRedirectURL,
		DingTalkConnectEnabled:                 req.DingTalkConnectEnabled,
		DingTalkConnectClientID:                req.DingTalkConnectClientID,
		DingTalkConnectClientSecret:            req.DingTalkConnectClientSecret,
		DingTalkConnectRedirectURL:             req.DingTalkConnectRedirectURL,
		DingTalkConnectCorpRestrictionPolicy:   req.DingTalkConnectCorpRestrictionPolicy,
		DingTalkConnectInternalCorpID:          req.DingTalkConnectInternalCorpID,
		DingTalkConnectBypassRegistration:      req.DingTalkConnectBypassRegistration,
		DingTalkConnectSyncCorpEmail:           req.DingTalkConnectSyncCorpEmail,
		DingTalkConnectSyncDisplayName:         req.DingTalkConnectSyncDisplayName,
		DingTalkConnectSyncDept:                req.DingTalkConnectSyncDept,
		DingTalkConnectSyncCorpEmailAttrKey:    req.DingTalkConnectSyncCorpEmailAttrKey,
		DingTalkConnectSyncDisplayNameAttrKey:  req.DingTalkConnectSyncDisplayNameAttrKey,
		DingTalkConnectSyncDeptAttrKey:         req.DingTalkConnectSyncDeptAttrKey,
		DingTalkConnectSyncCorpEmailAttrName:   req.DingTalkConnectSyncCorpEmailAttrName,
		DingTalkConnectSyncDisplayNameAttrName: req.DingTalkConnectSyncDisplayNameAttrName,
		DingTalkConnectSyncDeptAttrName:        req.DingTalkConnectSyncDeptAttrName,
		WeChatConnectEnabled:                   req.WeChatConnectEnabled,
		WeChatConnectAppID:                     req.WeChatConnectAppID,
		WeChatConnectAppSecret:                 req.WeChatConnectAppSecret,
		WeChatConnectOpenEnabled:               req.WeChatConnectOpenEnabled,
		WeChatConnectOpenAppID:                 req.WeChatConnectOpenAppID,
		WeChatConnectOpenAppSecret:             req.WeChatConnectOpenAppSecret,
		WeChatConnectMPEnabled:                 req.WeChatConnectMPEnabled,
		WeChatConnectMPAppID:                   req.WeChatConnectMPAppID,
		WeChatConnectMPAppSecret:               req.WeChatConnectMPAppSecret,
		WeChatConnectMobileEnabled:             req.WeChatConnectMobileEnabled,
		WeChatConnectMobileAppID:               req.WeChatConnectMobileAppID,
		WeChatConnectMobileAppSecret:           req.WeChatConnectMobileAppSecret,
		WeChatConnectMode:                      req.WeChatConnectMode,
		WeChatConnectScopes:                    req.WeChatConnectScopes,
		WeChatConnectRedirectURL:               req.WeChatConnectRedirectURL,
		WeChatConnectFrontendRedirectURL:       req.WeChatConnectFrontendRedirectURL,
		OIDCConnectEnabled:                     req.OIDCConnectEnabled,
		OIDCConnectProviderName:                req.OIDCConnectProviderName,
		OIDCConnectClientID:                    req.OIDCConnectClientID,
		OIDCConnectClientSecret:                req.OIDCConnectClientSecret,
		OIDCConnectIssuerURL:                   req.OIDCConnectIssuerURL,
		OIDCConnectDiscoveryURL:                req.OIDCConnectDiscoveryURL,
		OIDCConnectAuthorizeURL:                req.OIDCConnectAuthorizeURL,
		OIDCConnectTokenURL:                    req.OIDCConnectTokenURL,
		OIDCConnectUserInfoURL:                 req.OIDCConnectUserInfoURL,
		OIDCConnectJWKSURL:                     req.OIDCConnectJWKSURL,
		OIDCConnectScopes:                      req.OIDCConnectScopes,
		OIDCConnectRedirectURL:                 req.OIDCConnectRedirectURL,
		OIDCConnectFrontendRedirectURL:         req.OIDCConnectFrontendRedirectURL,
		OIDCConnectTokenAuthMethod:             req.OIDCConnectTokenAuthMethod,
		OIDCConnectUsePKCE:                     oidcUsePKCE,
		OIDCConnectValidateIDToken:             oidcValidateIDToken,
		OIDCConnectAllowedSigningAlgs:          req.OIDCConnectAllowedSigningAlgs,
		OIDCConnectClockSkewSeconds:            oidcClockSkewSeconds,
		OIDCConnectRequireEmailVerified:        req.OIDCConnectRequireEmailVerified,
		OIDCConnectUserInfoEmailPath:           req.OIDCConnectUserInfoEmailPath,
		OIDCConnectUserInfoIDPath:              req.OIDCConnectUserInfoIDPath,
		OIDCConnectUserInfoUsernamePath:        req.OIDCConnectUserInfoUsernamePath,
		GitHubOAuthEnabled:                     req.GitHubOAuthEnabled,
		GitHubOAuthClientID:                    req.GitHubOAuthClientID,
		GitHubOAuthClientSecret:                req.GitHubOAuthClientSecret,
		GitHubOAuthRedirectURL:                 req.GitHubOAuthRedirectURL,
		GitHubOAuthFrontendRedirectURL:         req.GitHubOAuthFrontendRedirectURL,
		GoogleOAuthEnabled:                     req.GoogleOAuthEnabled,
		GoogleOAuthClientID:                    req.GoogleOAuthClientID,
		GoogleOAuthClientSecret:                req.GoogleOAuthClientSecret,
		GoogleOAuthRedirectURL:                 req.GoogleOAuthRedirectURL,
		GoogleOAuthFrontendRedirectURL:         req.GoogleOAuthFrontendRedirectURL,
		SiteName:                               req.SiteName,
		SiteLogo:                               req.SiteLogo,
		SiteSubtitle:                           req.SiteSubtitle,
		APIBaseURL:                             req.APIBaseURL,
		ContactInfo:                            req.ContactInfo,
		DocURL:                                 req.DocURL,
		HomeContent:                            req.HomeContent,
		HideCcsImportButton:                    req.HideCcsImportButton,
		PurchaseSubscriptionEnabled:            purchaseEnabled,
		PurchaseSubscriptionURL:                purchaseURL,
		TableDefaultPageSize:                   req.TableDefaultPageSize,
		TablePageSizeOptions:                   req.TablePageSizeOptions,
		HiddenAdminMenuItems:                   hiddenAdminMenuItemsJSON,
		CustomMenuItems:                        customMenuJSON,
		CustomEndpoints:                        customEndpointsJSON,
		DefaultConcurrency:                     req.DefaultConcurrency,
		DefaultBalance:                         req.DefaultBalance,
		AffiliateEnabled:                       affiliateEnabled,
		AffiliateRebateRate:                    affiliateRebateRate,
		AffiliateRebateFreezeHours:             affiliateRebateFreezeHours,
		AffiliateRebateDurationDays:            affiliateRebateDurationDays,
		AffiliateRebatePerInviteeCap:           affiliateRebatePerInviteeCap,
		DefaultUserRPMLimit:                    req.DefaultUserRPMLimit,
		DefaultSubscriptions:                   defaultSubscriptions,
		EnableModelFallback:                    req.EnableModelFallback,
		FallbackModelAnthropic:                 req.FallbackModelAnthropic,
		FallbackModelOpenAI:                    req.FallbackModelOpenAI,
		FallbackModelGemini:                    req.FallbackModelGemini,
		EnableIdentityPatch:                    req.EnableIdentityPatch,
		IdentityPatchPrompt:                    req.IdentityPatchPrompt,
		MinClaudeCodeVersion:                   req.MinClaudeCodeVersion,
		MaxClaudeCodeVersion:                   req.MaxClaudeCodeVersion,
		AllowUngroupedKeyScheduling:            req.AllowUngroupedKeyScheduling,
		BackendModeEnabled:                     req.BackendModeEnabled,
		OpsMonitoringEnabled: func() bool {
			if req.OpsMonitoringEnabled != nil {
				return *req.OpsMonitoringEnabled
			}
			return previousSettings.OpsMonitoringEnabled
		}(),
		OpsRealtimeMonitoringEnabled: func() bool {
			if req.OpsRealtimeMonitoringEnabled != nil {
				return *req.OpsRealtimeMonitoringEnabled
			}
			return previousSettings.OpsRealtimeMonitoringEnabled
		}(),
		OpsQueryModeDefault: func() string {
			if req.OpsQueryModeDefault != nil {
				return *req.OpsQueryModeDefault
			}
			return previousSettings.OpsQueryModeDefault
		}(),
		OpsMetricsIntervalSeconds: func() int {
			if req.OpsMetricsIntervalSeconds != nil {
				return *req.OpsMetricsIntervalSeconds
			}
			return previousSettings.OpsMetricsIntervalSeconds
		}(),
		EnableFingerprintUnification: func() bool {
			if req.EnableFingerprintUnification != nil {
				return *req.EnableFingerprintUnification
			}
			return previousSettings.EnableFingerprintUnification
		}(),
		EnableMetadataPassthrough: func() bool {
			if req.EnableMetadataPassthrough != nil {
				return *req.EnableMetadataPassthrough
			}
			return previousSettings.EnableMetadataPassthrough
		}(),
		DefaultUpstreamUserAgent: func() string {
			if req.DefaultUpstreamUserAgent != nil {
				return strings.TrimSpace(*req.DefaultUpstreamUserAgent)
			}
			return previousSettings.DefaultUpstreamUserAgent
		}(),
		ForceUnifiedUpstreamUserAgent: func() bool {
			if req.ForceUnifiedUpstreamUserAgent != nil {
				return *req.ForceUnifiedUpstreamUserAgent
			}
			return previousSettings.ForceUnifiedUpstreamUserAgent
		}(),
		UpdateGitHubRepo: func() string {
			if req.UpdateGitHubRepo != nil {
				return strings.TrimSpace(*req.UpdateGitHubRepo)
			}
			return previousSettings.UpdateGitHubRepo
		}(),
		EnableCCHSigning: func() bool {
			if req.EnableCCHSigning != nil {
				return *req.EnableCCHSigning
			}
			return previousSettings.EnableCCHSigning
		}(),
		AntigravityUserAgentVersion: func() string {
			if req.AntigravityUserAgentVersion != nil {
				return *req.AntigravityUserAgentVersion
			}
			return previousSettings.AntigravityUserAgentVersion
		}(),
		PaymentVisibleMethodAlipaySource: func() string {
			if req.PaymentVisibleMethodAlipaySource != nil {
				return strings.TrimSpace(*req.PaymentVisibleMethodAlipaySource)
			}
			return previousSettings.PaymentVisibleMethodAlipaySource
		}(),
		PaymentVisibleMethodWxpaySource: func() string {
			if req.PaymentVisibleMethodWxpaySource != nil {
				return strings.TrimSpace(*req.PaymentVisibleMethodWxpaySource)
			}
			return previousSettings.PaymentVisibleMethodWxpaySource
		}(),
		PaymentVisibleMethodAlipayEnabled: func() bool {
			if req.PaymentVisibleMethodAlipayEnabled != nil {
				return *req.PaymentVisibleMethodAlipayEnabled
			}
			return previousSettings.PaymentVisibleMethodAlipayEnabled
		}(),
		PaymentVisibleMethodWxpayEnabled: func() bool {
			if req.PaymentVisibleMethodWxpayEnabled != nil {
				return *req.PaymentVisibleMethodWxpayEnabled
			}
			return previousSettings.PaymentVisibleMethodWxpayEnabled
		}(),
		OpenAIAdvancedSchedulerEnabled: func() bool {
			if req.OpenAIAdvancedSchedulerEnabled != nil {
				return *req.OpenAIAdvancedSchedulerEnabled
			}
			return previousSettings.OpenAIAdvancedSchedulerEnabled
		}(),
		BalanceLowNotifyEnabled: func() bool {
			if req.BalanceLowNotifyEnabled != nil {
				return *req.BalanceLowNotifyEnabled
			}
			return previousSettings.BalanceLowNotifyEnabled
		}(),
		BalanceLowNotifyThreshold: func() float64 {
			if req.BalanceLowNotifyThreshold != nil {
				return *req.BalanceLowNotifyThreshold
			}
			return previousSettings.BalanceLowNotifyThreshold
		}(),
		BalanceLowNotifyRechargeURL: func() string {
			if req.BalanceLowNotifyRechargeURL != nil {
				return *req.BalanceLowNotifyRechargeURL
			}
			return previousSettings.BalanceLowNotifyRechargeURL
		}(),
		SubscriptionExpiryNotifyEnabled: func() bool {
			if req.SubscriptionExpiryNotifyEnabled != nil {
				return *req.SubscriptionExpiryNotifyEnabled
			}
			return previousSettings.SubscriptionExpiryNotifyEnabled
		}(),
		AccountQuotaNotifyEnabled: func() bool {
			if req.AccountQuotaNotifyEnabled != nil {
				return *req.AccountQuotaNotifyEnabled
			}
			return previousSettings.AccountQuotaNotifyEnabled
		}(),
		AccountQuotaNotifyEmails: func() []service.NotifyEmailEntry {
			if req.AccountQuotaNotifyEmails != nil {
				return dto.NotifyEmailEntriesToService(*req.AccountQuotaNotifyEmails)
			}
			return previousSettings.AccountQuotaNotifyEmails
		}(),
		ChannelMonitorEnabled: func() bool {
			if req.ChannelMonitorEnabled != nil {
				return *req.ChannelMonitorEnabled
			}
			return previousSettings.ChannelMonitorEnabled
		}(),
		ChannelMonitorDefaultIntervalSeconds: func() int {
			if req.ChannelMonitorDefaultIntervalSeconds != nil {
				return *req.ChannelMonitorDefaultIntervalSeconds
			}
			return previousSettings.ChannelMonitorDefaultIntervalSeconds
		}(),
		AvailableChannelsEnabled: func() bool {
			if req.AvailableChannelsEnabled != nil {
				return *req.AvailableChannelsEnabled
			}
			return previousSettings.AvailableChannelsEnabled
		}(),
	}

	if !equalAuthSourceDefaultSettings(previousAuthDefaults, nextAuthDefaults) {
		if err := h.settingService.UpdateAuthSourceDefaultSettings(c.Request.Context(), nextAuthDefaults); err != nil {
			response.ErrorFrom(c, err)
			return
		}
	}

	if err := h.settingService.UpdateSettings(c.Request.Context(), settings); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	// Update payment configuration (integrated into system settings).
	// Skip if no payment fields were provided (prevents accidental wipe).
	if h.paymentConfigService != nil && hasPaymentFields(req) {
		paymentReq := service.UpdatePaymentConfigRequest{
			Enabled:                   req.PaymentEnabled,
			MinAmount:                 req.PaymentMinAmount,
			MaxAmount:                 req.PaymentMaxAmount,
			DailyLimit:                req.PaymentDailyLimit,
			OrderTimeoutMin:           req.PaymentOrderTimeoutMin,
			MaxPendingOrders:          req.PaymentMaxPendingOrders,
			EnabledTypes:              req.PaymentEnabledTypes,
			BalanceDisabled:           req.PaymentBalanceDisabled,
			BalanceRechargeMultiplier: req.PaymentBalanceRechargeMultiplier,
			RechargeFeeRate:           req.PaymentRechargeFeeRate,
			LoadBalanceStrategy:       req.PaymentLoadBalanceStrat,
			ProductNamePrefix:         req.PaymentProductNamePrefix,
			ProductNameSuffix:         req.PaymentProductNameSuffix,
			HelpImageURL:              req.PaymentHelpImageURL,
			HelpText:                  req.PaymentHelpText,
			CancelRateLimitEnabled:    req.PaymentCancelRateLimitEnabled,
			CancelRateLimitMax:        req.PaymentCancelRateLimitMax,
			CancelRateLimitWindow:     req.PaymentCancelRateLimitWindow,
			CancelRateLimitUnit:       req.PaymentCancelRateLimitUnit,
			CancelRateLimitMode:       req.PaymentCancelRateLimitMode,
		}
		if err := h.paymentConfigService.UpdatePaymentConfig(c.Request.Context(), paymentReq); err != nil {
			response.ErrorFrom(c, err)
			return
		}
		// Refresh in-memory provider registry so config changes take effect immediately
		if h.paymentService != nil {
			h.paymentService.RefreshProviders(c.Request.Context())
		}
	}

	h.auditSettingsUpdate(c, previousSettings, settings, previousAuthDefaults, nextAuthDefaults, req)

	// 重新获取设置返回
	updatedSettings, err := h.settingService.GetAllSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	updatedAuthDefaults, err := h.settingService.GetAuthSourceDefaultSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	updatedDefaultSubscriptions := make([]dto.DefaultSubscriptionSetting, 0, len(updatedSettings.DefaultSubscriptions))
	for _, sub := range updatedSettings.DefaultSubscriptions {
		updatedDefaultSubscriptions = append(updatedDefaultSubscriptions, dto.DefaultSubscriptionSetting{
			GroupID:      sub.GroupID,
			ValidityDays: sub.ValidityDays,
		})
	}
	updatedAuthSourceEmailSubscriptions := dtoDefaultSubscriptionsFromService(updatedAuthDefaults.Email.Subscriptions)
	updatedAuthSourceLinuxDoSubscriptions := dtoDefaultSubscriptionsFromService(updatedAuthDefaults.LinuxDo.Subscriptions)
	updatedAuthSourceOIDCSubscriptions := dtoDefaultSubscriptionsFromService(updatedAuthDefaults.OIDC.Subscriptions)
	updatedAuthSourceWeChatSubscriptions := dtoDefaultSubscriptionsFromService(updatedAuthDefaults.WeChat.Subscriptions)
	updatedAuthSourceGitHubSubscriptions := dtoDefaultSubscriptionsFromService(updatedAuthDefaults.GitHub.Subscriptions)
	updatedAuthSourceGoogleSubscriptions := dtoDefaultSubscriptionsFromService(updatedAuthDefaults.Google.Subscriptions)
	updatedAuthSourceDingTalkSubscriptions := dtoDefaultSubscriptionsFromService(updatedAuthDefaults.DingTalk.Subscriptions)

	// Reload payment config for response
	var updatedPaymentCfg *service.PaymentConfig
	if h.paymentConfigService != nil {
		updatedPaymentCfg, _ = h.paymentConfigService.GetPaymentConfig(c.Request.Context())
	}
	if updatedPaymentCfg == nil {
		updatedPaymentCfg = &service.PaymentConfig{}
	}

	response.Success(c, dto.SystemSettings{
		RegistrationEnabled:                       updatedSettings.RegistrationEnabled,
		EmailVerifyEnabled:                        updatedSettings.EmailVerifyEnabled,
		RegistrationEmailSuffixWhitelist:          updatedSettings.RegistrationEmailSuffixWhitelist,
		PromoCodeEnabled:                          updatedSettings.PromoCodeEnabled,
		PasswordResetEnabled:                      updatedSettings.PasswordResetEnabled,
		FrontendURL:                               updatedSettings.FrontendURL,
		InvitationCodeEnabled:                     updatedSettings.InvitationCodeEnabled,
		TotpEnabled:                               updatedSettings.TotpEnabled,
		TotpEncryptionKeyConfigured:               h.settingService.IsTotpEncryptionKeyConfigured(),
		SMTPHost:                                  updatedSettings.SMTPHost,
		SMTPPort:                                  updatedSettings.SMTPPort,
		SMTPUsername:                              updatedSettings.SMTPUsername,
		SMTPPasswordConfigured:                    updatedSettings.SMTPPasswordConfigured,
		SMTPFrom:                                  updatedSettings.SMTPFrom,
		SMTPFromName:                              updatedSettings.SMTPFromName,
		SMTPUseTLS:                                updatedSettings.SMTPUseTLS,
		TurnstileEnabled:                          updatedSettings.TurnstileEnabled,
		TurnstileSiteKey:                          updatedSettings.TurnstileSiteKey,
		TurnstileSecretKeyConfigured:              updatedSettings.TurnstileSecretKeyConfigured,
		LinuxDoConnectEnabled:                     updatedSettings.LinuxDoConnectEnabled,
		LinuxDoConnectClientID:                    updatedSettings.LinuxDoConnectClientID,
		LinuxDoConnectClientSecretConfigured:      updatedSettings.LinuxDoConnectClientSecretConfigured,
		LinuxDoConnectRedirectURL:                 updatedSettings.LinuxDoConnectRedirectURL,
		DingTalkConnectEnabled:                    updatedSettings.DingTalkConnectEnabled,
		DingTalkConnectClientID:                   updatedSettings.DingTalkConnectClientID,
		DingTalkConnectClientSecretConfigured:     updatedSettings.DingTalkConnectClientSecretConfigured,
		DingTalkConnectRedirectURL:                updatedSettings.DingTalkConnectRedirectURL,
		DingTalkConnectCorpRestrictionPolicy:      updatedSettings.DingTalkConnectCorpRestrictionPolicy,
		DingTalkConnectInternalCorpID:             updatedSettings.DingTalkConnectInternalCorpID,
		DingTalkConnectBypassRegistration:         updatedSettings.DingTalkConnectBypassRegistration,
		DingTalkConnectSyncCorpEmail:              updatedSettings.DingTalkConnectSyncCorpEmail,
		DingTalkConnectSyncDisplayName:            updatedSettings.DingTalkConnectSyncDisplayName,
		DingTalkConnectSyncDept:                   updatedSettings.DingTalkConnectSyncDept,
		DingTalkConnectSyncCorpEmailAttrKey:       updatedSettings.DingTalkConnectSyncCorpEmailAttrKey,
		DingTalkConnectSyncDisplayNameAttrKey:     updatedSettings.DingTalkConnectSyncDisplayNameAttrKey,
		DingTalkConnectSyncDeptAttrKey:            updatedSettings.DingTalkConnectSyncDeptAttrKey,
		DingTalkConnectSyncCorpEmailAttrName:      updatedSettings.DingTalkConnectSyncCorpEmailAttrName,
		DingTalkConnectSyncDisplayNameAttrName:    updatedSettings.DingTalkConnectSyncDisplayNameAttrName,
		DingTalkConnectSyncDeptAttrName:           updatedSettings.DingTalkConnectSyncDeptAttrName,
		WeChatConnectEnabled:                      updatedSettings.WeChatConnectEnabled,
		WeChatConnectAppID:                        updatedSettings.WeChatConnectAppID,
		WeChatConnectAppSecretConfigured:          updatedSettings.WeChatConnectAppSecretConfigured,
		WeChatConnectOpenAppID:                    updatedSettings.WeChatConnectOpenAppID,
		WeChatConnectOpenAppSecretConfigured:      updatedSettings.WeChatConnectOpenAppSecretConfigured,
		WeChatConnectMPAppID:                      updatedSettings.WeChatConnectMPAppID,
		WeChatConnectMPAppSecretConfigured:        updatedSettings.WeChatConnectMPAppSecretConfigured,
		WeChatConnectMobileAppID:                  updatedSettings.WeChatConnectMobileAppID,
		WeChatConnectMobileAppSecretConfigured:    updatedSettings.WeChatConnectMobileAppSecretConfigured,
		WeChatConnectOpenEnabled:                  updatedSettings.WeChatConnectOpenEnabled,
		WeChatConnectMPEnabled:                    updatedSettings.WeChatConnectMPEnabled,
		WeChatConnectMobileEnabled:                updatedSettings.WeChatConnectMobileEnabled,
		WeChatConnectMode:                         updatedSettings.WeChatConnectMode,
		WeChatConnectScopes:                       updatedSettings.WeChatConnectScopes,
		WeChatConnectRedirectURL:                  updatedSettings.WeChatConnectRedirectURL,
		WeChatConnectFrontendRedirectURL:          updatedSettings.WeChatConnectFrontendRedirectURL,
		OIDCConnectEnabled:                        updatedSettings.OIDCConnectEnabled,
		OIDCConnectProviderName:                   updatedSettings.OIDCConnectProviderName,
		OIDCConnectClientID:                       updatedSettings.OIDCConnectClientID,
		OIDCConnectClientSecretConfigured:         updatedSettings.OIDCConnectClientSecretConfigured,
		OIDCConnectIssuerURL:                      updatedSettings.OIDCConnectIssuerURL,
		OIDCConnectDiscoveryURL:                   updatedSettings.OIDCConnectDiscoveryURL,
		OIDCConnectAuthorizeURL:                   updatedSettings.OIDCConnectAuthorizeURL,
		OIDCConnectTokenURL:                       updatedSettings.OIDCConnectTokenURL,
		OIDCConnectUserInfoURL:                    updatedSettings.OIDCConnectUserInfoURL,
		OIDCConnectJWKSURL:                        updatedSettings.OIDCConnectJWKSURL,
		OIDCConnectScopes:                         updatedSettings.OIDCConnectScopes,
		OIDCConnectRedirectURL:                    updatedSettings.OIDCConnectRedirectURL,
		OIDCConnectFrontendRedirectURL:            updatedSettings.OIDCConnectFrontendRedirectURL,
		OIDCConnectTokenAuthMethod:                updatedSettings.OIDCConnectTokenAuthMethod,
		OIDCConnectUsePKCE:                        updatedSettings.OIDCConnectUsePKCE,
		OIDCConnectValidateIDToken:                updatedSettings.OIDCConnectValidateIDToken,
		OIDCConnectAllowedSigningAlgs:             updatedSettings.OIDCConnectAllowedSigningAlgs,
		OIDCConnectClockSkewSeconds:               updatedSettings.OIDCConnectClockSkewSeconds,
		OIDCConnectRequireEmailVerified:           updatedSettings.OIDCConnectRequireEmailVerified,
		OIDCConnectUserInfoEmailPath:              updatedSettings.OIDCConnectUserInfoEmailPath,
		OIDCConnectUserInfoIDPath:                 updatedSettings.OIDCConnectUserInfoIDPath,
		OIDCConnectUserInfoUsernamePath:           updatedSettings.OIDCConnectUserInfoUsernamePath,
		SiteName:                                  updatedSettings.SiteName,
		SiteLogo:                                  updatedSettings.SiteLogo,
		SiteSubtitle:                              updatedSettings.SiteSubtitle,
		APIBaseURL:                                updatedSettings.APIBaseURL,
		ContactInfo:                               updatedSettings.ContactInfo,
		DocURL:                                    updatedSettings.DocURL,
		HomeContent:                               updatedSettings.HomeContent,
		HideCcsImportButton:                       updatedSettings.HideCcsImportButton,
		PurchaseSubscriptionEnabled:               updatedSettings.PurchaseSubscriptionEnabled,
		PurchaseSubscriptionURL:                   updatedSettings.PurchaseSubscriptionURL,
		TableDefaultPageSize:                      updatedSettings.TableDefaultPageSize,
		TablePageSizeOptions:                      updatedSettings.TablePageSizeOptions,
		HiddenAdminMenuItems:                      dto.ParseHiddenAdminMenuItems(updatedSettings.HiddenAdminMenuItems),
		CustomMenuItems:                           dto.ParseCustomMenuItems(updatedSettings.CustomMenuItems),
		CustomEndpoints:                           dto.ParseCustomEndpoints(updatedSettings.CustomEndpoints),
		DefaultConcurrency:                        updatedSettings.DefaultConcurrency,
		DefaultBalance:                            updatedSettings.DefaultBalance,
		AffiliateEnabled:                          updatedSettings.AffiliateEnabled,
		AffiliateRebateRate:                       updatedSettings.AffiliateRebateRate,
		AffiliateRebateFreezeHours:                updatedSettings.AffiliateRebateFreezeHours,
		AffiliateRebateDurationDays:               updatedSettings.AffiliateRebateDurationDays,
		AffiliateRebatePerInviteeCap:              updatedSettings.AffiliateRebatePerInviteeCap,
		DefaultUserRPMLimit:                       updatedSettings.DefaultUserRPMLimit,
		DefaultSubscriptions:                      updatedDefaultSubscriptions,
		AuthSourceDefaultEmailBalance:             updatedAuthDefaults.Email.Balance,
		AuthSourceDefaultEmailConcurrency:         updatedAuthDefaults.Email.Concurrency,
		AuthSourceDefaultEmailSubscriptions:       updatedAuthSourceEmailSubscriptions,
		AuthSourceDefaultEmailGrantOnSignup:       updatedAuthDefaults.Email.GrantOnSignup,
		AuthSourceDefaultEmailGrantOnFirstBind:    updatedAuthDefaults.Email.GrantOnFirstBind,
		AuthSourceDefaultLinuxDoBalance:           updatedAuthDefaults.LinuxDo.Balance,
		AuthSourceDefaultLinuxDoConcurrency:       updatedAuthDefaults.LinuxDo.Concurrency,
		AuthSourceDefaultLinuxDoSubscriptions:     updatedAuthSourceLinuxDoSubscriptions,
		AuthSourceDefaultLinuxDoGrantOnSignup:     updatedAuthDefaults.LinuxDo.GrantOnSignup,
		AuthSourceDefaultLinuxDoGrantOnFirstBind:  updatedAuthDefaults.LinuxDo.GrantOnFirstBind,
		AuthSourceDefaultOIDCBalance:              updatedAuthDefaults.OIDC.Balance,
		AuthSourceDefaultOIDCConcurrency:          updatedAuthDefaults.OIDC.Concurrency,
		AuthSourceDefaultOIDCSubscriptions:        updatedAuthSourceOIDCSubscriptions,
		AuthSourceDefaultOIDCGrantOnSignup:        updatedAuthDefaults.OIDC.GrantOnSignup,
		AuthSourceDefaultOIDCGrantOnFirstBind:     updatedAuthDefaults.OIDC.GrantOnFirstBind,
		AuthSourceDefaultWeChatBalance:            updatedAuthDefaults.WeChat.Balance,
		AuthSourceDefaultWeChatConcurrency:        updatedAuthDefaults.WeChat.Concurrency,
		AuthSourceDefaultWeChatSubscriptions:      updatedAuthSourceWeChatSubscriptions,
		AuthSourceDefaultWeChatGrantOnSignup:      updatedAuthDefaults.WeChat.GrantOnSignup,
		AuthSourceDefaultWeChatGrantOnFirstBind:   updatedAuthDefaults.WeChat.GrantOnFirstBind,
		AuthSourceDefaultGitHubBalance:            updatedAuthDefaults.GitHub.Balance,
		AuthSourceDefaultGitHubConcurrency:        updatedAuthDefaults.GitHub.Concurrency,
		AuthSourceDefaultGitHubSubscriptions:      updatedAuthSourceGitHubSubscriptions,
		AuthSourceDefaultGitHubGrantOnSignup:      updatedAuthDefaults.GitHub.GrantOnSignup,
		AuthSourceDefaultGitHubGrantOnFirstBind:   updatedAuthDefaults.GitHub.GrantOnFirstBind,
		AuthSourceDefaultGoogleBalance:            updatedAuthDefaults.Google.Balance,
		AuthSourceDefaultGoogleConcurrency:        updatedAuthDefaults.Google.Concurrency,
		AuthSourceDefaultGoogleSubscriptions:      updatedAuthSourceGoogleSubscriptions,
		AuthSourceDefaultGoogleGrantOnSignup:      updatedAuthDefaults.Google.GrantOnSignup,
		AuthSourceDefaultGoogleGrantOnFirstBind:   updatedAuthDefaults.Google.GrantOnFirstBind,
		AuthSourceDefaultDingTalkBalance:          updatedAuthDefaults.DingTalk.Balance,
		AuthSourceDefaultDingTalkConcurrency:      updatedAuthDefaults.DingTalk.Concurrency,
		AuthSourceDefaultDingTalkSubscriptions:    updatedAuthSourceDingTalkSubscriptions,
		AuthSourceDefaultDingTalkGrantOnSignup:    updatedAuthDefaults.DingTalk.GrantOnSignup,
		AuthSourceDefaultDingTalkGrantOnFirstBind: updatedAuthDefaults.DingTalk.GrantOnFirstBind,
		ForceEmailOnThirdPartySignup:              updatedAuthDefaults.ForceEmailOnThirdPartySignup,
		EnableModelFallback:                       updatedSettings.EnableModelFallback,
		FallbackModelAnthropic:                    updatedSettings.FallbackModelAnthropic,
		FallbackModelOpenAI:                       updatedSettings.FallbackModelOpenAI,
		FallbackModelGemini:                       updatedSettings.FallbackModelGemini,
		EnableIdentityPatch:                       updatedSettings.EnableIdentityPatch,
		IdentityPatchPrompt:                       updatedSettings.IdentityPatchPrompt,
		OpsMonitoringEnabled:                      updatedSettings.OpsMonitoringEnabled,
		OpsRealtimeMonitoringEnabled:              updatedSettings.OpsRealtimeMonitoringEnabled,
		OpsQueryModeDefault:                       updatedSettings.OpsQueryModeDefault,
		OpsMetricsIntervalSeconds:                 updatedSettings.OpsMetricsIntervalSeconds,
		MinClaudeCodeVersion:                      updatedSettings.MinClaudeCodeVersion,
		MaxClaudeCodeVersion:                      updatedSettings.MaxClaudeCodeVersion,
		AllowUngroupedKeyScheduling:               updatedSettings.AllowUngroupedKeyScheduling,
		BackendModeEnabled:                        updatedSettings.BackendModeEnabled,
		EnableFingerprintUnification:              updatedSettings.EnableFingerprintUnification,
		EnableMetadataPassthrough:                 updatedSettings.EnableMetadataPassthrough,
		DefaultUpstreamUserAgent:                  updatedSettings.DefaultUpstreamUserAgent,
		ForceUnifiedUpstreamUserAgent:             updatedSettings.ForceUnifiedUpstreamUserAgent,
		UpdateGitHubRepo:                          updatedSettings.UpdateGitHubRepo,
		EnableCCHSigning:                          updatedSettings.EnableCCHSigning,
		AntigravityUserAgentVersion:               updatedSettings.AntigravityUserAgentVersion,
		PaymentVisibleMethodAlipaySource:          updatedSettings.PaymentVisibleMethodAlipaySource,
		PaymentVisibleMethodWxpaySource:           updatedSettings.PaymentVisibleMethodWxpaySource,
		PaymentVisibleMethodAlipayEnabled:         updatedSettings.PaymentVisibleMethodAlipayEnabled,
		PaymentVisibleMethodWxpayEnabled:          updatedSettings.PaymentVisibleMethodWxpayEnabled,
		OpenAIAdvancedSchedulerEnabled:            updatedSettings.OpenAIAdvancedSchedulerEnabled,
		BalanceLowNotifyEnabled:                   updatedSettings.BalanceLowNotifyEnabled,
		BalanceLowNotifyThreshold:                 updatedSettings.BalanceLowNotifyThreshold,
		BalanceLowNotifyRechargeURL:               updatedSettings.BalanceLowNotifyRechargeURL,
		AccountQuotaNotifyEnabled:                 updatedSettings.AccountQuotaNotifyEnabled,
		AccountQuotaNotifyEmails:                  dto.NotifyEmailEntriesFromService(updatedSettings.AccountQuotaNotifyEmails),
		PaymentEnabled:                            updatedPaymentCfg.Enabled,
		PaymentMinAmount:                          updatedPaymentCfg.MinAmount,
		PaymentMaxAmount:                          updatedPaymentCfg.MaxAmount,
		PaymentDailyLimit:                         updatedPaymentCfg.DailyLimit,
		PaymentOrderTimeoutMin:                    updatedPaymentCfg.OrderTimeoutMin,
		PaymentMaxPendingOrders:                   updatedPaymentCfg.MaxPendingOrders,
		PaymentEnabledTypes:                       updatedPaymentCfg.EnabledTypes,
		PaymentBalanceDisabled:                    updatedPaymentCfg.BalanceDisabled,
		PaymentBalanceRechargeMultiplier:          updatedPaymentCfg.BalanceRechargeMultiplier,
		PaymentRechargeFeeRate:                    updatedPaymentCfg.RechargeFeeRate,
		PaymentLoadBalanceStrat:                   updatedPaymentCfg.LoadBalanceStrategy,
		PaymentProductNamePrefix:                  updatedPaymentCfg.ProductNamePrefix,
		PaymentProductNameSuffix:                  updatedPaymentCfg.ProductNameSuffix,
		PaymentHelpImageURL:                       updatedPaymentCfg.HelpImageURL,
		PaymentHelpText:                           updatedPaymentCfg.HelpText,
		PaymentCancelRateLimitEnabled:             updatedPaymentCfg.CancelRateLimitEnabled,
		PaymentCancelRateLimitMax:                 updatedPaymentCfg.CancelRateLimitMax,
		PaymentCancelRateLimitWindow:              updatedPaymentCfg.CancelRateLimitWindow,
		PaymentCancelRateLimitUnit:                updatedPaymentCfg.CancelRateLimitUnit,
		PaymentCancelRateLimitMode:                updatedPaymentCfg.CancelRateLimitMode,
		ChannelMonitorEnabled:                     updatedSettings.ChannelMonitorEnabled,
		ChannelMonitorDefaultIntervalSeconds:      updatedSettings.ChannelMonitorDefaultIntervalSeconds,
		AvailableChannelsEnabled:                  updatedSettings.AvailableChannelsEnabled,
	})
}

// hasPaymentFields returns true if any payment-related field was explicitly provided.
// mapDingTalkValidateError maps ValidateDingTalkConfig errors to machine-readable reason codes.
func mapDingTalkValidateError(err error) string {
	switch {
	case errors.Is(err, config.ErrDingTalkV1AppTypeMismatch):
		return "dingtalk_apptype_mismatch"
	case errors.Is(err, config.ErrDingTalkV4InvalidAppKind):
		return "dingtalk_app_kind_invalid"
	default:
		return "dingtalk_corp_config_invalid"
	}
}

func hasPaymentFields(req UpdateSettingsRequest) bool {
	return req.PaymentEnabled != nil || req.PaymentMinAmount != nil ||
		req.PaymentMaxAmount != nil || req.PaymentDailyLimit != nil ||
		req.PaymentOrderTimeoutMin != nil || req.PaymentMaxPendingOrders != nil ||
		req.PaymentEnabledTypes != nil || req.PaymentBalanceDisabled != nil ||
		req.PaymentBalanceRechargeMultiplier != nil || req.PaymentRechargeFeeRate != nil ||
		req.PaymentLoadBalanceStrat != nil || req.PaymentProductNamePrefix != nil ||
		req.PaymentProductNameSuffix != nil || req.PaymentHelpImageURL != nil ||
		req.PaymentHelpText != nil || req.PaymentCancelRateLimitEnabled != nil ||
		req.PaymentCancelRateLimitMax != nil || req.PaymentCancelRateLimitWindow != nil ||
		req.PaymentCancelRateLimitUnit != nil || req.PaymentCancelRateLimitMode != nil
}

func (h *SettingHandler) auditSettingsUpdate(
	c *gin.Context,
	before *service.SystemSettings,
	after *service.SystemSettings,
	beforeAuthDefaults *service.AuthSourceDefaultSettings,
	afterAuthDefaults *service.AuthSourceDefaultSettings,
	req UpdateSettingsRequest,
) {
	if before == nil || after == nil {
		return
	}

	changed := diffSettings(before, after, beforeAuthDefaults, afterAuthDefaults, req)
	if len(changed) == 0 {
		return
	}

	subject, _ := middleware.GetAuthSubjectFromContext(c)
	role, _ := middleware.GetUserRoleFromContext(c)
	slog.Info("settings updated",
		"audit", true,
		"user_id", subject.UserID,
		"role", role,
		"changed", changed,
	)
}

func diffSettings(before *service.SystemSettings, after *service.SystemSettings, args ...any) []string {
	var req UpdateSettingsRequest
	var beforeAuthDefaults *service.AuthSourceDefaultSettings
	var afterAuthDefaults *service.AuthSourceDefaultSettings

	parseAuthDefaults := func(arg any) *service.AuthSourceDefaultSettings {
		defaults, _ := arg.(*service.AuthSourceDefaultSettings)
		return defaults
	}
	parseReq := func(arg any) (UpdateSettingsRequest, bool) {
		parsed, ok := arg.(UpdateSettingsRequest)
		return parsed, ok
	}

	switch len(args) {
	case 1:
		if parsedReq, ok := parseReq(args[0]); ok {
			req = parsedReq
		}
	case 3:
		beforeAuthDefaults = parseAuthDefaults(args[0])
		afterAuthDefaults = parseAuthDefaults(args[1])
		if parsedReq, ok := parseReq(args[2]); ok {
			req = parsedReq
		}
	default:
		for _, arg := range args {
			if parsedReq, ok := parseReq(arg); ok {
				req = parsedReq
				continue
			}
			if defaults := parseAuthDefaults(arg); defaults != nil {
				if beforeAuthDefaults == nil {
					beforeAuthDefaults = defaults
				} else if afterAuthDefaults == nil {
					afterAuthDefaults = defaults
				}
			}
		}
	}

	changed := make([]string, 0, 20)
	if before.RegistrationEnabled != after.RegistrationEnabled {
		changed = append(changed, "registration_enabled")
	}
	if before.EmailVerifyEnabled != after.EmailVerifyEnabled {
		changed = append(changed, "email_verify_enabled")
	}
	if !equalStringSlice(before.RegistrationEmailSuffixWhitelist, after.RegistrationEmailSuffixWhitelist) {
		changed = append(changed, "registration_email_suffix_whitelist")
	}
	if before.PromoCodeEnabled != after.PromoCodeEnabled {
		changed = append(changed, "promo_code_enabled")
	}
	if before.InvitationCodeEnabled != after.InvitationCodeEnabled {
		changed = append(changed, "invitation_code_enabled")
	}
	if before.PasswordResetEnabled != after.PasswordResetEnabled {
		changed = append(changed, "password_reset_enabled")
	}
	if before.FrontendURL != after.FrontendURL {
		changed = append(changed, "frontend_url")
	}
	if before.TotpEnabled != after.TotpEnabled {
		changed = append(changed, "totp_enabled")
	}
	if before.LoginAgreementEnabled != after.LoginAgreementEnabled {
		changed = append(changed, "login_agreement_enabled")
	}
	if before.LoginAgreementMode != after.LoginAgreementMode {
		changed = append(changed, "login_agreement_mode")
	}
	if before.LoginAgreementUpdatedAt != after.LoginAgreementUpdatedAt {
		changed = append(changed, "login_agreement_updated_at")
	}
	if !equalLoginAgreementDocuments(before.LoginAgreementDocuments, after.LoginAgreementDocuments) {
		changed = append(changed, "login_agreement_documents")
	}
	if before.SMTPHost != after.SMTPHost {
		changed = append(changed, "smtp_host")
	}
	if before.SMTPPort != after.SMTPPort {
		changed = append(changed, "smtp_port")
	}
	if before.SMTPUsername != after.SMTPUsername {
		changed = append(changed, "smtp_username")
	}
	if req.SMTPPassword != "" {
		changed = append(changed, "smtp_password")
	}
	if before.SMTPFrom != after.SMTPFrom {
		changed = append(changed, "smtp_from_email")
	}
	if before.SMTPFromName != after.SMTPFromName {
		changed = append(changed, "smtp_from_name")
	}
	if before.SMTPUseTLS != after.SMTPUseTLS {
		changed = append(changed, "smtp_use_tls")
	}
	if before.TurnstileEnabled != after.TurnstileEnabled {
		changed = append(changed, "turnstile_enabled")
	}
	if before.TurnstileSiteKey != after.TurnstileSiteKey {
		changed = append(changed, "turnstile_site_key")
	}
	if req.TurnstileSecretKey != "" {
		changed = append(changed, "turnstile_secret_key")
	}
	if before.APIKeyACLTrustForwardedIP != after.APIKeyACLTrustForwardedIP {
		changed = append(changed, "api_key_acl_trust_forwarded_ip")
	}
	if before.LinuxDoConnectEnabled != after.LinuxDoConnectEnabled {
		changed = append(changed, "linuxdo_connect_enabled")
	}
	if before.LinuxDoConnectClientID != after.LinuxDoConnectClientID {
		changed = append(changed, "linuxdo_connect_client_id")
	}
	if req.LinuxDoConnectClientSecret != "" {
		changed = append(changed, "linuxdo_connect_client_secret")
	}
	if before.LinuxDoConnectRedirectURL != after.LinuxDoConnectRedirectURL {
		changed = append(changed, "linuxdo_connect_redirect_url")
	}
	if before.OIDCConnectEnabled != after.OIDCConnectEnabled {
		changed = append(changed, "oidc_connect_enabled")
	}
	if before.OIDCConnectProviderName != after.OIDCConnectProviderName {
		changed = append(changed, "oidc_connect_provider_name")
	}
	if before.OIDCConnectClientID != after.OIDCConnectClientID {
		changed = append(changed, "oidc_connect_client_id")
	}
	if req.OIDCConnectClientSecret != "" {
		changed = append(changed, "oidc_connect_client_secret")
	}
	if before.OIDCConnectIssuerURL != after.OIDCConnectIssuerURL {
		changed = append(changed, "oidc_connect_issuer_url")
	}
	if before.OIDCConnectDiscoveryURL != after.OIDCConnectDiscoveryURL {
		changed = append(changed, "oidc_connect_discovery_url")
	}
	if before.OIDCConnectAuthorizeURL != after.OIDCConnectAuthorizeURL {
		changed = append(changed, "oidc_connect_authorize_url")
	}
	if before.OIDCConnectTokenURL != after.OIDCConnectTokenURL {
		changed = append(changed, "oidc_connect_token_url")
	}
	if before.OIDCConnectUserInfoURL != after.OIDCConnectUserInfoURL {
		changed = append(changed, "oidc_connect_userinfo_url")
	}
	if before.OIDCConnectJWKSURL != after.OIDCConnectJWKSURL {
		changed = append(changed, "oidc_connect_jwks_url")
	}
	if before.OIDCConnectScopes != after.OIDCConnectScopes {
		changed = append(changed, "oidc_connect_scopes")
	}
	if before.OIDCConnectRedirectURL != after.OIDCConnectRedirectURL {
		changed = append(changed, "oidc_connect_redirect_url")
	}
	if before.OIDCConnectFrontendRedirectURL != after.OIDCConnectFrontendRedirectURL {
		changed = append(changed, "oidc_connect_frontend_redirect_url")
	}
	if before.OIDCConnectTokenAuthMethod != after.OIDCConnectTokenAuthMethod {
		changed = append(changed, "oidc_connect_token_auth_method")
	}
	if before.OIDCConnectUsePKCE != after.OIDCConnectUsePKCE {
		changed = append(changed, "oidc_connect_use_pkce")
	}
	if before.OIDCConnectValidateIDToken != after.OIDCConnectValidateIDToken {
		changed = append(changed, "oidc_connect_validate_id_token")
	}
	if before.OIDCConnectAllowedSigningAlgs != after.OIDCConnectAllowedSigningAlgs {
		changed = append(changed, "oidc_connect_allowed_signing_algs")
	}
	if before.OIDCConnectClockSkewSeconds != after.OIDCConnectClockSkewSeconds {
		changed = append(changed, "oidc_connect_clock_skew_seconds")
	}
	if before.OIDCConnectRequireEmailVerified != after.OIDCConnectRequireEmailVerified {
		changed = append(changed, "oidc_connect_require_email_verified")
	}
	if before.OIDCConnectUserInfoEmailPath != after.OIDCConnectUserInfoEmailPath {
		changed = append(changed, "oidc_connect_userinfo_email_path")
	}
	if before.OIDCConnectUserInfoIDPath != after.OIDCConnectUserInfoIDPath {
		changed = append(changed, "oidc_connect_userinfo_id_path")
	}
	if before.OIDCConnectUserInfoUsernamePath != after.OIDCConnectUserInfoUsernamePath {
		changed = append(changed, "oidc_connect_userinfo_username_path")
	}
	if before.SiteName != after.SiteName {
		changed = append(changed, "site_name")
	}
	if before.SiteLogo != after.SiteLogo {
		changed = append(changed, "site_logo")
	}
	if before.SiteSubtitle != after.SiteSubtitle {
		changed = append(changed, "site_subtitle")
	}
	if before.APIBaseURL != after.APIBaseURL {
		changed = append(changed, "api_base_url")
	}
	if before.ContactInfo != after.ContactInfo {
		changed = append(changed, "contact_info")
	}
	if before.DocURL != after.DocURL {
		changed = append(changed, "doc_url")
	}
	if before.HomeContent != after.HomeContent {
		changed = append(changed, "home_content")
	}
	if before.HideCcsImportButton != after.HideCcsImportButton {
		changed = append(changed, "hide_ccs_import_button")
	}
	if before.DefaultConcurrency != after.DefaultConcurrency {
		changed = append(changed, "default_concurrency")
	}
	if before.DefaultBalance != after.DefaultBalance {
		changed = append(changed, "default_balance")
	}
	if !equalDefaultSubscriptions(before.DefaultSubscriptions, after.DefaultSubscriptions) {
		changed = append(changed, "default_subscriptions")
	}
	if before.EnableModelFallback != after.EnableModelFallback {
		changed = append(changed, "enable_model_fallback")
	}
	if before.FallbackModelAnthropic != after.FallbackModelAnthropic {
		changed = append(changed, "fallback_model_anthropic")
	}
	if before.FallbackModelOpenAI != after.FallbackModelOpenAI {
		changed = append(changed, "fallback_model_openai")
	}
	if before.FallbackModelGemini != after.FallbackModelGemini {
		changed = append(changed, "fallback_model_gemini")
	}
	if before.EnableIdentityPatch != after.EnableIdentityPatch {
		changed = append(changed, "enable_identity_patch")
	}
	if before.IdentityPatchPrompt != after.IdentityPatchPrompt {
		changed = append(changed, "identity_patch_prompt")
	}
	if before.OpsMonitoringEnabled != after.OpsMonitoringEnabled {
		changed = append(changed, "ops_monitoring_enabled")
	}
	if before.OpsRealtimeMonitoringEnabled != after.OpsRealtimeMonitoringEnabled {
		changed = append(changed, "ops_realtime_monitoring_enabled")
	}
	if before.OpsQueryModeDefault != after.OpsQueryModeDefault {
		changed = append(changed, "ops_query_mode_default")
	}
	if before.OpsMetricsIntervalSeconds != after.OpsMetricsIntervalSeconds {
		changed = append(changed, "ops_metrics_interval_seconds")
	}
	if before.MinClaudeCodeVersion != after.MinClaudeCodeVersion {
		changed = append(changed, "min_claude_code_version")
	}
	if before.MaxClaudeCodeVersion != after.MaxClaudeCodeVersion {
		changed = append(changed, "max_claude_code_version")
	}
	if before.AllowUngroupedKeyScheduling != after.AllowUngroupedKeyScheduling {
		changed = append(changed, "allow_ungrouped_key_scheduling")
	}
	if before.BackendModeEnabled != after.BackendModeEnabled {
		changed = append(changed, "backend_mode_enabled")
	}
	if before.PurchaseSubscriptionEnabled != after.PurchaseSubscriptionEnabled {
		changed = append(changed, "purchase_subscription_enabled")
	}
	if before.PurchaseSubscriptionURL != after.PurchaseSubscriptionURL {
		changed = append(changed, "purchase_subscription_url")
	}
	if before.TableDefaultPageSize != after.TableDefaultPageSize {
		changed = append(changed, "table_default_page_size")
	}
	if !equalIntSlice(before.TablePageSizeOptions, after.TablePageSizeOptions) {
		changed = append(changed, "table_page_size_options")
	}
	if before.HiddenAdminMenuItems != after.HiddenAdminMenuItems {
		changed = append(changed, "hidden_admin_menu_items")
	}
	if before.CustomMenuItems != after.CustomMenuItems {
		changed = append(changed, "custom_menu_items")
	}
	if before.CustomEndpoints != after.CustomEndpoints {
		changed = append(changed, "custom_endpoints")
	}
	if before.EnableFingerprintUnification != after.EnableFingerprintUnification {
		changed = append(changed, "enable_fingerprint_unification")
	}
	if before.EnableMetadataPassthrough != after.EnableMetadataPassthrough {
		changed = append(changed, "enable_metadata_passthrough")
	}
	if before.DefaultUpstreamUserAgent != after.DefaultUpstreamUserAgent {
		changed = append(changed, "default_upstream_user_agent")
	}
	if before.ForceUnifiedUpstreamUserAgent != after.ForceUnifiedUpstreamUserAgent {
		changed = append(changed, "force_unified_upstream_user_agent")
	}
	if before.UpdateGitHubRepo != after.UpdateGitHubRepo {
		changed = append(changed, "update_github_repo")
	}
	if before.EnableCCHSigning != after.EnableCCHSigning {
		changed = append(changed, "enable_cch_signing")
	}
	if before.PaymentVisibleMethodAlipaySource != after.PaymentVisibleMethodAlipaySource {
		changed = append(changed, "payment_visible_method_alipay_source")
	}
	if before.PaymentVisibleMethodWxpaySource != after.PaymentVisibleMethodWxpaySource {
		changed = append(changed, "payment_visible_method_wxpay_source")
	}
	if before.PaymentVisibleMethodAlipayEnabled != after.PaymentVisibleMethodAlipayEnabled {
		changed = append(changed, "payment_visible_method_alipay_enabled")
	}
	if before.PaymentVisibleMethodWxpayEnabled != after.PaymentVisibleMethodWxpayEnabled {
		changed = append(changed, "payment_visible_method_wxpay_enabled")
	}
	if before.OpenAIAdvancedSchedulerEnabled != after.OpenAIAdvancedSchedulerEnabled {
		changed = append(changed, "openai_advanced_scheduler_enabled")
	}
	// 余额、订阅到期与账号限额通知
	if before.BalanceLowNotifyEnabled != after.BalanceLowNotifyEnabled {
		changed = append(changed, "balance_low_notify_enabled")
	}
	if before.BalanceLowNotifyThreshold != after.BalanceLowNotifyThreshold {
		changed = append(changed, "balance_low_notify_threshold")
	}
	if before.BalanceLowNotifyRechargeURL != after.BalanceLowNotifyRechargeURL {
		changed = append(changed, "balance_low_notify_recharge_url")
	}
	if before.SubscriptionExpiryNotifyEnabled != after.SubscriptionExpiryNotifyEnabled {
		changed = append(changed, "subscription_expiry_notify_enabled")
	}
	if before.AccountQuotaNotifyEnabled != after.AccountQuotaNotifyEnabled {
		changed = append(changed, "account_quota_notify_enabled")
	}
	if !equalNotifyEmailEntries(before.AccountQuotaNotifyEmails, after.AccountQuotaNotifyEmails) {
		changed = append(changed, "account_quota_notify_emails")
	}
	if beforeAuthDefaults != nil && afterAuthDefaults != nil {
		if beforeAuthDefaults.Email.Balance != afterAuthDefaults.Email.Balance {
			changed = append(changed, "auth_source_default_email_balance")
		}
		if beforeAuthDefaults.Email.Concurrency != afterAuthDefaults.Email.Concurrency {
			changed = append(changed, "auth_source_default_email_concurrency")
		}
		if !equalDefaultSubscriptions(beforeAuthDefaults.Email.Subscriptions, afterAuthDefaults.Email.Subscriptions) {
			changed = append(changed, "auth_source_default_email_subscriptions")
		}
		if beforeAuthDefaults.Email.GrantOnSignup != afterAuthDefaults.Email.GrantOnSignup {
			changed = append(changed, "auth_source_default_email_grant_on_signup")
		}
		if beforeAuthDefaults.Email.GrantOnFirstBind != afterAuthDefaults.Email.GrantOnFirstBind {
			changed = append(changed, "auth_source_default_email_grant_on_first_bind")
		}
		if beforeAuthDefaults.LinuxDo.Balance != afterAuthDefaults.LinuxDo.Balance {
			changed = append(changed, "auth_source_default_linuxdo_balance")
		}
		if beforeAuthDefaults.LinuxDo.Concurrency != afterAuthDefaults.LinuxDo.Concurrency {
			changed = append(changed, "auth_source_default_linuxdo_concurrency")
		}
		if !equalDefaultSubscriptions(beforeAuthDefaults.LinuxDo.Subscriptions, afterAuthDefaults.LinuxDo.Subscriptions) {
			changed = append(changed, "auth_source_default_linuxdo_subscriptions")
		}
		if beforeAuthDefaults.LinuxDo.GrantOnSignup != afterAuthDefaults.LinuxDo.GrantOnSignup {
			changed = append(changed, "auth_source_default_linuxdo_grant_on_signup")
		}
		if beforeAuthDefaults.LinuxDo.GrantOnFirstBind != afterAuthDefaults.LinuxDo.GrantOnFirstBind {
			changed = append(changed, "auth_source_default_linuxdo_grant_on_first_bind")
		}
		if beforeAuthDefaults.OIDC.Balance != afterAuthDefaults.OIDC.Balance {
			changed = append(changed, "auth_source_default_oidc_balance")
		}
		if beforeAuthDefaults.OIDC.Concurrency != afterAuthDefaults.OIDC.Concurrency {
			changed = append(changed, "auth_source_default_oidc_concurrency")
		}
		if !equalDefaultSubscriptions(beforeAuthDefaults.OIDC.Subscriptions, afterAuthDefaults.OIDC.Subscriptions) {
			changed = append(changed, "auth_source_default_oidc_subscriptions")
		}
		if beforeAuthDefaults.OIDC.GrantOnSignup != afterAuthDefaults.OIDC.GrantOnSignup {
			changed = append(changed, "auth_source_default_oidc_grant_on_signup")
		}
		if beforeAuthDefaults.OIDC.GrantOnFirstBind != afterAuthDefaults.OIDC.GrantOnFirstBind {
			changed = append(changed, "auth_source_default_oidc_grant_on_first_bind")
		}
		if beforeAuthDefaults.WeChat.Balance != afterAuthDefaults.WeChat.Balance {
			changed = append(changed, "auth_source_default_wechat_balance")
		}
		if beforeAuthDefaults.WeChat.Concurrency != afterAuthDefaults.WeChat.Concurrency {
			changed = append(changed, "auth_source_default_wechat_concurrency")
		}
		if !equalDefaultSubscriptions(beforeAuthDefaults.WeChat.Subscriptions, afterAuthDefaults.WeChat.Subscriptions) {
			changed = append(changed, "auth_source_default_wechat_subscriptions")
		}
		if beforeAuthDefaults.WeChat.GrantOnSignup != afterAuthDefaults.WeChat.GrantOnSignup {
			changed = append(changed, "auth_source_default_wechat_grant_on_signup")
		}
		if beforeAuthDefaults.WeChat.GrantOnFirstBind != afterAuthDefaults.WeChat.GrantOnFirstBind {
			changed = append(changed, "auth_source_default_wechat_grant_on_first_bind")
		}
		if beforeAuthDefaults.GitHub.Balance != afterAuthDefaults.GitHub.Balance {
			changed = append(changed, "auth_source_default_github_balance")
		}
		if beforeAuthDefaults.GitHub.Concurrency != afterAuthDefaults.GitHub.Concurrency {
			changed = append(changed, "auth_source_default_github_concurrency")
		}
		if !equalDefaultSubscriptions(beforeAuthDefaults.GitHub.Subscriptions, afterAuthDefaults.GitHub.Subscriptions) {
			changed = append(changed, "auth_source_default_github_subscriptions")
		}
		if beforeAuthDefaults.GitHub.GrantOnSignup != afterAuthDefaults.GitHub.GrantOnSignup {
			changed = append(changed, "auth_source_default_github_grant_on_signup")
		}
		if beforeAuthDefaults.GitHub.GrantOnFirstBind != afterAuthDefaults.GitHub.GrantOnFirstBind {
			changed = append(changed, "auth_source_default_github_grant_on_first_bind")
		}
		if beforeAuthDefaults.Google.Balance != afterAuthDefaults.Google.Balance {
			changed = append(changed, "auth_source_default_google_balance")
		}
		if beforeAuthDefaults.Google.Concurrency != afterAuthDefaults.Google.Concurrency {
			changed = append(changed, "auth_source_default_google_concurrency")
		}
		if !equalDefaultSubscriptions(beforeAuthDefaults.Google.Subscriptions, afterAuthDefaults.Google.Subscriptions) {
			changed = append(changed, "auth_source_default_google_subscriptions")
		}
		if beforeAuthDefaults.Google.GrantOnSignup != afterAuthDefaults.Google.GrantOnSignup {
			changed = append(changed, "auth_source_default_google_grant_on_signup")
		}
		if beforeAuthDefaults.Google.GrantOnFirstBind != afterAuthDefaults.Google.GrantOnFirstBind {
			changed = append(changed, "auth_source_default_google_grant_on_first_bind")
		}
		if beforeAuthDefaults.DingTalk.Balance != afterAuthDefaults.DingTalk.Balance {
			changed = append(changed, "auth_source_default_dingtalk_balance")
		}
		if beforeAuthDefaults.DingTalk.Concurrency != afterAuthDefaults.DingTalk.Concurrency {
			changed = append(changed, "auth_source_default_dingtalk_concurrency")
		}
		if !equalDefaultSubscriptions(beforeAuthDefaults.DingTalk.Subscriptions, afterAuthDefaults.DingTalk.Subscriptions) {
			changed = append(changed, "auth_source_default_dingtalk_subscriptions")
		}
		if beforeAuthDefaults.DingTalk.GrantOnSignup != afterAuthDefaults.DingTalk.GrantOnSignup {
			changed = append(changed, "auth_source_default_dingtalk_grant_on_signup")
		}
		if beforeAuthDefaults.DingTalk.GrantOnFirstBind != afterAuthDefaults.DingTalk.GrantOnFirstBind {
			changed = append(changed, "auth_source_default_dingtalk_grant_on_first_bind")
		}
		if beforeAuthDefaults.ForceEmailOnThirdPartySignup != afterAuthDefaults.ForceEmailOnThirdPartySignup {
			changed = append(changed, "force_email_on_third_party_signup")
		}
	}
	return changed
}

func normalizeDefaultSubscriptions(input []dto.DefaultSubscriptionSetting) []dto.DefaultSubscriptionSetting {
	if len(input) == 0 {
		return nil
	}
	normalized := make([]dto.DefaultSubscriptionSetting, 0, len(input))
	for _, item := range input {
		if item.GroupID <= 0 || item.ValidityDays <= 0 {
			continue
		}
		if item.ValidityDays > service.MaxValidityDays {
			item.ValidityDays = service.MaxValidityDays
		}
		normalized = append(normalized, item)
	}
	return normalized
}

func dtoDefaultSubscriptionsFromService(input []service.DefaultSubscriptionSetting) []dto.DefaultSubscriptionSetting {
	if len(input) == 0 {
		return []dto.DefaultSubscriptionSetting{}
	}
	converted := make([]dto.DefaultSubscriptionSetting, 0, len(input))
	for _, item := range input {
		converted = append(converted, dto.DefaultSubscriptionSetting{
			GroupID:      item.GroupID,
			ValidityDays: item.ValidityDays,
		})
	}
	return converted
}

func serviceDefaultSubscriptionsFromDTO(input []dto.DefaultSubscriptionSetting) []service.DefaultSubscriptionSetting {
	if len(input) == 0 {
		return nil
	}
	converted := make([]service.DefaultSubscriptionSetting, 0, len(input))
	for _, item := range input {
		converted = append(converted, service.DefaultSubscriptionSetting{
			GroupID:      item.GroupID,
			ValidityDays: item.ValidityDays,
		})
	}
	return converted
}

func cloneAuthSourceDefaultSettings(input *service.AuthSourceDefaultSettings) *service.AuthSourceDefaultSettings {
	if input == nil {
		return &service.AuthSourceDefaultSettings{}
	}
	cloned := *input
	cloned.Email.Subscriptions = append([]service.DefaultSubscriptionSetting(nil), input.Email.Subscriptions...)
	cloned.LinuxDo.Subscriptions = append([]service.DefaultSubscriptionSetting(nil), input.LinuxDo.Subscriptions...)
	cloned.OIDC.Subscriptions = append([]service.DefaultSubscriptionSetting(nil), input.OIDC.Subscriptions...)
	cloned.WeChat.Subscriptions = append([]service.DefaultSubscriptionSetting(nil), input.WeChat.Subscriptions...)
	cloned.GitHub.Subscriptions = append([]service.DefaultSubscriptionSetting(nil), input.GitHub.Subscriptions...)
	cloned.Google.Subscriptions = append([]service.DefaultSubscriptionSetting(nil), input.Google.Subscriptions...)
	cloned.DingTalk.Subscriptions = append([]service.DefaultSubscriptionSetting(nil), input.DingTalk.Subscriptions...)
	return &cloned
}

func equalProviderDefaultGrantSettings(a, b service.ProviderDefaultGrantSettings) bool {
	if a.Balance != b.Balance {
		return false
	}
	if a.Concurrency != b.Concurrency {
		return false
	}
	if a.GrantOnSignup != b.GrantOnSignup {
		return false
	}
	if a.GrantOnFirstBind != b.GrantOnFirstBind {
		return false
	}
	return equalDefaultSubscriptions(a.Subscriptions, b.Subscriptions)
}

func equalAuthSourceDefaultSettings(a, b *service.AuthSourceDefaultSettings) bool {
	if a == nil || b == nil {
		return a == b
	}
	if !equalProviderDefaultGrantSettings(a.Email, b.Email) {
		return false
	}
	if !equalProviderDefaultGrantSettings(a.LinuxDo, b.LinuxDo) {
		return false
	}
	if !equalProviderDefaultGrantSettings(a.OIDC, b.OIDC) {
		return false
	}
	if !equalProviderDefaultGrantSettings(a.WeChat, b.WeChat) {
		return false
	}
	if !equalProviderDefaultGrantSettings(a.GitHub, b.GitHub) {
		return false
	}
	if !equalProviderDefaultGrantSettings(a.Google, b.Google) {
		return false
	}
	if !equalProviderDefaultGrantSettings(a.DingTalk, b.DingTalk) {
		return false
	}
	return a.ForceEmailOnThirdPartySignup == b.ForceEmailOnThirdPartySignup
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalDefaultSubscriptions(a, b []service.DefaultSubscriptionSetting) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].GroupID != b[i].GroupID || a[i].ValidityDays != b[i].ValidityDays {
			return false
		}
	}
	return true
}

func equalLoginAgreementDocuments(a, b []service.LoginAgreementDocument) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Title != b[i].Title || a[i].ContentMD != b[i].ContentMD {
			return false
		}
	}
	return true
}

func equalIntSlice(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalNotifyEmailEntries(a, b []service.NotifyEmailEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Email != b[i].Email || a[i].Verified != b[i].Verified || a[i].Disabled != b[i].Disabled {
			return false
		}
	}
	return true
}

// TestSMTPRequest 测试SMTP连接请求
type TestSMTPRequest struct {
	SMTPHost     string `json:"smtp_host"`
	SMTPPort     int    `json:"smtp_port"`
	SMTPUsername string `json:"smtp_username"`
	SMTPPassword string `json:"smtp_password"`
	SMTPUseTLS   bool   `json:"smtp_use_tls"`
}

// TestEndpointRequest 上游端点测速请求
type TestEndpointRequest struct {
	TargetURL string            `json:"target_url" binding:"required"`
	Mode      string            `json:"mode"` // tcp/head/get
	TimeoutMs int               `json:"timeout_ms"`
	Headers   map[string]string `json:"headers"`
}

// TestEndpointBatchRequest 批量测速请求
type TestEndpointBatchRequest struct {
	Targets        []string          `json:"targets" binding:"required"`
	Mode           string            `json:"mode"` // tcp/head/get
	TimeoutMs      int               `json:"timeout_ms"`
	Headers        map[string]string `json:"headers"`
	MaxConcurrency int               `json:"max_concurrency"`
}

type EndpointProbePlanRequest struct {
	Name            string            `json:"name" binding:"required"`
	Enabled         *bool             `json:"enabled"`
	Mode            string            `json:"mode"`
	Targets         []string          `json:"targets" binding:"required"`
	Headers         map[string]string `json:"headers"`
	TimeoutMs       int               `json:"timeout_ms"`
	IntervalSeconds int               `json:"interval_seconds"`
	MaxConcurrency  int               `json:"max_concurrency"`
}

// TestEndpoint 测试上游端点连通性与延迟
// POST /api/v1/admin/settings/test-endpoint
func (h *SettingHandler) TestEndpoint(c *gin.Context) {
	if h.endpointProbeService == nil {
		response.Error(c, http.StatusServiceUnavailable, "endpoint probe service is not available")
		return
	}
	var req TestEndpointRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.endpointProbeService.Probe(c.Request.Context(), service.EndpointProbeRequest{
		TargetURL: strings.TrimSpace(req.TargetURL),
		Mode:      strings.TrimSpace(req.Mode),
		TimeoutMs: req.TimeoutMs,
		Headers:   req.Headers,
	})
	if err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	response.Success(c, result)
}

// TestEndpointBatch 批量测试上游端点连通性与延迟（结果按健康度+延迟排序）
// POST /api/v1/admin/settings/test-endpoint-batch
func (h *SettingHandler) TestEndpointBatch(c *gin.Context) {
	if h.endpointProbeService == nil {
		response.Error(c, http.StatusServiceUnavailable, "endpoint probe service is not available")
		return
	}
	var req TestEndpointBatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	results, err := h.endpointProbeService.ProbeBatch(c.Request.Context(), service.EndpointBatchProbeRequest{
		Targets:        req.Targets,
		Mode:           strings.TrimSpace(req.Mode),
		TimeoutMs:      req.TimeoutMs,
		Headers:        req.Headers,
		MaxConcurrency: req.MaxConcurrency,
	})
	if err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	response.Success(c, gin.H{
		"items": results,
		"best": func() *service.EndpointProbeResult {
			if len(results) == 0 {
				return nil
			}
			return results[0]
		}(),
	})
}

// ListEndpointProbePlans 获取端点测速计划
// GET /api/v1/admin/settings/endpoint-probe/plans
func (h *SettingHandler) ListEndpointProbePlans(c *gin.Context) {
	if h.endpointProbePlanService == nil {
		response.Error(c, http.StatusServiceUnavailable, "endpoint probe service is not available")
		return
	}
	plans, err := h.endpointProbePlanService.ListPlans(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, plans)
}

// CreateEndpointProbePlan 创建端点测速计划
// POST /api/v1/admin/settings/endpoint-probe/plans
func (h *SettingHandler) CreateEndpointProbePlan(c *gin.Context) {
	if h.endpointProbePlanService == nil {
		response.Error(c, http.StatusServiceUnavailable, "endpoint probe service is not available")
		return
	}
	var req EndpointProbePlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	plan, err := h.endpointProbePlanService.CreatePlan(c.Request.Context(), &service.EndpointProbePlan{
		Name:            strings.TrimSpace(req.Name),
		Enabled:         enabled,
		Mode:            strings.TrimSpace(req.Mode),
		Targets:         req.Targets,
		Headers:         req.Headers,
		TimeoutMs:       req.TimeoutMs,
		IntervalSeconds: req.IntervalSeconds,
		MaxConcurrency:  req.MaxConcurrency,
	})
	if err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	response.Success(c, plan)
}

// UpdateEndpointProbePlan 更新端点测速计划
// PUT /api/v1/admin/settings/endpoint-probe/plans/:id
func (h *SettingHandler) UpdateEndpointProbePlan(c *gin.Context) {
	if h.endpointProbePlanService == nil {
		response.Error(c, http.StatusServiceUnavailable, "endpoint probe service is not available")
		return
	}
	planID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || planID <= 0 {
		response.BadRequest(c, "invalid plan id")
		return
	}
	existing, err := h.endpointProbePlanService.GetPlan(c.Request.Context(), planID)
	if err != nil {
		response.NotFound(c, "plan not found")
		return
	}
	var req EndpointProbePlanRequest
	if err = c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	existing.Name = strings.TrimSpace(req.Name)
	existing.Mode = strings.TrimSpace(req.Mode)
	existing.Targets = req.Targets
	existing.Headers = req.Headers
	existing.TimeoutMs = req.TimeoutMs
	existing.IntervalSeconds = req.IntervalSeconds
	existing.MaxConcurrency = req.MaxConcurrency
	if req.Enabled != nil {
		existing.Enabled = *req.Enabled
	}
	updated, err := h.endpointProbePlanService.UpdatePlan(c.Request.Context(), existing)
	if err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	response.Success(c, updated)
}

// DeleteEndpointProbePlan 删除端点测速计划
// DELETE /api/v1/admin/settings/endpoint-probe/plans/:id
func (h *SettingHandler) DeleteEndpointProbePlan(c *gin.Context) {
	if h.endpointProbePlanService == nil {
		response.Error(c, http.StatusServiceUnavailable, "endpoint probe service is not available")
		return
	}
	planID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || planID <= 0 {
		response.BadRequest(c, "invalid plan id")
		return
	}
	if err = h.endpointProbePlanService.DeletePlan(c.Request.Context(), planID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "deleted"})
}

// RunEndpointProbePlanNow 立即执行端点测速计划
// POST /api/v1/admin/settings/endpoint-probe/plans/:id/run
func (h *SettingHandler) RunEndpointProbePlanNow(c *gin.Context) {
	if h.endpointProbePlanService == nil {
		response.Error(c, http.StatusServiceUnavailable, "endpoint probe service is not available")
		return
	}
	planID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || planID <= 0 {
		response.BadRequest(c, "invalid plan id")
		return
	}
	results, err := h.endpointProbePlanService.RunNow(c.Request.Context(), planID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"items": results})
}

// ListEndpointProbePlanResults 获取测速历史
// GET /api/v1/admin/settings/endpoint-probe/plans/:id/results
func (h *SettingHandler) ListEndpointProbePlanResults(c *gin.Context) {
	if h.endpointProbePlanService == nil {
		response.Error(c, http.StatusServiceUnavailable, "endpoint probe service is not available")
		return
	}
	planID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || planID <= 0 {
		response.BadRequest(c, "invalid plan id")
		return
	}
	limit := 100
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if v, parseErr := strconv.Atoi(raw); parseErr == nil && v > 0 {
			limit = v
		}
	}
	items, err := h.endpointProbePlanService.ListResults(c.Request.Context(), planID, limit)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, items)
}

// TestSMTPConnection 测试SMTP连接
// POST /api/v1/admin/settings/test-smtp
func (h *SettingHandler) TestSMTPConnection(c *gin.Context) {
	var req TestSMTPRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	req.SMTPHost = strings.TrimSpace(req.SMTPHost)
	req.SMTPUsername = strings.TrimSpace(req.SMTPUsername)

	var savedConfig *service.SMTPConfig
	if cfg, err := h.emailService.GetSMTPConfig(c.Request.Context()); err == nil && cfg != nil {
		savedConfig = cfg
	}

	if req.SMTPHost == "" && savedConfig != nil {
		req.SMTPHost = savedConfig.Host
	}
	if req.SMTPPort <= 0 {
		if savedConfig != nil && savedConfig.Port > 0 {
			req.SMTPPort = savedConfig.Port
		} else {
			req.SMTPPort = 587
		}
	}
	if req.SMTPUsername == "" && savedConfig != nil {
		req.SMTPUsername = savedConfig.Username
	}
	password := strings.TrimSpace(req.SMTPPassword)
	if password == "" && savedConfig != nil {
		password = savedConfig.Password
	}
	if req.SMTPHost == "" {
		response.BadRequest(c, "SMTP host is required")
		return
	}

	config := &service.SMTPConfig{
		Host:     req.SMTPHost,
		Port:     req.SMTPPort,
		Username: req.SMTPUsername,
		Password: password,
		UseTLS:   req.SMTPUseTLS,
	}

	err := h.emailService.TestSMTPConnectionWithConfig(config)
	if err != nil {
		response.BadRequest(c, "SMTP connection test failed: "+err.Error())
		return
	}

	response.Success(c, gin.H{"message": "SMTP connection successful"})
}

// SendTestEmailRequest 发送测试邮件请求
type SendTestEmailRequest struct {
	Email        string `json:"email" binding:"required,email"`
	SMTPHost     string `json:"smtp_host"`
	SMTPPort     int    `json:"smtp_port"`
	SMTPUsername string `json:"smtp_username"`
	SMTPPassword string `json:"smtp_password"`
	SMTPFrom     string `json:"smtp_from_email"`
	SMTPFromName string `json:"smtp_from_name"`
	SMTPUseTLS   bool   `json:"smtp_use_tls"`
}

// SendTestEmail 发送测试邮件
// POST /api/v1/admin/settings/send-test-email
func (h *SettingHandler) SendTestEmail(c *gin.Context) {
	var req SendTestEmailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	req.SMTPHost = strings.TrimSpace(req.SMTPHost)
	req.SMTPUsername = strings.TrimSpace(req.SMTPUsername)
	req.SMTPFrom = strings.TrimSpace(req.SMTPFrom)
	req.SMTPFromName = strings.TrimSpace(req.SMTPFromName)

	var savedConfig *service.SMTPConfig
	if cfg, err := h.emailService.GetSMTPConfig(c.Request.Context()); err == nil && cfg != nil {
		savedConfig = cfg
	}

	if req.SMTPHost == "" && savedConfig != nil {
		req.SMTPHost = savedConfig.Host
	}
	if req.SMTPPort <= 0 {
		if savedConfig != nil && savedConfig.Port > 0 {
			req.SMTPPort = savedConfig.Port
		} else {
			req.SMTPPort = 587
		}
	}
	if req.SMTPUsername == "" && savedConfig != nil {
		req.SMTPUsername = savedConfig.Username
	}
	password := strings.TrimSpace(req.SMTPPassword)
	if password == "" && savedConfig != nil {
		password = savedConfig.Password
	}
	if req.SMTPFrom == "" && savedConfig != nil {
		req.SMTPFrom = savedConfig.From
	}
	if req.SMTPFromName == "" && savedConfig != nil {
		req.SMTPFromName = savedConfig.FromName
	}
	if req.SMTPHost == "" {
		response.BadRequest(c, "SMTP host is required")
		return
	}

	config := &service.SMTPConfig{
		Host:     req.SMTPHost,
		Port:     req.SMTPPort,
		Username: req.SMTPUsername,
		Password: password,
		From:     req.SMTPFrom,
		FromName: req.SMTPFromName,
		UseTLS:   req.SMTPUseTLS,
	}

	siteName := h.settingService.GetSiteName(c.Request.Context())
	subject := "[" + siteName + "] Test Email"
	body := `
<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <style>
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background-color: #f5f5f5; margin: 0; padding: 20px; }
        .container { max-width: 600px; margin: 0 auto; background-color: #ffffff; border-radius: 8px; overflow: hidden; box-shadow: 0 2px 8px rgba(0,0,0,0.1); }
        .header { background: linear-gradient(135deg, #667eea 0%, #764ba2 100%); color: white; padding: 30px; text-align: center; }
        .content { padding: 40px 30px; text-align: center; }
        .success { color: #10b981; font-size: 48px; margin-bottom: 20px; }
        .footer { background-color: #f8f9fa; padding: 20px; text-align: center; color: #999; font-size: 12px; }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>` + siteName + `</h1>
        </div>
        <div class="content">
            <div class="success">✓</div>
            <h2>Email Configuration Successful!</h2>
            <p>This is a test email to verify your SMTP settings are working correctly.</p>
        </div>
        <div class="footer">
            <p>This is an automated test message.</p>
        </div>
    </div>
</body>
</html>
`

	if err := h.emailService.SendEmailWithConfig(config, req.Email, subject, body); err != nil {
		response.BadRequest(c, "Failed to send test email: "+err.Error())
		return
	}

	response.Success(c, gin.H{"message": "Test email sent successfully"})
}

// GetAdminAPIKey 获取管理员 API Key 状态
// GET /api/v1/admin/settings/admin-api-key
func (h *SettingHandler) GetAdminAPIKey(c *gin.Context) {
	maskedKey, exists, err := h.settingService.GetAdminAPIKeyStatus(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{
		"exists":     exists,
		"masked_key": maskedKey,
	})
}

// RegenerateAdminAPIKey 生成/重新生成管理员 API Key
// POST /api/v1/admin/settings/admin-api-key/regenerate
func (h *SettingHandler) RegenerateAdminAPIKey(c *gin.Context) {
	key, err := h.settingService.GenerateAdminAPIKey(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{
		"key": key, // 完整 key 只在生成时返回一次
	})
}

// DeleteAdminAPIKey 删除管理员 API Key
// DELETE /api/v1/admin/settings/admin-api-key
func (h *SettingHandler) DeleteAdminAPIKey(c *gin.Context) {
	if err := h.settingService.DeleteAdminAPIKey(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "Admin API key deleted"})
}

// GetOverloadCooldownSettings 获取529过载冷却配置
// GET /api/v1/admin/settings/overload-cooldown
func (h *SettingHandler) GetOverloadCooldownSettings(c *gin.Context) {
	settings, err := h.settingService.GetOverloadCooldownSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, dto.OverloadCooldownSettings{
		Enabled:         settings.Enabled,
		CooldownMinutes: settings.CooldownMinutes,
	})
}

// UpdateOverloadCooldownSettingsRequest 更新529过载冷却配置请求
type UpdateOverloadCooldownSettingsRequest struct {
	Enabled         bool `json:"enabled"`
	CooldownMinutes int  `json:"cooldown_minutes"`
}

// UpdateOverloadCooldownSettings 更新529过载冷却配置
// PUT /api/v1/admin/settings/overload-cooldown
func (h *SettingHandler) UpdateOverloadCooldownSettings(c *gin.Context) {
	var req UpdateOverloadCooldownSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	settings := &service.OverloadCooldownSettings{
		Enabled:         req.Enabled,
		CooldownMinutes: req.CooldownMinutes,
	}

	if err := h.settingService.SetOverloadCooldownSettings(c.Request.Context(), settings); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	updatedSettings, err := h.settingService.GetOverloadCooldownSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, dto.OverloadCooldownSettings{
		Enabled:         updatedSettings.Enabled,
		CooldownMinutes: updatedSettings.CooldownMinutes,
	})
}

// RateLimit429CooldownSettings 429 默认回避配置响应。
type RateLimit429CooldownSettings struct {
	Enabled         bool `json:"enabled"`
	CooldownSeconds int  `json:"cooldown_seconds"`
}

// UpdateRateLimit429CooldownSettingsRequest 更新 429 默认回避配置请求。
type UpdateRateLimit429CooldownSettingsRequest struct {
	Enabled         bool `json:"enabled"`
	CooldownSeconds int  `json:"cooldown_seconds"`
}

// GetRateLimit429CooldownSettings 获取 429 默认回避配置。
// GET /api/v1/admin/settings/rate-limit-429-cooldown
func (h *SettingHandler) GetRateLimit429CooldownSettings(c *gin.Context) {
	settings, err := h.settingService.GetRateLimit429CooldownSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, RateLimit429CooldownSettings{
		Enabled:         settings.Enabled,
		CooldownSeconds: settings.CooldownSeconds,
	})
}

// UpdateRateLimit429CooldownSettings 更新 429 默认回避配置。
// PUT /api/v1/admin/settings/rate-limit-429-cooldown
func (h *SettingHandler) UpdateRateLimit429CooldownSettings(c *gin.Context) {
	var req UpdateRateLimit429CooldownSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	settings := &service.RateLimit429CooldownSettings{
		Enabled:         req.Enabled,
		CooldownSeconds: req.CooldownSeconds,
	}
	if err := h.settingService.SetRateLimit429CooldownSettings(c.Request.Context(), settings); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	h.GetRateLimit429CooldownSettings(c)
}

// GetStreamTimeoutSettings 获取流超时处理配置
// GET /api/v1/admin/settings/stream-timeout
func (h *SettingHandler) GetStreamTimeoutSettings(c *gin.Context) {
	settings, err := h.settingService.GetStreamTimeoutSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, dto.StreamTimeoutSettings{
		Enabled:                settings.Enabled,
		Action:                 settings.Action,
		TempUnschedMinutes:     settings.TempUnschedMinutes,
		ThresholdCount:         settings.ThresholdCount,
		ThresholdWindowMinutes: settings.ThresholdWindowMinutes,
	})
}

type UpdateStreamTimeoutSettingsRequest struct {
	Enabled                bool   `json:"enabled"`
	Action                 string `json:"action"`
	TempUnschedMinutes     int    `json:"temp_unsched_minutes"`
	ThresholdCount         int    `json:"threshold_count"`
	ThresholdWindowMinutes int    `json:"threshold_window_minutes"`
}

// UpdateStreamTimeoutSettings 更新流超时处理配置。
// PUT /api/v1/admin/settings/stream-timeout
func (h *SettingHandler) UpdateStreamTimeoutSettings(c *gin.Context) {
	var req UpdateStreamTimeoutSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	settings := &service.StreamTimeoutSettings{
		Enabled:                req.Enabled,
		Action:                 req.Action,
		TempUnschedMinutes:     req.TempUnschedMinutes,
		ThresholdCount:         req.ThresholdCount,
		ThresholdWindowMinutes: req.ThresholdWindowMinutes,
	}
	if err := h.settingService.SetStreamTimeoutSettings(c.Request.Context(), settings); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	h.GetStreamTimeoutSettings(c)
}

// GetRectifierSettings 获取请求整流器配置。
// GET /api/v1/admin/settings/rectifier
func (h *SettingHandler) GetRectifierSettings(c *gin.Context) {
	settings, err := h.settingService.GetRectifierSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}

// UpdateRectifierSettings 更新请求整流器配置。
// PUT /api/v1/admin/settings/rectifier
func (h *SettingHandler) UpdateRectifierSettings(c *gin.Context) {
	var settings service.RectifierSettings
	if err := c.ShouldBindJSON(&settings); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.settingService.SetRectifierSettings(c.Request.Context(), &settings); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, settings)
}

// GetBetaPolicySettings 获取 Beta 策略配置。
// GET /api/v1/admin/settings/beta-policy
func (h *SettingHandler) GetBetaPolicySettings(c *gin.Context) {
	settings, err := h.settingService.GetBetaPolicySettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}

// UpdateBetaPolicySettings 更新 Beta 策略配置。
// PUT /api/v1/admin/settings/beta-policy
func (h *SettingHandler) UpdateBetaPolicySettings(c *gin.Context) {
	var settings service.BetaPolicySettings
	if err := c.ShouldBindJSON(&settings); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.settingService.SetBetaPolicySettings(c.Request.Context(), &settings); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, settings)
}

// GetWebSearchEmulationConfig 获取 Web Search 模拟配置。
// GET /api/v1/admin/settings/web-search-emulation
func (h *SettingHandler) GetWebSearchEmulationConfig(c *gin.Context) {
	cfg, err := h.settingService.GetWebSearchEmulationConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, service.SanitizeWebSearchConfig(c.Request.Context(), cfg))
}

// UpdateWebSearchEmulationConfig 更新 Web Search 模拟配置。
// PUT /api/v1/admin/settings/web-search-emulation
func (h *SettingHandler) UpdateWebSearchEmulationConfig(c *gin.Context) {
	var cfg service.WebSearchEmulationConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.settingService.SaveWebSearchEmulationConfig(c.Request.Context(), &cfg); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, service.SanitizeWebSearchConfig(c.Request.Context(), &cfg))
}

type testWebSearchEmulationRequest struct {
	Query string `json:"query"`
}

// TestWebSearchEmulation 使用当前配置执行一次测试检索。
// POST /api/v1/admin/settings/web-search-emulation/test
func (h *SettingHandler) TestWebSearchEmulation(c *gin.Context) {
	var req testWebSearchEmulationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := service.TestWebSearch(c.Request.Context(), strings.TrimSpace(req.Query))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

type resetWebSearchUsageRequest struct {
	ProviderType string `json:"provider_type"`
}

// ResetWebSearchUsage 重置指定 provider 的额度统计。
// POST /api/v1/admin/settings/web-search-emulation/reset-usage
func (h *SettingHandler) ResetWebSearchUsage(c *gin.Context) {
	var req resetWebSearchUsageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := service.ResetWebSearchUsage(c.Request.Context(), strings.TrimSpace(req.ProviderType)); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"provider_type": strings.TrimSpace(req.ProviderType), "reset": true})
}
