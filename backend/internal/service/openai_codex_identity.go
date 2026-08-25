package service

import (
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// codexUpstreamMinVersion 上游 /backend-api/codex 接受的最低 version 头：
// 若请求携带 version 且低于该值，上游直接 404（upstream issue #3901，2026-07 实测）。
// 网关随后跨账号故障转移，最终对外表现为「没有账号支持该模型」。
const codexUpstreamMinVersion = "0.144.0"

// codexClientVersionMaxLen 官方版本号均为短 ASCII 串，远低于此上限。
const codexClientVersionMaxLen = 64

// codexClientVersionPattern 允许 0.149.1 与 0.150.0-alpha.4 两类官方形态。
var codexClientVersionPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+){1,3}(-[0-9A-Za-z.]+)?$`)

// NormalizeCodexClientVersion 校验并归一化 Codex 客户端版本号，非法值返回空串。
// 该值会被拼进出站 User-Agent 与 version 头，必须拒绝任意字节，避免自动同步拿到
// 异常值时把不可控内容透给上游。
func NormalizeCodexClientVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" || len(version) > codexClientVersionMaxLen || !codexClientVersionPattern.MatchString(version) {
		return ""
	}
	return version
}

// codexSyncedVersionTTL 生效版本的进程内缓存时长。同步是 6 小时级的，热路径不需要更实时。
const codexSyncedVersionTTL = time.Minute

// codexSyncedVersionProvider 返回自动同步得到的 Codex 客户端版本号（原始设置值）。
// 由 OpenAICodexVersionSyncService 在装配时注入；未注入时出站身份使用编译期常量。
type codexSyncedVersionProvider func() string

var (
	codexSyncedVersionMu       sync.RWMutex
	codexSyncedVersionFn       codexSyncedVersionProvider
	codexSyncedVersionCache    string
	codexSyncedVersionCachedAt time.Time
)

// SetCodexSyncedVersionProvider 注入同步版本号的读取器。
// enforceCodexIdentityHeaders 是纯函数收口点，无法在热路径注入依赖，故用进程级注册。
func SetCodexSyncedVersionProvider(provider func() string) {
	codexSyncedVersionMu.Lock()
	defer codexSyncedVersionMu.Unlock()
	codexSyncedVersionFn = provider
	codexSyncedVersionCache = ""
	codexSyncedVersionCachedAt = time.Time{}
}

// InvalidateCodexSyncedVersionCache 让下一次读取立即穿透到 provider。
// 同步服务写入新版本后调用，避免最长一个 TTL 的生效延迟。
func InvalidateCodexSyncedVersionCache() {
	codexSyncedVersionMu.Lock()
	defer codexSyncedVersionMu.Unlock()
	codexSyncedVersionCache = ""
	codexSyncedVersionCachedAt = time.Time{}
}

// codexResolvedVersionHighWater 记录本进程内已生效过的最高版本号。
// 读取链路上的瞬时故障（数据库抖动让 provider 返回空、缓存又把空值存住）本来会让
// 出站版本在一个 TTL 内退回编译期常量；用高水位把「只向前推进」从写入侧延伸到读取侧。
var codexResolvedVersionHighWater atomic.Value // string

// resolveCodexClientVersion 返回当前生效的 Codex 客户端版本号。
//
// 取值链：自动同步值 → 进程内高水位 → 编译期常量 codexCLIVersion。
// 同步值只有在合法且**高于**编译期常量时才生效，保证异常同步值不会把版本降到上游门槛以下；
// 常量始终是地板。
func resolveCodexClientVersion() string {
	best := codexCLIVersion
	if hw, _ := codexResolvedVersionHighWater.Load().(string); hw != "" && CompareVersions(hw, best) > 0 {
		best = hw
	}
	if synced := NormalizeCodexClientVersion(codexSyncedVersion()); synced != "" && CompareVersions(synced, best) > 0 {
		best = synced
		codexResolvedVersionHighWater.Store(best)
	}
	return best
}

// codexCanonicalUserAgent 按生效版本拼出规范 Codex CLI User-Agent。
// UA 形态只在 codexCLIUserAgentSuffix 一处定义，避免多处拼装漂移。
func codexCanonicalUserAgent() string {
	version := resolveCodexClientVersion()
	if version == codexCLIVersion {
		return codexCLIUserAgent
	}
	return openai.CodexDefaultOriginator + "/" + version + codexCLIUserAgentSuffix
}

func codexSyncedVersion() string {
	codexSyncedVersionMu.RLock()
	provider := codexSyncedVersionFn
	cached := codexSyncedVersionCache
	cachedAt := codexSyncedVersionCachedAt
	codexSyncedVersionMu.RUnlock()

	if provider == nil {
		return ""
	}
	if !cachedAt.IsZero() && time.Since(cachedAt) < codexSyncedVersionTTL {
		return cached
	}

	value := strings.TrimSpace(provider())

	codexSyncedVersionMu.Lock()
	codexSyncedVersionCache = value
	codexSyncedVersionCachedAt = time.Now()
	codexSyncedVersionMu.Unlock()
	return value
}

// codexOriginatorNormalization 控制 enforceCodexIdentityHeaders 是否把出站身份统一归一化为
// openai.CodexDefaultOriginator 对应的默认 Codex 身份，
// 由 gateway.disable_codex_originator_normalization 取反后在服务构造时发布。
//
// 默认开启：上游 /backend-api/codex 在容量紧张时按客户端身份分优先级降载，被降载的请求即使
// 返回 HTTP 200，也会立刻推 SSE error(code=server_is_overloaded) 并以 response.failed 收尾——
// 客户端表现为 "stream closed before response.completed"。归一化确保没有请求带着第三方身份
// 或陈旧版本出站。
//
// 具体身份取值见 openai.CodexDefaultOriginator：那是随上游容量策略变动的运营参数，
// 不是协议常量，改动只需替换该常量一处。
//
// 关闭后退回「仅按最终 User-Agent 配对 originator + version 门槛校正」的收口语义，
// 供上游调整策略后回滚。
var codexOriginatorNormalization = func() *atomic.Bool {
	v := &atomic.Bool{}
	v.Store(true)
	return v
}()

// SetCodexOriginatorNormalizationEnabled 发布 Codex 出站身份归一化开关。
// enforceCodexIdentityHeaders 是所有出站路径共用的纯函数收口点，无法在热路径注入配置，
// 故由持有配置的服务在构造时发布进程级快照。
func SetCodexOriginatorNormalizationEnabled(enabled bool) {
	codexOriginatorNormalization.Store(enabled)
}

// enforceCodexIdentityHeaders 收口 OAuth（ChatGPT 内部接口）出站请求的客户端身份头。
//
// 上游有两道与身份相关的门：
//  1. 配对校验：originator 必须与 User-Agent 首段（首个 '/' 之前的 client 名）配套且为官方
//     客户端标识，version 头（若携带）不低于 codexUpstreamMinVersion，任一不满足即 404
//     （issue #3901）。旧「非 Codex UA 安全兜底」正好制造错配：把官方 UA 强改为固定客户端名，
//     却保留客户端自报的 originator。
//  2. 优先级降载：容量紧张时按客户端身份分优先级降载，被降载的请求 HTTP 200 后立刻以
//     server_is_overloaded 收尾。
//
// 默认（归一化开启）直接改写为网关的规范身份，同时满足两道门；关闭后仅做配对与版本校正。
//
// 仅对携带 originator 的请求生效——compat messages bridge 故意不带 originator，保持原样。
// 必须在所有 User-Agent 改写（自定义 UA / ForceCodexCLI / 浏览器 UA 兜底）之后调用。
func enforceCodexIdentityHeaders(h http.Header) {
	if h == nil || h.Get("originator") == "" {
		return
	}
	if !codexOriginatorNormalization.Load() {
		pairCodexIdentityHeaders(h)
		return
	}
	h.Set("user-agent", codexCanonicalUserAgent())
	h.Set("originator", openai.CodexDefaultOriginator)
	h.Set("version", resolveCodexClientVersion())
}

// pairCodexIdentityHeaders 是关闭归一化后的兜底收口：保留客户端真实身份，
// 仅保证 originator 与最终 User-Agent 首段配套、version 不低于上游门槛（issue #3901）。
func pairCodexIdentityHeaders(h http.Header) {
	originator, pairedUA, ok := openai.PairCodexClientIdentity(h.Get("user-agent"))
	if !ok {
		originator, pairedUA = openai.CodexDefaultOriginator, codexCanonicalUserAgent()
	}
	h.Set("user-agent", pairedUA)
	h.Set("originator", originator)
	if v := strings.TrimSpace(h.Get("version")); v != "" && CompareVersions(v, codexUpstreamMinVersion) < 0 {
		h.Set("version", resolveCodexClientVersion())
	}
}
