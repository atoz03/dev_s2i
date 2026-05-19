package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

type ProviderDefaultGrantSettings struct {
	Balance          float64
	Concurrency      int
	Subscriptions    []DefaultSubscriptionSetting
	GrantOnSignup    bool
	GrantOnFirstBind bool
}

type AuthSourceDefaultSettings struct {
	Email                        ProviderDefaultGrantSettings
	LinuxDo                      ProviderDefaultGrantSettings
	OIDC                         ProviderDefaultGrantSettings
	WeChat                       ProviderDefaultGrantSettings
	GitHub                       ProviderDefaultGrantSettings
	Google                       ProviderDefaultGrantSettings
	DingTalk                     ProviderDefaultGrantSettings
	ForceEmailOnThirdPartySignup bool
}

type authSourceDefaultKeySet struct {
	balance          string
	concurrency      string
	subscriptions    string
	grantOnSignup    string
	grantOnFirstBind string
}

var (
	emailAuthSourceDefaultKeys = authSourceDefaultKeySet{
		balance:          SettingKeyAuthSourceDefaultEmailBalance,
		concurrency:      SettingKeyAuthSourceDefaultEmailConcurrency,
		subscriptions:    SettingKeyAuthSourceDefaultEmailSubscriptions,
		grantOnSignup:    SettingKeyAuthSourceDefaultEmailGrantOnSignup,
		grantOnFirstBind: SettingKeyAuthSourceDefaultEmailGrantOnFirstBind,
	}
	linuxDoAuthSourceDefaultKeys = authSourceDefaultKeySet{
		balance:          SettingKeyAuthSourceDefaultLinuxDoBalance,
		concurrency:      SettingKeyAuthSourceDefaultLinuxDoConcurrency,
		subscriptions:    SettingKeyAuthSourceDefaultLinuxDoSubscriptions,
		grantOnSignup:    SettingKeyAuthSourceDefaultLinuxDoGrantOnSignup,
		grantOnFirstBind: SettingKeyAuthSourceDefaultLinuxDoGrantOnFirstBind,
	}
	oidcAuthSourceDefaultKeys = authSourceDefaultKeySet{
		balance:          SettingKeyAuthSourceDefaultOIDCBalance,
		concurrency:      SettingKeyAuthSourceDefaultOIDCConcurrency,
		subscriptions:    SettingKeyAuthSourceDefaultOIDCSubscriptions,
		grantOnSignup:    SettingKeyAuthSourceDefaultOIDCGrantOnSignup,
		grantOnFirstBind: SettingKeyAuthSourceDefaultOIDCGrantOnFirstBind,
	}
	weChatAuthSourceDefaultKeys = authSourceDefaultKeySet{
		balance:          SettingKeyAuthSourceDefaultWeChatBalance,
		concurrency:      SettingKeyAuthSourceDefaultWeChatConcurrency,
		subscriptions:    SettingKeyAuthSourceDefaultWeChatSubscriptions,
		grantOnSignup:    SettingKeyAuthSourceDefaultWeChatGrantOnSignup,
		grantOnFirstBind: SettingKeyAuthSourceDefaultWeChatGrantOnFirstBind,
	}
	gitHubAuthSourceDefaultKeys = authSourceDefaultKeySet{
		balance:          SettingKeyAuthSourceDefaultGitHubBalance,
		concurrency:      SettingKeyAuthSourceDefaultGitHubConcurrency,
		subscriptions:    SettingKeyAuthSourceDefaultGitHubSubscriptions,
		grantOnSignup:    SettingKeyAuthSourceDefaultGitHubGrantOnSignup,
		grantOnFirstBind: SettingKeyAuthSourceDefaultGitHubGrantOnFirstBind,
	}
	googleAuthSourceDefaultKeys = authSourceDefaultKeySet{
		balance:          SettingKeyAuthSourceDefaultGoogleBalance,
		concurrency:      SettingKeyAuthSourceDefaultGoogleConcurrency,
		subscriptions:    SettingKeyAuthSourceDefaultGoogleSubscriptions,
		grantOnSignup:    SettingKeyAuthSourceDefaultGoogleGrantOnSignup,
		grantOnFirstBind: SettingKeyAuthSourceDefaultGoogleGrantOnFirstBind,
	}
	dingTalkAuthSourceDefaultKeys = authSourceDefaultKeySet{
		balance:          SettingKeyAuthSourceDefaultDingTalkBalance,
		concurrency:      SettingKeyAuthSourceDefaultDingTalkConcurrency,
		subscriptions:    SettingKeyAuthSourceDefaultDingTalkSubscriptions,
		grantOnSignup:    SettingKeyAuthSourceDefaultDingTalkGrantOnSignup,
		grantOnFirstBind: SettingKeyAuthSourceDefaultDingTalkGrantOnFirstBind,
	}
)

const (
	defaultAuthSourceBalance     = 0
	defaultAuthSourceConcurrency = 5
	defaultWeChatConnectMode     = "open"
	defaultWeChatConnectScopes   = "snsapi_login"
	defaultWeChatConnectFrontend = "/auth/wechat/callback"
	defaultGitHubOAuthAuthorize  = "https://github.com/login/oauth/authorize"
	defaultGitHubOAuthToken      = "https://github.com/login/oauth/access_token"
	defaultGitHubOAuthUserInfo   = "https://api.github.com/user"
	defaultGitHubOAuthEmails     = "https://api.github.com/user/emails"
	defaultGitHubOAuthScopes     = "read:user user:email"
	defaultGitHubOAuthFrontend   = "/auth/oauth/callback"
	defaultGoogleOAuthAuthorize  = "https://accounts.google.com/o/oauth2/v2/auth"
	defaultGoogleOAuthToken      = "https://oauth2.googleapis.com/token"
	defaultGoogleOAuthUserInfo   = "https://openidconnect.googleapis.com/v1/userinfo"
	defaultGoogleOAuthScopes     = "openid email profile"
	defaultGoogleOAuthFrontend   = "/auth/oauth/callback"
)

func (s *SettingService) GetAuthSourceDefaultSettings(ctx context.Context) (*AuthSourceDefaultSettings, error) {
	keys := []string{
		SettingKeyAuthSourceDefaultEmailBalance,
		SettingKeyAuthSourceDefaultEmailConcurrency,
		SettingKeyAuthSourceDefaultEmailSubscriptions,
		SettingKeyAuthSourceDefaultEmailGrantOnSignup,
		SettingKeyAuthSourceDefaultEmailGrantOnFirstBind,
		SettingKeyAuthSourceDefaultLinuxDoBalance,
		SettingKeyAuthSourceDefaultLinuxDoConcurrency,
		SettingKeyAuthSourceDefaultLinuxDoSubscriptions,
		SettingKeyAuthSourceDefaultLinuxDoGrantOnSignup,
		SettingKeyAuthSourceDefaultLinuxDoGrantOnFirstBind,
		SettingKeyAuthSourceDefaultOIDCBalance,
		SettingKeyAuthSourceDefaultOIDCConcurrency,
		SettingKeyAuthSourceDefaultOIDCSubscriptions,
		SettingKeyAuthSourceDefaultOIDCGrantOnSignup,
		SettingKeyAuthSourceDefaultOIDCGrantOnFirstBind,
		SettingKeyAuthSourceDefaultWeChatBalance,
		SettingKeyAuthSourceDefaultWeChatConcurrency,
		SettingKeyAuthSourceDefaultWeChatSubscriptions,
		SettingKeyAuthSourceDefaultWeChatGrantOnSignup,
		SettingKeyAuthSourceDefaultWeChatGrantOnFirstBind,
		SettingKeyAuthSourceDefaultGitHubBalance,
		SettingKeyAuthSourceDefaultGitHubConcurrency,
		SettingKeyAuthSourceDefaultGitHubSubscriptions,
		SettingKeyAuthSourceDefaultGitHubGrantOnSignup,
		SettingKeyAuthSourceDefaultGitHubGrantOnFirstBind,
		SettingKeyAuthSourceDefaultGoogleBalance,
		SettingKeyAuthSourceDefaultGoogleConcurrency,
		SettingKeyAuthSourceDefaultGoogleSubscriptions,
		SettingKeyAuthSourceDefaultGoogleGrantOnSignup,
		SettingKeyAuthSourceDefaultGoogleGrantOnFirstBind,
		SettingKeyAuthSourceDefaultDingTalkBalance,
		SettingKeyAuthSourceDefaultDingTalkConcurrency,
		SettingKeyAuthSourceDefaultDingTalkSubscriptions,
		SettingKeyAuthSourceDefaultDingTalkGrantOnSignup,
		SettingKeyAuthSourceDefaultDingTalkGrantOnFirstBind,
		SettingKeyForceEmailOnThirdPartySignup,
	}
	settings, err := s.settingRepo.GetMultiple(ctx, keys)
	if err != nil {
		return nil, fmt.Errorf("get auth source default settings: %w", err)
	}

	return &AuthSourceDefaultSettings{
		Email:                        parseProviderDefaultGrantSettings(settings, emailAuthSourceDefaultKeys),
		LinuxDo:                      parseProviderDefaultGrantSettings(settings, linuxDoAuthSourceDefaultKeys),
		OIDC:                         parseProviderDefaultGrantSettings(settings, oidcAuthSourceDefaultKeys),
		WeChat:                       parseProviderDefaultGrantSettings(settings, weChatAuthSourceDefaultKeys),
		GitHub:                       parseProviderDefaultGrantSettings(settings, gitHubAuthSourceDefaultKeys),
		Google:                       parseProviderDefaultGrantSettings(settings, googleAuthSourceDefaultKeys),
		DingTalk:                     parseProviderDefaultGrantSettings(settings, dingTalkAuthSourceDefaultKeys),
		ForceEmailOnThirdPartySignup: parseBool(settings[SettingKeyForceEmailOnThirdPartySignup]),
	}, nil
}

func (s *SettingService) ResolveAuthSourceGrantSettings(ctx context.Context, signupSource string, firstBind bool) (ProviderDefaultGrantSettings, bool, error) {
	result := ProviderDefaultGrantSettings{
		Balance:       s.GetDefaultBalance(ctx),
		Concurrency:   s.GetDefaultConcurrency(ctx),
		Subscriptions: s.GetDefaultSubscriptions(ctx),
	}

	defaults, err := s.GetAuthSourceDefaultSettings(ctx)
	if err != nil {
		return result, false, err
	}
	providerDefaults, ok := authSourceSignupSettings(defaults, signupSource)
	if !ok {
		return result, false, nil
	}

	enabled := providerDefaults.GrantOnSignup
	if firstBind {
		enabled = providerDefaults.GrantOnFirstBind
	}
	if !enabled {
		return result, false, nil
	}

	return mergeProviderDefaultGrantSettings(result, providerDefaults), true, nil
}

func (s *SettingService) UpdateAuthSourceDefaultSettings(ctx context.Context, settings *AuthSourceDefaultSettings) error {
	updates, err := s.buildAuthSourceDefaultUpdates(ctx, settings)
	if err != nil {
		return err
	}
	if len(updates) == 0 {
		return nil
	}
	if err := s.settingRepo.SetMultiple(ctx, updates); err != nil {
		return fmt.Errorf("update auth source default settings: %w", err)
	}
	return nil
}

func (s *SettingService) buildAuthSourceDefaultUpdates(ctx context.Context, settings *AuthSourceDefaultSettings) (map[string]string, error) {
	_ = ctx
	if settings == nil {
		return nil, fmt.Errorf("auth source default settings cannot be nil")
	}

	updates := make(map[string]string, 21)
	collectAuthSourceDefaultUpdate(updates, emailAuthSourceDefaultKeys, settings.Email)
	collectAuthSourceDefaultUpdate(updates, linuxDoAuthSourceDefaultKeys, settings.LinuxDo)
	collectAuthSourceDefaultUpdate(updates, oidcAuthSourceDefaultKeys, settings.OIDC)
	collectAuthSourceDefaultUpdate(updates, weChatAuthSourceDefaultKeys, settings.WeChat)
	collectAuthSourceDefaultUpdate(updates, gitHubAuthSourceDefaultKeys, settings.GitHub)
	collectAuthSourceDefaultUpdate(updates, googleAuthSourceDefaultKeys, settings.Google)
	collectAuthSourceDefaultUpdate(updates, dingTalkAuthSourceDefaultKeys, settings.DingTalk)
	updates[SettingKeyForceEmailOnThirdPartySignup] = strconv.FormatBool(settings.ForceEmailOnThirdPartySignup)
	return updates, nil
}

func collectAuthSourceDefaultUpdate(updates map[string]string, keys authSourceDefaultKeySet, settings ProviderDefaultGrantSettings) {
	updates[keys.balance] = strconv.FormatFloat(settings.Balance, 'f', 8, 64)
	updates[keys.concurrency] = strconv.Itoa(settings.Concurrency)
	updates[keys.subscriptions] = encodeDefaultSubscriptionSettings(settings.Subscriptions)
	updates[keys.grantOnSignup] = strconv.FormatBool(settings.GrantOnSignup)
	updates[keys.grantOnFirstBind] = strconv.FormatBool(settings.GrantOnFirstBind)
}

func parseProviderDefaultGrantSettings(settings map[string]string, keys authSourceDefaultKeySet) ProviderDefaultGrantSettings {
	return ProviderDefaultGrantSettings{
		Balance:          parseFloat(settings[keys.balance], defaultAuthSourceBalance),
		Concurrency:      parseInt(settings[keys.concurrency], defaultAuthSourceConcurrency),
		Subscriptions:    parseDefaultSubscriptionSettings(settings[keys.subscriptions]),
		GrantOnSignup:    parseBool(settings[keys.grantOnSignup]),
		GrantOnFirstBind: parseBool(settings[keys.grantOnFirstBind]),
	}
}

func mergeProviderDefaultGrantSettings(globalDefaults ProviderDefaultGrantSettings, providerDefaults ProviderDefaultGrantSettings) ProviderDefaultGrantSettings {
	result := globalDefaults
	// 管理员显式设置来源并发（包括 5）应覆盖全局默认，不能把 5 当成“未配置”哨兵值。
	if providerDefaults.Concurrency > 0 {
		result.Concurrency = providerDefaults.Concurrency
	}
	if providerDefaults.Balance >= 0 {
		result.Balance = providerDefaults.Balance
	}
	if len(providerDefaults.Subscriptions) > 0 {
		result.Subscriptions = providerDefaults.Subscriptions
	}
	return result
}

func parseDefaultSubscriptionSettings(raw string) []DefaultSubscriptionSetting {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return []DefaultSubscriptionSetting{}
	}
	var out []DefaultSubscriptionSetting
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return []DefaultSubscriptionSetting{}
	}
	if out == nil {
		return []DefaultSubscriptionSetting{}
	}
	return out
}

func encodeDefaultSubscriptionSettings(subs []DefaultSubscriptionSetting) string {
	if subs == nil {
		subs = []DefaultSubscriptionSetting{}
	}
	data, err := json.Marshal(subs)
	if err != nil {
		return "[]"
	}
	return string(data)
}

func parseBool(raw string) bool {
	return strings.EqualFold(strings.TrimSpace(raw), "true")
}

func parseFloat(raw string, fallback float64) float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return fallback
	}
	return value
}

func parseInt(raw string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return value
}

type WeChatConnectOAuthConfig struct {
	Enabled             bool
	LegacyAppID         string
	LegacyAppSecret     string
	OpenAppID           string
	OpenAppSecret       string
	MPAppID             string
	MPAppSecret         string
	MobileAppID         string
	MobileAppSecret     string
	OpenEnabled         bool
	MPEnabled           bool
	MobileEnabled       bool
	Mode                string
	Scopes              string
	RedirectURL         string
	FrontendRedirectURL string
}

func (cfg WeChatConnectOAuthConfig) SupportsMode(mode string) bool {
	switch normalizeWeChatConnectModeSetting(mode) {
	case "mp":
		return cfg.MPEnabled
	case "mobile":
		return cfg.MobileEnabled
	default:
		return cfg.OpenEnabled
	}
}

func (cfg WeChatConnectOAuthConfig) AppIDForMode(mode string) string {
	switch normalizeWeChatConnectModeSetting(mode) {
	case "mp":
		return strings.TrimSpace(firstNonEmpty(cfg.MPAppID, cfg.LegacyAppID))
	case "mobile":
		return strings.TrimSpace(firstNonEmpty(cfg.MobileAppID, cfg.LegacyAppID))
	default:
		return strings.TrimSpace(firstNonEmpty(cfg.OpenAppID, cfg.LegacyAppID))
	}
}

func (cfg WeChatConnectOAuthConfig) ScopeForMode(mode string) string {
	switch normalizeWeChatConnectModeSetting(mode) {
	case "mp":
		return normalizeWeChatConnectScopeSetting(cfg.Scopes, "mp")
	case "mobile":
		return ""
	default:
		return defaultWeChatConnectScopeForMode("open")
	}
}

func (cfg WeChatConnectOAuthConfig) AppSecretForMode(mode string) string {
	switch normalizeWeChatConnectModeSetting(mode) {
	case "mp":
		return strings.TrimSpace(firstNonEmpty(cfg.MPAppSecret, cfg.LegacyAppSecret))
	case "mobile":
		return strings.TrimSpace(firstNonEmpty(cfg.MobileAppSecret, cfg.LegacyAppSecret))
	default:
		return strings.TrimSpace(firstNonEmpty(cfg.OpenAppSecret, cfg.LegacyAppSecret))
	}
}

func (s *SettingService) GetWeChatConnectOAuthConfig(ctx context.Context) (WeChatConnectOAuthConfig, error) {
	keys := []string{
		SettingKeyWeChatConnectEnabled,
		SettingKeyWeChatConnectAppID,
		SettingKeyWeChatConnectAppSecret,
		SettingKeyWeChatConnectOpenAppID,
		SettingKeyWeChatConnectOpenAppSecret,
		SettingKeyWeChatConnectMPAppID,
		SettingKeyWeChatConnectMPAppSecret,
		SettingKeyWeChatConnectMobileAppID,
		SettingKeyWeChatConnectMobileAppSecret,
		SettingKeyWeChatConnectOpenEnabled,
		SettingKeyWeChatConnectMPEnabled,
		SettingKeyWeChatConnectMobileEnabled,
		SettingKeyWeChatConnectMode,
		SettingKeyWeChatConnectScopes,
		SettingKeyWeChatConnectRedirectURL,
		SettingKeyWeChatConnectFrontendRedirectURL,
	}
	settings, err := s.settingRepo.GetMultiple(ctx, keys)
	if err != nil {
		return WeChatConnectOAuthConfig{}, fmt.Errorf("get wechat connect settings: %w", err)
	}
	return s.parseWeChatConnectOAuthConfig(settings)
}

func (s *SettingService) parseWeChatConnectOAuthConfig(settings map[string]string) (WeChatConnectOAuthConfig, error) {
	cfg := s.buildWeChatConnectOAuthConfig(settings)

	if cfg.Enabled && !cfg.SupportsMode(cfg.Mode) {
		return WeChatConnectOAuthConfig{}, fmt.Errorf("wechat connect mode %s is disabled", cfg.Mode)
	}
	if cfg.Enabled {
		if strings.TrimSpace(cfg.AppIDForMode(cfg.Mode)) == "" {
			return WeChatConnectOAuthConfig{}, fmt.Errorf("wechat connect app id is required")
		}
		if strings.TrimSpace(cfg.AppSecretForMode(cfg.Mode)) == "" {
			return WeChatConnectOAuthConfig{}, fmt.Errorf("wechat connect app secret is required")
		}
		if cfg.RedirectURL != "" {
			if err := config.ValidateAbsoluteHTTPURL(cfg.RedirectURL); err != nil {
				return WeChatConnectOAuthConfig{}, err
			}
		}
	}
	if cfg.FrontendRedirectURL == "" {
		cfg.FrontendRedirectURL = defaultWeChatConnectFrontend
	}
	return cfg, nil
}

func (s *SettingService) buildWeChatConnectOAuthConfig(settings map[string]string) WeChatConnectOAuthConfig {
	base := config.WeChatConnectConfig{}
	if s != nil && s.cfg != nil {
		base = s.cfg.WeChat
	}

	enabled := base.Enabled
	if raw, ok := settings[SettingKeyWeChatConnectEnabled]; ok {
		enabled = parseBool(raw)
	}

	mode := normalizeWeChatConnectModeSetting(base.Mode)
	if raw, ok := settings[SettingKeyWeChatConnectMode]; ok && strings.TrimSpace(raw) != "" {
		mode = normalizeWeChatConnectModeSetting(raw)
	}

	openEnabled := base.OpenEnabled
	mpEnabled := base.MPEnabled
	mobileEnabled := base.MobileEnabled
	ignoreSyntheticDisabledCaps := shouldIgnoreSyntheticWeChatCapabilityOverrides(settings)
	if !ignoreSyntheticDisabledCaps {
		if raw, ok := settings[SettingKeyWeChatConnectOpenEnabled]; ok {
			openEnabled = parseBool(raw)
		}
		if raw, ok := settings[SettingKeyWeChatConnectMPEnabled]; ok {
			mpEnabled = parseBool(raw)
		}
		if raw, ok := settings[SettingKeyWeChatConnectMobileEnabled]; ok {
			mobileEnabled = parseBool(raw)
		}
	}
	openEnabled, mpEnabled, mobileEnabled = normalizeWeChatConnectCapabilityFlags(enabled, mode, openEnabled, mpEnabled, mobileEnabled)

	legacyAppID := strings.TrimSpace(firstNonEmpty(settings[SettingKeyWeChatConnectAppID], base.AppID))
	legacyAppSecret := strings.TrimSpace(firstNonEmpty(settings[SettingKeyWeChatConnectAppSecret], base.AppSecret))

	openAppID := strings.TrimSpace(firstNonEmpty(settings[SettingKeyWeChatConnectOpenAppID], base.OpenAppID, legacyAppID))
	openAppSecret := strings.TrimSpace(firstNonEmpty(settings[SettingKeyWeChatConnectOpenAppSecret], base.OpenAppSecret, legacyAppSecret))
	mpAppID := strings.TrimSpace(firstNonEmpty(settings[SettingKeyWeChatConnectMPAppID], base.MPAppID, legacyAppID))
	mpAppSecret := strings.TrimSpace(firstNonEmpty(settings[SettingKeyWeChatConnectMPAppSecret], base.MPAppSecret, legacyAppSecret))
	mobileAppID := strings.TrimSpace(firstNonEmpty(settings[SettingKeyWeChatConnectMobileAppID], base.MobileAppID, legacyAppID))
	mobileAppSecret := strings.TrimSpace(firstNonEmpty(settings[SettingKeyWeChatConnectMobileAppSecret], base.MobileAppSecret, legacyAppSecret))

	scopes := normalizeWeChatConnectScopeSetting(firstNonEmpty(settings[SettingKeyWeChatConnectScopes], base.Scopes), mode)
	redirectURL := strings.TrimSpace(firstNonEmpty(settings[SettingKeyWeChatConnectRedirectURL], base.RedirectURL))
	frontendRedirectURL := strings.TrimSpace(firstNonEmpty(settings[SettingKeyWeChatConnectFrontendRedirectURL], base.FrontendRedirectURL))
	if frontendRedirectURL == "" {
		frontendRedirectURL = defaultWeChatConnectFrontend
	}

	return WeChatConnectOAuthConfig{
		Enabled:             enabled,
		LegacyAppID:         legacyAppID,
		LegacyAppSecret:     legacyAppSecret,
		OpenAppID:           openAppID,
		OpenAppSecret:       openAppSecret,
		MPAppID:             mpAppID,
		MPAppSecret:         mpAppSecret,
		MobileAppID:         mobileAppID,
		MobileAppSecret:     mobileAppSecret,
		OpenEnabled:         openEnabled,
		MPEnabled:           mpEnabled,
		MobileEnabled:       mobileEnabled,
		Mode:                mode,
		Scopes:              scopes,
		RedirectURL:         redirectURL,
		FrontendRedirectURL: frontendRedirectURL,
	}
}

func shouldIgnoreSyntheticWeChatCapabilityOverrides(settings map[string]string) bool {
	if settings == nil {
		return false
	}
	if _, ok := settings[SettingKeyWeChatConnectEnabled]; ok {
		return false
	}
	if _, ok := settings[SettingKeyWeChatConnectMode]; ok {
		return false
	}
	meaningfulKeys := []string{
		SettingKeyWeChatConnectAppID,
		SettingKeyWeChatConnectAppSecret,
		SettingKeyWeChatConnectOpenAppID,
		SettingKeyWeChatConnectOpenAppSecret,
		SettingKeyWeChatConnectMPAppID,
		SettingKeyWeChatConnectMPAppSecret,
		SettingKeyWeChatConnectMobileAppID,
		SettingKeyWeChatConnectMobileAppSecret,
		SettingKeyWeChatConnectScopes,
		SettingKeyWeChatConnectRedirectURL,
		SettingKeyWeChatConnectFrontendRedirectURL,
	}
	for _, key := range meaningfulKeys {
		if v, ok := settings[key]; ok && strings.TrimSpace(v) != "" {
			return false
		}
	}

	capKeys := []string{
		SettingKeyWeChatConnectOpenEnabled,
		SettingKeyWeChatConnectMPEnabled,
		SettingKeyWeChatConnectMobileEnabled,
	}
	hasCapOverride := false
	for _, key := range capKeys {
		v, ok := settings[key]
		if !ok {
			continue
		}
		hasCapOverride = true
		if parseBool(v) {
			return false
		}
	}
	return hasCapOverride
}

func parseWeChatConnectCapabilitySettings(settings map[string]string, enabled bool, mode string) (bool, bool, bool) {
	mode = normalizeWeChatConnectModeSetting(mode)
	openEnabled := parseBool(settings[SettingKeyWeChatConnectOpenEnabled])
	mpEnabled := parseBool(settings[SettingKeyWeChatConnectMPEnabled])
	mobileEnabled := parseBool(settings[SettingKeyWeChatConnectMobileEnabled])

	// 兼容旧配置：仅开启总开关时默认当前模式可用。
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

func normalizeWeChatConnectModeSetting(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "mp":
		return "mp"
	case "mobile":
		return "mobile"
	default:
		return "open"
	}
}

func defaultWeChatConnectScopeForMode(mode string) string {
	switch normalizeWeChatConnectModeSetting(mode) {
	case "mp":
		return "snsapi_userinfo"
	case "mobile":
		return ""
	default:
		return defaultWeChatConnectScopes
	}
}

// DefaultWeChatConnectScopesForMode 提供按模式返回默认 scope 的兼容导出函数。
// 供跨包测试/调用复用，内部仍走统一的默认值逻辑。
func DefaultWeChatConnectScopesForMode(mode string) string {
	return defaultWeChatConnectScopeForMode(mode)
}

func normalizeWeChatConnectScopeSetting(raw, mode string) string {
	switch normalizeWeChatConnectModeSetting(mode) {
	case "mp":
		switch strings.TrimSpace(raw) {
		case "snsapi_base":
			return "snsapi_base"
		case "snsapi_userinfo":
			return "snsapi_userinfo"
		default:
			return defaultWeChatConnectScopeForMode(mode)
		}
	case "mobile":
		return ""
	default:
		return defaultWeChatConnectScopeForMode(mode)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
