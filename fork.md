# sub2api fork 协作与合并偏好（长期约定）

## 结论

本文件用于固定 fork 协作策略、冲突处理口径与个人偏好，目标是：同步 upstream 时尽量吸收有效更新，同时避免改到本地不希望变化的实现与行为。

## 2026-08-10 `v1.4.6` API Key 入参校验、apicompat 与日志退避定向回灌

### 本次取舍

- 定向回灌 upstream 三条边界明确、彼此独立的修复：
  - `f5c108c83`：API Key 的 quota / rate limit 拒绝 NaN、Inf 与负数，`expires_in_days` 必须大于零。纯新增校验，本地 `CreateAPIKeyRequest`/`UpdateAPIKeyRequest` 字段与 upstream 完全一致。
  - `64090de66`：Responses→Anthropic 转换显式跳过 `reasoning` item，未知 role/type 改走 `convertResponsesUserToAnthropicContent` 白名单，并丢弃转换后为空内容或纯空白 text 的消息（Fixes upstream #5329）。
  - `e687ca3e9`：系统日志落库连续失败后按 2s→60s 指数退避暂停写入，避免日志这类尽力而为的观测数据长期占用数据库连接池。
- 合并冲突只做减法，不引入无关能力：
  - `api_key_service.go` 仅补 `math` import，不引入本地未使用的 `sort`。
  - `ops_system_log_sink.go` 只吸收退避常量、`flushBackoffFor` 与 run 循环接线，丢弃 upstream 同文件的 `host` 字段与 `normalizeSystemLogHost`（本地 sink 无 host 概念）。
- 本轮**不**回灌 `358e4a89a`（个人订阅到期时间被 POID workspace entitlement 覆盖）。该修复依赖本地缺失的前置链：`eba204632`（个人订阅端点 `fetchChatGPTSubscriptionExpiresAt` + wire DI 改为 `ProvideOpenAIOAuthService`）与 `d0b8760eb`（`shouldApplyChatGPTAccountInfoPlanType` 保护 plan_type），且与本地已有的 `selectChatGPTAccountInfo` 重构存在冲突，需手工调和。留待 v1.4.7 单独移植与验证。
- 继续保留本地 image 生图链路与单体 OpenAI 网关结构；upstream 的 Grok 产品线、channel-monitor-v2、response-model 计费系列本轮均不吸收。

### 原因与影响

- 三项均为低耦合修复，不改变模型路由、定价配置、调度策略与 image 产品行为。
- `64090de66` 价值最高：带 content 数组的 reasoning item 会被原样透传给 Anthropic 换回 400，且该 item 常驻会话历史，导致此后每一轮持续失败。
- `e687ca3e9` 改变了日志落库的失败行为——连续失败时会主动暂停写入而非立即重试，健康路径不受影响（已有 `TestOpsSystemLogSinkHealthyPathNeverSuppressed` 覆盖）。
- upstream 已 revert 的风控 fail-closed（`e01c917a9` → `af6928a26`）两侧都不吸收；nanoid 本地 `3.3.18` 已高于 upstream 的 `3.3.17`，无需跟随。

### 验证

- `gofmt -l`（本次改动文件全部干净）、`golangci-lint run ./...` 0 issues。
- `make -C backend test-unit`、`make -C backend test-integration` 全绿。
- `make test-frontend`（vue-tsc typecheck + vitest 78 tests）全绿。
- `/bin/sh deploy/tests/docker-runtime-resources-test.sh` 通过。
- 新增用例：API Key 校验 4 条、apicompat invalid blocks 9 条、日志退避 6 条，均已确认实际执行（注意本仓库单测带 `//go:build unit`，须用 `-tags=unit` 运行）。

### 回退方式

- 三条修复互相独立，可按需单独 `git revert` 对应提交，不改写已发布的 tag。
- 若日志退避在极端场景下不合适，单独回退 `ops_system_log_sink.go` 与其测试即可，不影响另外两项。

## 2026-08-09 `v1.4.5` OpenAI 流终止恢复定向回灌

### 本次取舍

- 按依赖顺序定向回灌 upstream `47ad29db3`、`da49ce3f2`、`30d2589ef`：
  - HTTP SSE 流异常断开后隔离对应代理，减少连续选择同一故障路径。
  - 代理隔离执行 burst collapse 与无容量时 fail-open，避免单次 HTTP/2 连接事故被重复计数或把全部容量隔离成 502。
  - WS ingress lease 丢失时，downstream terminal event 写入使用独立的客户端生命周期 context，避免 lease cancellation 抢先截断终止事件。
- `30d2589ef` 依赖本地原先不存在的 ingress lease 生命周期；同步移植 `c8cfc9363` 所需的最小 Redis lease、刷新、释放与每 API Key 连接上限，不引入其余无关重构。
- 保留本地 image 生图链路和既有 Codex manifest 实现；遇到冲突仅吸收上述流恢复语义，不引入无关身份、路由或 UI 改造。

### 原因与影响

- `v1.4.4` 只修复了 Codex 0.147.0 模型清单缺少 `display_name` 的确定性错误，不包含这三条流恢复提交。
- HTTP SSE 代理隔离会改变故障代理的短期调度行为；fail-open 保证没有健康候选时仍尝试被隔离代理，而不是直接拒绝请求。
- lease loss 修复只作用于 WebSocket ingress；当前 HTTP SSE 客户端不直接走此路径，但合入可补齐上游已验证的 terminal event 保护。

### 回退方式

- 按反向顺序正常 `git revert` lease-loss、fail-open、proxy-quarantine 三个回灌提交；不改写已经发布的 tag。

## 2026-08-09 `v1.4.4` Codex 模型清单兼容热修

### 本次取舍

- 标准 `/v1/models` 转换后的 Codex 条目同时写入 `slug` 与必需的 `display_name`，避免 Codex 0.147.0 因模型清单解码失败持续重连。
- 不顺带合并 WS、代理熔断或其他大范围 upstream 改造；当前客户端使用 HTTP SSE，且线上 `/v1/responses` 连续验证均正常收到 `response.completed`。

### 原因与影响

- `v1.4.3` 已解决 API Key 账号被错误要求 OAuth 的问题，但转换结果只有 `slug`；Codex 0.147.0 会拒绝该清单并报告缺少 `display_name`。
- 本次仅补齐客户端必需字段，不改变模型 ID、模型路由、Responses 流式协议或 image 产品行为。

### 回退方式

- 如需回退，正常 `git revert` 本次热修提交即可；回退后 API Key 模型清单仍可请求，但 Codex 0.147.0 会恢复清单解码失败。

## 2026-08-09 `v1.4.3` Codex 模型清单与高价值修复定向回灌

### 本次取舍

- 不整包合并 `upstream/main`，仅按本地结构移植 Codex API Key 模型清单修复：
  - OpenAI OAuth 账号继续透传 ChatGPT Codex manifest。
  - 自定义 API Key 账号改为请求账号自身的 `/v1/models`，不再错误要求 OAuth。
  - 普通 OpenAI `data[].id` 模型列表转换为 Codex `models[].slug` envelope。
  - API Key 清单增加短缓存、ETag、并发刷新合并、过期清单后台刷新和失败换号。
  - `gpt-5.6-sol/terra/luna` 在自定义 API Key 清单中关闭 `use_responses_lite`，保留完整 Responses 工具能力。
- 同步 OAuth pending exchange 账号接管修复，仅允许已完成身份所有权证明的终态登录或当前登录用户主动绑定执行 adoption。
- 同步计费金额 `NUMERIC(20,8)` 统一量化，避免余额扣减与 API Key 累计用量在 half 边界产生 1e-8 对账偏差。
- 同步上游 TCP/TLS 和 SOCKS5 建连超时，避免不可达上游或代理把串行故障转移拖到内核重传超时。
- 同步 `nanoid` 安全升级至 `3.3.18`，关闭 `GHSA-2v37-7h3g-55p8` 高危审计项，不新增长期豁免。
- 继续保留本地 image 生图链路，本次不吸收 upstream 的 Agent Identity、Codex 身份体系、URL 路径护栏和大范围 WS/路由重构。

### 原因与影响

- 直接触发原因是 Codex 自动请求 `/v1/models?client_version=...` 时，本地实现把自定义 API Key 上游误判成 OAuth-only manifest，导致请求在本地返回 502。
- 本次让模型发现服从已配置的自定义上游，不要求修改第三方 `input` 上游；只要其支持标准 `GET /v1/models` 即可。
- 安全、计费和建连修复均为边界明确的高价值变更，不改变既有模型路由、定价配置和 image 产品行为。

### 回退方式

- 若 API Key 模型清单出现兼容问题，优先回退 `openai_codex_models_service.go`、对应测试以及 `OpenAIGatewayService` 中的 manifest cache 字段，OAuth 原路径可独立恢复。
- 若建连超时对极慢网络不合适，可单独回退 `proxyutil` 和 `http_upstream` 的 dial/TLS timeout，不影响业务层逻辑。
- 若整版需要撤回，使用正常 `git revert` 回退 `v1.4.3` 对应提交，不改写已经发布的 tag 历史。

### 工作方式

- 优先真实落地：UI 要看真实页面与保存链路，不把“后端有字段”当完成。
- 任务完成后，若验证通过，默认继续做到收尾动作（例如 commit/push/发布相关步骤），不做冗余停顿。
- 遇到 cleanup 或回归修复，优先定向补洞，不把你明确不要的旧逻辑加回来。

### 质量与验证

- 修改前先定位影响范围与依赖。
- 修改后不得留下无效代码、半删除状态、占位实现。
- 默认补齐或更新测试，并提供可复现验证命令与结果摘要。
- 收尾默认给修改清单、报错对应修复点、验证结果和当前状态。
- 必须全盘跑lint和ci！

### 仓库与发布偏好

- 仓库维护类任务默认先同步主线并检查状态（`git pull --ff-only`、`git log`、`git status`）。
- 网络或推送异常时，默认主动排查并完成发布链路，不把基础操作回抛。
- Git 提交邮箱默认使用 `31232741+atoz03@users.noreply.github.com`。

## upstream 合并策略（fork 专用）

### 总原则

- 其他模块照常合并，尽量吸收 upstream 的非冲突更新。
- 对你明确指定“本地优先”的模块执行白名单保留策略。

### 当前固定保留项（高优先级）

- image 生图链路保留本地实现为主。
- 对应冲突文件优先保留本地版本：
- `backend/internal/service/openai_images.go`
- `backend/internal/service/openai_images_test.go`
- `backend/internal/service/openai_codex_transform.go`
- `frontend/src/components/account/AccountTestModal.vue`
- `frontend/src/components/admin/account/AccountTestModal.vue`
- `frontend/src/components/admin/account/__tests__/AccountTestModal.spec.ts`
- `frontend/src/views/admin/GroupsView.vue`

### 冲突与冗余处理

- 若 upstream 引入与本地 image 主线重复的新拆分文件（例如将逻辑拆到新增文件），优先避免双实现并存。
- 处理顺序：
- 先判断是否与本地主链路重复或引入行为漂移。
- 能局部吸收的安全修复（边界保护、测试补洞）再定向移植。
- 会改变既有行为的改动先记录、再由你决定是否接入。

## 2026-04-25 `upstream/main -> main` 合并决议

### 本次取舍

- `backend/internal/service/openai_gateway_service.go`：保留本地 strict-priority 与 `normalizeCodexModel` 逻辑，同时吸收 upstream 的 compact 排序与 SSE->JSON 透传转换能力。
- `backend/internal/service/openai_account_scheduler.go`：保留本地 strict-priority 筛选，再吸收 upstream 的 compact 候选分层与统计字段（`candidateCount/loadSkew`）。
- `backend/internal/handler/openai_gateway_handler.go`：保留本地 fallback `prompt_cache_key/session hash` 兼容逻辑，同时吸收 upstream 的 `requireCompact` 路径变量。
- `frontend/src/views/admin/SettingsView.vue`：保留本地页面结构，避免 upstream 冲突块把 defaults 区域错误插入 turnstile 区。
- `frontend/src/components/account/AccountTestModal.vue`：吸收 upstream 版本，保留 openai test mode 与 `supportsImageTest` 行为。
- `backend/internal/service/openai_codex_transform.go`：补齐 `gpt-5.5` 归一化映射，避免显式模型被误判成 group default 回落。

### 原因与影响

- 目标是保持本地既有产品行为不漂移（尤其是设置页布局、Codex 模型路由与兼容逻辑），同时吸收 upstream 的新能力（compact 选择和 Responses 透传补全）。
- 风险点集中在 OpenAI 网关路径；已通过后端回归覆盖 `service/handler/server` 关键包，当前未发现回归失败。

### 回退方式

- 若线上观察到网关行为异常，优先回退 merge commit（`git revert -m 1 <merge_commit_sha>`），保持主线历史可追踪。
- 如仅个别冲突决议有问题，可在该 revert 基础上定向 cherry-pick 本地确认稳定的修复提交，避免整仓回退。

## 2026-04-30 `upstream/main -> main` 合并决议

### 本次取舍

- 合并方式固定为 `git merge --no-ff upstream/main`，保留合并历史，不做 rebase。
- `backend/cmd/server/VERSION` 保持本地 `1.2.4` 体系，不跟随 upstream 的 `0.1.120` 版本线。
- 设置链路与页面展示口径保持本地为主：
  - `frontend/src/views/admin/SettingsView.vue`
  - `frontend/src/api/admin/settings.ts`
  - `backend/internal/handler/admin/setting_handler.go`
  - `backend/internal/handler/dto/settings.go`
  - `backend/internal/service/setting_service.go`
- OpenAI 关键链路按“本地优先 + 定向吸收安全修复”处理：
  - `backend/internal/service/openai_gateway_service.go`：保留本地主流程，不直接整包切到 upstream 结构。
  - `backend/internal/service/openai_gateway_messages.go`：吸收 `normalizeOpenAIModelForUpstream` 与 fast policy 相关低侵入修复。
  - `backend/internal/service/openai_ws_forwarder.go`：吸收显式 tool replay 与 item_reference/previous response 相关修复。
  - `backend/internal/service/openai_codex_transform.go`：保留本地 `gpt-5.5-*` 细分映射，同时保留 upstream 的 Responses 兼容字段修复（如 tool_choice 与 reasoning 过滤）。

### 原因与影响

- 目标是维持现网行为稳定（版本体系、设置页显示与保存口径、OpenAI 本地既有策略）并吸收 upstream 的通用修复能力。
- 风险仍集中在 OpenAI 网关与 WS 续链路径；因此以最小行为漂移为优先，不做大规模结构重排。

### 回退方式

- 优先整体回退本次 merge：`git revert -m 1 <merge_commit_sha>`。
- 若仅单点回归，可在回退后按文件级别 cherry-pick 已验证修复，避免再次引入整包行为变化。

## 2026-05-01 CI 残留问题修复（回滚后）

- 根因：回滚到 `de83d5e8` 后，`gateway_handler.go` 带入 antigravity 依赖但对应实现未纳入，且同时存在 openai images/session hash、public settings 字段、wire 生成文件与构造签名漂移。
- 修复：`gateway_handler.go` 回到 `10d7deca` 的无-antigravity 版本；补齐 `GenerateExplicitSessionHash` 与 `AffiliateEnabled`（dto + SSR 注入）；更新 `api_contract_test.go` 的 `NewAccountHandler` 参数；重生成 `backend/cmd/server/wire_gen.go`；移除未使用函数 `writeOpenAIFastPolicyBlockedResponse`。
- 验证：`cd backend && go list ./... && go test ./... && golangci-lint run ./...` 通过。

## 2026-05-01 API contract 修复

- 根因：`TestAPIContracts` 仍按旧 settings/usage 快照断言；同时 admin settings 响应遗漏现有 affiliate 与 channel monitor 设置字段，导致真实 API 与 contract 不一致。
- 修复：补齐 affiliate 开关、返利冻结/有效期/单人上限以及 channel monitor/available channels 的 settings 读写与响应映射；usage contract 移除已不存在的 `media_type`；settings contract 补上当前本地设置字段，移除未暴露到 admin settings API 的 `fallback_model_antigravity` 与 `openai_fast_policy_settings` 断言；WeChat OAuth config fallback 保持多通道字段独立，不把 open 配置复制到 legacy/mp/mobile。
- 约束：本次不恢复已删除的 antigravity gateway package/service 逻辑，只修 API contract 与现有 settings 链路。
- 验证：`cd backend && go test -tags=unit ./internal/server -run TestAPIContracts -count=1`、`cd backend && go test -tags=unit ./...`、`cd backend && go list ./... && golangci-lint run ./...` 通过。

## 2026-05-02 Anthropic passthrough 流超时错误分类

- 根因：client disconnect 后继续读取 upstream usage 时，CI 慢环境可能先收到上游 EOF，再处理 idle ticker，导致超时场景被归类成 `missing terminal event`。
- 修复：无 terminal event 且客户端已断开时，若距离最后上游数据已超过 `stream_data_interval_timeout`，统一返回 `stream usage incomplete after timeout`。
- 验证：目标单测连续 20 次通过，`make -C backend test-unit` 通过。

## 2026-05-02 scheduler cache integration 修复

- 根因：合并后保留了 `LastUsedAt` side-key 测试，但实现回退成重写账号 JSON，且测试仍引用旧 `full/slim` key 名。
- 修复：恢复 `sched:acc:last_used:*` 热字段缓存，读取账号与快照时覆盖 `LastUsedAt`；测试改为当前 `account/meta` key 命名，不恢复旧 `full` key。
- 验证：`go test ./...`、`make test-unit`、`make test-integration`、`golangci-lint run ./...` 通过。

## 2026-05-17 `upstream/main -> main` 合并决议

### 本次取舍

- 合并方式保持 `git merge --no-ff upstream/main`，本轮先用 `-X ours --no-commit` 缩小冲突面，再逐项补齐 upstream 新增功能与本地保留链路。
- `backend/internal/pkg/antigravity/*` 继续按本地决议物理删除，不恢复 upstream 引入的 package 依赖；Antigravity User-Agent 只保留在 settings 链路中。
- image 生图链路继续保留本地 responses-only 实现，同时吸收 upstream 对 moderation body / upload data URL 的低侵入补充。
- 设置链路吸收 upstream 的登录协议、GitHub/Google 邮箱 OAuth、auth source 默认赠送、内容审核/风控开关与 payment/Airwallex 相关字段；合并后补齐 service、DTO、SSR 注入 payload、admin contract、SettingsView 类型与保存 payload。
- WebSocket 内容审核按 upstream 新能力接入，但首帧阻断必须在并发槽位与账号选择前完成，避免应返回 policy violation 的请求被误判为 try-again-later。

### 原因与影响

- 目标是吸收 upstream 的安全与设置能力，同时不改变本地已确认的 Antigravity package 删除、OpenAI image 路由和网关行为边界。
- 风险点集中在 settings contract、OpenAI WS 首帧审核、前端设置页保存 payload；已用后端单元/集成、前端 lint/typecheck/关键 vitest 覆盖。

### 回退方式

- 若整体合并引发回归，优先回退本次 merge commit：`git revert -m 1 <merge_commit_sha>`。
- 若仅 settings 或风控链路有问题，优先按字段链路定向回退或修补，避免恢复已删除的 antigravity package。
