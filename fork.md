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
