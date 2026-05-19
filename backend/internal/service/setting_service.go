package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/imroc/req/v3"
	"golang.org/x/sync/singleflight"
)

// CoerceDingTalkCorpPolicyForWrite 是 coerceDeprecatedDingTalkCorpPolicy 的导出版本，
// 用于 admin handler 在写入路径上对客户端直传的入参做防御性 coerce（前端 UI 虽已无 whitelist 选项，
// 但 API 可被直接调用）。
func CoerceDingTalkCorpPolicyForWrite(policy string) string {
	return coerceDeprecatedDingTalkCorpPolicy(policy)
}

// coerceDeprecatedDingTalkCorpPolicy 把已废弃的 corp_restriction_policy 值替换成安全的等价值。
// 升级前残留在 DB 中的 "whitelist" 会导致 callback 链路在 default case 静默 fail-closed
// （所有钉钉登录被拒）。这里统一退化为 "none" 让服务保持可用，并 warn 日志提醒 admin 重新保存设置。
func coerceDeprecatedDingTalkCorpPolicy(policy string) string {
	if policy == "whitelist" {
		slog.Warn("dingtalk: corp_restriction_policy=whitelist is deprecated and unsupported, coercing to none",
			"hint", "re-save DingTalk settings in admin UI to clear this warning")
		return "none"
	}
	return policy
}

var (
	ErrRegistrationDisabled   = infraerrors.Forbidden("REGISTRATION_DISABLED", "registration is currently disabled")
	ErrSettingNotFound        = infraerrors.NotFound("SETTING_NOT_FOUND", "setting not found")
	ErrDefaultSubGroupInvalid = infraerrors.BadRequest(
		"DEFAULT_SUBSCRIPTION_GROUP_INVALID",
		"default subscription group must exist and be subscription type",
	)
	ErrDefaultSubGroupDuplicate = infraerrors.BadRequest(
		"DEFAULT_SUBSCRIPTION_GROUP_DUPLICATE",
		"default subscription group cannot be duplicated",
	)
)

type SettingRepository interface {
	Get(ctx context.Context, key string) (*Setting, error)
	GetValue(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
	GetMultiple(ctx context.Context, keys []string) (map[string]string, error)
	SetMultiple(ctx context.Context, settings map[string]string) error
	GetAll(ctx context.Context) (map[string]string, error)
	Delete(ctx context.Context, key string) error
}

// cachedVersionBounds 缓存 Claude Code 版本号上下限（进程内缓存，60s TTL）
type cachedVersionBounds struct {
	min       string // 空字符串 = 不检查
	max       string // 空字符串 = 不检查
	expiresAt int64  // unix nano
}

// versionBoundsCache 版本号上下限进程内缓存
var versionBoundsCache atomic.Value // *cachedVersionBounds

// versionBoundsSF 防止缓存过期时 thundering herd
var versionBoundsSF singleflight.Group

// versionBoundsCacheTTL 缓存有效期
const versionBoundsCacheTTL = 60 * time.Second

// versionBoundsErrorTTL DB 错误时的短缓存，快速重试
const versionBoundsErrorTTL = 5 * time.Second

// versionBoundsDBTimeout singleflight 内 DB 查询超时，独立于请求 context
const versionBoundsDBTimeout = 5 * time.Second

// cachedBackendMode Backend Mode cache (in-process, 60s TTL)
type cachedBackendMode struct {
	value     bool
	expiresAt int64 // unix nano
}

var backendModeCache atomic.Value // *cachedBackendMode
var backendModeSF singleflight.Group

const backendModeCacheTTL = 60 * time.Second
const backendModeErrorTTL = 5 * time.Second
const backendModeDBTimeout = 5 * time.Second

// cachedGatewayForwardingSettings 缓存网关转发行为设置（进程内缓存，60s TTL）
type cachedGatewayForwardingSettings struct {
	fingerprintUnification       bool
	metadataPassthrough          bool
	cchSigning                   bool
	anthropicCacheTTL1hInjection bool
	rewriteMessageCacheControl   bool
	expiresAt                    int64 // unix nano
}

var gatewayForwardingCache atomic.Value // *cachedGatewayForwardingSettings
var gatewayForwardingSF singleflight.Group

const gatewayForwardingCacheTTL = 60 * time.Second
const gatewayForwardingErrorTTL = 5 * time.Second
const gatewayForwardingDBTimeout = 5 * time.Second

// cachedAntigravityUserAgentVersion 缓存 Antigravity UA 版本号（进程内缓存，60s TTL）
type cachedAntigravityUserAgentVersion struct {
	version   string
	expiresAt int64 // unix nano
}

const antigravityUserAgentVersionCacheTTL = 60 * time.Second
const antigravityUserAgentVersionErrorTTL = 5 * time.Second
const antigravityUserAgentVersionDBTimeout = 5 * time.Second
const antigravityUserAgentVersionCacheKey = "antigravity_user_agent_version"
const antigravityUserAgentVersionEnv = "ANTIGRAVITY_USER_AGENT_VERSION"
const fallbackAntigravityUserAgentVersion = "1.23.2"

// DefaultSubscriptionGroupReader validates group references used by default subscriptions.
type DefaultSubscriptionGroupReader interface {
	GetByID(ctx context.Context, id int64) (*Group, error)
}

// WebSearchManagerBuilder creates a websearch.Manager from config (injected by infra layer).
// proxyURLs maps proxy ID to resolved URL for provider-level proxy support.
type WebSearchManagerBuilder func(cfg *WebSearchEmulationConfig, proxyURLs map[int64]string)

// SettingService 系统设置服务
type SettingService struct {
	settingRepo               SettingRepository
	defaultSubGroupReader     DefaultSubscriptionGroupReader
	proxyRepo                 ProxyRepository // for resolving websearch provider proxy URLs
	cfg                       *config.Config
	onUpdate                  func() // Callback when settings are updated (for cache invalidation)
	version                   string // Application version
	webSearchManagerBuilder   WebSearchManagerBuilder
	antigravityUAVersionCache atomic.Value // *cachedAntigravityUserAgentVersion
	antigravityUAVersionSF    singleflight.Group
}

// NewSettingService 创建系统设置服务实例
func NewSettingService(settingRepo SettingRepository, cfg *config.Config) *SettingService {
	return &SettingService{
		settingRepo: settingRepo,
		cfg:         cfg,
	}
}

func normalizeAntigravityUserAgentVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return ""
	}
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return ""
	}
	for _, part := range parts {
		if part == "" {
			return ""
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return ""
			}
		}
	}
	return version
}

func defaultAntigravityUserAgentVersion() string {
	if version := normalizeAntigravityUserAgentVersion(os.Getenv(antigravityUserAgentVersionEnv)); version != "" {
		return version
	}
	return fallbackAntigravityUserAgentVersion
}

// SetDefaultSubscriptionGroupReader injects an optional group reader for default subscription validation.
func (s *SettingService) SetDefaultSubscriptionGroupReader(reader DefaultSubscriptionGroupReader) {
	s.defaultSubGroupReader = reader
}

// SetProxyRepository injects a proxy repo for resolving websearch provider proxy URLs.
func (s *SettingService) SetProxyRepository(repo ProxyRepository) {
	s.proxyRepo = repo
}

// GetAllSettings 获取所有系统设置
func (s *SettingService) GetAllSettings(ctx context.Context) (*SystemSettings, error) {
	settings, err := s.settingRepo.GetAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("get all settings: %w", err)
	}

	return s.parseSettings(settings), nil
}

// GetFrontendURL 获取前端基础URL（数据库优先，fallback 到配置文件）
func (s *SettingService) GetFrontendURL(ctx context.Context) string {
	val, err := s.settingRepo.GetValue(ctx, SettingKeyFrontendURL)
	if err == nil && strings.TrimSpace(val) != "" {
		return strings.TrimSpace(val)
	}
	return s.cfg.Server.FrontendURL
}

// GetPublicSettings 获取公开设置（无需登录）
func (s *SettingService) GetPublicSettings(ctx context.Context) (*PublicSettings, error) {
	keys := []string{
		SettingKeyRegistrationEnabled,
		SettingKeyEmailVerifyEnabled,
		SettingKeyRegistrationEmailSuffixWhitelist,
		SettingKeyPromoCodeEnabled,
		SettingKeyPasswordResetEnabled,
		SettingKeyInvitationCodeEnabled,
		SettingKeyTotpEnabled,
		SettingKeyLoginAgreementEnabled,
		SettingKeyLoginAgreementMode,
		SettingKeyLoginAgreementUpdatedAt,
		SettingKeyLoginAgreementDocuments,
		SettingKeyTurnstileEnabled,
		SettingKeyTurnstileSiteKey,
		SettingKeySiteName,
		SettingKeySiteLogo,
		SettingKeySiteSubtitle,
		SettingKeyAPIBaseURL,
		SettingKeyContactInfo,
		SettingKeyDocURL,
		SettingKeyHomeContent,
		SettingKeyHideCcsImportButton,
		SettingKeyPurchaseSubscriptionEnabled,
		SettingKeyPurchaseSubscriptionURL,
		SettingKeyTableDefaultPageSize,
		SettingKeyTablePageSizeOptions,
		SettingKeyCustomMenuItems,
		SettingKeyCustomEndpoints,
		SettingKeyLinuxDoConnectEnabled,
		SettingKeyDingTalkConnectEnabled,
		SettingKeyGitHubOAuthEnabled,
		SettingKeyGitHubOAuthClientID,
		SettingKeyGitHubOAuthClientSecret,
		SettingKeyGoogleOAuthEnabled,
		SettingKeyGoogleOAuthClientID,
		SettingKeyGoogleOAuthClientSecret,
		SettingKeyBackendModeEnabled,
		SettingPaymentEnabled,
		SettingKeyOIDCConnectEnabled,
		SettingKeyOIDCConnectProviderName,
		SettingKeyWeChatConnectEnabled,
		SettingKeyWeChatConnectOpenEnabled,
		SettingKeyWeChatConnectMPEnabled,
		SettingKeyWeChatConnectMobileEnabled,
		SettingKeyForceEmailOnThirdPartySignup,
		SettingKeyBalanceLowNotifyEnabled,
		SettingKeyBalanceLowNotifyThreshold,
		SettingKeyBalanceLowNotifyRechargeURL,
		SettingKeyAccountQuotaNotifyEnabled,
	}

	settings, err := s.settingRepo.GetMultiple(ctx, keys)
	if err != nil {
		return nil, fmt.Errorf("get public settings: %w", err)
	}

	linuxDoEnabled := false
	if raw, ok := settings[SettingKeyLinuxDoConnectEnabled]; ok {
		linuxDoEnabled = raw == "true"
	} else {
		linuxDoEnabled = s.cfg != nil && s.cfg.LinuxDo.Enabled
	}
	dingTalkEnabled := false
	if raw, ok := settings[SettingKeyDingTalkConnectEnabled]; ok {
		dingTalkEnabled = raw == "true"
	} else {
		dingTalkEnabled = s.cfg != nil && s.cfg.DingTalk.Enabled
	}
	oidcEnabled := false
	if raw, ok := settings[SettingKeyOIDCConnectEnabled]; ok {
		oidcEnabled = raw == "true"
	} else {
		oidcEnabled = s.cfg != nil && s.cfg.OIDC.Enabled
	}
	oidcProviderName := strings.TrimSpace(settings[SettingKeyOIDCConnectProviderName])
	if oidcProviderName == "" && s.cfg != nil {
		oidcProviderName = strings.TrimSpace(s.cfg.OIDC.ProviderName)
	}
	if oidcProviderName == "" {
		oidcProviderName = "OIDC"
	}

	// Password reset requires email verification to be enabled
	emailVerifyEnabled := settings[SettingKeyEmailVerifyEnabled] == "true"
	passwordResetEnabled := emailVerifyEnabled && settings[SettingKeyPasswordResetEnabled] == "true"
	registrationEmailSuffixWhitelist := ParseRegistrationEmailSuffixWhitelist(
		settings[SettingKeyRegistrationEmailSuffixWhitelist],
	)
	tableDefaultPageSize, tablePageSizeOptions := parseTablePreferences(
		settings[SettingKeyTableDefaultPageSize],
		settings[SettingKeyTablePageSizeOptions],
	)
	loginAgreementDocuments := parseLoginAgreementDocuments(settings[SettingKeyLoginAgreementDocuments])
	loginAgreementUpdatedAt := strings.TrimSpace(settings[SettingKeyLoginAgreementUpdatedAt])
	if loginAgreementUpdatedAt == "" {
		loginAgreementUpdatedAt = defaultLoginAgreementDate
	}

	var balanceLowNotifyThreshold float64
	if v, err := strconv.ParseFloat(settings[SettingKeyBalanceLowNotifyThreshold], 64); err == nil && v >= 0 {
		balanceLowNotifyThreshold = v
	}
	weChatCfg := s.buildWeChatConnectOAuthConfig(settings)
	weChatWebOAuthEnabled := weChatCfg.Enabled && (weChatCfg.OpenEnabled || weChatCfg.MPEnabled)

	return &PublicSettings{
		RegistrationEnabled:              settings[SettingKeyRegistrationEnabled] == "true",
		EmailVerifyEnabled:               emailVerifyEnabled,
		RegistrationEmailSuffixWhitelist: registrationEmailSuffixWhitelist,
		PromoCodeEnabled:                 settings[SettingKeyPromoCodeEnabled] != "false", // 默认启用
		PasswordResetEnabled:             passwordResetEnabled,
		InvitationCodeEnabled:            settings[SettingKeyInvitationCodeEnabled] == "true",
		TotpEnabled:                      settings[SettingKeyTotpEnabled] == "true",
		LoginAgreementEnabled:            settings[SettingKeyLoginAgreementEnabled] == "true" && len(loginAgreementDocuments) > 0,
		LoginAgreementMode:               normalizeLoginAgreementMode(settings[SettingKeyLoginAgreementMode]),
		LoginAgreementUpdatedAt:          loginAgreementUpdatedAt,
		LoginAgreementRevision:           buildLoginAgreementRevision(loginAgreementUpdatedAt, loginAgreementDocuments),
		LoginAgreementDocuments:          loginAgreementDocuments,
		TurnstileEnabled:                 settings[SettingKeyTurnstileEnabled] == "true",
		TurnstileSiteKey:                 settings[SettingKeyTurnstileSiteKey],
		SiteName:                         s.getStringOrDefault(settings, SettingKeySiteName, "Sub2API"),
		SiteLogo:                         settings[SettingKeySiteLogo],
		SiteSubtitle:                     s.getStringOrDefault(settings, SettingKeySiteSubtitle, "Subscription to API Conversion Platform"),
		APIBaseURL:                       settings[SettingKeyAPIBaseURL],
		ContactInfo:                      settings[SettingKeyContactInfo],
		DocURL:                           settings[SettingKeyDocURL],
		HomeContent:                      settings[SettingKeyHomeContent],
		HideCcsImportButton:              settings[SettingKeyHideCcsImportButton] == "true",
		PurchaseSubscriptionEnabled:      settings[SettingKeyPurchaseSubscriptionEnabled] == "true",
		PurchaseSubscriptionURL:          strings.TrimSpace(settings[SettingKeyPurchaseSubscriptionURL]),
		TableDefaultPageSize:             tableDefaultPageSize,
		TablePageSizeOptions:             tablePageSizeOptions,
		CustomMenuItems:                  settings[SettingKeyCustomMenuItems],
		CustomEndpoints:                  settings[SettingKeyCustomEndpoints],
		LinuxDoOAuthEnabled:              linuxDoEnabled,
		DingTalkOAuthEnabled:             dingTalkEnabled,
		WeChatOAuthEnabled:               weChatWebOAuthEnabled,
		WeChatOAuthOpenEnabled:           weChatCfg.Enabled && weChatCfg.OpenEnabled,
		WeChatOAuthMPEnabled:             weChatCfg.Enabled && weChatCfg.MPEnabled,
		WeChatOAuthMobileEnabled:         weChatCfg.Enabled && weChatCfg.MobileEnabled,
		BackendModeEnabled:               settings[SettingKeyBackendModeEnabled] == "true",
		PaymentEnabled:                   settings[SettingPaymentEnabled] == "true",
		OIDCOAuthEnabled:                 oidcEnabled,
		OIDCOAuthProviderName:            oidcProviderName,
		GitHubOAuthEnabled:               s.emailOAuthPublicEnabled(settings, "github"),
		GoogleOAuthEnabled:               s.emailOAuthPublicEnabled(settings, "google"),
		ForceEmailOnThirdPartySignup:     settings[SettingKeyForceEmailOnThirdPartySignup] == "true",
		BalanceLowNotifyEnabled:          settings[SettingKeyBalanceLowNotifyEnabled] == "true",
		AccountQuotaNotifyEnabled:        settings[SettingKeyAccountQuotaNotifyEnabled] == "true",
		BalanceLowNotifyThreshold:        balanceLowNotifyThreshold,
		BalanceLowNotifyRechargeURL:      settings[SettingKeyBalanceLowNotifyRechargeURL],
	}, nil
}

// SetOnUpdateCallback sets a callback function to be called when settings are updated
// This is used for cache invalidation (e.g., HTML cache in frontend server)
func (s *SettingService) SetOnUpdateCallback(callback func()) {
	s.onUpdate = callback
}

// SetVersion sets the application version for injection into public settings
func (s *SettingService) SetVersion(version string) {
	s.version = version
}

// GetPublicSettingsForInjection returns public settings in a format suitable for HTML injection
// This implements the web.PublicSettingsProvider interface
func (s *SettingService) GetPublicSettingsForInjection(ctx context.Context) (any, error) {
	settings, err := s.GetPublicSettings(ctx)
	if err != nil {
		return nil, err
	}

	return &PublicSettingsInjectionPayload{
		RegistrationEnabled:                  settings.RegistrationEnabled,
		EmailVerifyEnabled:                   settings.EmailVerifyEnabled,
		ForceEmailOnThirdPartySignup:         settings.ForceEmailOnThirdPartySignup,
		RegistrationEmailSuffixWhitelist:     settings.RegistrationEmailSuffixWhitelist,
		PromoCodeEnabled:                     settings.PromoCodeEnabled,
		PasswordResetEnabled:                 settings.PasswordResetEnabled,
		InvitationCodeEnabled:                settings.InvitationCodeEnabled,
		TotpEnabled:                          settings.TotpEnabled,
		LoginAgreementEnabled:                settings.LoginAgreementEnabled,
		LoginAgreementMode:                   settings.LoginAgreementMode,
		LoginAgreementUpdatedAt:              settings.LoginAgreementUpdatedAt,
		LoginAgreementRevision:               settings.LoginAgreementRevision,
		LoginAgreementDocuments:              settings.LoginAgreementDocuments,
		TurnstileEnabled:                     settings.TurnstileEnabled,
		TurnstileSiteKey:                     settings.TurnstileSiteKey,
		SiteName:                             settings.SiteName,
		SiteLogo:                             settings.SiteLogo,
		SiteSubtitle:                         settings.SiteSubtitle,
		APIBaseURL:                           settings.APIBaseURL,
		ContactInfo:                          settings.ContactInfo,
		DocURL:                               settings.DocURL,
		HomeContent:                          settings.HomeContent,
		HideCcsImportButton:                  settings.HideCcsImportButton,
		PurchaseSubscriptionEnabled:          settings.PurchaseSubscriptionEnabled,
		PurchaseSubscriptionURL:              settings.PurchaseSubscriptionURL,
		TableDefaultPageSize:                 settings.TableDefaultPageSize,
		TablePageSizeOptions:                 settings.TablePageSizeOptions,
		CustomMenuItems:                      filterUserVisibleMenuItems(settings.CustomMenuItems),
		CustomEndpoints:                      safeRawJSONArray(settings.CustomEndpoints),
		LinuxDoOAuthEnabled:                  settings.LinuxDoOAuthEnabled,
		DingTalkOAuthEnabled:                 settings.DingTalkOAuthEnabled,
		WeChatOAuthEnabled:                   settings.WeChatOAuthEnabled,
		WeChatOAuthOpenEnabled:               settings.WeChatOAuthOpenEnabled,
		WeChatOAuthMPEnabled:                 settings.WeChatOAuthMPEnabled,
		WeChatOAuthMobileEnabled:             settings.WeChatOAuthMobileEnabled,
		BackendModeEnabled:                   settings.BackendModeEnabled,
		PaymentEnabled:                       settings.PaymentEnabled,
		OIDCOAuthEnabled:                     settings.OIDCOAuthEnabled,
		OIDCOAuthProviderName:                settings.OIDCOAuthProviderName,
		GitHubOAuthEnabled:                   settings.GitHubOAuthEnabled,
		GoogleOAuthEnabled:                   settings.GoogleOAuthEnabled,
		Version:                              s.version,
		BalanceLowNotifyEnabled:              settings.BalanceLowNotifyEnabled,
		AccountQuotaNotifyEnabled:            settings.AccountQuotaNotifyEnabled,
		BalanceLowNotifyThreshold:            settings.BalanceLowNotifyThreshold,
		BalanceLowNotifyRechargeURL:          settings.BalanceLowNotifyRechargeURL,
		ChannelMonitorEnabled:                settings.ChannelMonitorEnabled,
		ChannelMonitorDefaultIntervalSeconds: settings.ChannelMonitorDefaultIntervalSeconds,
		AvailableChannelsEnabled:             settings.AvailableChannelsEnabled,
		AffiliateEnabled:                     settings.AffiliateEnabled,
		RiskControlEnabled:                   settings.RiskControlEnabled,
	}, nil
}

// filterUserVisibleMenuItems filters out admin-only menu items from a raw JSON
// array string, returning only items with visibility != "admin".
func filterUserVisibleMenuItems(raw string) json.RawMessage {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return json.RawMessage("[]")
	}
	var items []struct {
		Visibility string `json:"visibility"`
	}
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return json.RawMessage("[]")
	}

	// Parse full items to preserve all fields
	var fullItems []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fullItems); err != nil {
		return json.RawMessage("[]")
	}

	var filtered []json.RawMessage
	for i, item := range items {
		if item.Visibility != "admin" {
			filtered = append(filtered, fullItems[i])
		}
	}
	if len(filtered) == 0 {
		return json.RawMessage("[]")
	}
	result, err := json.Marshal(filtered)
	if err != nil {
		return json.RawMessage("[]")
	}
	return result
}

// safeRawJSONArray returns raw as json.RawMessage if it's valid JSON, otherwise "[]".
func safeRawJSONArray(raw string) json.RawMessage {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return json.RawMessage("[]")
	}
	if json.Valid([]byte(raw)) {
		return json.RawMessage(raw)
	}
	return json.RawMessage("[]")
}

// GetFrameSrcOrigins returns deduplicated http(s) origins from home_content URL,
// purchase_subscription_url, and all custom_menu_items URLs. Used by the router layer for CSP frame-src injection.
func (s *SettingService) GetFrameSrcOrigins(ctx context.Context) ([]string, error) {
	settings, err := s.GetPublicSettings(ctx)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{})
	var origins []string

	addOrigin := func(rawURL string) {
		if origin := extractOriginFromURL(rawURL); origin != "" {
			if _, ok := seen[origin]; !ok {
				seen[origin] = struct{}{}
				origins = append(origins, origin)
			}
		}
	}

	// home content URL (when home_content is set to a URL for iframe embedding)
	addOrigin(settings.HomeContent)

	// purchase subscription URL
	if settings.PurchaseSubscriptionEnabled {
		addOrigin(settings.PurchaseSubscriptionURL)
	}

	// all custom menu items (including admin-only, since CSP must allow all iframes)
	for _, item := range parseCustomMenuItemURLs(settings.CustomMenuItems) {
		addOrigin(item)
	}

	return origins, nil
}

// extractOriginFromURL returns the scheme+host origin from rawURL.
// Only http and https schemes are accepted.
func extractOriginFromURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// parseCustomMenuItemURLs extracts URLs from a raw JSON array of custom menu items.
func parseCustomMenuItemURLs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return nil
	}
	var items []struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil
	}
	urls := make([]string, 0, len(items))
	for _, item := range items {
		if item.URL != "" {
			urls = append(urls, item.URL)
		}
	}
	return urls
}

// UpdateSettings 更新系统设置
func (s *SettingService) UpdateSettings(ctx context.Context, settings *SystemSettings) error {
	if err := s.validateDefaultSubscriptionGroups(ctx, settings.DefaultSubscriptions); err != nil {
		return err
	}
	normalizedWhitelist, err := NormalizeRegistrationEmailSuffixWhitelist(settings.RegistrationEmailSuffixWhitelist)
	if err != nil {
		return infraerrors.BadRequest("INVALID_REGISTRATION_EMAIL_SUFFIX_WHITELIST", err.Error())
	}
	if normalizedWhitelist == nil {
		normalizedWhitelist = []string{}
	}
	settings.RegistrationEmailSuffixWhitelist = normalizedWhitelist
	alipaySource, err := normalizeVisibleMethodSettingSource(
		"alipay",
		settings.PaymentVisibleMethodAlipaySource,
		settings.PaymentVisibleMethodAlipayEnabled,
	)
	if err != nil {
		return err
	}
	wxpaySource, err := normalizeVisibleMethodSettingSource(
		"wxpay",
		settings.PaymentVisibleMethodWxpaySource,
		settings.PaymentVisibleMethodWxpayEnabled,
	)
	if err != nil {
		return err
	}
	settings.PaymentVisibleMethodAlipaySource = alipaySource
	settings.PaymentVisibleMethodWxpaySource = wxpaySource
	settings.WeChatConnectMode = normalizeWeChatConnectModeSetting(settings.WeChatConnectMode)
	settings.WeChatConnectScopes = normalizeWeChatConnectScopeSetting(settings.WeChatConnectScopes, settings.WeChatConnectMode)
	settings.WeChatConnectRedirectURL = strings.TrimSpace(settings.WeChatConnectRedirectURL)
	settings.WeChatConnectFrontendRedirectURL = strings.TrimSpace(settings.WeChatConnectFrontendRedirectURL)
	if settings.WeChatConnectFrontendRedirectURL == "" {
		settings.WeChatConnectFrontendRedirectURL = defaultWeChatConnectFrontend
	}
	settings.WeChatConnectOpenEnabled, settings.WeChatConnectMPEnabled, settings.WeChatConnectMobileEnabled = normalizeWeChatConnectCapabilityFlags(
		settings.WeChatConnectEnabled,
		settings.WeChatConnectMode,
		settings.WeChatConnectOpenEnabled,
		settings.WeChatConnectMPEnabled,
		settings.WeChatConnectMobileEnabled,
	)

	updates := make(map[string]string)

	// 注册设置
	updates[SettingKeyRegistrationEnabled] = strconv.FormatBool(settings.RegistrationEnabled)
	updates[SettingKeyEmailVerifyEnabled] = strconv.FormatBool(settings.EmailVerifyEnabled)
	registrationEmailSuffixWhitelistJSON, err := json.Marshal(settings.RegistrationEmailSuffixWhitelist)
	if err != nil {
		return fmt.Errorf("marshal registration email suffix whitelist: %w", err)
	}
	updates[SettingKeyRegistrationEmailSuffixWhitelist] = string(registrationEmailSuffixWhitelistJSON)
	updates[SettingKeyPromoCodeEnabled] = strconv.FormatBool(settings.PromoCodeEnabled)
	updates[SettingKeyPasswordResetEnabled] = strconv.FormatBool(settings.PasswordResetEnabled)
	updates[SettingKeyFrontendURL] = settings.FrontendURL
	updates[SettingKeyInvitationCodeEnabled] = strconv.FormatBool(settings.InvitationCodeEnabled)
	updates[SettingKeyTotpEnabled] = strconv.FormatBool(settings.TotpEnabled)
	settings.LoginAgreementMode = normalizeLoginAgreementMode(settings.LoginAgreementMode)
	settings.LoginAgreementUpdatedAt = strings.TrimSpace(settings.LoginAgreementUpdatedAt)
	if settings.LoginAgreementUpdatedAt == "" {
		settings.LoginAgreementUpdatedAt = defaultLoginAgreementDate
	}
	loginAgreementDocumentsJSON, err := marshalLoginAgreementDocuments(settings.LoginAgreementDocuments)
	if err != nil {
		return err
	}
	updates[SettingKeyLoginAgreementEnabled] = strconv.FormatBool(settings.LoginAgreementEnabled)
	updates[SettingKeyLoginAgreementMode] = settings.LoginAgreementMode
	updates[SettingKeyLoginAgreementUpdatedAt] = settings.LoginAgreementUpdatedAt
	updates[SettingKeyLoginAgreementDocuments] = loginAgreementDocumentsJSON

	// 邮件服务设置（只有非空才更新密码）
	updates[SettingKeySMTPHost] = settings.SMTPHost
	updates[SettingKeySMTPPort] = strconv.Itoa(settings.SMTPPort)
	updates[SettingKeySMTPUsername] = settings.SMTPUsername
	if settings.SMTPPassword != "" {
		updates[SettingKeySMTPPassword] = settings.SMTPPassword
	}
	updates[SettingKeySMTPFrom] = settings.SMTPFrom
	updates[SettingKeySMTPFromName] = settings.SMTPFromName
	updates[SettingKeySMTPUseTLS] = strconv.FormatBool(settings.SMTPUseTLS)

	// Cloudflare Turnstile 设置（只有非空才更新密钥）
	updates[SettingKeyTurnstileEnabled] = strconv.FormatBool(settings.TurnstileEnabled)
	updates[SettingKeyTurnstileSiteKey] = settings.TurnstileSiteKey
	if settings.TurnstileSecretKey != "" {
		updates[SettingKeyTurnstileSecretKey] = settings.TurnstileSecretKey
	}

	// LinuxDo Connect OAuth 登录
	updates[SettingKeyLinuxDoConnectEnabled] = strconv.FormatBool(settings.LinuxDoConnectEnabled)
	updates[SettingKeyLinuxDoConnectClientID] = settings.LinuxDoConnectClientID
	updates[SettingKeyLinuxDoConnectRedirectURL] = settings.LinuxDoConnectRedirectURL
	if settings.LinuxDoConnectClientSecret != "" {
		updates[SettingKeyLinuxDoConnectClientSecret] = settings.LinuxDoConnectClientSecret
	}

	// DingTalk Connect OAuth 登录
	updates[SettingKeyDingTalkConnectEnabled] = strconv.FormatBool(settings.DingTalkConnectEnabled)
	updates[SettingKeyDingTalkConnectClientID] = settings.DingTalkConnectClientID
	updates[SettingKeyDingTalkConnectRedirectURL] = settings.DingTalkConnectRedirectURL
	if settings.DingTalkConnectClientSecret != "" {
		updates[SettingKeyDingTalkConnectClientSecret] = settings.DingTalkConnectClientSecret
	}
	updates[SettingKeyDingTalkConnectCorpRestrictionPolicy] = settings.DingTalkConnectCorpRestrictionPolicy
	updates[SettingKeyDingTalkConnectInternalCorpID] = settings.DingTalkConnectInternalCorpID
	updates[SettingKeyDingTalkConnectBypassRegistration] = strconv.FormatBool(settings.DingTalkConnectBypassRegistration)
	updates[SettingKeyDingTalkConnectSyncCorpEmail] = strconv.FormatBool(settings.DingTalkConnectSyncCorpEmail)
	updates[SettingKeyDingTalkConnectSyncDisplayName] = strconv.FormatBool(settings.DingTalkConnectSyncDisplayName)
	updates[SettingKeyDingTalkConnectSyncDept] = strconv.FormatBool(settings.DingTalkConnectSyncDept)
	updates[SettingKeyDingTalkConnectSyncCorpEmailAttrKey] = settings.DingTalkConnectSyncCorpEmailAttrKey
	updates[SettingKeyDingTalkConnectSyncDisplayNameAttrKey] = settings.DingTalkConnectSyncDisplayNameAttrKey
	updates[SettingKeyDingTalkConnectSyncDeptAttrKey] = settings.DingTalkConnectSyncDeptAttrKey
	updates[SettingKeyDingTalkConnectSyncCorpEmailAttrName] = settings.DingTalkConnectSyncCorpEmailAttrName
	updates[SettingKeyDingTalkConnectSyncDisplayNameAttrName] = settings.DingTalkConnectSyncDisplayNameAttrName
	updates[SettingKeyDingTalkConnectSyncDeptAttrName] = settings.DingTalkConnectSyncDeptAttrName

	// WeChat Connect OAuth 登录
	updates[SettingKeyWeChatConnectEnabled] = strconv.FormatBool(settings.WeChatConnectEnabled)
	updates[SettingKeyWeChatConnectAppID] = strings.TrimSpace(settings.WeChatConnectAppID)
	if secret := strings.TrimSpace(settings.WeChatConnectAppSecret); secret != "" {
		updates[SettingKeyWeChatConnectAppSecret] = secret
	}
	updates[SettingKeyWeChatConnectOpenAppID] = strings.TrimSpace(settings.WeChatConnectOpenAppID)
	if secret := strings.TrimSpace(settings.WeChatConnectOpenAppSecret); secret != "" {
		updates[SettingKeyWeChatConnectOpenAppSecret] = secret
	}
	updates[SettingKeyWeChatConnectMPAppID] = strings.TrimSpace(settings.WeChatConnectMPAppID)
	if secret := strings.TrimSpace(settings.WeChatConnectMPAppSecret); secret != "" {
		updates[SettingKeyWeChatConnectMPAppSecret] = secret
	}
	updates[SettingKeyWeChatConnectMobileAppID] = strings.TrimSpace(settings.WeChatConnectMobileAppID)
	if secret := strings.TrimSpace(settings.WeChatConnectMobileAppSecret); secret != "" {
		updates[SettingKeyWeChatConnectMobileAppSecret] = secret
	}
	updates[SettingKeyWeChatConnectOpenEnabled] = strconv.FormatBool(settings.WeChatConnectOpenEnabled)
	updates[SettingKeyWeChatConnectMPEnabled] = strconv.FormatBool(settings.WeChatConnectMPEnabled)
	updates[SettingKeyWeChatConnectMobileEnabled] = strconv.FormatBool(settings.WeChatConnectMobileEnabled)
	updates[SettingKeyWeChatConnectMode] = settings.WeChatConnectMode
	updates[SettingKeyWeChatConnectScopes] = settings.WeChatConnectScopes
	updates[SettingKeyWeChatConnectRedirectURL] = settings.WeChatConnectRedirectURL
	updates[SettingKeyWeChatConnectFrontendRedirectURL] = settings.WeChatConnectFrontendRedirectURL

	// Generic OIDC OAuth 登录
	updates[SettingKeyOIDCConnectEnabled] = strconv.FormatBool(settings.OIDCConnectEnabled)
	updates[SettingKeyOIDCConnectProviderName] = settings.OIDCConnectProviderName
	updates[SettingKeyOIDCConnectClientID] = settings.OIDCConnectClientID
	updates[SettingKeyOIDCConnectIssuerURL] = settings.OIDCConnectIssuerURL
	updates[SettingKeyOIDCConnectDiscoveryURL] = settings.OIDCConnectDiscoveryURL
	updates[SettingKeyOIDCConnectAuthorizeURL] = settings.OIDCConnectAuthorizeURL
	updates[SettingKeyOIDCConnectTokenURL] = settings.OIDCConnectTokenURL
	updates[SettingKeyOIDCConnectUserInfoURL] = settings.OIDCConnectUserInfoURL
	updates[SettingKeyOIDCConnectJWKSURL] = settings.OIDCConnectJWKSURL
	updates[SettingKeyOIDCConnectScopes] = settings.OIDCConnectScopes
	updates[SettingKeyOIDCConnectRedirectURL] = settings.OIDCConnectRedirectURL
	updates[SettingKeyOIDCConnectFrontendRedirectURL] = settings.OIDCConnectFrontendRedirectURL
	updates[SettingKeyOIDCConnectTokenAuthMethod] = settings.OIDCConnectTokenAuthMethod
	updates[SettingKeyOIDCConnectUsePKCE] = strconv.FormatBool(settings.OIDCConnectUsePKCE)
	updates[SettingKeyOIDCConnectValidateIDToken] = strconv.FormatBool(settings.OIDCConnectValidateIDToken)
	updates[SettingKeyOIDCConnectAllowedSigningAlgs] = settings.OIDCConnectAllowedSigningAlgs
	updates[SettingKeyOIDCConnectClockSkewSeconds] = strconv.Itoa(settings.OIDCConnectClockSkewSeconds)
	updates[SettingKeyOIDCConnectRequireEmailVerified] = strconv.FormatBool(settings.OIDCConnectRequireEmailVerified)
	updates[SettingKeyOIDCConnectUserInfoEmailPath] = settings.OIDCConnectUserInfoEmailPath
	updates[SettingKeyOIDCConnectUserInfoIDPath] = settings.OIDCConnectUserInfoIDPath
	updates[SettingKeyOIDCConnectUserInfoUsernamePath] = settings.OIDCConnectUserInfoUsernamePath
	if settings.OIDCConnectClientSecret != "" {
		updates[SettingKeyOIDCConnectClientSecret] = settings.OIDCConnectClientSecret
	}

	// OEM设置
	updates[SettingKeySiteName] = settings.SiteName
	updates[SettingKeySiteLogo] = settings.SiteLogo
	updates[SettingKeySiteSubtitle] = settings.SiteSubtitle
	updates[SettingKeyAPIBaseURL] = settings.APIBaseURL
	updates[SettingKeyContactInfo] = settings.ContactInfo
	updates[SettingKeyDocURL] = settings.DocURL
	updates[SettingKeyHomeContent] = settings.HomeContent
	updates[SettingKeyHideCcsImportButton] = strconv.FormatBool(settings.HideCcsImportButton)
	updates[SettingKeyPurchaseSubscriptionEnabled] = strconv.FormatBool(settings.PurchaseSubscriptionEnabled)
	updates[SettingKeyPurchaseSubscriptionURL] = strings.TrimSpace(settings.PurchaseSubscriptionURL)
	tableDefaultPageSize, tablePageSizeOptions := normalizeTablePreferences(
		settings.TableDefaultPageSize,
		settings.TablePageSizeOptions,
	)
	updates[SettingKeyTableDefaultPageSize] = strconv.Itoa(tableDefaultPageSize)
	tablePageSizeOptionsJSON, err := json.Marshal(tablePageSizeOptions)
	if err != nil {
		return fmt.Errorf("marshal table page size options: %w", err)
	}
	updates[SettingKeyTablePageSizeOptions] = string(tablePageSizeOptionsJSON)
	updates[SettingKeyHiddenAdminMenuItems] = settings.HiddenAdminMenuItems
	updates[SettingKeyCustomMenuItems] = settings.CustomMenuItems
	updates[SettingKeyCustomEndpoints] = settings.CustomEndpoints

	// 默认配置
	updates[SettingKeyDefaultConcurrency] = strconv.Itoa(settings.DefaultConcurrency)
	updates[SettingKeyDefaultBalance] = strconv.FormatFloat(settings.DefaultBalance, 'f', 8, 64)
	updates[SettingKeyAffiliateEnabled] = strconv.FormatBool(settings.AffiliateEnabled)
	settings.AffiliateRebateRate = clampAffiliateRebateRate(settings.AffiliateRebateRate)
	updates[SettingKeyAffiliateRebateRate] = strconv.FormatFloat(settings.AffiliateRebateRate, 'f', 8, 64)
	updates[SettingKeyAffiliateRebateFreezeHours] = strconv.Itoa(clampAffiliateRebateFreezeHours(settings.AffiliateRebateFreezeHours))
	updates[SettingKeyAffiliateRebateDurationDays] = strconv.Itoa(clampAffiliateRebateDurationDays(settings.AffiliateRebateDurationDays))
	updates[SettingKeyAffiliateRebatePerInviteeCap] = strconv.FormatFloat(normalizeAffiliateRebatePerInviteeCap(settings.AffiliateRebatePerInviteeCap), 'f', 8, 64)
	updates[SettingKeyDefaultUserRPMLimit] = strconv.Itoa(settings.DefaultUserRPMLimit)
	defaultSubsJSON, err := json.Marshal(settings.DefaultSubscriptions)
	if err != nil {
		return fmt.Errorf("marshal default subscriptions: %w", err)
	}
	updates[SettingKeyDefaultSubscriptions] = string(defaultSubsJSON)

	// Model fallback configuration
	updates[SettingKeyEnableModelFallback] = strconv.FormatBool(settings.EnableModelFallback)
	updates[SettingKeyFallbackModelAnthropic] = settings.FallbackModelAnthropic
	updates[SettingKeyFallbackModelOpenAI] = settings.FallbackModelOpenAI
	updates[SettingKeyFallbackModelGemini] = settings.FallbackModelGemini

	// Identity patch configuration (Claude -> Gemini)
	updates[SettingKeyEnableIdentityPatch] = strconv.FormatBool(settings.EnableIdentityPatch)
	updates[SettingKeyIdentityPatchPrompt] = settings.IdentityPatchPrompt

	// Ops monitoring (vNext)
	updates[SettingKeyOpsMonitoringEnabled] = strconv.FormatBool(settings.OpsMonitoringEnabled)
	updates[SettingKeyOpsRealtimeMonitoringEnabled] = strconv.FormatBool(settings.OpsRealtimeMonitoringEnabled)
	updates[SettingKeyOpsQueryModeDefault] = string(ParseOpsQueryMode(settings.OpsQueryModeDefault))
	if settings.OpsMetricsIntervalSeconds > 0 {
		updates[SettingKeyOpsMetricsIntervalSeconds] = strconv.Itoa(settings.OpsMetricsIntervalSeconds)
	}

	// Claude Code version check
	updates[SettingKeyMinClaudeCodeVersion] = settings.MinClaudeCodeVersion
	updates[SettingKeyMaxClaudeCodeVersion] = settings.MaxClaudeCodeVersion

	// 分组隔离
	updates[SettingKeyAllowUngroupedKeyScheduling] = strconv.FormatBool(settings.AllowUngroupedKeyScheduling)

	// Backend Mode
	updates[SettingKeyBackendModeEnabled] = strconv.FormatBool(settings.BackendModeEnabled)

	// Gateway forwarding behavior
	updates[SettingKeyEnableFingerprintUnification] = strconv.FormatBool(settings.EnableFingerprintUnification)
	updates[SettingKeyEnableMetadataPassthrough] = strconv.FormatBool(settings.EnableMetadataPassthrough)
	updates[SettingKeyDefaultUpstreamUserAgent] = strings.TrimSpace(settings.DefaultUpstreamUserAgent)
	updates[SettingKeyForceUnifiedUpstreamUserAgent] = strconv.FormatBool(settings.ForceUnifiedUpstreamUserAgent)
	updates[SettingKeyUpdateGitHubRepo] = strings.TrimSpace(settings.UpdateGitHubRepo)
	updates[SettingKeyEnableCCHSigning] = strconv.FormatBool(settings.EnableCCHSigning)
	normalizedAntigravityUAVersion := normalizeAntigravityUserAgentVersion(settings.AntigravityUserAgentVersion)
	updates[SettingKeyAntigravityUserAgentVersion] = normalizedAntigravityUAVersion
	updates[SettingPaymentVisibleMethodAlipaySource] = settings.PaymentVisibleMethodAlipaySource
	updates[SettingPaymentVisibleMethodWxpaySource] = settings.PaymentVisibleMethodWxpaySource
	updates[SettingPaymentVisibleMethodAlipayEnabled] = strconv.FormatBool(settings.PaymentVisibleMethodAlipayEnabled)
	updates[SettingPaymentVisibleMethodWxpayEnabled] = strconv.FormatBool(settings.PaymentVisibleMethodWxpayEnabled)
	updates[openAIAdvancedSchedulerSettingKey] = strconv.FormatBool(settings.OpenAIAdvancedSchedulerEnabled)

	// Balance low notification
	updates[SettingKeyBalanceLowNotifyEnabled] = strconv.FormatBool(settings.BalanceLowNotifyEnabled)
	updates[SettingKeyBalanceLowNotifyThreshold] = strconv.FormatFloat(settings.BalanceLowNotifyThreshold, 'f', 8, 64)
	updates[SettingKeyBalanceLowNotifyRechargeURL] = settings.BalanceLowNotifyRechargeURL
	updates[SettingKeyAccountQuotaNotifyEnabled] = strconv.FormatBool(settings.AccountQuotaNotifyEnabled)
	updates[SettingKeyAccountQuotaNotifyEmails] = MarshalNotifyEmails(settings.AccountQuotaNotifyEmails)
	updates[SettingKeyChannelMonitorEnabled] = strconv.FormatBool(settings.ChannelMonitorEnabled)
	updates[SettingKeyChannelMonitorDefaultIntervalSeconds] = strconv.Itoa(clampChannelMonitorInterval(settings.ChannelMonitorDefaultIntervalSeconds))
	updates[SettingKeyAvailableChannelsEnabled] = strconv.FormatBool(settings.AvailableChannelsEnabled)

	err = s.settingRepo.SetMultiple(ctx, updates)
	if err == nil {
		// 先使 inflight singleflight 失效，再刷新缓存，缩小旧值覆盖新值的竞态窗口
		versionBoundsSF.Forget("version_bounds")
		versionBoundsCache.Store(&cachedVersionBounds{
			min:       settings.MinClaudeCodeVersion,
			max:       settings.MaxClaudeCodeVersion,
			expiresAt: time.Now().Add(versionBoundsCacheTTL).UnixNano(),
		})
		backendModeSF.Forget("backend_mode")
		backendModeCache.Store(&cachedBackendMode{
			value:     settings.BackendModeEnabled,
			expiresAt: time.Now().Add(backendModeCacheTTL).UnixNano(),
		})
		gatewayForwardingSF.Forget("gateway_forwarding")
		gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{
			fingerprintUnification: settings.EnableFingerprintUnification,
			metadataPassthrough:    settings.EnableMetadataPassthrough,
			cchSigning:             settings.EnableCCHSigning,
			expiresAt:              time.Now().Add(gatewayForwardingCacheTTL).UnixNano(),
		})
		openAIAdvancedSchedulerSettingSF.Forget(openAIAdvancedSchedulerSettingKey)
		openAIAdvancedSchedulerSettingCache.Store(&cachedOpenAIAdvancedSchedulerSetting{
			enabled:   settings.OpenAIAdvancedSchedulerEnabled,
			expiresAt: time.Now().Add(openAIAdvancedSchedulerSettingCacheTTL).UnixNano(),
		})
		s.antigravityUAVersionSF.Forget(antigravityUserAgentVersionCacheKey)
		effectiveAntigravityUAVersion := normalizedAntigravityUAVersion
		if effectiveAntigravityUAVersion == "" {
			effectiveAntigravityUAVersion = defaultAntigravityUserAgentVersion()
		}
		s.antigravityUAVersionCache.Store(&cachedAntigravityUserAgentVersion{
			version:   effectiveAntigravityUAVersion,
			expiresAt: time.Now().Add(antigravityUserAgentVersionCacheTTL).UnixNano(),
		})
		if s.onUpdate != nil {
			s.onUpdate() // Invalidate cache after settings update
		}
	}
	return err
}

// GetAntigravityUserAgentVersion 返回后台设置或环境变量中的 Antigravity UA 版本号。
func (s *SettingService) GetAntigravityUserAgentVersion(ctx context.Context) string {
	if s == nil || s.settingRepo == nil {
		return defaultAntigravityUserAgentVersion()
	}
	if cached, ok := s.antigravityUAVersionCache.Load().(*cachedAntigravityUserAgentVersion); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.version
		}
	}

	result, err, _ := s.antigravityUAVersionSF.Do(antigravityUserAgentVersionCacheKey, func() (any, error) {
		if cached, ok := s.antigravityUAVersionCache.Load().(*cachedAntigravityUserAgentVersion); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cached.version, nil
			}
		}

		if ctx == nil {
			ctx = context.Background()
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), antigravityUserAgentVersionDBTimeout)
		defer cancel()

		value, err := s.settingRepo.GetValue(dbCtx, SettingKeyAntigravityUserAgentVersion)
		if err != nil {
			version := defaultAntigravityUserAgentVersion()
			slog.Warn("failed to get antigravity user-agent version setting, using default", "error", err)
			s.antigravityUAVersionCache.Store(&cachedAntigravityUserAgentVersion{
				version:   version,
				expiresAt: time.Now().Add(antigravityUserAgentVersionErrorTTL).UnixNano(),
			})
			return version, nil
		}

		version := normalizeAntigravityUserAgentVersion(value)
		if version == "" {
			version = defaultAntigravityUserAgentVersion()
		}
		s.antigravityUAVersionCache.Store(&cachedAntigravityUserAgentVersion{
			version:   version,
			expiresAt: time.Now().Add(antigravityUserAgentVersionCacheTTL).UnixNano(),
		})
		return version, nil
	})
	if err != nil {
		return defaultAntigravityUserAgentVersion()
	}
	version, ok := result.(string)
	if !ok || version == "" {
		return defaultAntigravityUserAgentVersion()
	}
	return version
}

func (s *SettingService) defaultRewriteMessageCacheControl() bool {
	return false
}

func (s *SettingService) validateDefaultSubscriptionGroups(ctx context.Context, items []DefaultSubscriptionSetting) error {
	if len(items) == 0 {
		return nil
	}

	checked := make(map[int64]struct{}, len(items))
	for _, item := range items {
		if item.GroupID <= 0 {
			continue
		}
		if _, ok := checked[item.GroupID]; ok {
			return ErrDefaultSubGroupDuplicate.WithMetadata(map[string]string{
				"group_id": strconv.FormatInt(item.GroupID, 10),
			})
		}
		checked[item.GroupID] = struct{}{}
		if s.defaultSubGroupReader == nil {
			continue
		}

		group, err := s.defaultSubGroupReader.GetByID(ctx, item.GroupID)
		if err != nil {
			if errors.Is(err, ErrGroupNotFound) {
				return ErrDefaultSubGroupInvalid.WithMetadata(map[string]string{
					"group_id": strconv.FormatInt(item.GroupID, 10),
				})
			}
			return fmt.Errorf("get default subscription group %d: %w", item.GroupID, err)
		}
		if !group.IsSubscriptionType() {
			return ErrDefaultSubGroupInvalid.WithMetadata(map[string]string{
				"group_id": strconv.FormatInt(item.GroupID, 10),
			})
		}
	}

	return nil
}

func (s *SettingService) GetEmailOAuthProviderConfig(ctx context.Context, provider string) (config.EmailOAuthProviderConfig, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider != "github" && provider != "google" {
		return config.EmailOAuthProviderConfig{}, infraerrors.NotFound("OAUTH_PROVIDER_NOT_FOUND", "oauth provider not found")
	}
	keys := []string{
		SettingKeyGitHubOAuthEnabled,
		SettingKeyGitHubOAuthClientID,
		SettingKeyGitHubOAuthClientSecret,
		SettingKeyGitHubOAuthRedirectURL,
		SettingKeyGitHubOAuthFrontendRedirectURL,
		SettingKeyGoogleOAuthEnabled,
		SettingKeyGoogleOAuthClientID,
		SettingKeyGoogleOAuthClientSecret,
		SettingKeyGoogleOAuthRedirectURL,
		SettingKeyGoogleOAuthFrontendRedirectURL,
	}
	settings, err := s.settingRepo.GetMultiple(ctx, keys)
	if err != nil {
		return config.EmailOAuthProviderConfig{}, fmt.Errorf("get email oauth settings: %w", err)
	}
	cfg := s.effectiveEmailOAuthConfig(settings, provider)
	if !cfg.Enabled {
		return config.EmailOAuthProviderConfig{}, infraerrors.NotFound("OAUTH_DISABLED", "oauth login is disabled")
	}
	if strings.TrimSpace(cfg.ClientID) == "" {
		return config.EmailOAuthProviderConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth client id not configured")
	}
	if strings.TrimSpace(cfg.ClientSecret) == "" {
		return config.EmailOAuthProviderConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth client secret not configured")
	}
	for label, rawURL := range map[string]string{
		"authorize": cfg.AuthorizeURL,
		"token":     cfg.TokenURL,
		"userinfo":  cfg.UserInfoURL,
		"redirect":  cfg.RedirectURL,
	} {
		if strings.TrimSpace(rawURL) == "" {
			return config.EmailOAuthProviderConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth "+label+" url not configured")
		}
		if err := config.ValidateAbsoluteHTTPURL(rawURL); err != nil {
			return config.EmailOAuthProviderConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth "+label+" url invalid")
		}
	}
	if strings.TrimSpace(cfg.EmailsURL) != "" {
		if err := config.ValidateAbsoluteHTTPURL(cfg.EmailsURL); err != nil {
			return config.EmailOAuthProviderConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth emails url invalid")
		}
	}
	if err := config.ValidateFrontendRedirectURL(cfg.FrontendRedirectURL); err != nil {
		return config.EmailOAuthProviderConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth frontend redirect url invalid")
	}
	return cfg, nil
}

func (s *SettingService) effectiveEmailOAuthConfig(settings map[string]string, provider string) config.EmailOAuthProviderConfig {
	cfg := s.emailOAuthBaseConfig(provider)
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "github":
		if raw, ok := settings[SettingKeyGitHubOAuthEnabled]; ok {
			cfg.Enabled = raw == "true"
		}
		cfg.ClientID = firstNonEmpty(settings[SettingKeyGitHubOAuthClientID], cfg.ClientID)
		cfg.ClientSecret = firstNonEmpty(settings[SettingKeyGitHubOAuthClientSecret], cfg.ClientSecret)
		cfg.RedirectURL = firstNonEmpty(settings[SettingKeyGitHubOAuthRedirectURL], cfg.RedirectURL)
		cfg.FrontendRedirectURL = firstNonEmpty(settings[SettingKeyGitHubOAuthFrontendRedirectURL], cfg.FrontendRedirectURL, defaultGitHubOAuthFrontend)
	case "google":
		if raw, ok := settings[SettingKeyGoogleOAuthEnabled]; ok {
			cfg.Enabled = raw == "true"
		}
		cfg.ClientID = firstNonEmpty(settings[SettingKeyGoogleOAuthClientID], cfg.ClientID)
		cfg.ClientSecret = firstNonEmpty(settings[SettingKeyGoogleOAuthClientSecret], cfg.ClientSecret)
		cfg.RedirectURL = firstNonEmpty(settings[SettingKeyGoogleOAuthRedirectURL], cfg.RedirectURL)
		cfg.FrontendRedirectURL = firstNonEmpty(settings[SettingKeyGoogleOAuthFrontendRedirectURL], cfg.FrontendRedirectURL, defaultGoogleOAuthFrontend)
	}
	return cfg
}

func (s *SettingService) emailOAuthPublicEnabled(settings map[string]string, provider string) bool {
	cfg := s.effectiveEmailOAuthConfig(settings, provider)
	return cfg.Enabled && strings.TrimSpace(cfg.ClientID) != "" && strings.TrimSpace(cfg.ClientSecret) != ""
}

func (s *SettingService) emailOAuthBaseConfig(provider string) config.EmailOAuthProviderConfig {
	var cfg config.EmailOAuthProviderConfig
	if s != nil && s.cfg != nil {
		switch strings.ToLower(strings.TrimSpace(provider)) {
		case "github":
			cfg = s.cfg.GitHubOAuth
		case "google":
			cfg = s.cfg.GoogleOAuth
		}
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "github":
		cfg.AuthorizeURL = firstNonEmpty(cfg.AuthorizeURL, defaultGitHubOAuthAuthorize)
		cfg.TokenURL = firstNonEmpty(cfg.TokenURL, defaultGitHubOAuthToken)
		cfg.UserInfoURL = firstNonEmpty(cfg.UserInfoURL, defaultGitHubOAuthUserInfo)
		cfg.EmailsURL = firstNonEmpty(cfg.EmailsURL, defaultGitHubOAuthEmails)
		cfg.Scopes = firstNonEmpty(cfg.Scopes, defaultGitHubOAuthScopes)
		cfg.FrontendRedirectURL = firstNonEmpty(cfg.FrontendRedirectURL, defaultGitHubOAuthFrontend)
	case "google":
		cfg.AuthorizeURL = firstNonEmpty(cfg.AuthorizeURL, defaultGoogleOAuthAuthorize)
		cfg.TokenURL = firstNonEmpty(cfg.TokenURL, defaultGoogleOAuthToken)
		cfg.UserInfoURL = firstNonEmpty(cfg.UserInfoURL, defaultGoogleOAuthUserInfo)
		cfg.Scopes = firstNonEmpty(cfg.Scopes, defaultGoogleOAuthScopes)
		cfg.FrontendRedirectURL = firstNonEmpty(cfg.FrontendRedirectURL, defaultGoogleOAuthFrontend)
	}
	return cfg
}

// IsRegistrationEnabled 检查是否开放注册
func (s *SettingService) IsRegistrationEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyRegistrationEnabled)
	if err != nil {
		// 安全默认：如果设置不存在或查询出错，默认关闭注册
		return false
	}
	return value == "true"
}

// OIDCSecurityWriteDefaults 返回 OIDC 安全字段在写入时应采用的默认值。
// 仅根据数据库原始键值判断；键缺失时返回 false，避免把配置层隐式默认值固化写回数据库。
func (s *SettingService) OIDCSecurityWriteDefaults(ctx context.Context) (usePKCE bool, validateIDToken bool) {
	values, err := s.settingRepo.GetMultiple(ctx, []string{
		SettingKeyOIDCConnectUsePKCE,
		SettingKeyOIDCConnectValidateIDToken,
	})
	if err != nil {
		return false, false
	}
	usePKCE = strings.EqualFold(strings.TrimSpace(values[SettingKeyOIDCConnectUsePKCE]), "true")
	validateIDToken = strings.EqualFold(strings.TrimSpace(values[SettingKeyOIDCConnectValidateIDToken]), "true")
	return usePKCE, validateIDToken
}

// GetDefaultUserRPMLimit 获取新用户默认 RPM 限制（0 = 不限制）。
// 设置缺失、解析失败或值为负数时，统一回退为 0。
func (s *SettingService) GetDefaultUserRPMLimit(ctx context.Context) int {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyDefaultUserRPMLimit)
	if err != nil {
		return 0
	}
	limit, convErr := strconv.Atoi(strings.TrimSpace(value))
	if convErr != nil || limit < 0 {
		return 0
	}
	return limit
}

// IsBackendModeEnabled checks if backend mode is enabled
// Uses in-process atomic.Value cache with 60s TTL, zero-lock hot path
func (s *SettingService) IsBackendModeEnabled(ctx context.Context) bool {
	if cached, ok := backendModeCache.Load().(*cachedBackendMode); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.value
		}
	}
	result, _, _ := backendModeSF.Do("backend_mode", func() (any, error) {
		if cached, ok := backendModeCache.Load().(*cachedBackendMode); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cached.value, nil
			}
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backendModeDBTimeout)
		defer cancel()
		value, err := s.settingRepo.GetValue(dbCtx, SettingKeyBackendModeEnabled)
		if err != nil {
			if errors.Is(err, ErrSettingNotFound) {
				// Setting not yet created (fresh install) - default to disabled with full TTL
				backendModeCache.Store(&cachedBackendMode{
					value:     false,
					expiresAt: time.Now().Add(backendModeCacheTTL).UnixNano(),
				})
				return false, nil
			}
			slog.Warn("failed to get backend_mode_enabled setting", "error", err)
			backendModeCache.Store(&cachedBackendMode{
				value:     false,
				expiresAt: time.Now().Add(backendModeErrorTTL).UnixNano(),
			})
			return false, nil
		}
		enabled := value == "true"
		backendModeCache.Store(&cachedBackendMode{
			value:     enabled,
			expiresAt: time.Now().Add(backendModeCacheTTL).UnixNano(),
		})
		return enabled, nil
	})
	if val, ok := result.(bool); ok {
		return val
	}
	return false
}

// GetGatewayForwardingSettings returns cached gateway forwarding settings.
// Uses in-process atomic.Value cache with 60s TTL, zero-lock hot path.
// Returns (fingerprintUnification, metadataPassthrough, cchSigning).
func (s *SettingService) GetGatewayForwardingSettings(ctx context.Context) (fingerprintUnification, metadataPassthrough, cchSigning bool) {
	result := s.getGatewayForwardingSettingsCached(ctx)
	return result.fp, result.mp, result.cch
}

type gatewayForwardingSettingsResult struct {
	fp, mp, cch, cacheTTL1h, rewriteMessageCacheControl bool
}

func (s *SettingService) getGatewayForwardingSettingsCached(ctx context.Context) gatewayForwardingSettingsResult {
	if cached, ok := gatewayForwardingCache.Load().(*cachedGatewayForwardingSettings); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return gatewayForwardingSettingsResult{
				fp:                         cached.fingerprintUnification,
				mp:                         cached.metadataPassthrough,
				cch:                        cached.cchSigning,
				cacheTTL1h:                 cached.anthropicCacheTTL1hInjection,
				rewriteMessageCacheControl: cached.rewriteMessageCacheControl,
			}
		}
	}
	val, _, _ := gatewayForwardingSF.Do("gateway_forwarding", func() (any, error) {
		if cached, ok := gatewayForwardingCache.Load().(*cachedGatewayForwardingSettings); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return gatewayForwardingSettingsResult{
					fp:                         cached.fingerprintUnification,
					mp:                         cached.metadataPassthrough,
					cch:                        cached.cchSigning,
					cacheTTL1h:                 cached.anthropicCacheTTL1hInjection,
					rewriteMessageCacheControl: cached.rewriteMessageCacheControl,
				}, nil
			}
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gatewayForwardingDBTimeout)
		defer cancel()
		values, err := s.settingRepo.GetMultiple(dbCtx, []string{
			SettingKeyEnableFingerprintUnification,
			SettingKeyEnableMetadataPassthrough,
			SettingKeyEnableCCHSigning,
			SettingKeyEnableAnthropicCacheTTL1hInjection,
			SettingKeyRewriteMessageCacheControl,
		})
		if err != nil {
			slog.Warn("failed to get gateway forwarding settings", "error", err)
			gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{
				fingerprintUnification:       true,
				metadataPassthrough:          false,
				cchSigning:                   false,
				anthropicCacheTTL1hInjection: false,
				rewriteMessageCacheControl:   s.defaultRewriteMessageCacheControl(),
				expiresAt:                    time.Now().Add(gatewayForwardingErrorTTL).UnixNano(),
			})
			return gatewayForwardingSettingsResult{fp: true, rewriteMessageCacheControl: s.defaultRewriteMessageCacheControl()}, nil
		}
		fp := true
		if v, ok := values[SettingKeyEnableFingerprintUnification]; ok && v != "" {
			fp = v == "true"
		}
		mp := values[SettingKeyEnableMetadataPassthrough] == "true"
		cch := values[SettingKeyEnableCCHSigning] == "true"
		cacheTTL1h := values[SettingKeyEnableAnthropicCacheTTL1hInjection] == "true"
		rewriteMessageCacheControl := s.defaultRewriteMessageCacheControl()
		if v, ok := values[SettingKeyRewriteMessageCacheControl]; ok && v != "" {
			rewriteMessageCacheControl = v == "true"
		}
		gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{
			fingerprintUnification:       fp,
			metadataPassthrough:          mp,
			cchSigning:                   cch,
			anthropicCacheTTL1hInjection: cacheTTL1h,
			rewriteMessageCacheControl:   rewriteMessageCacheControl,
			expiresAt:                    time.Now().Add(gatewayForwardingCacheTTL).UnixNano(),
		})
		return gatewayForwardingSettingsResult{
			fp:                         fp,
			mp:                         mp,
			cch:                        cch,
			cacheTTL1h:                 cacheTTL1h,
			rewriteMessageCacheControl: rewriteMessageCacheControl,
		}, nil
	})
	if r, ok := val.(gatewayForwardingSettingsResult); ok {
		return r
	}
	return gatewayForwardingSettingsResult{fp: true}
}

// IsAnthropicCacheTTL1hInjectionEnabled 检查是否对 Anthropic OAuth/SetupToken 请求体注入 1h cache_control ttl。
func (s *SettingService) IsAnthropicCacheTTL1hInjectionEnabled(ctx context.Context) bool {
	if cached, ok := gatewayForwardingCache.Load().(*cachedGatewayForwardingSettings); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.anthropicCacheTTL1hInjection
		}
	}
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gatewayForwardingDBTimeout)
	defer cancel()
	value, err := s.settingRepo.GetValue(dbCtx, SettingKeyEnableAnthropicCacheTTL1hInjection)
	if err != nil {
		return false
	}
	return value == "true"
}

// IsRewriteMessageCacheControlEnabled 检查是否启用 messages cache_control 改写。
func (s *SettingService) IsRewriteMessageCacheControlEnabled(ctx context.Context) bool {
	return s.getGatewayForwardingSettingsCached(ctx).rewriteMessageCacheControl
}

// IsEmailVerifyEnabled 检查是否开启邮件验证
func (s *SettingService) IsEmailVerifyEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyEmailVerifyEnabled)
	if err != nil {
		return false
	}
	return value == "true"
}

// GetRegistrationEmailSuffixWhitelist returns normalized registration email suffix whitelist.
func (s *SettingService) GetRegistrationEmailSuffixWhitelist(ctx context.Context) []string {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyRegistrationEmailSuffixWhitelist)
	if err != nil {
		return []string{}
	}
	return ParseRegistrationEmailSuffixWhitelist(value)
}

// IsPromoCodeEnabled 检查是否启用优惠码功能
func (s *SettingService) IsPromoCodeEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyPromoCodeEnabled)
	if err != nil {
		return true // 默认启用
	}
	return value != "false"
}

// IsInvitationCodeEnabled 检查是否启用邀请码注册功能
func (s *SettingService) IsInvitationCodeEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyInvitationCodeEnabled)
	if err != nil {
		return false // 默认关闭
	}
	return value == "true"
}

// GetCustomMenuItemsRaw returns the raw JSON string of custom_menu_items setting.
func (s *SettingService) GetCustomMenuItemsRaw(ctx context.Context) string {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyCustomMenuItems)
	if err != nil {
		return "[]"
	}
	return value
}

// IsAffiliateEnabled 检查是否启用邀请返利功能（总开关）
func (s *SettingService) IsAffiliateEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyAffiliateEnabled)
	if err != nil {
		return false // 默认关闭
	}
	return value == "true"
}

// GetAffiliateRebateRatePercent 读取并 clamp 全局返利比例。
func (s *SettingService) GetAffiliateRebateRatePercent(ctx context.Context) float64 {
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyAffiliateRebateRate)
	if err != nil {
		return AffiliateRebateRateDefault
	}
	rate, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return AffiliateRebateRateDefault
	}
	return clampAffiliateRebateRate(rate)
}

// GetAffiliateRebateFreezeHours 返回返利冻结期（小时）。
func (s *SettingService) GetAffiliateRebateFreezeHours(ctx context.Context) int {
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyAffiliateRebateFreezeHours)
	if err != nil {
		return AffiliateRebateFreezeHoursDefault
	}
	return parseAffiliateRebateFreezeHours(raw)
}

// GetAffiliateRebateDurationDays 返回返利有效期（天）。
func (s *SettingService) GetAffiliateRebateDurationDays(ctx context.Context) int {
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyAffiliateRebateDurationDays)
	if err != nil {
		return AffiliateRebateDurationDaysDefault
	}
	return parseAffiliateRebateDurationDays(raw)
}

// GetAffiliateRebatePerInviteeCap 返回单人返利上限。
func (s *SettingService) GetAffiliateRebatePerInviteeCap(ctx context.Context) float64 {
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyAffiliateRebatePerInviteeCap)
	if err != nil {
		return AffiliateRebatePerInviteeCapDefault
	}
	return parseAffiliateRebatePerInviteeCap(raw)
}

func parseAffiliateRebateFreezeHours(raw string) int {
	hours, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || hours < 0 {
		return AffiliateRebateFreezeHoursDefault
	}
	return clampAffiliateRebateFreezeHours(hours)
}

func clampAffiliateRebateFreezeHours(hours int) int {
	if hours < 0 {
		return AffiliateRebateFreezeHoursDefault
	}
	if hours > AffiliateRebateFreezeHoursMax {
		return AffiliateRebateFreezeHoursMax
	}
	return hours
}

func parseAffiliateRebateDurationDays(raw string) int {
	days, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || days < 0 {
		return AffiliateRebateDurationDaysDefault
	}
	return clampAffiliateRebateDurationDays(days)
}

func clampAffiliateRebateDurationDays(days int) int {
	if days < 0 {
		return AffiliateRebateDurationDaysDefault
	}
	if days > AffiliateRebateDurationDaysMax {
		return AffiliateRebateDurationDaysMax
	}
	return days
}

func parseAffiliateRebatePerInviteeCap(raw string) float64 {
	capValue, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || capValue < 0 || math.IsNaN(capValue) || math.IsInf(capValue, 0) {
		return AffiliateRebatePerInviteeCapDefault
	}
	return capValue
}

func normalizeAffiliateRebatePerInviteeCap(capValue float64) float64 {
	if capValue < 0 || math.IsNaN(capValue) || math.IsInf(capValue, 0) {
		return AffiliateRebatePerInviteeCapDefault
	}
	return capValue
}

// IsPasswordResetEnabled 检查是否启用密码重置功能
// 要求：必须同时开启邮件验证
func (s *SettingService) IsPasswordResetEnabled(ctx context.Context) bool {
	// Password reset requires email verification to be enabled
	if !s.IsEmailVerifyEnabled(ctx) {
		return false
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyPasswordResetEnabled)
	if err != nil {
		return false // 默认关闭
	}
	return value == "true"
}

// IsTotpEnabled 检查是否启用 TOTP 双因素认证功能
func (s *SettingService) IsTotpEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyTotpEnabled)
	if err != nil {
		return false // 默认关闭
	}
	return value == "true"
}

// IsTotpEncryptionKeyConfigured 检查 TOTP 加密密钥是否已手动配置
// 只有手动配置了密钥才允许在管理后台启用 TOTP 功能
func (s *SettingService) IsTotpEncryptionKeyConfigured() bool {
	return s.cfg.Totp.EncryptionKeyConfigured
}

// GetSiteName 获取网站名称
func (s *SettingService) GetSiteName(ctx context.Context) string {
	value, err := s.settingRepo.GetValue(ctx, SettingKeySiteName)
	if err != nil || value == "" {
		return "Sub2API"
	}
	return value
}

// GetDefaultConcurrency 获取默认并发量
func (s *SettingService) GetDefaultConcurrency(ctx context.Context) int {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyDefaultConcurrency)
	if err != nil {
		return s.cfg.Default.UserConcurrency
	}
	if v, err := strconv.Atoi(value); err == nil && v > 0 {
		return v
	}
	return s.cfg.Default.UserConcurrency
}

// GetDefaultBalance 获取默认余额
func (s *SettingService) GetDefaultBalance(ctx context.Context) float64 {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyDefaultBalance)
	if err != nil {
		return s.cfg.Default.UserBalance
	}
	if v, err := strconv.ParseFloat(value, 64); err == nil && v >= 0 {
		return v
	}
	return s.cfg.Default.UserBalance
}

// GetDefaultSubscriptions 获取新用户默认订阅配置列表。
func (s *SettingService) GetDefaultSubscriptions(ctx context.Context) []DefaultSubscriptionSetting {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyDefaultSubscriptions)
	if err != nil {
		return nil
	}
	return parseDefaultSubscriptions(value)
}

// InitializeDefaultSettings 初始化默认设置
func (s *SettingService) InitializeDefaultSettings(ctx context.Context) error {
	// 检查是否已有设置
	_, err := s.settingRepo.GetValue(ctx, SettingKeyRegistrationEnabled)
	if err == nil {
		// 已有设置，不需要初始化
		return nil
	}
	if !errors.Is(err, ErrSettingNotFound) {
		return fmt.Errorf("check existing settings: %w", err)
	}

	oidcUsePKCEDefault := true
	oidcValidateIDTokenDefault := true
	if s != nil && s.cfg != nil {
		if s.cfg.OIDC.UsePKCEExplicit {
			oidcUsePKCEDefault = s.cfg.OIDC.UsePKCE
		}
		if s.cfg.OIDC.ValidateIDTokenExplicit {
			oidcValidateIDTokenDefault = s.cfg.OIDC.ValidateIDToken
		}
	}
	loginAgreementDocumentsJSON, err := marshalLoginAgreementDocuments(defaultLoginAgreementDocuments())
	if err != nil {
		return err
	}

	// 初始化默认设置
	defaults := map[string]string{
		SettingKeyRegistrationEnabled:                       "true",
		SettingKeyEmailVerifyEnabled:                        "false",
		SettingKeyRegistrationEmailSuffixWhitelist:          "[]",
		SettingKeyPromoCodeEnabled:                          "true", // 默认启用优惠码功能
		SettingKeyLoginAgreementEnabled:                     "false",
		SettingKeyLoginAgreementMode:                        defaultLoginAgreementMode,
		SettingKeyLoginAgreementUpdatedAt:                   defaultLoginAgreementDate,
		SettingKeyLoginAgreementDocuments:                   loginAgreementDocumentsJSON,
		SettingKeySiteName:                                  "Sub2API",
		SettingKeySiteLogo:                                  "",
		SettingKeyPurchaseSubscriptionEnabled:               "false",
		SettingKeyPurchaseSubscriptionURL:                   "",
		SettingKeyTableDefaultPageSize:                      "20",
		SettingKeyTablePageSizeOptions:                      "[10,20,50,100]",
		SettingKeyHiddenAdminMenuItems:                      "[]",
		SettingKeyCustomMenuItems:                           "[]",
		SettingKeyCustomEndpoints:                           "[]",
		SettingKeyLinuxDoConnectEnabled:                     "false",
		SettingKeyDingTalkConnectEnabled:                    "false",
		SettingKeyDingTalkConnectClientID:                   "",
		SettingKeyDingTalkConnectClientSecret:               "",
		SettingKeyDingTalkConnectRedirectURL:                "",
		SettingKeyDingTalkConnectCorpRestrictionPolicy:      "",
		SettingKeyDingTalkConnectInternalCorpID:             "",
		SettingKeyDingTalkConnectBypassRegistration:         "false",
		SettingKeyDingTalkConnectSyncCorpEmail:              "false",
		SettingKeyDingTalkConnectSyncDisplayName:            "false",
		SettingKeyDingTalkConnectSyncDept:                   "false",
		SettingKeyDingTalkConnectSyncCorpEmailAttrKey:       "dingtalk_email",
		SettingKeyDingTalkConnectSyncDisplayNameAttrKey:     "dingtalk_name",
		SettingKeyDingTalkConnectSyncDeptAttrKey:            "dingtalk_department",
		SettingKeyDingTalkConnectSyncCorpEmailAttrName:      "钉钉企业邮箱",
		SettingKeyDingTalkConnectSyncDisplayNameAttrName:    "钉钉姓名",
		SettingKeyDingTalkConnectSyncDeptAttrName:           "钉钉部门",
		SettingKeyWeChatConnectEnabled:                      "false",
		SettingKeyWeChatConnectAppID:                        "",
		SettingKeyWeChatConnectAppSecret:                    "",
		SettingKeyWeChatConnectOpenAppID:                    "",
		SettingKeyWeChatConnectOpenAppSecret:                "",
		SettingKeyWeChatConnectMPAppID:                      "",
		SettingKeyWeChatConnectMPAppSecret:                  "",
		SettingKeyWeChatConnectMobileAppID:                  "",
		SettingKeyWeChatConnectMobileAppSecret:              "",
		SettingKeyWeChatConnectOpenEnabled:                  "false",
		SettingKeyWeChatConnectMPEnabled:                    "false",
		SettingKeyWeChatConnectMobileEnabled:                "false",
		SettingKeyWeChatConnectMode:                         defaultWeChatConnectMode,
		SettingKeyWeChatConnectScopes:                       defaultWeChatConnectScopes,
		SettingKeyWeChatConnectRedirectURL:                  "",
		SettingKeyWeChatConnectFrontendRedirectURL:          defaultWeChatConnectFrontend,
		SettingKeyGitHubOAuthEnabled:                        "false",
		SettingKeyGitHubOAuthClientID:                       "",
		SettingKeyGitHubOAuthClientSecret:                   "",
		SettingKeyGitHubOAuthRedirectURL:                    "",
		SettingKeyGitHubOAuthFrontendRedirectURL:            defaultGitHubOAuthFrontend,
		SettingKeyGoogleOAuthEnabled:                        "false",
		SettingKeyGoogleOAuthClientID:                       "",
		SettingKeyGoogleOAuthClientSecret:                   "",
		SettingKeyGoogleOAuthRedirectURL:                    "",
		SettingKeyGoogleOAuthFrontendRedirectURL:            defaultGoogleOAuthFrontend,
		SettingKeyOIDCConnectEnabled:                        "false",
		SettingKeyOIDCConnectProviderName:                   "OIDC",
		SettingKeyOIDCConnectClientID:                       "",
		SettingKeyOIDCConnectClientSecret:                   "",
		SettingKeyOIDCConnectIssuerURL:                      "",
		SettingKeyOIDCConnectDiscoveryURL:                   "",
		SettingKeyOIDCConnectAuthorizeURL:                   "",
		SettingKeyOIDCConnectTokenURL:                       "",
		SettingKeyOIDCConnectUserInfoURL:                    "",
		SettingKeyOIDCConnectJWKSURL:                        "",
		SettingKeyOIDCConnectScopes:                         "openid email profile",
		SettingKeyOIDCConnectRedirectURL:                    "",
		SettingKeyOIDCConnectFrontendRedirectURL:            "/auth/oidc/callback",
		SettingKeyOIDCConnectTokenAuthMethod:                "client_secret_post",
		SettingKeyOIDCConnectUsePKCE:                        strconv.FormatBool(oidcUsePKCEDefault),
		SettingKeyOIDCConnectValidateIDToken:                strconv.FormatBool(oidcValidateIDTokenDefault),
		SettingKeyOIDCConnectAllowedSigningAlgs:             "RS256,ES256,PS256",
		SettingKeyOIDCConnectClockSkewSeconds:               "120",
		SettingKeyOIDCConnectRequireEmailVerified:           "false",
		SettingKeyOIDCConnectUserInfoEmailPath:              "",
		SettingKeyOIDCConnectUserInfoIDPath:                 "",
		SettingKeyOIDCConnectUserInfoUsernamePath:           "",
		SettingKeyDefaultConcurrency:                        strconv.Itoa(s.cfg.Default.UserConcurrency),
		SettingKeyDefaultBalance:                            strconv.FormatFloat(s.cfg.Default.UserBalance, 'f', 8, 64),
		SettingKeyAffiliateEnabled:                          strconv.FormatBool(AffiliateEnabledDefault),
		SettingKeyAffiliateRebateRate:                       strconv.FormatFloat(AffiliateRebateRateDefault, 'f', 8, 64),
		SettingKeyAffiliateRebateFreezeHours:                strconv.Itoa(AffiliateRebateFreezeHoursDefault),
		SettingKeyAffiliateRebateDurationDays:               strconv.Itoa(AffiliateRebateDurationDaysDefault),
		SettingKeyAffiliateRebatePerInviteeCap:              strconv.FormatFloat(AffiliateRebatePerInviteeCapDefault, 'f', 8, 64),
		SettingKeyDefaultUserRPMLimit:                       "0",
		SettingKeyDefaultSubscriptions:                      "[]",
		SettingKeyAuthSourceDefaultEmailBalance:             "0",
		SettingKeyAuthSourceDefaultEmailConcurrency:         "5",
		SettingKeyAuthSourceDefaultEmailSubscriptions:       "[]",
		SettingKeyAuthSourceDefaultEmailGrantOnSignup:       "false",
		SettingKeyAuthSourceDefaultEmailGrantOnFirstBind:    "false",
		SettingKeyAuthSourceDefaultLinuxDoBalance:           "0",
		SettingKeyAuthSourceDefaultLinuxDoConcurrency:       "5",
		SettingKeyAuthSourceDefaultLinuxDoSubscriptions:     "[]",
		SettingKeyAuthSourceDefaultLinuxDoGrantOnSignup:     "false",
		SettingKeyAuthSourceDefaultLinuxDoGrantOnFirstBind:  "false",
		SettingKeyAuthSourceDefaultOIDCBalance:              "0",
		SettingKeyAuthSourceDefaultOIDCConcurrency:          "5",
		SettingKeyAuthSourceDefaultOIDCSubscriptions:        "[]",
		SettingKeyAuthSourceDefaultOIDCGrantOnSignup:        "false",
		SettingKeyAuthSourceDefaultOIDCGrantOnFirstBind:     "false",
		SettingKeyAuthSourceDefaultWeChatBalance:            "0",
		SettingKeyAuthSourceDefaultWeChatConcurrency:        "5",
		SettingKeyAuthSourceDefaultWeChatSubscriptions:      "[]",
		SettingKeyAuthSourceDefaultWeChatGrantOnSignup:      "false",
		SettingKeyAuthSourceDefaultWeChatGrantOnFirstBind:   "false",
		SettingKeyAuthSourceDefaultGitHubBalance:            "0",
		SettingKeyAuthSourceDefaultGitHubConcurrency:        "5",
		SettingKeyAuthSourceDefaultGitHubSubscriptions:      "[]",
		SettingKeyAuthSourceDefaultGitHubGrantOnSignup:      "false",
		SettingKeyAuthSourceDefaultGitHubGrantOnFirstBind:   "false",
		SettingKeyAuthSourceDefaultGoogleBalance:            "0",
		SettingKeyAuthSourceDefaultGoogleConcurrency:        "5",
		SettingKeyAuthSourceDefaultGoogleSubscriptions:      "[]",
		SettingKeyAuthSourceDefaultGoogleGrantOnSignup:      "false",
		SettingKeyAuthSourceDefaultGoogleGrantOnFirstBind:   "false",
		SettingKeyAuthSourceDefaultDingTalkBalance:          "0",
		SettingKeyAuthSourceDefaultDingTalkConcurrency:      "5",
		SettingKeyAuthSourceDefaultDingTalkSubscriptions:    "[]",
		SettingKeyAuthSourceDefaultDingTalkGrantOnSignup:    "false",
		SettingKeyAuthSourceDefaultDingTalkGrantOnFirstBind: "false",
		SettingKeyForceEmailOnThirdPartySignup:              "false",
		SettingKeySMTPPort:                                  "587",
		SettingKeySMTPUseTLS:                                "false",
		// Model fallback defaults
		SettingKeyEnableModelFallback:    "false",
		SettingKeyFallbackModelAnthropic: "claude-3-5-sonnet-20241022",
		SettingKeyFallbackModelOpenAI:    "gpt-4o",
		SettingKeyFallbackModelGemini:    "gemini-2.5-pro",
		// Identity patch defaults
		SettingKeyEnableIdentityPatch: "true",
		SettingKeyIdentityPatchPrompt: "",

		// Ops monitoring defaults (vNext)
		SettingKeyOpsMonitoringEnabled:         "true",
		SettingKeyOpsRealtimeMonitoringEnabled: "true",
		SettingKeyOpsQueryModeDefault:          "auto",
		SettingKeyOpsMetricsIntervalSeconds:    "60",

		// Claude Code version check (default: empty = disabled)
		SettingKeyMinClaudeCodeVersion: "",
		SettingKeyMaxClaudeCodeVersion: "",

		// 分组隔离（默认不允许未分组 Key 调度）
		SettingKeyAllowUngroupedKeyScheduling:          "false",
		SettingPaymentVisibleMethodAlipaySource:        "",
		SettingPaymentVisibleMethodWxpaySource:         "",
		SettingPaymentVisibleMethodAlipayEnabled:       "false",
		SettingPaymentVisibleMethodWxpayEnabled:        "false",
		openAIAdvancedSchedulerSettingKey:              "false",
		SettingKeyChannelMonitorEnabled:                "true",
		SettingKeyChannelMonitorDefaultIntervalSeconds: strconv.Itoa(channelMonitorIntervalFallback),
		SettingKeyAvailableChannelsEnabled:             "false",
	}

	return s.settingRepo.SetMultiple(ctx, defaults)
}

// parseSettings 解析设置到结构体
func (s *SettingService) parseSettings(settings map[string]string) *SystemSettings {
	emailVerifyEnabled := settings[SettingKeyEmailVerifyEnabled] == "true"
	loginAgreementDocuments := parseLoginAgreementDocuments(settings[SettingKeyLoginAgreementDocuments])
	loginAgreementUpdatedAt := strings.TrimSpace(settings[SettingKeyLoginAgreementUpdatedAt])
	if loginAgreementUpdatedAt == "" {
		loginAgreementUpdatedAt = defaultLoginAgreementDate
	}
	result := &SystemSettings{
		RegistrationEnabled:              settings[SettingKeyRegistrationEnabled] == "true",
		EmailVerifyEnabled:               emailVerifyEnabled,
		RegistrationEmailSuffixWhitelist: ParseRegistrationEmailSuffixWhitelist(settings[SettingKeyRegistrationEmailSuffixWhitelist]),
		PromoCodeEnabled:                 settings[SettingKeyPromoCodeEnabled] != "false", // 默认启用
		PasswordResetEnabled:             emailVerifyEnabled && settings[SettingKeyPasswordResetEnabled] == "true",
		FrontendURL:                      settings[SettingKeyFrontendURL],
		InvitationCodeEnabled:            settings[SettingKeyInvitationCodeEnabled] == "true",
		TotpEnabled:                      settings[SettingKeyTotpEnabled] == "true",
		LoginAgreementEnabled:            settings[SettingKeyLoginAgreementEnabled] == "true",
		LoginAgreementMode:               normalizeLoginAgreementMode(settings[SettingKeyLoginAgreementMode]),
		LoginAgreementUpdatedAt:          loginAgreementUpdatedAt,
		LoginAgreementDocuments:          loginAgreementDocuments,
		SMTPHost:                         settings[SettingKeySMTPHost],
		SMTPUsername:                     settings[SettingKeySMTPUsername],
		SMTPFrom:                         settings[SettingKeySMTPFrom],
		SMTPFromName:                     settings[SettingKeySMTPFromName],
		SMTPUseTLS:                       settings[SettingKeySMTPUseTLS] == "true",
		SMTPPasswordConfigured:           settings[SettingKeySMTPPassword] != "",
		TurnstileEnabled:                 settings[SettingKeyTurnstileEnabled] == "true",
		TurnstileSiteKey:                 settings[SettingKeyTurnstileSiteKey],
		TurnstileSecretKeyConfigured:     settings[SettingKeyTurnstileSecretKey] != "",
		SiteName:                         s.getStringOrDefault(settings, SettingKeySiteName, "Sub2API"),
		SiteLogo:                         settings[SettingKeySiteLogo],
		SiteSubtitle:                     s.getStringOrDefault(settings, SettingKeySiteSubtitle, "Subscription to API Conversion Platform"),
		APIBaseURL:                       settings[SettingKeyAPIBaseURL],
		ContactInfo:                      settings[SettingKeyContactInfo],
		DocURL:                           settings[SettingKeyDocURL],
		HomeContent:                      settings[SettingKeyHomeContent],
		HideCcsImportButton:              settings[SettingKeyHideCcsImportButton] == "true",
		PurchaseSubscriptionEnabled:      settings[SettingKeyPurchaseSubscriptionEnabled] == "true",
		PurchaseSubscriptionURL:          strings.TrimSpace(settings[SettingKeyPurchaseSubscriptionURL]),
		HiddenAdminMenuItems:             settings[SettingKeyHiddenAdminMenuItems],
		CustomMenuItems:                  settings[SettingKeyCustomMenuItems],
		CustomEndpoints:                  settings[SettingKeyCustomEndpoints],
		BackendModeEnabled:               settings[SettingKeyBackendModeEnabled] == "true",
	}
	result.TableDefaultPageSize, result.TablePageSizeOptions = parseTablePreferences(
		settings[SettingKeyTableDefaultPageSize],
		settings[SettingKeyTablePageSizeOptions],
	)

	// 解析整数类型
	if port, err := strconv.Atoi(settings[SettingKeySMTPPort]); err == nil {
		result.SMTPPort = port
	} else {
		result.SMTPPort = 587
	}

	if concurrency, err := strconv.Atoi(settings[SettingKeyDefaultConcurrency]); err == nil {
		result.DefaultConcurrency = concurrency
	} else {
		result.DefaultConcurrency = s.cfg.Default.UserConcurrency
	}

	// 解析浮点数类型
	if balance, err := strconv.ParseFloat(settings[SettingKeyDefaultBalance], 64); err == nil {
		result.DefaultBalance = balance
	} else {
		result.DefaultBalance = s.cfg.Default.UserBalance
	}
	if rebateRate, err := strconv.ParseFloat(settings[SettingKeyAffiliateRebateRate], 64); err == nil {
		result.AffiliateRebateRate = clampAffiliateRebateRate(rebateRate)
	} else {
		result.AffiliateRebateRate = AffiliateRebateRateDefault
	}
	result.AffiliateEnabled = settings[SettingKeyAffiliateEnabled] == "true"
	result.AffiliateRebateFreezeHours = parseAffiliateRebateFreezeHours(settings[SettingKeyAffiliateRebateFreezeHours])
	result.AffiliateRebateDurationDays = parseAffiliateRebateDurationDays(settings[SettingKeyAffiliateRebateDurationDays])
	result.AffiliateRebatePerInviteeCap = parseAffiliateRebatePerInviteeCap(settings[SettingKeyAffiliateRebatePerInviteeCap])
	result.DefaultSubscriptions = parseDefaultSubscriptions(settings[SettingKeyDefaultSubscriptions])

	// 敏感信息直接返回，方便测试连接时使用
	result.SMTPPassword = settings[SettingKeySMTPPassword]
	result.TurnstileSecretKey = settings[SettingKeyTurnstileSecretKey]

	// LinuxDo Connect 设置：
	// - 兼容 config.yaml/env（避免老部署因为未迁移到数据库设置而被意外关闭）
	// - 支持在后台“系统设置”中覆盖并持久化（存储于 DB）
	linuxDoBase := config.LinuxDoConnectConfig{}
	if s.cfg != nil {
		linuxDoBase = s.cfg.LinuxDo
	}

	if raw, ok := settings[SettingKeyLinuxDoConnectEnabled]; ok {
		result.LinuxDoConnectEnabled = raw == "true"
	} else {
		result.LinuxDoConnectEnabled = linuxDoBase.Enabled
	}

	if v, ok := settings[SettingKeyLinuxDoConnectClientID]; ok && strings.TrimSpace(v) != "" {
		result.LinuxDoConnectClientID = strings.TrimSpace(v)
	} else {
		result.LinuxDoConnectClientID = linuxDoBase.ClientID
	}

	if v, ok := settings[SettingKeyLinuxDoConnectRedirectURL]; ok && strings.TrimSpace(v) != "" {
		result.LinuxDoConnectRedirectURL = strings.TrimSpace(v)
	} else {
		result.LinuxDoConnectRedirectURL = linuxDoBase.RedirectURL
	}

	result.LinuxDoConnectClientSecret = strings.TrimSpace(settings[SettingKeyLinuxDoConnectClientSecret])
	if result.LinuxDoConnectClientSecret == "" {
		result.LinuxDoConnectClientSecret = strings.TrimSpace(linuxDoBase.ClientSecret)
	}
	result.LinuxDoConnectClientSecretConfigured = result.LinuxDoConnectClientSecret != ""

	// DingTalk Connect 设置：
	// - 兼容 config.yaml/env
	// - 支持后台系统设置覆盖并持久化（存储于 DB）
	dingTalkBase := config.DingTalkConnectConfig{}
	if s.cfg != nil {
		dingTalkBase = s.cfg.DingTalk
	}

	if raw, ok := settings[SettingKeyDingTalkConnectEnabled]; ok {
		result.DingTalkConnectEnabled = raw == "true"
	} else {
		result.DingTalkConnectEnabled = dingTalkBase.Enabled
	}

	if v, ok := settings[SettingKeyDingTalkConnectClientID]; ok && strings.TrimSpace(v) != "" {
		result.DingTalkConnectClientID = strings.TrimSpace(v)
	} else {
		result.DingTalkConnectClientID = dingTalkBase.ClientID
	}

	if v, ok := settings[SettingKeyDingTalkConnectRedirectURL]; ok && strings.TrimSpace(v) != "" {
		result.DingTalkConnectRedirectURL = strings.TrimSpace(v)
	} else {
		result.DingTalkConnectRedirectURL = dingTalkBase.RedirectURL
	}

	result.DingTalkConnectClientSecret = strings.TrimSpace(settings[SettingKeyDingTalkConnectClientSecret])
	if result.DingTalkConnectClientSecret == "" {
		result.DingTalkConnectClientSecret = strings.TrimSpace(dingTalkBase.ClientSecret)
	}
	result.DingTalkConnectClientSecretConfigured = result.DingTalkConnectClientSecret != ""

	if v, ok := settings[SettingKeyDingTalkConnectCorpRestrictionPolicy]; ok && strings.TrimSpace(v) != "" {
		result.DingTalkConnectCorpRestrictionPolicy = strings.TrimSpace(v)
	} else {
		result.DingTalkConnectCorpRestrictionPolicy = dingTalkBase.CorpRestrictionPolicy
	}
	result.DingTalkConnectCorpRestrictionPolicy = coerceDeprecatedDingTalkCorpPolicy(result.DingTalkConnectCorpRestrictionPolicy)

	if v, ok := settings[SettingKeyDingTalkConnectInternalCorpID]; ok && strings.TrimSpace(v) != "" {
		result.DingTalkConnectInternalCorpID = strings.TrimSpace(v)
	} else {
		result.DingTalkConnectInternalCorpID = dingTalkBase.InternalCorpID
	}

	if v, ok := settings[SettingKeyDingTalkConnectBypassRegistration]; ok && strings.TrimSpace(v) != "" {
		result.DingTalkConnectBypassRegistration = strings.EqualFold(strings.TrimSpace(v), "true")
	} else {
		result.DingTalkConnectBypassRegistration = dingTalkBase.BypassRegistration
	}
	if result.DingTalkConnectCorpRestrictionPolicy != "internal_only" {
		result.DingTalkConnectBypassRegistration = false
	}

	if v, ok := settings[SettingKeyDingTalkConnectSyncCorpEmail]; ok && strings.TrimSpace(v) != "" {
		result.DingTalkConnectSyncCorpEmail = strings.EqualFold(strings.TrimSpace(v), "true")
	} else {
		result.DingTalkConnectSyncCorpEmail = dingTalkBase.SyncCorpEmail
	}
	if v, ok := settings[SettingKeyDingTalkConnectSyncDisplayName]; ok && strings.TrimSpace(v) != "" {
		result.DingTalkConnectSyncDisplayName = strings.EqualFold(strings.TrimSpace(v), "true")
	} else {
		result.DingTalkConnectSyncDisplayName = dingTalkBase.SyncDisplayName
	}
	if v, ok := settings[SettingKeyDingTalkConnectSyncDept]; ok && strings.TrimSpace(v) != "" {
		result.DingTalkConnectSyncDept = strings.EqualFold(strings.TrimSpace(v), "true")
	} else {
		result.DingTalkConnectSyncDept = dingTalkBase.SyncDept
	}
	if result.DingTalkConnectCorpRestrictionPolicy != "internal_only" {
		result.DingTalkConnectSyncCorpEmail = false
		result.DingTalkConnectSyncDisplayName = false
		result.DingTalkConnectSyncDept = false
	}

	result.DingTalkConnectSyncCorpEmailAttrKey = strings.TrimSpace(settings[SettingKeyDingTalkConnectSyncCorpEmailAttrKey])
	if result.DingTalkConnectSyncCorpEmailAttrKey == "" {
		if v := strings.TrimSpace(dingTalkBase.SyncCorpEmailAttrKey); v != "" {
			result.DingTalkConnectSyncCorpEmailAttrKey = v
		} else {
			result.DingTalkConnectSyncCorpEmailAttrKey = "dingtalk_email"
		}
	}
	result.DingTalkConnectSyncDisplayNameAttrKey = strings.TrimSpace(settings[SettingKeyDingTalkConnectSyncDisplayNameAttrKey])
	if result.DingTalkConnectSyncDisplayNameAttrKey == "" {
		if v := strings.TrimSpace(dingTalkBase.SyncDisplayNameAttrKey); v != "" {
			result.DingTalkConnectSyncDisplayNameAttrKey = v
		} else {
			result.DingTalkConnectSyncDisplayNameAttrKey = "dingtalk_name"
		}
	}
	result.DingTalkConnectSyncDeptAttrKey = strings.TrimSpace(settings[SettingKeyDingTalkConnectSyncDeptAttrKey])
	if result.DingTalkConnectSyncDeptAttrKey == "" {
		if v := strings.TrimSpace(dingTalkBase.SyncDeptAttrKey); v != "" {
			result.DingTalkConnectSyncDeptAttrKey = v
		} else {
			result.DingTalkConnectSyncDeptAttrKey = "dingtalk_department"
		}
	}

	result.DingTalkConnectSyncCorpEmailAttrName = strings.TrimSpace(settings[SettingKeyDingTalkConnectSyncCorpEmailAttrName])
	if result.DingTalkConnectSyncCorpEmailAttrName == "" {
		if v := strings.TrimSpace(dingTalkBase.SyncCorpEmailAttrName); v != "" {
			result.DingTalkConnectSyncCorpEmailAttrName = v
		} else {
			result.DingTalkConnectSyncCorpEmailAttrName = "钉钉企业邮箱"
		}
	}
	result.DingTalkConnectSyncDisplayNameAttrName = strings.TrimSpace(settings[SettingKeyDingTalkConnectSyncDisplayNameAttrName])
	if result.DingTalkConnectSyncDisplayNameAttrName == "" {
		if v := strings.TrimSpace(dingTalkBase.SyncDisplayNameAttrName); v != "" {
			result.DingTalkConnectSyncDisplayNameAttrName = v
		} else {
			result.DingTalkConnectSyncDisplayNameAttrName = "钉钉姓名"
		}
	}
	result.DingTalkConnectSyncDeptAttrName = strings.TrimSpace(settings[SettingKeyDingTalkConnectSyncDeptAttrName])
	if result.DingTalkConnectSyncDeptAttrName == "" {
		if v := strings.TrimSpace(dingTalkBase.SyncDeptAttrName); v != "" {
			result.DingTalkConnectSyncDeptAttrName = v
		} else {
			result.DingTalkConnectSyncDeptAttrName = "钉钉部门"
		}
	}

	// WeChat Connect 设置：
	// - 兼容 config.yaml/env 的兜底值
	// - 兼容旧版单 app_id/app_secret 字段
	// - mode/scopes/frontend_redirect 提供稳定默认值，避免接口返回空值
	weChatCfg := s.buildWeChatConnectOAuthConfig(settings)
	result.WeChatConnectEnabled = weChatCfg.Enabled
	result.WeChatConnectMode = weChatCfg.Mode
	result.WeChatConnectOpenEnabled = weChatCfg.OpenEnabled
	result.WeChatConnectMPEnabled = weChatCfg.MPEnabled
	result.WeChatConnectMobileEnabled = weChatCfg.MobileEnabled
	result.WeChatConnectAppID = weChatCfg.LegacyAppID
	result.WeChatConnectAppSecret = weChatCfg.LegacyAppSecret
	result.WeChatConnectAppSecretConfigured = weChatCfg.LegacyAppSecret != ""
	result.WeChatConnectOpenAppID = weChatCfg.OpenAppID
	result.WeChatConnectOpenAppSecret = weChatCfg.OpenAppSecret
	result.WeChatConnectOpenAppSecretConfigured = weChatCfg.OpenAppSecret != ""
	result.WeChatConnectMPAppID = weChatCfg.MPAppID
	result.WeChatConnectMPAppSecret = weChatCfg.MPAppSecret
	result.WeChatConnectMPAppSecretConfigured = weChatCfg.MPAppSecret != ""
	result.WeChatConnectMobileAppID = weChatCfg.MobileAppID
	result.WeChatConnectMobileAppSecret = weChatCfg.MobileAppSecret
	result.WeChatConnectMobileAppSecretConfigured = weChatCfg.MobileAppSecret != ""
	result.WeChatConnectScopes = weChatCfg.Scopes
	result.WeChatConnectRedirectURL = weChatCfg.RedirectURL
	result.WeChatConnectFrontendRedirectURL = weChatCfg.FrontendRedirectURL

	gitHubOAuthCfg := s.effectiveEmailOAuthConfig(settings, "github")
	result.GitHubOAuthEnabled = gitHubOAuthCfg.Enabled
	result.GitHubOAuthClientID = strings.TrimSpace(gitHubOAuthCfg.ClientID)
	result.GitHubOAuthClientSecret = strings.TrimSpace(gitHubOAuthCfg.ClientSecret)
	result.GitHubOAuthClientSecretConfigured = result.GitHubOAuthClientSecret != ""
	result.GitHubOAuthRedirectURL = strings.TrimSpace(gitHubOAuthCfg.RedirectURL)
	result.GitHubOAuthFrontendRedirectURL = strings.TrimSpace(gitHubOAuthCfg.FrontendRedirectURL)

	googleOAuthCfg := s.effectiveEmailOAuthConfig(settings, "google")
	result.GoogleOAuthEnabled = googleOAuthCfg.Enabled
	result.GoogleOAuthClientID = strings.TrimSpace(googleOAuthCfg.ClientID)
	result.GoogleOAuthClientSecret = strings.TrimSpace(googleOAuthCfg.ClientSecret)
	result.GoogleOAuthClientSecretConfigured = result.GoogleOAuthClientSecret != ""
	result.GoogleOAuthRedirectURL = strings.TrimSpace(googleOAuthCfg.RedirectURL)
	result.GoogleOAuthFrontendRedirectURL = strings.TrimSpace(googleOAuthCfg.FrontendRedirectURL)

	// Generic OIDC 设置：
	// - 兼容 config.yaml/env
	// - 支持后台系统设置覆盖并持久化（存储于 DB）
	oidcBase := config.OIDCConnectConfig{}
	if s.cfg != nil {
		oidcBase = s.cfg.OIDC
	}

	if raw, ok := settings[SettingKeyOIDCConnectEnabled]; ok {
		result.OIDCConnectEnabled = raw == "true"
	} else {
		result.OIDCConnectEnabled = oidcBase.Enabled
	}

	if v, ok := settings[SettingKeyOIDCConnectProviderName]; ok && strings.TrimSpace(v) != "" {
		result.OIDCConnectProviderName = strings.TrimSpace(v)
	} else {
		result.OIDCConnectProviderName = strings.TrimSpace(oidcBase.ProviderName)
	}
	if result.OIDCConnectProviderName == "" {
		result.OIDCConnectProviderName = "OIDC"
	}

	if v, ok := settings[SettingKeyOIDCConnectClientID]; ok && strings.TrimSpace(v) != "" {
		result.OIDCConnectClientID = strings.TrimSpace(v)
	} else {
		result.OIDCConnectClientID = strings.TrimSpace(oidcBase.ClientID)
	}
	if v, ok := settings[SettingKeyOIDCConnectIssuerURL]; ok && strings.TrimSpace(v) != "" {
		result.OIDCConnectIssuerURL = strings.TrimSpace(v)
	} else {
		result.OIDCConnectIssuerURL = strings.TrimSpace(oidcBase.IssuerURL)
	}
	if v, ok := settings[SettingKeyOIDCConnectDiscoveryURL]; ok && strings.TrimSpace(v) != "" {
		result.OIDCConnectDiscoveryURL = strings.TrimSpace(v)
	} else {
		result.OIDCConnectDiscoveryURL = strings.TrimSpace(oidcBase.DiscoveryURL)
	}
	if v, ok := settings[SettingKeyOIDCConnectAuthorizeURL]; ok && strings.TrimSpace(v) != "" {
		result.OIDCConnectAuthorizeURL = strings.TrimSpace(v)
	} else {
		result.OIDCConnectAuthorizeURL = strings.TrimSpace(oidcBase.AuthorizeURL)
	}
	if v, ok := settings[SettingKeyOIDCConnectTokenURL]; ok && strings.TrimSpace(v) != "" {
		result.OIDCConnectTokenURL = strings.TrimSpace(v)
	} else {
		result.OIDCConnectTokenURL = strings.TrimSpace(oidcBase.TokenURL)
	}
	if v, ok := settings[SettingKeyOIDCConnectUserInfoURL]; ok && strings.TrimSpace(v) != "" {
		result.OIDCConnectUserInfoURL = strings.TrimSpace(v)
	} else {
		result.OIDCConnectUserInfoURL = strings.TrimSpace(oidcBase.UserInfoURL)
	}
	if v, ok := settings[SettingKeyOIDCConnectJWKSURL]; ok && strings.TrimSpace(v) != "" {
		result.OIDCConnectJWKSURL = strings.TrimSpace(v)
	} else {
		result.OIDCConnectJWKSURL = strings.TrimSpace(oidcBase.JWKSURL)
	}
	if v, ok := settings[SettingKeyOIDCConnectScopes]; ok && strings.TrimSpace(v) != "" {
		result.OIDCConnectScopes = strings.TrimSpace(v)
	} else {
		result.OIDCConnectScopes = strings.TrimSpace(oidcBase.Scopes)
	}
	if v, ok := settings[SettingKeyOIDCConnectRedirectURL]; ok && strings.TrimSpace(v) != "" {
		result.OIDCConnectRedirectURL = strings.TrimSpace(v)
	} else {
		result.OIDCConnectRedirectURL = strings.TrimSpace(oidcBase.RedirectURL)
	}
	if v, ok := settings[SettingKeyOIDCConnectFrontendRedirectURL]; ok && strings.TrimSpace(v) != "" {
		result.OIDCConnectFrontendRedirectURL = strings.TrimSpace(v)
	} else {
		result.OIDCConnectFrontendRedirectURL = strings.TrimSpace(oidcBase.FrontendRedirectURL)
	}
	if v, ok := settings[SettingKeyOIDCConnectTokenAuthMethod]; ok && strings.TrimSpace(v) != "" {
		result.OIDCConnectTokenAuthMethod = strings.ToLower(strings.TrimSpace(v))
	} else {
		result.OIDCConnectTokenAuthMethod = strings.ToLower(strings.TrimSpace(oidcBase.TokenAuthMethod))
	}
	if raw, ok := settings[SettingKeyOIDCConnectUsePKCE]; ok {
		result.OIDCConnectUsePKCE = raw == "true"
	} else {
		result.OIDCConnectUsePKCE = oidcBase.UsePKCE
	}
	if raw, ok := settings[SettingKeyOIDCConnectValidateIDToken]; ok {
		result.OIDCConnectValidateIDToken = raw == "true"
	} else {
		result.OIDCConnectValidateIDToken = oidcBase.ValidateIDToken
	}
	if v, ok := settings[SettingKeyOIDCConnectAllowedSigningAlgs]; ok && strings.TrimSpace(v) != "" {
		result.OIDCConnectAllowedSigningAlgs = strings.TrimSpace(v)
	} else {
		result.OIDCConnectAllowedSigningAlgs = strings.TrimSpace(oidcBase.AllowedSigningAlgs)
	}
	clockSkewSet := false
	if raw, ok := settings[SettingKeyOIDCConnectClockSkewSeconds]; ok && strings.TrimSpace(raw) != "" {
		if parsed, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
			result.OIDCConnectClockSkewSeconds = parsed
			clockSkewSet = true
		}
	}
	if !clockSkewSet {
		result.OIDCConnectClockSkewSeconds = oidcBase.ClockSkewSeconds
	}
	if !clockSkewSet && result.OIDCConnectClockSkewSeconds == 0 {
		result.OIDCConnectClockSkewSeconds = 120
	}
	if raw, ok := settings[SettingKeyOIDCConnectRequireEmailVerified]; ok {
		result.OIDCConnectRequireEmailVerified = raw == "true"
	} else {
		result.OIDCConnectRequireEmailVerified = oidcBase.RequireEmailVerified
	}
	if v, ok := settings[SettingKeyOIDCConnectUserInfoEmailPath]; ok {
		result.OIDCConnectUserInfoEmailPath = strings.TrimSpace(v)
	} else {
		result.OIDCConnectUserInfoEmailPath = strings.TrimSpace(oidcBase.UserInfoEmailPath)
	}
	if v, ok := settings[SettingKeyOIDCConnectUserInfoIDPath]; ok {
		result.OIDCConnectUserInfoIDPath = strings.TrimSpace(v)
	} else {
		result.OIDCConnectUserInfoIDPath = strings.TrimSpace(oidcBase.UserInfoIDPath)
	}
	if v, ok := settings[SettingKeyOIDCConnectUserInfoUsernamePath]; ok {
		result.OIDCConnectUserInfoUsernamePath = strings.TrimSpace(v)
	} else {
		result.OIDCConnectUserInfoUsernamePath = strings.TrimSpace(oidcBase.UserInfoUsernamePath)
	}
	result.OIDCConnectClientSecret = strings.TrimSpace(settings[SettingKeyOIDCConnectClientSecret])
	if result.OIDCConnectClientSecret == "" {
		result.OIDCConnectClientSecret = strings.TrimSpace(oidcBase.ClientSecret)
	}
	result.OIDCConnectClientSecretConfigured = result.OIDCConnectClientSecret != ""

	// Model fallback settings
	result.EnableModelFallback = settings[SettingKeyEnableModelFallback] == "true"
	result.FallbackModelAnthropic = s.getStringOrDefault(settings, SettingKeyFallbackModelAnthropic, "claude-3-5-sonnet-20241022")
	result.FallbackModelOpenAI = s.getStringOrDefault(settings, SettingKeyFallbackModelOpenAI, "gpt-4o")
	result.FallbackModelGemini = s.getStringOrDefault(settings, SettingKeyFallbackModelGemini, "gemini-2.5-pro")

	// Identity patch settings (default: enabled, to preserve existing behavior)
	if v, ok := settings[SettingKeyEnableIdentityPatch]; ok && v != "" {
		result.EnableIdentityPatch = v == "true"
	} else {
		result.EnableIdentityPatch = true
	}
	result.IdentityPatchPrompt = settings[SettingKeyIdentityPatchPrompt]

	// Ops monitoring settings (default: enabled, fail-open)
	result.OpsMonitoringEnabled = !isFalseSettingValue(settings[SettingKeyOpsMonitoringEnabled])
	result.OpsRealtimeMonitoringEnabled = !isFalseSettingValue(settings[SettingKeyOpsRealtimeMonitoringEnabled])
	result.OpsQueryModeDefault = string(ParseOpsQueryMode(settings[SettingKeyOpsQueryModeDefault]))
	result.OpsMetricsIntervalSeconds = 60
	if raw := strings.TrimSpace(settings[SettingKeyOpsMetricsIntervalSeconds]); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil {
			if v < 60 {
				v = 60
			}
			if v > 3600 {
				v = 3600
			}
			result.OpsMetricsIntervalSeconds = v
		}
	}

	// Claude Code version check
	result.MinClaudeCodeVersion = settings[SettingKeyMinClaudeCodeVersion]
	result.MaxClaudeCodeVersion = settings[SettingKeyMaxClaudeCodeVersion]

	// 分组隔离
	result.AllowUngroupedKeyScheduling = settings[SettingKeyAllowUngroupedKeyScheduling] == "true"

	// Gateway forwarding behavior (defaults: fingerprint=true, metadata_passthrough=false, cch_signing=false)
	if v, ok := settings[SettingKeyEnableFingerprintUnification]; ok && v != "" {
		result.EnableFingerprintUnification = v == "true"
	} else {
		result.EnableFingerprintUnification = true // default: enabled (current behavior)
	}
	result.EnableMetadataPassthrough = settings[SettingKeyEnableMetadataPassthrough] == "true"
	result.DefaultUpstreamUserAgent = strings.TrimSpace(settings[SettingKeyDefaultUpstreamUserAgent])
	result.ForceUnifiedUpstreamUserAgent = settings[SettingKeyForceUnifiedUpstreamUserAgent] == "true"
	result.UpdateGitHubRepo = strings.TrimSpace(settings[SettingKeyUpdateGitHubRepo])
	result.EnableCCHSigning = settings[SettingKeyEnableCCHSigning] == "true"
	result.AntigravityUserAgentVersion = normalizeAntigravityUserAgentVersion(settings[SettingKeyAntigravityUserAgentVersion])

	// Web search emulation: quick enabled check from the JSON config
	if raw := settings[SettingKeyWebSearchEmulationConfig]; raw != "" {
		var wsCfg WebSearchEmulationConfig
		if err := json.Unmarshal([]byte(raw), &wsCfg); err == nil {
			result.WebSearchEmulationEnabled = wsCfg.Enabled && len(wsCfg.Providers) > 0
		}
	}
	result.PaymentVisibleMethodAlipaySource = NormalizeVisibleMethodSource("alipay", settings[SettingPaymentVisibleMethodAlipaySource])
	result.PaymentVisibleMethodWxpaySource = NormalizeVisibleMethodSource("wxpay", settings[SettingPaymentVisibleMethodWxpaySource])
	result.PaymentVisibleMethodAlipayEnabled = settings[SettingPaymentVisibleMethodAlipayEnabled] == "true"
	result.PaymentVisibleMethodWxpayEnabled = settings[SettingPaymentVisibleMethodWxpayEnabled] == "true"
	result.OpenAIAdvancedSchedulerEnabled = settings[openAIAdvancedSchedulerSettingKey] == "true"

	// Balance low notification
	result.BalanceLowNotifyEnabled = settings[SettingKeyBalanceLowNotifyEnabled] == "true"
	if v, err := strconv.ParseFloat(settings[SettingKeyBalanceLowNotifyThreshold], 64); err == nil && v >= 0 {
		result.BalanceLowNotifyThreshold = v
	}
	result.BalanceLowNotifyRechargeURL = settings[SettingKeyBalanceLowNotifyRechargeURL]

	// Account quota notification
	result.AccountQuotaNotifyEnabled = settings[SettingKeyAccountQuotaNotifyEnabled] == "true"
	if raw := strings.TrimSpace(settings[SettingKeyAccountQuotaNotifyEmails]); raw != "" {
		result.AccountQuotaNotifyEmails = ParseNotifyEmails(raw)
	}
	if result.AccountQuotaNotifyEmails == nil {
		result.AccountQuotaNotifyEmails = []NotifyEmailEntry{}
	}
	result.ChannelMonitorEnabled = !isFalseSettingValue(settings[SettingKeyChannelMonitorEnabled])
	result.ChannelMonitorDefaultIntervalSeconds = parseChannelMonitorInterval(settings[SettingKeyChannelMonitorDefaultIntervalSeconds])
	result.AvailableChannelsEnabled = settings[SettingKeyAvailableChannelsEnabled] == "true"
	result.RiskControlEnabled = settings[SettingKeyRiskControlEnabled] == "true"

	return result
}

func clampAffiliateRebateRate(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return AffiliateRebateRateDefault
	}
	if value < AffiliateRebateRateMin {
		return AffiliateRebateRateMin
	}
	if value > AffiliateRebateRateMax {
		return AffiliateRebateRateMax
	}
	return value
}

func isFalseSettingValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "false", "0", "off", "disabled":
		return true
	default:
		return false
	}
}

func normalizeVisibleMethodSettingSource(method, source string, enabled bool) (string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		// 兼容历史配置：旧版本可能在 visible method 启用时留下空 source。
		// 这里保留空值，避免仅更新无关设置时触发 400 并阻断保存。
		return "", nil
	}

	normalized := NormalizeVisibleMethodSource(method, source)
	if normalized == "" {
		return "", infraerrors.BadRequest(
			"INVALID_PAYMENT_VISIBLE_METHOD_SOURCE",
			fmt.Sprintf("%s source must be one of the supported payment providers", method),
		)
	}

	return normalized, nil
}

func normalizeWeChatConnectCapabilityFlags(enabled bool, mode string, openEnabled, mpEnabled, mobileEnabled bool) (bool, bool, bool) {
	mode = normalizeWeChatConnectModeSetting(mode)
	if enabled && !openEnabled && !mpEnabled && !mobileEnabled {
		switch mode {
		case "mp":
			mpEnabled = true
		case "mobile":
			mobileEnabled = true
		default:
			openEnabled = true
		}
	}
	return openEnabled, mpEnabled, mobileEnabled
}

func parseDefaultSubscriptions(raw string) []DefaultSubscriptionSetting {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	var items []DefaultSubscriptionSetting
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil
	}

	normalized := make([]DefaultSubscriptionSetting, 0, len(items))
	for _, item := range items {
		if item.GroupID <= 0 || item.ValidityDays <= 0 {
			continue
		}
		if item.ValidityDays > MaxValidityDays {
			item.ValidityDays = MaxValidityDays
		}
		normalized = append(normalized, item)
	}

	return normalized
}

func parseTablePreferences(defaultPageSizeRaw, optionsRaw string) (int, []int) {
	defaultPageSize := 20
	if v, err := strconv.Atoi(strings.TrimSpace(defaultPageSizeRaw)); err == nil {
		defaultPageSize = v
	}

	var options []int
	if strings.TrimSpace(optionsRaw) != "" {
		_ = json.Unmarshal([]byte(optionsRaw), &options)
	}

	return normalizeTablePreferences(defaultPageSize, options)
}

func normalizeTablePreferences(defaultPageSize int, options []int) (int, []int) {
	const minPageSize = 5
	const maxPageSize = 1000
	const fallbackPageSize = 20

	seen := make(map[int]struct{}, len(options))
	normalizedOptions := make([]int, 0, len(options))
	for _, option := range options {
		if option < minPageSize || option > maxPageSize {
			continue
		}
		if _, ok := seen[option]; ok {
			continue
		}
		seen[option] = struct{}{}
		normalizedOptions = append(normalizedOptions, option)
	}
	sort.Ints(normalizedOptions)

	if defaultPageSize < minPageSize || defaultPageSize > maxPageSize {
		defaultPageSize = fallbackPageSize
	}

	if len(normalizedOptions) == 0 {
		normalizedOptions = []int{10, 20, 50}
	}

	return defaultPageSize, normalizedOptions
}

// getStringOrDefault 获取字符串值或默认值
func (s *SettingService) getStringOrDefault(settings map[string]string, key, defaultValue string) string {
	if value, ok := settings[key]; ok && value != "" {
		return value
	}
	return defaultValue
}

// IsTurnstileEnabled 检查是否启用 Turnstile 验证
func (s *SettingService) IsTurnstileEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyTurnstileEnabled)
	if err != nil {
		return false
	}
	return value == "true"
}

// GetTurnstileSecretKey 获取 Turnstile Secret Key
func (s *SettingService) GetTurnstileSecretKey(ctx context.Context) string {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyTurnstileSecretKey)
	if err != nil {
		return ""
	}
	return value
}

// IsIdentityPatchEnabled 检查是否启用身份补丁（Claude -> Gemini systemInstruction 注入）
func (s *SettingService) IsIdentityPatchEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyEnableIdentityPatch)
	if err != nil {
		// 默认开启，保持兼容
		return true
	}
	return value == "true"
}

// GetIdentityPatchPrompt 获取自定义身份补丁提示词（为空表示使用内置默认模板）
func (s *SettingService) GetIdentityPatchPrompt(ctx context.Context) string {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyIdentityPatchPrompt)
	if err != nil {
		return ""
	}
	return value
}

// GenerateAdminAPIKey 生成新的管理员 API Key
func (s *SettingService) GenerateAdminAPIKey(ctx context.Context) (string, error) {
	// 生成 32 字节随机数 = 64 位十六进制字符
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}

	key := AdminAPIKeyPrefix + hex.EncodeToString(bytes)

	// 存储到 settings 表
	if err := s.settingRepo.Set(ctx, SettingKeyAdminAPIKey, key); err != nil {
		return "", fmt.Errorf("save admin api key: %w", err)
	}

	return key, nil
}

// GetAdminAPIKeyStatus 获取管理员 API Key 状态
// 返回脱敏的 key、是否存在、错误
func (s *SettingService) GetAdminAPIKeyStatus(ctx context.Context) (maskedKey string, exists bool, err error) {
	key, err := s.settingRepo.GetValue(ctx, SettingKeyAdminAPIKey)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	if key == "" {
		return "", false, nil
	}

	// 脱敏：显示前 10 位和后 4 位
	if len(key) > 14 {
		maskedKey = key[:10] + "..." + key[len(key)-4:]
	} else {
		maskedKey = key
	}

	return maskedKey, true, nil
}

// GetAdminAPIKey 获取完整的管理员 API Key（仅供内部验证使用）
// 如果未配置返回空字符串和 nil 错误，只有数据库错误时才返回 error
func (s *SettingService) GetAdminAPIKey(ctx context.Context) (string, error) {
	key, err := s.settingRepo.GetValue(ctx, SettingKeyAdminAPIKey)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return "", nil // 未配置，返回空字符串
		}
		return "", err // 数据库错误
	}
	return key, nil
}

// DeleteAdminAPIKey 删除管理员 API Key
func (s *SettingService) DeleteAdminAPIKey(ctx context.Context) error {
	return s.settingRepo.Delete(ctx, SettingKeyAdminAPIKey)
}

// IsModelFallbackEnabled 检查是否启用模型兜底机制
func (s *SettingService) IsModelFallbackEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyEnableModelFallback)
	if err != nil {
		return false // Default: disabled
	}
	return value == "true"
}

// GetFallbackModel 获取指定平台的兜底模型
func (s *SettingService) GetFallbackModel(ctx context.Context, platform string) string {
	var key string
	var defaultModel string

	switch platform {
	case PlatformAnthropic:
		key = SettingKeyFallbackModelAnthropic
		defaultModel = "claude-3-5-sonnet-20241022"
	case PlatformOpenAI:
		key = SettingKeyFallbackModelOpenAI
		defaultModel = "gpt-4o"
	case PlatformGemini:
		key = SettingKeyFallbackModelGemini
		defaultModel = "gemini-2.5-pro"
	default:
		return ""
	}

	value, err := s.settingRepo.GetValue(ctx, key)
	if err != nil || value == "" {
		return defaultModel
	}
	return value
}

// GetLinuxDoConnectOAuthConfig 返回用于登录的"最终生效" LinuxDo Connect 配置。
//
// 优先级：
// - 若对应系统设置键存在，则覆盖 config.yaml/env 的值
// - 否则回退到 config.yaml/env 的值
func (s *SettingService) GetLinuxDoConnectOAuthConfig(ctx context.Context) (config.LinuxDoConnectConfig, error) {
	if s == nil || s.cfg == nil {
		return config.LinuxDoConnectConfig{}, infraerrors.ServiceUnavailable("CONFIG_NOT_READY", "config not loaded")
	}

	effective := s.cfg.LinuxDo

	keys := []string{
		SettingKeyLinuxDoConnectEnabled,
		SettingKeyLinuxDoConnectClientID,
		SettingKeyLinuxDoConnectClientSecret,
		SettingKeyLinuxDoConnectRedirectURL,
	}
	settings, err := s.settingRepo.GetMultiple(ctx, keys)
	if err != nil {
		return config.LinuxDoConnectConfig{}, fmt.Errorf("get linuxdo connect settings: %w", err)
	}

	if raw, ok := settings[SettingKeyLinuxDoConnectEnabled]; ok {
		effective.Enabled = raw == "true"
	}
	if v, ok := settings[SettingKeyLinuxDoConnectClientID]; ok && strings.TrimSpace(v) != "" {
		effective.ClientID = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyLinuxDoConnectClientSecret]; ok && strings.TrimSpace(v) != "" {
		effective.ClientSecret = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyLinuxDoConnectRedirectURL]; ok && strings.TrimSpace(v) != "" {
		effective.RedirectURL = strings.TrimSpace(v)
	}

	if !effective.Enabled {
		return config.LinuxDoConnectConfig{}, infraerrors.NotFound("OAUTH_DISABLED", "oauth login is disabled")
	}

	// 基础健壮性校验（避免把用户重定向到一个必然失败或不安全的 OAuth 流程里）。
	if strings.TrimSpace(effective.ClientID) == "" {
		return config.LinuxDoConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth client id not configured")
	}
	if strings.TrimSpace(effective.AuthorizeURL) == "" {
		return config.LinuxDoConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth authorize url not configured")
	}
	if strings.TrimSpace(effective.TokenURL) == "" {
		return config.LinuxDoConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth token url not configured")
	}
	if strings.TrimSpace(effective.UserInfoURL) == "" {
		return config.LinuxDoConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth userinfo url not configured")
	}
	if strings.TrimSpace(effective.RedirectURL) == "" {
		return config.LinuxDoConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth redirect url not configured")
	}
	if strings.TrimSpace(effective.FrontendRedirectURL) == "" {
		return config.LinuxDoConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth frontend redirect url not configured")
	}

	if err := config.ValidateAbsoluteHTTPURL(effective.AuthorizeURL); err != nil {
		return config.LinuxDoConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth authorize url invalid")
	}
	if err := config.ValidateAbsoluteHTTPURL(effective.TokenURL); err != nil {
		return config.LinuxDoConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth token url invalid")
	}
	if err := config.ValidateAbsoluteHTTPURL(effective.UserInfoURL); err != nil {
		return config.LinuxDoConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth userinfo url invalid")
	}
	if err := config.ValidateAbsoluteHTTPURL(effective.RedirectURL); err != nil {
		return config.LinuxDoConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth redirect url invalid")
	}
	if err := config.ValidateFrontendRedirectURL(effective.FrontendRedirectURL); err != nil {
		return config.LinuxDoConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth frontend redirect url invalid")
	}

	method := strings.ToLower(strings.TrimSpace(effective.TokenAuthMethod))
	switch method {
	case "", "client_secret_post", "client_secret_basic":
		if strings.TrimSpace(effective.ClientSecret) == "" {
			return config.LinuxDoConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth client secret not configured")
		}
	case "none":
		if !effective.UsePKCE {
			return config.LinuxDoConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth pkce must be enabled when token_auth_method=none")
		}
	default:
		return config.LinuxDoConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth token_auth_method invalid")
	}

	return effective, nil
}

// GetDingTalkConnectOAuthConfig 返回用于登录的"最终生效" DingTalk Connect 配置。
//
// 优先级：
// - 若对应系统设置键存在，则覆盖 config.yaml/env 的值
// - 否则回退到 config.yaml/env 的值
func (s *SettingService) GetDingTalkConnectOAuthConfig(ctx context.Context) (config.DingTalkConnectConfig, error) {
	if s == nil || s.cfg == nil {
		return config.DingTalkConnectConfig{}, infraerrors.ServiceUnavailable("CONFIG_NOT_READY", "config not loaded")
	}

	effective := s.cfg.DingTalk

	keys := []string{
		SettingKeyDingTalkConnectEnabled,
		SettingKeyDingTalkConnectClientID,
		SettingKeyDingTalkConnectClientSecret,
		SettingKeyDingTalkConnectRedirectURL,
		SettingKeyDingTalkConnectCorpRestrictionPolicy,
		SettingKeyDingTalkConnectInternalCorpID,
		SettingKeyDingTalkConnectBypassRegistration,
		SettingKeyDingTalkConnectSyncCorpEmail,
		SettingKeyDingTalkConnectSyncDisplayName,
		SettingKeyDingTalkConnectSyncDept,
		SettingKeyDingTalkConnectSyncCorpEmailAttrKey,
		SettingKeyDingTalkConnectSyncDisplayNameAttrKey,
		SettingKeyDingTalkConnectSyncDeptAttrKey,
	}
	settings, err := s.settingRepo.GetMultiple(ctx, keys)
	if err != nil {
		return config.DingTalkConnectConfig{}, fmt.Errorf("get dingtalk connect settings: %w", err)
	}

	if raw, ok := settings[SettingKeyDingTalkConnectEnabled]; ok {
		effective.Enabled = raw == "true"
	}
	if v, ok := settings[SettingKeyDingTalkConnectClientID]; ok && strings.TrimSpace(v) != "" {
		effective.ClientID = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyDingTalkConnectClientSecret]; ok && strings.TrimSpace(v) != "" {
		effective.ClientSecret = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyDingTalkConnectRedirectURL]; ok && strings.TrimSpace(v) != "" {
		effective.RedirectURL = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyDingTalkConnectCorpRestrictionPolicy]; ok && strings.TrimSpace(v) != "" {
		effective.CorpRestrictionPolicy = strings.TrimSpace(v)
	}
	effective.CorpRestrictionPolicy = coerceDeprecatedDingTalkCorpPolicy(effective.CorpRestrictionPolicy)
	if v, ok := settings[SettingKeyDingTalkConnectInternalCorpID]; ok && strings.TrimSpace(v) != "" {
		effective.InternalCorpID = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyDingTalkConnectBypassRegistration]; ok && strings.TrimSpace(v) != "" {
		effective.BypassRegistration = strings.EqualFold(strings.TrimSpace(v), "true")
	}
	if effective.CorpRestrictionPolicy != "internal_only" {
		effective.BypassRegistration = false
	}
	if v, ok := settings[SettingKeyDingTalkConnectSyncCorpEmail]; ok && strings.TrimSpace(v) != "" {
		effective.SyncCorpEmail = strings.EqualFold(strings.TrimSpace(v), "true")
	}
	if v, ok := settings[SettingKeyDingTalkConnectSyncDisplayName]; ok && strings.TrimSpace(v) != "" {
		effective.SyncDisplayName = strings.EqualFold(strings.TrimSpace(v), "true")
	}
	if v, ok := settings[SettingKeyDingTalkConnectSyncDept]; ok && strings.TrimSpace(v) != "" {
		effective.SyncDept = strings.EqualFold(strings.TrimSpace(v), "true")
	}
	if effective.CorpRestrictionPolicy != "internal_only" {
		effective.SyncCorpEmail = false
		effective.SyncDisplayName = false
		effective.SyncDept = false
	}
	if v := strings.TrimSpace(settings[SettingKeyDingTalkConnectSyncCorpEmailAttrKey]); v != "" {
		effective.SyncCorpEmailAttrKey = v
	}
	if effective.SyncCorpEmailAttrKey == "" {
		effective.SyncCorpEmailAttrKey = "dingtalk_email"
	}
	if v := strings.TrimSpace(settings[SettingKeyDingTalkConnectSyncDisplayNameAttrKey]); v != "" {
		effective.SyncDisplayNameAttrKey = v
	}
	if effective.SyncDisplayNameAttrKey == "" {
		effective.SyncDisplayNameAttrKey = "dingtalk_name"
	}
	if v := strings.TrimSpace(settings[SettingKeyDingTalkConnectSyncDeptAttrKey]); v != "" {
		effective.SyncDeptAttrKey = v
	}
	if effective.SyncDeptAttrKey == "" {
		effective.SyncDeptAttrKey = "dingtalk_department"
	}

	if !effective.Enabled {
		return config.DingTalkConnectConfig{}, infraerrors.NotFound("OAUTH_DISABLED", "dingtalk oauth login is disabled")
	}
	if strings.TrimSpace(effective.ClientID) == "" {
		return config.DingTalkConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "dingtalk oauth client id not configured")
	}
	if strings.TrimSpace(effective.AuthorizeURL) == "" {
		return config.DingTalkConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "dingtalk oauth authorize url not configured")
	}
	if strings.TrimSpace(effective.TokenURL) == "" {
		return config.DingTalkConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "dingtalk oauth token url not configured")
	}
	if strings.TrimSpace(effective.UserInfoURL) == "" {
		return config.DingTalkConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "dingtalk oauth userinfo url not configured")
	}
	if strings.TrimSpace(effective.RedirectURL) == "" {
		return config.DingTalkConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "dingtalk oauth redirect url not configured")
	}
	if strings.TrimSpace(effective.FrontendRedirectURL) == "" {
		return config.DingTalkConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "dingtalk oauth frontend redirect url not configured")
	}
	if err := config.ValidateAbsoluteHTTPURL(effective.AuthorizeURL); err != nil {
		return config.DingTalkConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "dingtalk oauth authorize url invalid")
	}
	if err := config.ValidateAbsoluteHTTPURL(effective.TokenURL); err != nil {
		return config.DingTalkConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "dingtalk oauth token url invalid")
	}
	if err := config.ValidateAbsoluteHTTPURL(effective.UserInfoURL); err != nil {
		return config.DingTalkConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "dingtalk oauth userinfo url invalid")
	}
	if err := config.ValidateAbsoluteHTTPURL(effective.RedirectURL); err != nil {
		return config.DingTalkConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "dingtalk oauth redirect url invalid")
	}
	if err := config.ValidateFrontendRedirectURL(effective.FrontendRedirectURL); err != nil {
		return config.DingTalkConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "dingtalk oauth frontend redirect url invalid")
	}
	if strings.TrimSpace(effective.ClientSecret) == "" {
		return config.DingTalkConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "dingtalk oauth client secret not configured")
	}
	if effective.CorpRestrictionPolicy == "internal_only" {
		effective.AppType = "internal"
	}
	if err := config.ValidateDingTalkConfig(effective); err != nil {
		return config.DingTalkConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", err.Error())
	}
	return effective, nil
}

// GetOverloadCooldownSettings 获取529过载冷却配置
func (s *SettingService) GetOverloadCooldownSettings(ctx context.Context) (*OverloadCooldownSettings, error) {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyOverloadCooldownSettings)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return DefaultOverloadCooldownSettings(), nil
		}
		return nil, fmt.Errorf("get overload cooldown settings: %w", err)
	}
	if value == "" {
		return DefaultOverloadCooldownSettings(), nil
	}

	var settings OverloadCooldownSettings
	if err := json.Unmarshal([]byte(value), &settings); err != nil {
		return DefaultOverloadCooldownSettings(), nil
	}

	// 修正配置值范围
	if settings.CooldownMinutes < 1 {
		settings.CooldownMinutes = 1
	}
	if settings.CooldownMinutes > 120 {
		settings.CooldownMinutes = 120
	}

	return &settings, nil
}

// SetOverloadCooldownSettings 设置529过载冷却配置
func (s *SettingService) SetOverloadCooldownSettings(ctx context.Context, settings *OverloadCooldownSettings) error {
	if settings == nil {
		return fmt.Errorf("settings cannot be nil")
	}

	// 禁用时修正为合法值即可，不拒绝请求
	if settings.CooldownMinutes < 1 || settings.CooldownMinutes > 120 {
		if settings.Enabled {
			return fmt.Errorf("cooldown_minutes must be between 1-120")
		}
		settings.CooldownMinutes = 10 // 禁用状态下归一化为默认值
	}

	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal overload cooldown settings: %w", err)
	}

	return s.settingRepo.Set(ctx, SettingKeyOverloadCooldownSettings, string(data))
}

// GetRateLimit429CooldownSettings 获取 429 默认回避配置。
func (s *SettingService) GetRateLimit429CooldownSettings(ctx context.Context) (*RateLimit429CooldownSettings, error) {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyRateLimit429CooldownSettings)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return DefaultRateLimit429CooldownSettings(), nil
		}
		return nil, fmt.Errorf("get rate_limit_429 cooldown settings: %w", err)
	}
	if strings.TrimSpace(value) == "" {
		return DefaultRateLimit429CooldownSettings(), nil
	}

	var settings RateLimit429CooldownSettings
	if err := json.Unmarshal([]byte(value), &settings); err != nil {
		return DefaultRateLimit429CooldownSettings(), nil
	}

	// 启用状态允许 [1,7200]；禁用状态允许任意输入并回填到默认值。
	if settings.CooldownSeconds < 1 || settings.CooldownSeconds > 7200 {
		if settings.Enabled {
			return nil, fmt.Errorf("cooldown_seconds must be between 1-7200")
		}
		settings.CooldownSeconds = DefaultRateLimit429CooldownSettings().CooldownSeconds
	}
	return &settings, nil
}

// SetRateLimit429CooldownSettings 设置 429 默认回避配置。
func (s *SettingService) SetRateLimit429CooldownSettings(ctx context.Context, settings *RateLimit429CooldownSettings) error {
	if settings == nil {
		return fmt.Errorf("settings cannot be nil")
	}
	if settings.CooldownSeconds < 1 || settings.CooldownSeconds > 7200 {
		if settings.Enabled {
			return fmt.Errorf("cooldown_seconds must be between 1-7200")
		}
		settings.CooldownSeconds = DefaultRateLimit429CooldownSettings().CooldownSeconds
	}

	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal rate_limit_429 cooldown settings: %w", err)
	}
	return s.settingRepo.Set(ctx, SettingKeyRateLimit429CooldownSettings, string(data))
}

// GetOIDCConnectOAuthConfig 返回用于登录的“最终生效” OIDC 配置。
//
// 优先级：
// - 若对应系统设置键存在，则覆盖 config.yaml/env 的值
// - 否则回退到 config.yaml/env 的值
func (s *SettingService) GetOIDCConnectOAuthConfig(ctx context.Context) (config.OIDCConnectConfig, error) {
	if s == nil || s.cfg == nil {
		return config.OIDCConnectConfig{}, infraerrors.ServiceUnavailable("CONFIG_NOT_READY", "config not loaded")
	}

	effective := s.cfg.OIDC

	keys := []string{
		SettingKeyOIDCConnectEnabled,
		SettingKeyOIDCConnectProviderName,
		SettingKeyOIDCConnectClientID,
		SettingKeyOIDCConnectClientSecret,
		SettingKeyOIDCConnectIssuerURL,
		SettingKeyOIDCConnectDiscoveryURL,
		SettingKeyOIDCConnectAuthorizeURL,
		SettingKeyOIDCConnectTokenURL,
		SettingKeyOIDCConnectUserInfoURL,
		SettingKeyOIDCConnectJWKSURL,
		SettingKeyOIDCConnectScopes,
		SettingKeyOIDCConnectRedirectURL,
		SettingKeyOIDCConnectFrontendRedirectURL,
		SettingKeyOIDCConnectTokenAuthMethod,
		SettingKeyOIDCConnectUsePKCE,
		SettingKeyOIDCConnectValidateIDToken,
		SettingKeyOIDCConnectAllowedSigningAlgs,
		SettingKeyOIDCConnectClockSkewSeconds,
		SettingKeyOIDCConnectRequireEmailVerified,
		SettingKeyOIDCConnectUserInfoEmailPath,
		SettingKeyOIDCConnectUserInfoIDPath,
		SettingKeyOIDCConnectUserInfoUsernamePath,
	}
	settings, err := s.settingRepo.GetMultiple(ctx, keys)
	if err != nil {
		return config.OIDCConnectConfig{}, fmt.Errorf("get oidc connect settings: %w", err)
	}

	if raw, ok := settings[SettingKeyOIDCConnectEnabled]; ok {
		effective.Enabled = raw == "true"
	}
	if v, ok := settings[SettingKeyOIDCConnectProviderName]; ok && strings.TrimSpace(v) != "" {
		effective.ProviderName = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectClientID]; ok && strings.TrimSpace(v) != "" {
		effective.ClientID = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectClientSecret]; ok && strings.TrimSpace(v) != "" {
		effective.ClientSecret = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectIssuerURL]; ok && strings.TrimSpace(v) != "" {
		effective.IssuerURL = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectDiscoveryURL]; ok && strings.TrimSpace(v) != "" {
		effective.DiscoveryURL = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectAuthorizeURL]; ok && strings.TrimSpace(v) != "" {
		effective.AuthorizeURL = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectTokenURL]; ok && strings.TrimSpace(v) != "" {
		effective.TokenURL = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectUserInfoURL]; ok && strings.TrimSpace(v) != "" {
		effective.UserInfoURL = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectJWKSURL]; ok && strings.TrimSpace(v) != "" {
		effective.JWKSURL = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectScopes]; ok && strings.TrimSpace(v) != "" {
		effective.Scopes = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectRedirectURL]; ok && strings.TrimSpace(v) != "" {
		effective.RedirectURL = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectFrontendRedirectURL]; ok && strings.TrimSpace(v) != "" {
		effective.FrontendRedirectURL = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectTokenAuthMethod]; ok && strings.TrimSpace(v) != "" {
		effective.TokenAuthMethod = strings.ToLower(strings.TrimSpace(v))
	}
	if raw, ok := settings[SettingKeyOIDCConnectUsePKCE]; ok {
		effective.UsePKCE = raw == "true"
	}
	if raw, ok := settings[SettingKeyOIDCConnectValidateIDToken]; ok {
		effective.ValidateIDToken = raw == "true"
	}
	if v, ok := settings[SettingKeyOIDCConnectAllowedSigningAlgs]; ok && strings.TrimSpace(v) != "" {
		effective.AllowedSigningAlgs = strings.TrimSpace(v)
	}
	if raw, ok := settings[SettingKeyOIDCConnectClockSkewSeconds]; ok && strings.TrimSpace(raw) != "" {
		if parsed, parseErr := strconv.Atoi(strings.TrimSpace(raw)); parseErr == nil {
			effective.ClockSkewSeconds = parsed
		}
	}
	if raw, ok := settings[SettingKeyOIDCConnectRequireEmailVerified]; ok {
		effective.RequireEmailVerified = raw == "true"
	}
	if v, ok := settings[SettingKeyOIDCConnectUserInfoEmailPath]; ok {
		effective.UserInfoEmailPath = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectUserInfoIDPath]; ok {
		effective.UserInfoIDPath = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectUserInfoUsernamePath]; ok {
		effective.UserInfoUsernamePath = strings.TrimSpace(v)
	}

	if !effective.Enabled {
		return config.OIDCConnectConfig{}, infraerrors.NotFound("OAUTH_DISABLED", "oauth login is disabled")
	}
	if strings.TrimSpace(effective.ProviderName) == "" {
		effective.ProviderName = "OIDC"
	}
	if strings.TrimSpace(effective.ClientID) == "" {
		return config.OIDCConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth client id not configured")
	}
	if strings.TrimSpace(effective.IssuerURL) == "" {
		return config.OIDCConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth issuer url not configured")
	}
	if strings.TrimSpace(effective.RedirectURL) == "" {
		return config.OIDCConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth redirect url not configured")
	}
	if strings.TrimSpace(effective.FrontendRedirectURL) == "" {
		return config.OIDCConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth frontend redirect url not configured")
	}
	if !scopesContainOpenID(effective.Scopes) {
		return config.OIDCConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth scopes must contain openid")
	}
	if effective.ClockSkewSeconds < 0 || effective.ClockSkewSeconds > 600 {
		return config.OIDCConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth clock skew must be between 0 and 600")
	}

	if err := config.ValidateAbsoluteHTTPURL(effective.IssuerURL); err != nil {
		return config.OIDCConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth issuer url invalid")
	}

	discoveryURL := strings.TrimSpace(effective.DiscoveryURL)
	if discoveryURL == "" {
		discoveryURL = oidcDefaultDiscoveryURL(effective.IssuerURL)
		effective.DiscoveryURL = discoveryURL
	}
	if discoveryURL != "" {
		if err := config.ValidateAbsoluteHTTPURL(discoveryURL); err != nil {
			return config.OIDCConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth discovery url invalid")
		}
	}

	needsDiscovery := strings.TrimSpace(effective.AuthorizeURL) == "" ||
		strings.TrimSpace(effective.TokenURL) == "" ||
		(effective.ValidateIDToken && strings.TrimSpace(effective.JWKSURL) == "")
	if needsDiscovery && discoveryURL != "" {
		metadata, resolveErr := oidcResolveProviderMetadata(ctx, discoveryURL)
		if resolveErr != nil {
			return config.OIDCConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth discovery resolve failed").WithCause(resolveErr)
		}
		if strings.TrimSpace(effective.AuthorizeURL) == "" {
			effective.AuthorizeURL = strings.TrimSpace(metadata.AuthorizationEndpoint)
		}
		if strings.TrimSpace(effective.TokenURL) == "" {
			effective.TokenURL = strings.TrimSpace(metadata.TokenEndpoint)
		}
		if strings.TrimSpace(effective.UserInfoURL) == "" {
			effective.UserInfoURL = strings.TrimSpace(metadata.UserInfoEndpoint)
		}
		if strings.TrimSpace(effective.JWKSURL) == "" {
			effective.JWKSURL = strings.TrimSpace(metadata.JWKSURI)
		}
	}

	if strings.TrimSpace(effective.AuthorizeURL) == "" {
		return config.OIDCConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth authorize url not configured")
	}
	if strings.TrimSpace(effective.TokenURL) == "" {
		return config.OIDCConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth token url not configured")
	}
	if err := config.ValidateAbsoluteHTTPURL(effective.AuthorizeURL); err != nil {
		return config.OIDCConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth authorize url invalid")
	}
	if err := config.ValidateAbsoluteHTTPURL(effective.TokenURL); err != nil {
		return config.OIDCConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth token url invalid")
	}
	if v := strings.TrimSpace(effective.UserInfoURL); v != "" {
		if err := config.ValidateAbsoluteHTTPURL(v); err != nil {
			return config.OIDCConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth userinfo url invalid")
		}
	}
	if effective.ValidateIDToken {
		if strings.TrimSpace(effective.JWKSURL) == "" {
			return config.OIDCConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth jwks url not configured")
		}
		if strings.TrimSpace(effective.AllowedSigningAlgs) == "" {
			return config.OIDCConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth signing algs not configured")
		}
	}
	if v := strings.TrimSpace(effective.JWKSURL); v != "" {
		if err := config.ValidateAbsoluteHTTPURL(v); err != nil {
			return config.OIDCConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth jwks url invalid")
		}
	}
	if err := config.ValidateAbsoluteHTTPURL(effective.RedirectURL); err != nil {
		return config.OIDCConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth redirect url invalid")
	}
	if err := config.ValidateFrontendRedirectURL(effective.FrontendRedirectURL); err != nil {
		return config.OIDCConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth frontend redirect url invalid")
	}

	method := strings.ToLower(strings.TrimSpace(effective.TokenAuthMethod))
	switch method {
	case "", "client_secret_post", "client_secret_basic":
		if strings.TrimSpace(effective.ClientSecret) == "" {
			return config.OIDCConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth client secret not configured")
		}
	case "none":
		if !effective.UsePKCE {
			return config.OIDCConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth pkce must be enabled when token_auth_method=none")
		}
	default:
		return config.OIDCConnectConfig{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth token_auth_method invalid")
	}

	return effective, nil
}

func scopesContainOpenID(scopes string) bool {
	for _, scope := range strings.Fields(strings.ToLower(strings.TrimSpace(scopes))) {
		if scope == "openid" {
			return true
		}
	}
	return false
}

type oidcProviderMetadata struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserInfoEndpoint      string `json:"userinfo_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

func oidcDefaultDiscoveryURL(issuerURL string) string {
	issuerURL = strings.TrimSpace(issuerURL)
	if issuerURL == "" {
		return ""
	}
	return strings.TrimRight(issuerURL, "/") + "/.well-known/openid-configuration"
}

func oidcResolveProviderMetadata(ctx context.Context, discoveryURL string) (*oidcProviderMetadata, error) {
	discoveryURL = strings.TrimSpace(discoveryURL)
	if discoveryURL == "" {
		return nil, fmt.Errorf("discovery url is empty")
	}

	resp, err := req.C().
		SetTimeout(15*time.Second).
		R().
		SetContext(ctx).
		SetHeader("Accept", "application/json").
		Get(discoveryURL)
	if err != nil {
		return nil, fmt.Errorf("request discovery document: %w", err)
	}
	if !resp.IsSuccessState() {
		return nil, fmt.Errorf("discovery request failed: status=%d", resp.StatusCode)
	}

	metadata := &oidcProviderMetadata{}
	if err := json.Unmarshal(resp.Bytes(), metadata); err != nil {
		return nil, fmt.Errorf("parse discovery document: %w", err)
	}
	return metadata, nil
}

// GetStreamTimeoutSettings 获取流超时处理配置
func (s *SettingService) GetStreamTimeoutSettings(ctx context.Context) (*StreamTimeoutSettings, error) {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyStreamTimeoutSettings)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return DefaultStreamTimeoutSettings(), nil
		}
		return nil, fmt.Errorf("get stream timeout settings: %w", err)
	}
	if value == "" {
		return DefaultStreamTimeoutSettings(), nil
	}

	var settings StreamTimeoutSettings
	if err := json.Unmarshal([]byte(value), &settings); err != nil {
		return DefaultStreamTimeoutSettings(), nil
	}

	// 验证并修正配置值
	if settings.TempUnschedMinutes < 1 {
		settings.TempUnschedMinutes = 1
	}
	if settings.TempUnschedMinutes > 60 {
		settings.TempUnschedMinutes = 60
	}
	if settings.ThresholdCount < 1 {
		settings.ThresholdCount = 1
	}
	if settings.ThresholdCount > 10 {
		settings.ThresholdCount = 10
	}
	if settings.ThresholdWindowMinutes < 1 {
		settings.ThresholdWindowMinutes = 1
	}
	if settings.ThresholdWindowMinutes > 60 {
		settings.ThresholdWindowMinutes = 60
	}

	// 验证 action
	switch settings.Action {
	case StreamTimeoutActionTempUnsched, StreamTimeoutActionError, StreamTimeoutActionNone:
		// valid
	default:
		settings.Action = StreamTimeoutActionTempUnsched
	}

	return &settings, nil
}

// IsUngroupedKeySchedulingAllowed 查询是否允许未分组 Key 调度
func (s *SettingService) IsUngroupedKeySchedulingAllowed(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyAllowUngroupedKeyScheduling)
	if err != nil {
		return false // fail-closed: 查询失败时默认不允许
	}
	return value == "true"
}

// GetClaudeCodeVersionBounds 获取 Claude Code 版本号上下限要求
// 使用进程内 atomic.Value 缓存，60 秒 TTL，热路径零锁开销
// singleflight 防止缓存过期时 thundering herd
// 返回空字符串表示不做对应方向的版本检查
func (s *SettingService) GetClaudeCodeVersionBounds(ctx context.Context) (min, max string) {
	if cached, ok := versionBoundsCache.Load().(*cachedVersionBounds); ok {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.min, cached.max
		}
	}
	// singleflight: 同一时刻只有一个 goroutine 查询 DB，其余复用结果
	type bounds struct{ min, max string }
	result, err, _ := versionBoundsSF.Do("version_bounds", func() (any, error) {
		// 二次检查，避免排队的 goroutine 重复查询
		if cached, ok := versionBoundsCache.Load().(*cachedVersionBounds); ok {
			if time.Now().UnixNano() < cached.expiresAt {
				return bounds{cached.min, cached.max}, nil
			}
		}
		// 使用独立 context：断开请求取消链，避免客户端断连导致空值被长期缓存
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), versionBoundsDBTimeout)
		defer cancel()
		values, err := s.settingRepo.GetMultiple(dbCtx, []string{
			SettingKeyMinClaudeCodeVersion,
			SettingKeyMaxClaudeCodeVersion,
		})
		if err != nil {
			// fail-open: DB 错误时不阻塞请求，但记录日志并使用短 TTL 快速重试
			slog.Warn("failed to get claude code version bounds setting, skipping version check", "error", err)
			versionBoundsCache.Store(&cachedVersionBounds{
				min:       "",
				max:       "",
				expiresAt: time.Now().Add(versionBoundsErrorTTL).UnixNano(),
			})
			return bounds{"", ""}, nil
		}
		b := bounds{
			min: values[SettingKeyMinClaudeCodeVersion],
			max: values[SettingKeyMaxClaudeCodeVersion],
		}
		versionBoundsCache.Store(&cachedVersionBounds{
			min:       b.min,
			max:       b.max,
			expiresAt: time.Now().Add(versionBoundsCacheTTL).UnixNano(),
		})
		return b, nil
	})
	if err != nil {
		return "", ""
	}
	b, ok := result.(bounds)
	if !ok {
		return "", ""
	}
	return b.min, b.max
}

// GetRectifierSettings 获取请求整流器配置
func (s *SettingService) GetRectifierSettings(ctx context.Context) (*RectifierSettings, error) {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyRectifierSettings)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return DefaultRectifierSettings(), nil
		}
		return nil, fmt.Errorf("get rectifier settings: %w", err)
	}
	if value == "" {
		return DefaultRectifierSettings(), nil
	}

	var settings RectifierSettings
	if err := json.Unmarshal([]byte(value), &settings); err != nil {
		return DefaultRectifierSettings(), nil
	}

	return &settings, nil
}

// SetRectifierSettings 设置请求整流器配置
func (s *SettingService) SetRectifierSettings(ctx context.Context, settings *RectifierSettings) error {
	if settings == nil {
		return fmt.Errorf("settings cannot be nil")
	}

	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal rectifier settings: %w", err)
	}

	return s.settingRepo.Set(ctx, SettingKeyRectifierSettings, string(data))
}

// IsSignatureRectifierEnabled 判断签名整流是否启用（总开关 && 签名子开关）
func (s *SettingService) IsSignatureRectifierEnabled(ctx context.Context) bool {
	settings, err := s.GetRectifierSettings(ctx)
	if err != nil {
		return true // fail-open: 查询失败时默认启用
	}
	return settings.Enabled && settings.ThinkingSignatureEnabled
}

// IsBudgetRectifierEnabled 判断 Budget 整流是否启用（总开关 && Budget 子开关）
func (s *SettingService) IsBudgetRectifierEnabled(ctx context.Context) bool {
	settings, err := s.GetRectifierSettings(ctx)
	if err != nil {
		return true // fail-open: 查询失败时默认启用
	}
	return settings.Enabled && settings.ThinkingBudgetEnabled
}

// GetBetaPolicySettings 获取 Beta 策略配置
func (s *SettingService) GetBetaPolicySettings(ctx context.Context) (*BetaPolicySettings, error) {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyBetaPolicySettings)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return DefaultBetaPolicySettings(), nil
		}
		return nil, fmt.Errorf("get beta policy settings: %w", err)
	}
	if value == "" {
		return DefaultBetaPolicySettings(), nil
	}

	var settings BetaPolicySettings
	if err := json.Unmarshal([]byte(value), &settings); err != nil {
		return DefaultBetaPolicySettings(), nil
	}

	return &settings, nil
}

// SetBetaPolicySettings 设置 Beta 策略配置
func (s *SettingService) SetBetaPolicySettings(ctx context.Context, settings *BetaPolicySettings) error {
	if settings == nil {
		return fmt.Errorf("settings cannot be nil")
	}

	validActions := map[string]bool{
		BetaPolicyActionPass: true, BetaPolicyActionFilter: true, BetaPolicyActionBlock: true,
	}
	validScopes := map[string]bool{
		BetaPolicyScopeAll: true, BetaPolicyScopeOAuth: true, BetaPolicyScopeAPIKey: true, BetaPolicyScopeBedrock: true,
	}

	for i, rule := range settings.Rules {
		if rule.BetaToken == "" {
			return fmt.Errorf("rule[%d]: beta_token cannot be empty", i)
		}
		if !validActions[rule.Action] {
			return fmt.Errorf("rule[%d]: invalid action %q", i, rule.Action)
		}
		if !validScopes[rule.Scope] {
			return fmt.Errorf("rule[%d]: invalid scope %q", i, rule.Scope)
		}
		// Validate model_whitelist patterns
		for j, pattern := range rule.ModelWhitelist {
			trimmed := strings.TrimSpace(pattern)
			if trimmed == "" {
				return fmt.Errorf("rule[%d]: model_whitelist[%d] cannot be empty", i, j)
			}
			settings.Rules[i].ModelWhitelist[j] = trimmed
		}
		// Validate fallback_action
		if rule.FallbackAction != "" && !validActions[rule.FallbackAction] {
			return fmt.Errorf("rule[%d]: invalid fallback_action %q", i, rule.FallbackAction)
		}
	}

	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal beta policy settings: %w", err)
	}

	return s.settingRepo.Set(ctx, SettingKeyBetaPolicySettings, string(data))
}

// GetOpenAIFastPolicySettings 获取 OpenAI fast 策略配置
func (s *SettingService) GetOpenAIFastPolicySettings(ctx context.Context) (*OpenAIFastPolicySettings, error) {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAIFastPolicySettings)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return DefaultOpenAIFastPolicySettings(), nil
		}
		return nil, fmt.Errorf("get openai fast policy settings: %w", err)
	}
	if value == "" {
		return DefaultOpenAIFastPolicySettings(), nil
	}

	var settings OpenAIFastPolicySettings
	if err := json.Unmarshal([]byte(value), &settings); err != nil {
		slog.Warn("failed to unmarshal openai fast policy settings, falling back to defaults",
			"error", err,
			"key", SettingKeyOpenAIFastPolicySettings)
		return DefaultOpenAIFastPolicySettings(), nil
	}

	return &settings, nil
}

// SetOpenAIFastPolicySettings 设置 OpenAI fast 策略配置
func (s *SettingService) SetOpenAIFastPolicySettings(ctx context.Context, settings *OpenAIFastPolicySettings) error {
	if settings == nil {
		return fmt.Errorf("settings cannot be nil")
	}

	validActions := map[string]bool{
		BetaPolicyActionPass: true, BetaPolicyActionFilter: true, BetaPolicyActionBlock: true,
	}
	validScopes := map[string]bool{
		BetaPolicyScopeAll: true, BetaPolicyScopeOAuth: true, BetaPolicyScopeAPIKey: true, BetaPolicyScopeBedrock: true,
	}
	validTiers := map[string]bool{
		OpenAIFastTierAny: true, OpenAIFastTierPriority: true, OpenAIFastTierFlex: true,
	}

	for i, rule := range settings.Rules {
		tier := strings.ToLower(strings.TrimSpace(rule.ServiceTier))
		if tier == "" {
			tier = OpenAIFastTierAny
		}
		if !validTiers[tier] {
			return fmt.Errorf("rule[%d]: invalid service_tier %q", i, rule.ServiceTier)
		}
		settings.Rules[i].ServiceTier = tier
		if !validActions[rule.Action] {
			return fmt.Errorf("rule[%d]: invalid action %q", i, rule.Action)
		}
		if !validScopes[rule.Scope] {
			return fmt.Errorf("rule[%d]: invalid scope %q", i, rule.Scope)
		}
		for j, pattern := range rule.ModelWhitelist {
			trimmed := strings.TrimSpace(pattern)
			if trimmed == "" {
				return fmt.Errorf("rule[%d]: model_whitelist[%d] cannot be empty", i, j)
			}
			settings.Rules[i].ModelWhitelist[j] = trimmed
		}
		if rule.FallbackAction != "" && !validActions[rule.FallbackAction] {
			return fmt.Errorf("rule[%d]: invalid fallback_action %q", i, rule.FallbackAction)
		}
	}

	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal openai fast policy settings: %w", err)
	}

	return s.settingRepo.Set(ctx, SettingKeyOpenAIFastPolicySettings, string(data))
}

// SetStreamTimeoutSettings 设置流超时处理配置
func (s *SettingService) SetStreamTimeoutSettings(ctx context.Context, settings *StreamTimeoutSettings) error {
	if settings == nil {
		return fmt.Errorf("settings cannot be nil")
	}

	// 验证配置值
	if settings.TempUnschedMinutes < 1 || settings.TempUnschedMinutes > 60 {
		return fmt.Errorf("temp_unsched_minutes must be between 1-60")
	}
	if settings.ThresholdCount < 1 || settings.ThresholdCount > 10 {
		return fmt.Errorf("threshold_count must be between 1-10")
	}
	if settings.ThresholdWindowMinutes < 1 || settings.ThresholdWindowMinutes > 60 {
		return fmt.Errorf("threshold_window_minutes must be between 1-60")
	}

	switch settings.Action {
	case StreamTimeoutActionTempUnsched, StreamTimeoutActionError, StreamTimeoutActionNone:
		// valid
	default:
		return fmt.Errorf("invalid action: %s", settings.Action)
	}

	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal stream timeout settings: %w", err)
	}

	return s.settingRepo.Set(ctx, SettingKeyStreamTimeoutSettings, string(data))
}
