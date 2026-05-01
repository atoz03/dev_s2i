# sub2api fork 协作与合并偏好（长期约定）

## 结论

本文件用于固定 fork 协作策略、冲突处理口径与个人偏好，目标是：同步 upstream 时尽量吸收有效更新，同时避免改到本地不希望变化的实现与行为。



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
