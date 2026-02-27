# 账号生产与管理架构设计（v1）

## 1. 目标

建立一个可持续的账号生产与消耗闭环，避免“注册成功但无人后续上号”导致账号浪费。

核心要求：
- 注册与上号解耦，但可在紧急场景下联动。
- 自动补号（定时 + webhook）与人工操作并存。
- 所有状态可追踪、可重试、可审计。

---

## 2. 组件职责

### 2.1 `producers/keygen_colab/register_and_cpa_feed_single.ipynb`（生产者）

- 负责注册账号（默认）。
- 可选：在冷启动/紧急模式下，注册后立即尝试上号并写入 token。
- 把账号写入 Supabase `accounts_pool`。

### 2.2 `go-team-api`（管理面板 / 编排层）

- 提供账号查询、标记分类、手动上号/下号、删除等管理接口。
- 提供 worker 任务编排接口（claim/complete/fail，见后文建议）。
- 提供插件访问入口（插件不直接连 Supabase）。

### 2.3 外部 `worker`（自动执行层）

- 负责自动上号流程（重依赖、长耗时、失败重试逻辑集中在这里）。
- 支持两种触发：
  - 定时触发：周期扫描待上号账号。
  - 事件触发：接收“缺号”webhook 后立即补号。

### 2.4 浏览器插件（人工升级层）

- 人工执行 Plus/Team 升级类高交互流程。
- 升级成功后通过 API 回写状态（例如 `subscription-success`）。

---

## 3. 账号主数据与状态机

账号主表：`public.accounts_pool`。

建议状态（`status`）：
- `registered_pending_login`：已注册，未上号。
- `login_in_progress`：worker 正在上号。
- `ready`：可用（含有效 token 或已确认可登录）。
- `team_subscribed` / `team_owner`：已升级后的业务状态。
- `login_failed`：上号失败，待重试。
- `abandoned`：超过重试阈值，进入人工池。

现有兼容状态：
- `normal`、`team`、`team_subscribed`、`team_owner` 仍可读写；建议逐步收敛到上面的状态集。

---

## 4. 关键业务流

### 4.1 常规流（默认）

1. `ipynb` 注册成功写入 `accounts_pool`，状态为 `registered_pending_login`。  
2. worker 定时/事件触发，领取账号并执行上号。  
3. 成功：状态置为 `ready`，写入 `access_token/refresh_token/id_token` 与 `token_alive=true`。  
4. 失败：状态置为 `login_failed`，增加重试计数，按退避策略设置下次重试时间。  
5. 达到重试阈值：状态置为 `abandoned`，等待人工处理。

### 4.2 紧急流（冷启动）

1. `ipynb` 注册后立即尝试上号。  
2. 成功则直接产出 `ready` 账号。  
3. 失败则回落到常规流，由 worker 后续重试。

### 4.3 插件升级流

1. 插件人工完成 Plus/Team 升级。  
2. 回调 `go-team-api`（例如 `subscription-success`）。  
3. API 校验有效性后写回 `accounts_pool`（`team_subscribed/team_owner` 等）。

---

## 5. worker 触发与调度建议

### 5.1 定时触发（必需）

- 建议 1-5 分钟执行一次。
- 用于兜底：即使 webhook 丢失，也能持续推进待上号账号。

可选实现：
- 平台 cron（GitHub Actions / Cloud Scheduler / server cron）调用 worker webhook。
- worker 进程内部 loop + sleep。

### 5.2 事件触发（建议）

- 下游（插件/业务服务）在“库存不足”时发 webhook。
- worker 收到事件后即时补号，降低缺号窗口。

---

## 6. 并发与幂等（必须）

- 任务领取使用数据库锁语义（建议 `FOR UPDATE SKIP LOCKED`）。
- 每个账号同一时刻只能被一个 worker 处理。
- webhook 需带幂等键（`request_id`），防止重复触发导致重复上号。
- worker 结果回写必须幂等（同一账号重复上报不破坏最终状态）。

---

## 7. 建议补充字段（用于自动化闭环）

建议在 `accounts_pool` 增加：
- `login_retry_count int not null default 0`
- `last_login_attempt timestamptz`
- `next_login_at timestamptz`
- `locked_by text`
- `locked_at timestamptz`

说明：
- `next_login_at` 用于退避调度（指数退避）。
- `locked_by/locked_at` 用于 worker 崩溃恢复与抢占保护。

---

## 8. API 边界建议（go-team-api）

人工管理接口（已有）：账号查询、标记、手动 token-check、升级回写。

建议新增 worker 编排接口：
- `POST /v1/worker/accounts/claim`
- `POST /v1/worker/accounts/{email}/complete`
- `POST /v1/worker/accounts/{email}/fail`
- `POST /v1/hooks/account-demand`（缺号 webhook 入队）

原则：
- API 只做编排与校验，不内嵌重型上号执行。
- 重型执行交给外部 worker。

---

## 9. 当前落地状态（2026-02-25）

- 账号主链路已切到 `accounts_pool`。
- `token-check` 已改为 refresh token 测活（不再内置登录回退）。
- 插件通过 `go-team-api` 访问数据库面板的方向成立。
- 尚需补全：worker 编排接口、调度字段、自动重试策略的正式实现。

---

## 10. 结论

当前架构方向合理，且具备可扩展性。  
要解决“注册账号浪费”问题，关键不是把上号逻辑塞进 API，而是落实“状态机 + 外部 worker + 定时/事件双触发 + 重试治理”这套闭环。
