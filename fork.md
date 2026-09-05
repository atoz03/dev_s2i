# sub2api fork 协作与合并偏好

本文件固定 fork 的协作策略、冲突处理口径与决议记录。目标：同步 upstream 时尽量吸收有效更新，
同时不改动本地已确认的实现与行为。

- **第一部分「长期约定」**：常驻规则，每次动手前先看这里。
- **第二部分「决议记录」**：按时间倒序（新 → 旧），每条记录 **取舍 / 原因 / 回退**三要素。

---

# 一、长期约定

## 工作方式

- 优先真实落地：UI 要看真实页面与保存链路，不把「后端有字段」当完成。
- 验证通过后默认做到收尾动作（commit / push / 发布），不做冗余停顿。
- 遇到 cleanup 或回归修复，优先定向补洞，不把明确不要的旧逻辑加回来。
- 修改前先定位影响范围与依赖；修改后不得留下无效代码、半删除状态、占位实现。

## 质量与验证

- 默认补齐或更新测试，并给出可复现的验证命令与结果摘要。
- **必须全盘跑 lint 和 CI。**
- 收尾默认给：修改清单、报错对应修复点、验证结果、当前状态。

### 本仓库固有的验证陷阱

| 陷阱 | 说明 |
| --- | --- |
| `golangci-lint` | 必须在**没有** `backend/internal/web/dist` 构建产物时运行（与 CI 的 lint job 一致）。本地残留 dist 会让 staticcheck 对 `internal/server/router.go` 误报 SA4023。 |
| 单元测试 | 带 `//go:build unit`，须用 `-tags=unit` 运行，否则「跑绿了」其实一条没执行。同理，只被 unit 测试引用的 helper 会被 lint 误报未使用，应放进测试文件。 |
| `wire` 代码生成 | 与当前 Go 工具链不兼容（`package "golang.org/x/sys/unix" without types`）。`cmd/server/wire_gen.go` 为**手工同步**：改动 provider 后须逐项核对参数顺序、依赖声明先于使用，并 diff 确认 `provideCleanup` 在 `wire.go` 与 `wire_gen.go` 中签名一致。 |
| Go 版本升级 | 版本号散落在 `backend/go.mod`、3 个 workflow 的硬断言、3 个 Dockerfile、README 徽章。CI **不会**发现 Dockerfile 漏改。清单见 `DEV_GUIDE.md`。 |

## 仓库与发布偏好

- 仓库维护类任务默认先同步主线并检查状态（`git pull --ff-only`、`git log`、`git status`）。
- 网络或推送异常时主动排查并完成发布链路，不把基础操作回抛。
- Git 提交邮箱固定 `31232741+atoz03@users.noreply.github.com`。
- 回退优先 `git revert`，不改写已发布的 tag 历史。

## upstream 合并策略

**总原则**：其他模块照常合并，尽量吸收 upstream 的非冲突更新；对标记「本地优先」的模块执行白名单保留。

**固定保留项（本地优先，高优先级）**

- image 生图链路保留本地实现为主。
- 冲突时优先保留本地版本的文件：
  - `backend/internal/service/openai_images.go` / `openai_images_test.go`
  - `backend/internal/service/openai_codex_transform.go`
  - `frontend/src/components/account/AccountTestModal.vue`
  - `frontend/src/components/admin/account/AccountTestModal.vue` 及其 spec
  - `frontend/src/views/admin/GroupsView.vue`
- settings 链路（DTO / service / handler / `SettingsView.vue` / contract 用例）本地优先：
  新增开关默认走**配置文件**而非 admin settings API，避免动到 settings 契约。
- `backend/internal/pkg/antigravity/*` 已物理删除，不恢复。
- 后端 OpenAI 网关维持单体 `openai_gateway_service.go`，不跟随 upstream 的文件拆分。
- 前端 i18n 维持本地 `en.ts` / `zh.ts` 单文件，不恢复 upstream 的分片结构。

**冲突与冗余处理顺序**

1. 先判断是否与本地主链路重复或引入行为漂移；避免双实现并存。
2. 能局部吸收的安全修复（边界保护、测试补洞）再定向移植。
3. 会改变既有行为的改动**先记录、再决定**是否接入。

**新增开关的命名约定**：一律用**反义命名**（`disable_xxx`）。正向命名的 Go 零值 `false`
会让未经 viper 加载而手工构造的 `Config` 静默关掉全局保护，`viper.SetDefault` 救不了这条路径。

---

# 二、决议记录（新 → 旧）

## 2026-09-04 · GPT-6 Astra 适配

upstream 截至 `b1748c4ea`（09-03）**尚未合入** Astra 支持，相关改动全部停留在开放 PR：
`#6572`（模型注册/能力/计价）、`#6620`（Messages 缓存身份，修 `#6615`）、`#6611`（保留 max 档位，已关闭）。
本轮按本 fork 结构**手工移植**这三条的结论，不 cherry-pick（PR head 在贡献者 fork，且文件布局差异大）。

### 本 fork 特有的缺陷（与 upstream 表现不同，需单独修）

1. **Astra 按 gpt-5.4 计价（静默少收约 60~75%）**。`normalizeKnownOpenAICodexModel("gpt-6-astra")`
   命中不了任何 `gpt-5` 分支返回 `""`，于是 `getFallbackPricing` 返回 nil、`matchOpenAIModel`
   穿过所有分支落到 `DefaultTestModel`(gpt-5.4) 兜底：按 2.5e-6/15e-6 计，而官方是 1e-5/5e-5。
   已补 `openAIGPT6AstraFallbackPricing` 与 `fallbackPrices["gpt-6-astra"]`，
   并把 Astra 分支**放在图片模型判定与默认兜底之前**。
2. **长上下文换档只能靠策略补齐**。本 fork 不解析目录的 `*_above_272k_tokens`（upstream 的
   `530fb20f2` 未回灌），而远端价目仓库**已删除**显式 `long_context_*` 字段——即 5.6 家族现在
   也一样靠 `applyModelSpecificPricingPolicy` 补。因此把 Astra 纳入 `isOpenAIGPT54Model` 这道门
   （该函数名是历史叫法，语义实为「适用 272K 换档政策的 OpenAI 模型」，已加注释）。
   缓存写入 1.25 倍拆出 `openAIModelUsesCacheWritePremium`，不再借 `isOpenAIGPT56Model` 的名字表达。
3. **`reasoning.effort=max` 在用量记录里丢失**。upstream `#6611` 的症状是 `Max → XHigh`，本 fork
   没有 upstream 的 `max → xhigh` 出站归一化（`normalizeOpenAIReasoningEffort` 只用于观测），
   所以症状是 **max 落到 `default:` 分支被丢成空**，记录里干脆没有档位。改为保留 `"max"`。
   ⚠️ 配套护栏：`deriveOpenAIReasoningEffortFromModel` 取模型名最后一段做档位推断，
   `gpt-5.1-codex-max` 的 `-max` 是**型号名的一部分**，会凭空写出 max。已加判定：
   末段为 max 且整名命中已知型号表时不推断。
4. **Codex 清单没有档位，Astra 被 `none` 打死**（= upstream `#6622` 在本 fork 的等价缺陷，
   也是社区帖 lostsheep 第 3 条报障的根因）。API Key 账号的 `/v1/models` 经
   `convertOpenAIModelListToCodexManifest` 只转出 `slug` + `display_name`，Codex 拿不到档位便按
   `reasoning.effort=none` 发起请求，而 Astra 对 none 直接 400
   （`Unsupported value: 'none' ... Supported values are: 'low','medium','high','xhigh','max'`）。
   在 `adjustAPIKeyCodexModelsManifest` 增加 `applyCodexManifestReasoningLevels`：
   声明的档位是网关口径的子集**且**默认档在该声明之内时保持原样，否则**成对**改写为
   `low/medium/high/xhigh/max` + 默认 `medium`。判定必须成对——只补默认档会写出
   `default_reasoning_level ∉ supported_reasoning_levels` 的条目，而 Codex 的 ModelInfo
   要求默认档必须在支持列表里。不含 `none`/`minimal`，也不含 Sub2API 扩展的 `ultra`
   （Astra 无该档）。OAuth 透传的 ChatGPT 原生清单不受影响。

### 取舍与原因

- **谓词统一**：upstream 三个 PR 口径互相打架（`#6572` 精确等值、`#6611` 前缀、`#6620` 明确拒绝日期后缀）。
  本 fork 统一为单一 `isOpenAIGPT6AstraModel`：`gpt-6-astra` 允许任意后缀，裸别名 `gpt-6`
  只放行**已知后缀**（`isKnownCodexModelSuffix`：档位与日期）。裸别名必须收档位后缀——
  前端下发的 OpenCode 配置给 `gpt-6` 带了 low…max 变体，客户端会以 `gpt-6-max` 形式请求；
  若只做精确匹配，`gpt-6-max` 会归一化失败并回落到 gpt-5.4 计价兜底。
  其他 GPT-6 家族（`gpt-6-terra`、`gpt-6.1`）仍判为未知，不并入 Astra 的能力与计费口径。
- **`isKnownCodexModelSuffix` 新增 `max`**：`max` 是 5.6 / Astra 独有档位，OpenCode 会把档位拼进模型名
  （`gpt-6-astra-max`）下发。`gpt-5.1-codex-max` 先命中型号表，不受影响（已加回归用例）。
- **价目表不改 `resources/model-pricing/*.json`**：远端仓库已含 `gpt-6-astra`，静态兜底已覆盖离线场景。

### 明确的行为变化（均为有意）

1. **新增 `gpt-6` 公开别名**（→ `gpt-6-astra`），出现在 `/v1/models`、账号可用模型、白名单与预设映射。
   本 fork 此前**没有**对应的裸 `gpt-5.6` 别名，这是新引入的模式。
2. 用量记录新增 `max` 档位取值（此前 OpenAI 侧只有 low/medium/high/xhigh）。
3. API Key 账号的 Codex 清单会被网关**补写/纠正** Astra 的档位字段（此前原样透传）。

### 不吸收

- upstream 的 `configuredCodexModelDescriptor` 体系（分组模型清单描述符全量生成，含 service tier、
  truncation policy、verbosity 等几十个字段）：本 fork 的清单是 `slug`+`display_name` 的轻量转换，
  整体移植等于引入一个新子系统。本轮只补「会导致请求失败」的档位字段。
- **GPT-5.6 Sol 降价**（`#6565` 的另一半）：远端价目仓库与 upstream 静态兜底当前均仍是
  5e-6 / 3e-5，无可信来源，不动。
- upstream `530fb20f2` 的数据驱动长上下文解析（改动面覆盖整条计费链，需单独决策）。
- 5.6 家族的清单档位补写：5.6 的 `supports_none_reasoning_effort` 为 true，不存在 Astra 那条报错，
  不顺手改动既有行为。

### 已知遗留（本轮有意不动，下次专项处理）

- **`openai_compat_model.go` 的模型名档位拆分不认 `max`**：`gpt-6-astra-max` 走
  `/v1/chat/completions` 时模型名能折叠回 `gpt-6-astra`，但档位提不出来（`max` 落到 `default:`
  返回「不是档位后缀」）。**不能直接加 `case "max"`**：本 fork 没有 upstream 的
  `supportsOpenAIReasoningEffortMax` 门，加了会把 `gpt-5.4-max` 这类请求从「静默忽略」
  变成「上游 400」。要修得连同按型号的 max 支持判定一起加。
- **`apicompat/anthropic_to_responses.go` 把 Anthropic `max` 映射为 `xhigh`**：Astra 有原生 `max`，
  经 `/v1/messages` 进来的最高档会被降一级。属保真度问题，非功能故障。

### 回退

| 项 | 方式 |
| --- | --- |
| 全量 | `git revert` 本次提交；Astra 会回到「按 gpt-5.4 计价 + Codex 报 none 不支持」 |
| 仅计价 | 删除 `openAIGPT6AstraFallbackPricing`、`fallbackPrices["gpt-6-astra"]` 与两处 Astra 分支 |
| 仅清单档位 | 删除 `applyCodexManifestReasoningLevels` 及其调用（`adjustAPIKeyCodexModelsManifest` 内） |
| 仅 max 记录 | `normalizeOpenAIReasoningEffort` 删除 `case "max"`，并同时删除 derive 侧护栏 |
| 仅 gpt-6 别名 | 移除 `codexModelMap["gpt-6"]`、`isOpenAIGPT6AstraModel` 的 `== "gpt-6"` 分支与前端两处条目 |

### 验证

`go test ./...`、`go test -tags=unit ./...`、`go test -tags=integration ./...` 全绿；
`golangci-lint run`（**已先移走** `backend/internal/web/dist` 构建产物）0 issues；
前端 `pnpm lint` / `pnpm typecheck` / `pnpm exec vitest run`（108 文件 646 用例）全绿；`pnpm build` 通过。

## 2026-08-25 · v1.4.9 — OAuth 透传流式修复 + Codex 身份默认值对齐

### 本地缺陷（非回灌，upstream 无对应提交）

报障：OAuth 账号**开启透传**稳定报 `stream closed before response.completed`，同账号不开透传正常。

1. **透传流式收尾字节被丢弃（根因）**。`handleStreamingResponsePassthrough` 全函数只有一处 flush，
   条件是「本行是客户端输出事件」。而 SSE 事件由 data 行之后的**空行**分帧，`data: [DONE]` 与
   `event:` 行都不算客户端输出，于是终止事件的分帧空行、`[DONE]` 及其空行全部留在 4KB bufio
   缓冲里，函数返回时随缓冲丢弃（原生路径在 `finalizeStream` 有收尾 flush，透传没有）。
   客户端拿到的最后一帧永远缺少分帧空行，SSE 解析器不会派发该事件。
   **修复**：改用与原生路径同一不变式 `shouldFlush = clientOutputStarted || lineStartsClientOutput`。
   首个输出**之前**仍只写不 flush——pre-output failover 依赖「客户端尚未收到任何字节」，
   提前 flush 会让换号重试把第二份 `response.created` 追加到同一条流上。
   顺带修掉长推理阶段的静默（reasoning / in_progress 等此前也从不 flush）。
2. **`stream=false` 收到裸 SSE**。`normalizeOpenAIPassthroughOAuthBody` 对非 compact 强制
   `stream=true`（该端点只接受流式，行为保留），但结果被回写成 `reqStream`，把**上游取数方式**
   当成了**客户端意图**，导致「上游返回 SSE 时折叠回 JSON」的分支永远走不到。
   **修复**：只在 compact 分支回写 `reqStream = false`。

两处均加用例，并验证「还原修复后失败」。

### 定向回灌

- **指纹收敛默认 `session` → `off`**（upstream `fce41e318`）。原默认让**升级动作本身**就静默改写
  每个存量 OAuth 账号的 `installation_id` / `session_id` / `thread_id`；upstream 有对应的额度回退
  报障与 A/B 回滚证据（#5555 / #5556 / #5582）。
  ⚠️ 三个账号弹窗（Edit / Create / BulkEdit）的持久化判定必须**跟随默认值成对翻转**为
  `=== 'off'`；沿用旧的 `'session'` 判定会把管理员**显式选择**的 session 当成默认值删掉。
- **出站默认 originator `codex_cli_rs` → `codex-tui`**（upstream `dbb42881c`），**修正 v1.4.8 的选择**。
  v1.4.8 依据 upstream `e1b76e224`（08-02）的降载桶快照选了 `codex_cli_rs`，而该提交自己就写明
  「降载桶集合是上游容量策略快照而非协议常量」——一条声明了会过期的前提。upstream 5 天后翻转为
  `codex-tui` 并持续三周四次身份提交保持该口径；本 fork 自己的注释也记着 codex-tui 是「真实流量
  占比最高的一支」。拆成 `CodexCLIOriginator`（仅用于识别）与 `CodexDefaultOriginator`（出站默认），
  四处硬编码站点改为引用常量。
- **流内降载错误码改写**（upstream `c33c3208e` 的客户端改写部分）。`server_is_overloaded` /
  `slow_down` 在 Codex CLI 属**致命集**，收到即终止会话。已无法改走 failover 时改写为
  `server_error`（消息与其余字段原样保留）；监控、计费与账号状态判定一律基于**改写前**的原始事件。
- **TLS 指纹链路建连超时**（与 upstream `66ad405dd` 同类，upstream 只修了非指纹路径）。
  `tlsfingerprint/dialer.go` 仍有三处零值 `net.Dialer`（无上限，只能等内核 TCP 重传约 130 秒），
  且 SOCKS5 分支调用忽略 ctx 的 `Dial`。统一改 10s 超时并新增 `dialSOCKS5Context` 优先走 `DialContext`。
- **dompurify 3.3.1 → 3.4.14**（upstream `4a1da2950`）+ pnpm override。该库用于公告、自定义页面、
  法务文档与 SVG 净化，均经 `v-html` 渲染，绕过 sanitizer 可直接形成存储型 XSS。

### 不吸收

- upstream 把指纹收敛**扩展到透传路径**：本 fork 的 `buildUpstreamRequestOpenAIPassthrough`
  从来不做指纹改写，扩展它属于新增功能。默认翻转后本 fork 是严格更保守的一侧。
- upstream 的 `RequestScopedTransient` + `TempUnscheduleRetryableError` 守卫：本 fork 的
  `tempUnscheduleGoogleConfigError` / `tempUnscheduleEmptyResponse` 早已是空实现，
  「一个降载请求沿 failover 把整池账号摘掉」在本 fork 不成立，加守卫等于新增死代码。
- `fcd3bc127`（无账号支持模型时返回 404 `model_not_found` 而非 503）+ 其依赖 `6b0ec50f2`：
  689 行 / 13 文件，新增 `no_account_error.go` 与两套 model availability 服务，且**改变对客户端的
  错误契约**，需单独决策。
- upstream 的 Codex 身份体系重构（`fce41e318` / `6793d5ac8` / `bb6c3b4f6` 把凭据面统一到推理解析链、
  `d493ce0bb` 按账号收窄、面板 UA 设置、`resolveCodexOutboundIdentity` 解析链）：与既有决议一致，
  只取其**默认值结论**（见上「定向回灌」），不吸收结构。

### 记一笔重要事实

报障者贴出的 404 文案 `Model "gpt-5.6-sol" is not supported by any configured account in this group`
**只可能来自包含 upstream `fcd3bc127` 的构建**（`git log -S` 已验证：在 `upstream/main`，不在本 fork）。
其部署是「代理的代理」——下一跳是运行 upstream 版本的 sub2api。因此 OAuth 专属修复（身份、指纹、
版本同步）对这类 API Key + `base_url` 账号**不生效**（`enforceCodexIdentityHeaders` 只在
`account.Type == AccountTypeOAuth` 下调用），生效的是流式 flush、建连超时与前端依赖。

### 回退

| 项 | 方式 |
| --- | --- |
| 透传 flush | `shouldFlush` 改回 `else if lineStartsClientOutput` 单条件 |
| stream 意图 | `if compactPath { reqStream = false }` 改回 `reqStream = gjson...Bool()` |
| 指纹默认值 | `GetCodexFingerprintMode()` 的 `default:` 改回 `codexFingerprintSession`，**并成对翻回**三个弹窗判定 |
| originator | `CodexDefaultOriginator` 改回 `"codex_cli_rs"`；或运行期 `gateway.disable_codex_originator_normalization: true` |
| 降载码改写 | 删除两条流式路径上的 `rewriteOpenAICapacityShedErrorCodeForClient` 调用 |
| 建连超时 | `tlsfingerprint/dialer.go` 的 `newBaseDialer()` 改回零值 `net.Dialer` |

## 2026-08-25 · v1.4.8 — Go 1.26.6 安全基线 + Codex 出站身份收口

### 取舍与原因

- **Security Scan 红灯**：根因是 Go 1.26.5 标准库 6 条漏洞（`GO-2026-6218` net/url、`GO-2026-6090`
  crypto/tls、`GO-2026-6089` net/http、`GO-2026-6088` encoding/xml、`GO-2026-5972` encoding/asn1、
  `GO-2026-5026` net/http），全部 `Fixed in go1.26.6`。跟随 upstream `11e1e2288` 升到 **1.26.6**
  （go.mod + 3 workflow + 3 Dockerfile + README + `DEV_GUIDE.md`）。
  不跟随 Go 1.27.0（`cbe258fd1`）：带 jsonv2 适配与 golangci-lint v2.13 升级，超出「只修安全红灯」边界。
- **出站 Codex 身份陈旧且自相矛盾**导致上游 404 与降载。回灌 upstream `8a51119e3`（issue #3901
  配对校验）与 `e1b76e224`（按 originator 分桶降载）的**语义**，不吸收其身份体系重构：
  - `codexCLIVersion` `0.125.0` → `0.149.1`（上游对携带且低于 `0.144.0` 的 version 头一律 404）。
  - `codexCLIUserAgent` 改为官方形态 `<originator>/<ver> (Ubuntu 22.4.0; x86_64) xterm-256color`；
    裸 `originator/version` 形态易被判为非官方客户端。
  - `request.go` 补齐官方客户端识别：新增 `codex-tui/` 与 `codex_vscode_copilot/`；originator 改为
    **精确集合 + `Codex ` 家族前缀**；新增 `PairCodexClientIdentity` / `codexUATrailerName`
    （从 UA 尾部 `(name; version)` 恢复被 `CODEX_INTERNAL_ORIGINATOR_OVERRIDE` 改写的真实身份）。
  - 新增 `openai_codex_identity.go` 作为唯一收口点 `enforceCodexIdentityHeaders`，接入 HTTP 转发、
    OAuth 透传、WS 握手、用量探针、账号 responses / 生图测试、Codex 模型清单共七条出站路径。
- **Codex 客户端版本号自动同步**（只取后端内核）：硬编码版本号会自然腐化，而上游对陈旧版本既有
  404 门槛又会优先降载，「版本号停更」等价于慢性故障。移植 upstream `2eb24814f` / `2d3e84520` /
  `4c4ff3638` 的同步内核，**不吸收其管理面**：
  - `OpenAICodexVersionSyncService` 每 6 小时从 `openai/codex` 取最新稳定版。主路径
    `/releases/latest`（约 0.3MB，本身排除 draft/prerelease）；latest 属于同仓库其他组件
    （如 `rusty-v8-*`）被 `rust-v` 前缀挡掉时，回退扫一页 release（`per_page=30`，实测 30 条里
    仅 3 条稳定版，不能再调小）。两条路径共用同一套过滤。
  - **只向前推进**，抓取失败保持既有值（不清空、不降级）。启动防抖借设置行 `UpdatedAt` 判断，
    避免滚动发布把「启动即同步」放大成对 GitHub 的连续请求。
  - 复用既有 `GitHubReleaseClient`（新增 `FetchRecentReleases` 并加 32MiB 读取上限），不新增 HTTP 栈。
  - 同步值写入 `SettingKeyOpenAICodexClientVersionSynced`，**不在 admin settings API 暴露**，
    settings 契约不受影响；开关走配置文件 `gateway.disable_codex_version_auto_sync`。
  - 生效版本解析链：**同步值 → 进程内高水位 → `codexCLIVersion` 常量**（常量始终是地板）。
    高水位把「只向前推进」延伸到读取侧：数据库抖动让 provider 返回空值时，缓存会把空值存住一个
    TTL，若无高水位，出站版本会在这段时间退回编译期常量。热路径读取用 3s 超时。
  - 清单请求的 `client_version` 缺省值也改用生效版本，否则同步推进后会用旧版本号取 manifest，
    拿到少了新模型的清单，重新制造「模型发现不到」。

### 明确的行为变化（均为有意）

1. **出站身份归一化默认开启**：OAuth 出站的 `user-agent` / `originator` / `version` 一律改写为网关
   规范身份，不再向上游暴露客户端真实 Codex 类型。需保真透传的部署可关开关。
   （身份取值已在 v1.4.9 改为 `codex-tui`。）
2. **`codex_cli_only` 访问门收紧**（安全）：原 `"codex "` 前缀经 `TrimSpace` 退化成裸 `"codex"` 并走
   `Contains` 兜底，`evil-codex_thing`、`Mozilla/5.0 codex bypass` 之类伪造标识都被判为官方客户端
   （本地实测复现）。改为独立的 `codexOfficialClientFamilyPrefix`（保留空格、只做 HasPrefix）
   ＋ originator 精确集合，访问门改用 `IsCodexOfficialClientRequestStrict`。透传路径保留宽松版。
3. **账号生图测试的 `originator=opencode`** 与 Codex UA 错配，上游一律 404，该测试原本恒失败；已收口。

### 不吸收

- `280c1c862`（空 `response.completed` 失败转移）：本 fork 标准流式路径是 `handleStreamingResponse`，
  与 upstream 的 `handleStreamingResponseWithReasoning` 结构不同且缺 `openAIStreamEventTypeIsTerminal`；
  只移植透传半边会让两条流式路径口径不一致。
- Grok 产线、channel-monitor-v2、Responses Lite 系列、Go 1.27.0。

### 回退

- 运行时（不改代码）：`gateway.disable_codex_originator_normalization: true` 退回「保留客户端身份 +
  仅保证配对与版本门槛」；`gateway.disable_codex_version_auto_sync: true` 停止出网拉取（离线部署也用
  这个），已同步值仍生效。
- 代码：身份识别改动、七处收口调用、版本常量互相独立，可单独 `git revert`；未新增数据库字段。
- Go 1.26.6 与身份收口彼此独立；回退 Go 会让 Security Scan 重新变红。

## 2026-08-13 Codex OAuth thread 共享指纹收敛定向回灌

- 回灌 upstream PR #5553（`c0ab3a00e` + 测试修正 `04f8cdb19`），用于多人共享同一 OAuth 账号时收敛
  Codex 设备 / 会话 / thread 指纹。保留 upstream 四档账号级策略：`off` / `device` / `session` / `full`。
- **原因**：共享账号原先把各客户端不同的 `installation_id`、`session_id`、`thread_id` 全部暴露给上游。
- 配置只写账号 `extra.codex_fingerprint_mode`，无数据库迁移；API Key、compact 与其他平台不受影响。
- 不恢复已拆除的 `openai_gateway_forward.go`，接线迁移到现有入口；前端翻译继续维护在本地单文件 i18n。
- **回退**：回退指纹 helper、网关接线、三个弹窗与翻译即可；临时停用可把相关账号批量设为 `off`。
- ⚠️ **后续修正**：当时的默认值 `session` 已在 v1.4.9 改为 `off`（详见该条）。

## 2026-08-10 · v1.4.6 — API Key 入参校验、apicompat 与日志退避

- 回灌三条边界明确、彼此独立的 upstream 修复：
  - `f5c108c83`：API Key 的 quota / rate limit 拒绝 NaN、Inf 与负数，`expires_in_days` 必须大于零。
  - `64090de66`（价值最高）：Responses→Anthropic 转换显式跳过 `reasoning` item，未知 role/type 改走
    白名单，并丢弃转换后为空的消息。带 content 数组的 reasoning item 会被原样透传给 Anthropic 换回
    400，且该 item 常驻会话历史，导致此后每一轮持续失败（upstream #5329）。
  - `e687ca3e9`：系统日志落库连续失败后按 2s→60s 指数退避暂停写入，避免观测数据长期占用连接池。
    这改变了失败行为（主动暂停而非立即重试），健康路径不受影响。
- 合并冲突只做减法：`api_key_service.go` 仅补 `math` import；`ops_system_log_sink.go` 只吸收退避常量与
  接线，丢弃 upstream 的 `host` 字段（本地 sink 无 host 概念）。
- **不回灌** `358e4a89a`（订阅到期被 POID workspace entitlement 覆盖）：依赖本地缺失的前置链
  `eba204632` / `d0b8760eb`，且与本地 `selectChatGPTAccountInfo` 重构冲突。
- upstream 已 revert 的风控 fail-closed（`e01c917a9` → `af6928a26`）两侧都不吸收。
- **回退**：三条互相独立，可单独 `git revert`。

## 2026-08-09 · v1.4.5 — OpenAI 流终止恢复

- 按依赖顺序回灌 upstream `47ad29db3`、`da49ce3f2`、`30d2589ef`：
  - HTTP SSE 流异常断开后隔离对应代理，减少连续选择同一故障路径。
  - 代理隔离执行 burst collapse 与无容量时 **fail-open**，避免单次 HTTP/2 事故被重复计数，或把全部
    容量隔离成 502。
  - WS ingress lease 丢失时，downstream terminal event 改用独立的客户端生命周期 context，避免 lease
    cancellation 抢先截断终止事件。
- `30d2589ef` 依赖本地原先不存在的 ingress lease 生命周期；同步移植 `c8cfc9363` 所需的**最小** Redis
  lease、刷新、释放与每 API Key 连接上限，不引入其余重构。
- **回退**：按反向顺序 `git revert` 三个回灌提交。

## 2026-08-09 · v1.4.4 — Codex 模型清单兼容热修

- 标准 `/v1/models` 转换后的 Codex 条目同时写入 `slug` 与必需的 `display_name`。
  v1.4.3 的转换结果只有 `slug`，Codex 0.147.0 会拒绝该清单并持续重连。
- 不顺带合并 WS、代理熔断等大范围改造。
- **回退**：`git revert` 本次热修；回退后 Codex 0.147.0 会恢复清单解码失败。

## 2026-08-09 · v1.4.3 — Codex 模型清单与高价值修复

- 不整包合并 upstream，仅按本地结构移植 Codex API Key 模型清单修复：
  - OAuth 账号继续透传 ChatGPT Codex manifest；自定义 API Key 账号改为请求账号自身的 `/v1/models`，
    不再错误要求 OAuth（直接触发原因：本地把自定义上游误判成 OAuth-only manifest，请求返回 502）。
  - 普通 `data[].id` 列表转换为 Codex `models[].slug` envelope；加短缓存、ETag、并发刷新合并、
    过期清单后台刷新与失败换号。
  - `gpt-5.6-sol/terra/luna` 在自定义 API Key 清单中关闭 `use_responses_lite`，保留完整工具能力。
- 同步 OAuth pending exchange **账号接管修复**：仅允许已完成身份所有权证明的终态登录、或当前登录用户
  主动绑定，才能执行 adoption。
- 同步计费金额 `NUMERIC(20,8)` 统一量化，避免 half 边界产生 1e-8 对账偏差。
- 同步上游 TCP/TLS 与 SOCKS5 建连超时（避免不可达上游把串行故障转移拖到内核重传超时）。
- 同步 `nanoid` → `3.3.18`，关闭 `GHSA-2v37-7h3g-55p8`，不新增长期豁免。
- 不吸收 upstream 的 Agent Identity、Codex 身份体系、URL 路径护栏与大范围 WS/路由重构。
- **回退**：模型清单问题优先回退 `openai_codex_models_service.go` + 测试 + manifest cache 字段，
  OAuth 原路径可独立恢复；建连超时若对极慢网络不合适，可单独回退 `proxyutil` 与 `http_upstream`。

## 2026-05-17 `upstream/main -> main` 合并决议

- 合并方式 `git merge --no-ff upstream/main`，本轮先用 `-X ours --no-commit` 缩小冲突面，再逐项补齐。
- `backend/internal/pkg/antigravity/*` 继续物理删除；Antigravity User-Agent 只保留在 settings 链路。
- image 生图链路保留本地 responses-only 实现，同时吸收 upstream 对 moderation body / upload data URL
  的低侵入补充。
- settings 吸收 upstream 的登录协议、GitHub/Google 邮箱 OAuth、auth source 默认赠送、内容审核/风控开关
  与 payment/Airwallex 字段；合并后补齐 service、DTO、SSR 注入 payload、admin contract、SettingsView
  类型与保存 payload。
- WebSocket 内容审核按 upstream 新能力接入，但**首帧阻断必须在并发槽位与账号选择前完成**，
  避免应返回 policy violation 的请求被误判为 try-again-later。
- **回退**：优先 `git revert -m 1 <merge_commit_sha>`；若仅 settings 或风控链路有问题，按字段链路定向
  回退，避免恢复已删除的 antigravity package。

## 2026-05-02 scheduler cache integration 修复

- 根因：合并后保留了 `LastUsedAt` side-key 测试，但实现回退成重写账号 JSON，且测试仍引用旧
  `full/slim` key 名。
- 修复：恢复 `sched:acc:last_used:*` 热字段缓存，读取账号与快照时覆盖 `LastUsedAt`；测试改为当前
  `account/meta` key 命名，不恢复旧 `full` key。

## 2026-05-02 Anthropic passthrough 流超时错误分类

- 根因：client disconnect 后继续读取 upstream usage 时，CI 慢环境可能先收到上游 EOF 再处理 idle
  ticker，导致超时场景被归类成 `missing terminal event`。
- 修复：无 terminal event 且客户端已断开时，若距最后上游数据已超过 `stream_data_interval_timeout`，
  统一返回 `stream usage incomplete after timeout`。

## 2026-05-01 API contract 修复

- 根因：`TestAPIContracts` 仍按旧 settings/usage 快照断言，且 admin settings 响应遗漏 affiliate 与
  channel monitor 字段。
- 修复：补齐 affiliate 开关、返利冻结/有效期/单人上限、channel monitor/available channels 的读写与
  响应映射；usage contract 移除已不存在的 `media_type`；settings contract 移除未暴露到 admin API 的
  `fallback_model_antigravity` 与 `openai_fast_policy_settings`；WeChat OAuth config fallback 保持
  多通道字段独立。
- 约束：不恢复已删除的 antigravity gateway package/service，只修 contract 与现有 settings 链路。

## 2026-05-01 CI 残留问题修复（回滚后）

- 根因：回滚到 `de83d5e8` 后，`gateway_handler.go` 带入 antigravity 依赖但实现未纳入，且存在
  openai images/session hash、public settings 字段、wire 生成文件与构造签名漂移。
- 修复：`gateway_handler.go` 回到 `10d7deca` 的无-antigravity 版本；补齐 `GenerateExplicitSessionHash`
  与 `AffiliateEnabled`；更新 `api_contract_test.go` 的 `NewAccountHandler` 参数；重生成 `wire_gen.go`；
  移除未使用的 `writeOpenAIFastPolicyBlockedResponse`。

## 2026-04-30 `upstream/main -> main` 合并决议

- 合并方式固定 `git merge --no-ff upstream/main`，保留合并历史，不做 rebase。
- `backend/cmd/server/VERSION` 保持本地 `1.2.x` 体系，不跟随 upstream 的 `0.1.x` 版本线。
- settings 链路（`SettingsView.vue`、`api/admin/settings.ts`、`setting_handler.go`、`dto/settings.go`、
  `setting_service.go`）保持本地为主。
- OpenAI 关键链路「本地优先 + 定向吸收安全修复」：`openai_gateway_service.go` 保留本地主流程；
  `openai_gateway_messages.go` 吸收 `normalizeOpenAIModelForUpstream` 与 fast policy 低侵入修复；
  `openai_ws_forwarder.go` 吸收显式 tool replay 与 item_reference/previous response 修复；
  `openai_codex_transform.go` 保留本地 `gpt-5.5-*` 细分映射，同时保留 upstream 的 Responses 兼容字段修复。
- **回退**：优先 `git revert -m 1 <merge_commit_sha>`；单点回归则按文件级 cherry-pick 已验证修复。

## 2026-04-25 `upstream/main -> main` 合并决议

- `openai_gateway_service.go`：保留本地 strict-priority 与 `normalizeCodexModel`，吸收 upstream 的
  compact 排序与 SSE→JSON 透传转换。
- `openai_account_scheduler.go`：保留本地 strict-priority 筛选，吸收 compact 候选分层与统计字段
  （`candidateCount` / `loadSkew`）。
- `openai_gateway_handler.go`：保留本地 fallback `prompt_cache_key` / session hash 兼容逻辑，吸收
  upstream 的 `requireCompact` 路径变量。
- `SettingsView.vue`：保留本地页面结构，避免 upstream 冲突块把 defaults 区域错误插入 turnstile 区。
- `AccountTestModal.vue`：吸收 upstream 版本，保留 openai test mode 与 `supportsImageTest` 行为。
- `openai_codex_transform.go`：补齐 `gpt-5.5` 归一化映射，避免显式模型被误判成 group default 回落。
- **回退**：优先 `git revert -m 1 <merge_commit_sha>`；仅个别决议有问题则在 revert 基础上定向
  cherry-pick 已确认稳定的修复。
