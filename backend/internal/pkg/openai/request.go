package openai

import "strings"

// CodexCLIOriginator 是 codex-rs 的历史默认 originator（DEFAULT_ORIGINATOR），
// 保留用于官方客户端识别，不再作为网关出站身份。
const CodexCLIOriginator = "codex_cli_rs"

// CodexDefaultOriginator 是网关出站使用的默认 Codex 身份：交互式 TUI。
// 推导不出官方身份时，出站身份整体回退到该标识配套的默认 Codex 身份。
//
// 选 codex-tui 而非 codex_cli_rs：codex-tui 是真实 Codex 流量占比最高的一支
// （见 codexOfficialClientUAPrefixes 注释），把它归一化成 codex_cli_rs 反而让网关
// 出站流量偏离大盘。上游 2026-08-02 曾据「codex-tui 落入降载桶」的容量快照做过
// 相反选择，5 天后即修正回 codex-tui 并持续至今——该快照是上游容量策略而非协议常量。
const CodexDefaultOriginator = "codex-tui"

// CodexCLIUserAgentPrefixes matches Codex CLI User-Agent patterns
// Examples: "codex_vscode/1.0.0", "codex_cli_rs/0.1.2"
var CodexCLIUserAgentPrefixes = []string{
	"codex_vscode/",
	"codex_cli_rs/",
}

// codexOfficialClientUAPrefixes：Codex 官方客户端家族 User-Agent 前缀（均含下划线/连字符，
// 每项都是确定字面量；**不含**会被 TrimSpace 退化成裸 "codex" 的空格前缀）。
// 用途：OpenAI OAuth `codex_cli_only` 访问限制判定，以及透传路径的官方 UA 识别。
//
// 交互式 TUI 自报 `codex-tui/`（连字符），是真实流量占比最高的一支，必须显式列出：
// 缺失时它既过不了官方客户端识别，又会在 OAuth 透传里被兜底改写成 codex_cli_rs，
// 与客户端自报的 originator=codex-tui 错配，被上游 /backend-api/codex 判 404。
// `Codex Desktop/` 等 `Codex ` 前缀家族由 codexOfficialClientFamilyPrefix 单独处理。
var codexOfficialClientUAPrefixes = []string{
	"codex_cli_rs/",
	"codex-tui/",
	"codex_vscode/",
	"codex_vscode_copilot/",
	"codex_app/",
	"codex_chatgpt_desktop/",
	"codex_atlas/",
	"codex_exec/",
	"codex_sdk_ts/",
}

// codexOfficialClientFamilyPrefix 覆盖 `Codex ` 前缀家族（Codex Desktop 等），对应 codex-rs
// is_first_party_originator 的 starts_with("Codex ")。**保留尾随空格**，并以 HasPrefix 直接比对
// 已归一化（小写 + 去首尾空格）的值——绝不能再经 normalizeCodexClientHeader 处理本前缀，否则
// 空格会被 TrimSpace 去掉、退化成裸 "codex"，把任何含 codex 的串（如 evil-codex_thing、
// "Mozilla/5.0 codex bypass"）都当成官方客户端放行。
const codexOfficialClientFamilyPrefix = "codex "

// codexOfficialClientOriginators：Codex 官方客户端家族 originator 精确集合。
// app-server `initialize` 把 originator 设为 clientInfo.name 逐字值（codex-rs default_client.rs），
// 故官方集合是这些确定字面量。用精确匹配而非「含 codex_ / codex」的宽松兜底，避免
// evil-codex_ 之类伪造绕过 `codex_cli_only` 访问门。
var codexOfficialClientOriginators = map[string]bool{
	"codex_cli_rs":          true, // CLI 默认 DEFAULT_ORIGINATOR
	"codex-tui":             true, // 交互式 TUI（连字符，真实流量占比最高）
	"codex_vscode":          true, // VSCode/Cursor 扩展
	"codex_vscode_copilot":  true, // 扩展 GitHub Copilot 集成模式
	"codex_app":             true, // 历史保留
	"codex_chatgpt_desktop": true,
	"codex_atlas":           true,
	"codex_exec":            true, // codex exec 非交互
	"codex_sdk_ts":          true, // TypeScript SDK
}

// IsBrowserUserAgent 判断 User-Agent 是否来自浏览器（Chrome/Firefox/Safari/Edge/Opera 等）。
// 所有现代浏览器的 UA 均以 "Mozilla/" 作为前缀，CLI 工具（codex/claude/curl/postman/python-requests 等）不会。
// 该判定用于避免 Cloudflare 对浏览器型 UA 在 OpenAI 上游接口上触发 JS 质询。
func IsBrowserUserAgent(userAgent string) bool {
	ua := strings.TrimSpace(userAgent)
	if ua == "" {
		return false
	}
	return strings.HasPrefix(strings.ToLower(ua), "mozilla/")
}

// IsCodexCLIRequest checks if the User-Agent indicates a Codex CLI request
func IsCodexCLIRequest(userAgent string) bool {
	ua := normalizeCodexClientHeader(userAgent)
	if ua == "" {
		return false
	}
	return matchCodexClientHeaderPrefixes(ua, CodexCLIUserAgentPrefixes)
}

// IsCodexOfficialClientRequest checks if the User-Agent indicates a Codex 官方客户端请求。
// 与 IsCodexCLIRequest 解耦，避免影响历史兼容逻辑。宽松版：官方 UA 前缀集允许 Contains 子串兜底，
// 供透传等历史路径使用。
func IsCodexOfficialClientRequest(userAgent string) bool {
	return isCodexOfficialClientRequest(userAgent, false)
}

// IsCodexOfficialClientRequestStrict 同 IsCodexOfficialClientRequest，但官方 UA 前缀集只做前缀
// 匹配（HasPrefix），不退化为 Contains 子串兜底——专供 codex_cli_only 访问门，收窄「浏览器前缀 +
// 中段 codex token」之类的伪造面。
func IsCodexOfficialClientRequestStrict(userAgent string) bool {
	return isCodexOfficialClientRequest(userAgent, true)
}

// isCodexOfficialClientRequest 匹配层级（优先级由高到低）：
//  1. UA 前缀集 codexOfficialClientUAPrefixes（strict=仅 HasPrefix；否则含 Contains 子串兜底）
//  2. `Codex ` 家族前缀（保留空格，避免退化为裸 codex）
//  3. UA 尾部兜底：codex-rs 把 clientInfo.name 写入 UA 末尾括号组 `(name; version)`。
//     CODEX_INTERNAL_ORIGINATOR_OVERRIDE 只改前缀、不改尾部——可借此恢复被 override 的真实
//     客户端标识（例如 cccc/0.142.0 ... (codex-tui; 0.142.0)）。非官方尾部仍被精确集拒绝。
func isCodexOfficialClientRequest(userAgent string, strict bool) bool {
	ua := normalizeCodexClientHeader(userAgent)
	if ua == "" {
		return false
	}
	if strict {
		if matchCodexClientHeaderStrictPrefixes(ua, codexOfficialClientUAPrefixes) {
			return true
		}
	} else if matchCodexClientHeaderPrefixes(ua, codexOfficialClientUAPrefixes) {
		return true
	}
	if strings.HasPrefix(ua, codexOfficialClientFamilyPrefix) {
		return true
	}
	if name := codexUATrailerName(ua); name != "" {
		return IsCodexOfficialClientOriginator(name)
	}
	return false
}

// codexUATrailerName extracts the clientInfo.name from the last parenthesized group
// of a codex-rs formatted User-Agent: `{orig}/{ver} ({os}; {arch}) {term} ({name}; {ver})`.
//
// CODEX_INTERNAL_ORIGINATOR_OVERRIDE 修改 UA 前缀（originator 段），但不修改尾部的
// `(name; version)` 括号组——该组由 codex-rs engine 写入，保留真实 clientInfo.name。
//
// input 应为去首尾空格的 UA；本函数本身大小写无关，大小写由调用方按需处理
// （isCodexOfficialClientRequest 传入已小写化的 UA 做匹配；PairCodexClientIdentity
// 传入原始大小写以保留 originator 的真实大小写）。若无法解析则返回空字符串。
func codexUATrailerName(ua string) string {
	last := strings.LastIndex(ua, "(")
	if last < 0 {
		return ""
	}
	rest := ua[last+1:]
	closeIdx := strings.Index(rest, ")")
	if closeIdx < 0 {
		return ""
	}
	inner := strings.TrimSpace(rest[:closeIdx])
	if semi := strings.Index(inner, ";"); semi >= 0 {
		inner = strings.TrimSpace(inner[:semi])
	}
	return inner
}

// IsCodexOfficialClientOriginator checks if originator indicates a Codex 官方客户端请求。
// 精确集合匹配 + `Codex ` 家族前缀，不做「含 codex_」的宽松兜底。
func IsCodexOfficialClientOriginator(originator string) bool {
	v := normalizeCodexClientHeader(originator)
	if v == "" {
		return false
	}
	if codexOfficialClientOriginators[v] {
		return true
	}
	return strings.HasPrefix(v, codexOfficialClientFamilyPrefix)
}

// IsCodexOfficialClientByHeaders checks whether the request headers indicate an
// official Codex client family request.
func IsCodexOfficialClientByHeaders(userAgent, originator string) bool {
	return IsCodexOfficialClientRequest(userAgent) || IsCodexOfficialClientOriginator(originator)
}

// PairCodexClientIdentity 由最终出站 User-Agent 推导与其配套的 originator，必要时归一化
// UA 首段，保证两者一致。上游 /backend-api/codex 会校验 originator 与 UA 首段（首个 '/'
// 之前的 client 名）是否配套，错配（如 originator=codex-tui + UA=codex_cli_rs/...）一律
// 404（upstream issue #3901）。
//
// 推导优先级：
//  1. UA 首段是官方 originator（精确集合或 `Codex ` 家族前缀）→ 直接配对，UA 原样保留；
//  2. UA 尾部括号组 `(name; version)` 的 name 是官方 originator——CODEX_INTERNAL_ORIGINATOR_OVERRIDE
//     只改 UA 前缀不改尾部（如 cccc/0.142.0 ... (codex-tui; 0.142.0)）→ 用尾部 name 重写
//     UA 首段后配对，保留真实版本/OS/终端指纹；
//  3. 均不命中 → ok=false，调用方应整体回退为默认官方身份。
func PairCodexClientIdentity(userAgent string) (originator string, pairedUA string, ok bool) {
	ua := strings.TrimSpace(userAgent)
	slash := strings.IndexByte(ua, '/')
	if slash <= 0 {
		return "", "", false
	}
	if leading := strings.TrimSpace(ua[:slash]); isSaneCodexOriginator(leading) && IsCodexOfficialClientOriginator(leading) {
		leading = canonicalizeCodexOriginator(leading)
		return leading, leading + ua[slash:], true
	}
	// 传原始大小写 UA 提取 trailer，保留 `Codex ` 家族身份的真实大小写；含 '/' 的
	// trailer 会破坏重写后 UA 首段与 originator 的一致性，直接拒绝。
	if trailer := codexUATrailerName(ua); trailer != "" && !strings.ContainsRune(trailer, '/') &&
		isSaneCodexOriginator(trailer) && IsCodexOfficialClientOriginator(trailer) {
		trailer = canonicalizeCodexOriginator(trailer)
		return trailer, trailer + ua[slash:], true
	}
	return "", "", false
}

// codexOriginatorMaxLen 官方 clientInfo.name 均为短 ASCII 标识，远低于此上限。
const codexOriginatorMaxLen = 64

// isSaneCodexOriginator 拒绝超长或含不可打印/非 ASCII 字节的候选 originator，
// 避免 `Codex ` 家族宽前缀把客户端可控的任意字节当作官方身份逐字转发给上游。
func isSaneCodexOriginator(name string) bool {
	if name == "" || len(name) > codexOriginatorMaxLen {
		return false
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// canonicalizeCodexOriginator 把精确集合的官方 originator 大小写变体归一为规范小写形态
// （如 CODEX_CLI_RS → codex_cli_rs）；`Codex ` 家族不在精确集合中，保留原大小写
// （其规范形态本就是混合大小写，上游按大小写敏感 starts_with("Codex ") 判定）。
func canonicalizeCodexOriginator(name string) string {
	if lower := normalizeCodexClientHeader(name); codexOfficialClientOriginators[lower] {
		return lower
	}
	return name
}

func normalizeCodexClientHeader(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func matchCodexClientHeaderPrefixes(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		normalizedPrefix := normalizeCodexClientHeader(prefix)
		if normalizedPrefix == "" {
			continue
		}
		// 优先前缀匹配；若 UA/Originator 被网关拼接为复合字符串时，退化为包含匹配。
		if strings.HasPrefix(value, normalizedPrefix) || strings.Contains(value, normalizedPrefix) {
			return true
		}
	}
	return false
}

// matchCodexClientHeaderStrictPrefixes 只做前缀匹配，不退化为 Contains 子串兜底。
func matchCodexClientHeaderStrictPrefixes(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		normalizedPrefix := normalizeCodexClientHeader(prefix)
		if normalizedPrefix == "" {
			continue
		}
		if strings.HasPrefix(value, normalizedPrefix) {
			return true
		}
	}
	return false
}
