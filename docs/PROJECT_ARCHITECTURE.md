# 全项目架构设计（v1）

## 1. 目标与边界

本项目目标是构建账号供给闭环，解决“注册后无人上号导致账号浪费”的问题。

覆盖范围：
- 账号注册与初始写库
- 账号上号与 token 刷新测活
- 管理面板与人工操作
- 自动补号（定时 + webhook）
- 插件侧人工升级 Plus/Team

不覆盖范围：
- Team 邀请 accept 的完整自动化闭环（当前仍有限制）

## 2. 分层与职责

### 2.1 生产层（Producer）

目录：`producers/keygen_colab/`

职责：
- 注册账号（主流程）
- 可选冷启动策略：注册后立即上号
- 推送账号记录到 Supabase `accounts_pool`

### 2.2 管理层（Control Plane）

目录：`apps/go_team_api/`

职责：
- 提供账号查询、标记分类、手动测活、升级状态回写接口
- 提供插件统一访问入口（插件不直连 Supabase）
- 作为 worker 的编排与状态查询入口

### 2.3 执行层（Worker）

目录：外置服务（建议独立仓库或独立进程）

职责：
- 领取待上号账号
- 执行上号流程（refresh token 测活 + 必要时外置 OAuth 登录）
- 写回成功/失败、重试与错误信息
- 同时支持定时触发和 webhook 触发

### 2.4 交互层（Extension）

目录：`extensions/lite_login_extension/`（当前开发） + `legacy/checkout_launcher_extension/`（历史）

职责：
- 执行高交互人工流程（例如 Plus/Team 升级）
- 将升级结果回写到 `apps/go_team_api`

### 2.5 协议与遗留层

目录：`legacy/extension_backend/`、`legacy/reference/`、`legacy/上号/`

职责：
- 提供协议核心与历史实现参考
- 作为迁移过渡与调试材料，不作为主控面板

## 3. 数据层设计

主表：
- `public.accounts_pool`：账号主数据与 token 状态
- `public.keygen_runs`：批次运行记录

当前核心字段（`accounts_pool`）：
- 基础：`email/password/status/error/updated_at`
- token：`access_token/refresh_token/id_token/token_alive`
- 测活：`token_check_method/token_expired_at/token_last_refresh/last_token_check/token_len`

## 4. 关键业务流程

### 4.1 常规供给流程

1. notebook 注册账号并写入 `accounts_pool`
2. worker 轮询或事件触发领取账号
3. worker 上号并写回 token 与状态
4. `apps/go_team_api` 提供查询和人工介入操作

### 4.2 紧急冷启动流程

1. notebook 开启“注册即上号”模式
2. 成功账号直接进入可用池
3. 失败账号回落到 worker 重试队列

### 4.3 升级回写流程

1. 插件执行人工升级
2. 调用 `apps/go_team_api` 回写升级成功信息
3. API 校验后写回 `accounts_pool`（例如 `team_subscribed/team_owner`）

## 5. 状态机建议

建议统一状态：
- `registered_pending_login`
- `login_in_progress`
- `ready`
- `login_failed`
- `abandoned`
- `team_subscribed`
- `team_owner`

说明：
- 现存 `normal/team` 可作为兼容态，逐步收敛到统一状态机。

## 6. 自动化调度建议

### 6.1 定时触发（必选）

- 每 1-5 分钟触发 worker 扫描待处理账号。
- 用于兜底，避免 webhook 丢失导致停摆。

### 6.2 webhook 触发（建议）

- 下游缺号时立即请求补号。
- 与定时触发并行，保证实时性和稳定性。

## 7. 并发与幂等原则

- 任务领取需要行级锁（推荐 `FOR UPDATE SKIP LOCKED`）
- 同一账号同一时刻只允许一个 worker 执行
- webhook 使用幂等键避免重复触发
- 结果回写操作必须幂等

## 8. 目录说明（全量）

- `artifacts/`：运行产物、导出结果（非源码）
- `legacy/checkout_launcher_extension/`：结账相关历史插件
- `legacy/extension_backend/`：协议脚本与旧 API 服务
- `apps/go_team_api/`：管理 API、UI、桥接脚本
- `producers/keygen_colab/`：注册与 CPA/OAuth notebook
- `extensions/lite_login_extension/`：轻量登录/升级插件（当前开发）
- `legacy/reference/`：参考与实验脚本
- `infra/scripts/`：迁移与运维脚本
- `infra/supabase/`：数据库 schema
- `workers/`：外置上号 worker 脚本
- `上号/`：历史上号流程快照

## 9. 当前结论

当前架构方向合理，已经具备“生产、管理、消费”分层。  
后续重点是把 worker 的任务编排、定时调度和失败重试规则完全产品化。
