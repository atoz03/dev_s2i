package service

import "encoding/json"

// PublicSettingsInjectionPayload 定义 SSR 注入到 HTML 的公共设置结构。
// 字段需与 dto.PublicSettings 对齐，避免前端刷新时出现 feature flag 漂移。
type PublicSettingsInjectionPayload struct {
	RegistrationEnabled                  bool                     `json:"registration_enabled"`
	EmailVerifyEnabled                   bool                     `json:"email_verify_enabled"`
	ForceEmailOnThirdPartySignup         bool                     `json:"force_email_on_third_party_signup"`
	RegistrationEmailSuffixWhitelist     []string                 `json:"registration_email_suffix_whitelist"`
	PromoCodeEnabled                     bool                     `json:"promo_code_enabled"`
	PasswordResetEnabled                 bool                     `json:"password_reset_enabled"`
	InvitationCodeEnabled                bool                     `json:"invitation_code_enabled"`
	TotpEnabled                          bool                     `json:"totp_enabled"`
	LoginAgreementEnabled                bool                     `json:"login_agreement_enabled"`
	LoginAgreementMode                   string                   `json:"login_agreement_mode"`
	LoginAgreementUpdatedAt              string                   `json:"login_agreement_updated_at"`
	LoginAgreementRevision               string                   `json:"login_agreement_revision"`
	LoginAgreementDocuments              []LoginAgreementDocument `json:"login_agreement_documents"`
	TurnstileEnabled                     bool                     `json:"turnstile_enabled"`
	TurnstileSiteKey                     string                   `json:"turnstile_site_key,omitempty"`
	SiteName                             string                   `json:"site_name"`
	SiteLogo                             string                   `json:"site_logo,omitempty"`
	SiteSubtitle                         string                   `json:"site_subtitle,omitempty"`
	APIBaseURL                           string                   `json:"api_base_url,omitempty"`
	ContactInfo                          string                   `json:"contact_info,omitempty"`
	DocURL                               string                   `json:"doc_url,omitempty"`
	HomeContent                          string                   `json:"home_content,omitempty"`
	HideCcsImportButton                  bool                     `json:"hide_ccs_import_button"`
	PurchaseSubscriptionEnabled          bool                     `json:"purchase_subscription_enabled"`
	PurchaseSubscriptionURL              string                   `json:"purchase_subscription_url,omitempty"`
	TableDefaultPageSize                 int                      `json:"table_default_page_size"`
	TablePageSizeOptions                 []int                    `json:"table_page_size_options"`
	CustomMenuItems                      json.RawMessage          `json:"custom_menu_items"`
	CustomEndpoints                      json.RawMessage          `json:"custom_endpoints"`
	LinuxDoOAuthEnabled                  bool                     `json:"linuxdo_oauth_enabled"`
	DingTalkOAuthEnabled                 bool                     `json:"dingtalk_oauth_enabled"`
	WeChatOAuthEnabled                   bool                     `json:"wechat_oauth_enabled"`
	WeChatOAuthOpenEnabled               bool                     `json:"wechat_oauth_open_enabled"`
	WeChatOAuthMPEnabled                 bool                     `json:"wechat_oauth_mp_enabled"`
	WeChatOAuthMobileEnabled             bool                     `json:"wechat_oauth_mobile_enabled"`
	BackendModeEnabled                   bool                     `json:"backend_mode_enabled"`
	PaymentEnabled                       bool                     `json:"payment_enabled"`
	OIDCOAuthEnabled                     bool                     `json:"oidc_oauth_enabled"`
	OIDCOAuthProviderName                string                   `json:"oidc_oauth_provider_name"`
	GitHubOAuthEnabled                   bool                     `json:"github_oauth_enabled"`
	GoogleOAuthEnabled                   bool                     `json:"google_oauth_enabled"`
	Version                              string                   `json:"version,omitempty"`
	BalanceLowNotifyEnabled              bool                     `json:"balance_low_notify_enabled"`
	AccountQuotaNotifyEnabled            bool                     `json:"account_quota_notify_enabled"`
	BalanceLowNotifyThreshold            float64                  `json:"balance_low_notify_threshold"`
	BalanceLowNotifyRechargeURL          string                   `json:"balance_low_notify_recharge_url"`
	ChannelMonitorEnabled                bool                     `json:"channel_monitor_enabled"`
	ChannelMonitorDefaultIntervalSeconds int                      `json:"channel_monitor_default_interval_seconds"`
	AvailableChannelsEnabled             bool                     `json:"available_channels_enabled"`
	AffiliateEnabled                     bool                     `json:"affiliate_enabled"`
	RiskControlEnabled                   bool                     `json:"risk_control_enabled"`
}
