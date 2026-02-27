# Go Team API (PostgreSQL)

## 架构文档

- 全项目设计：`../../docs/PROJECT_ARCHITECTURE.md`
- 账号生产/上号/补号闭环设计：`../../docs/ARCHITECTURE_ACCOUNT_PIPELINE.md`
- 外置手动/定时上号 worker：`../../workers/README.md`
- 本地 PG -> Supabase 迁移脚本：`../../infra/scripts/README.md`

最小迁移版本，目标：
- `accounts.txt` 导入 Supabase `accounts_pool`
- 普号/Team 号标签管理
- 权限分割（`viewer/operator/admin`）
- Team 一键从邀请池挑号
- 通过 OAuth refresh token 做账号 token 测活（无 Python 依赖）

## 1. 环境变量

- 数据库连接（两者任选其一，优先读取 `DATABASE_URL`）：
  - `DATABASE_URL`
  - `SUPABASE_DB_URL`（或 `SUPABASE_DATABASE_URL`）
- 本地 PostgreSQL 示例：
  - `postgres://postgres:postgres@127.0.0.1:5432/teamdb?sslmode=disable`
- Supabase 直连示例：
  - `postgresql://postgres:<password>@db.<project-ref>.supabase.co:5432/postgres?sslmode=require`
- Supabase Pooler 示例：
  - `postgresql://postgres.<project-ref>:<password>@aws-0-<region>.pooler.supabase.com:6543/postgres?sslmode=require&default_query_exec_mode=simple_protocol`
- `API_KEYS`（可选）：
  - 例如：`admin:adminkey,operator:opkey,viewer:viewkey`
- `GO_TEAM_API_ADDR`（可选，默认 `127.0.0.1:18081`）
- `MAIL_WORKER_DOMAIN`（可选，默认 `cf-temp-email.mengcenfay.workers.dev`）
- `MAIL_ADMIN_PASSWORD`（可选，无默认值）
- `MAIL_EMAIL_DOMAIN`（可选，随机邀请邮箱域名，默认 `agibar.x10.mx`）
- `MAIL_OTP_TIMEOUT_SECONDS`（可选，默认 `120`）
- `MAIL_OTP_POLL_SECONDS`（可选，默认 `3`）
- `MAIL_OTP_RECENT_SECONDS`（可选，默认 `300`）
- 连接池（可选）：
  - `DB_MAX_OPEN_CONNS`（默认 `10`）
  - `DB_MAX_IDLE_CONNS`（默认 `5`）
  - `DB_CONN_MAX_LIFETIME_SECONDS`（默认 `1800`）
  - `DB_CONN_MAX_IDLE_TIME_SECONDS`（默认 `600`）

## 2. 运行

```bash
cd apps/go_team_api
go mod tidy
go run .
```

Windows PowerShell（推荐，先加载 `.env` 再启动）：

```powershell
cd apps/go_team_api
Get-Content .env | ForEach-Object {
  if ($_ -match '^\s*#' -or $_ -match '^\s*$') { return }
  $kv = $_ -split '=', 2
  if ($kv.Length -eq 2) { Set-Item -Path "Env:$($kv[0])" -Value $kv[1] }
}
go run .
```

## 3. 主要接口

- `GET /health`
- `GET /v1/teams?page=1&per_page=20&search=keyword`
- `POST /v1/accounts/import-txt`
  - body: `{"path":"../../legacy/extension_backend/accounts.txt"}` 或 `{"text":"a@b.com:pwd"}`
- `GET /v1/accounts?account_type=normal&tag=team_owner`
- 说明：账户主数据来自 `accounts_pool`，并且 `{id}` 使用账号邮箱（`email`）
- 说明：身份字段仅使用 `accounts_pool.account_identity`（`normal/plus/team_owner/team_member`），`status` 仅作流程状态
- `GET /v1/accounts/{id}/credentials`
  - 需要 `operator/admin` 权限；返回 email/password（供轻量登录插件）
- `POST /v1/accounts/{id}/otp-fetch`
  - body: `{"timeout_seconds":120}`（可选）
  - 自动拉取该账号邮箱最新 OTP（供轻量登录插件）
- `PATCH /v1/accounts/{id}/tags`
  - body: `{"account_type":"normal","tags":["invite_pool","fresh"]}`
- `POST /v1/accounts/subscription-success`
  - 供 `lite_login_extension` 在检测到 `success-team` 后回写
  - body: `{"email":"x@x.com","account_id":"acct_xxx","access_token":"...","source":"lite_login_extension_success_team"}`
  - 第一步先用 `access_token` 调用 `https://chatgpt.com/backend-api/accounts/check/v4-2023-04-27` 校验 Team 是否 `active`
  - 校验通过后落库到 `accounts_pool`：更新 `status/token_*` 字段；校验失败直接拒绝写入
- `POST /v1/teams`
  - body: `{"name":"team-A"}`
- `GET /v1/teams/{teamId}/info`
- `POST /v1/teams/{teamId}/update`
- `GET /v1/teams/{teamId}/members/list`
- `POST /v1/teams/{teamId}/members/add`
- `POST /v1/teams/{teamId}/members/{accountId}/delete`
- `POST /v1/teams/{teamId}/invites/revoke`
- `POST /v1/teams/{teamId}/owner-check`
- `POST /v1/teams/{teamId}/one-click-onboard`
  - body: `{"count":5}`
- `POST /v1/teams/{teamId}/one-click-random-invite`
  - body: `{"count":5}`
- `POST /v1/accounts/{id}/token-check`
  - 默认只使用库内 `refresh_token` 走 OAuth refresh（`grant_type=refresh_token`）测活
  - 持久化字段：`access_token`、`refresh_token`、`id_token`、`token_expired_at`、`token_last_refresh`、`token_alive`、`token_check_method`、`token_len`、`error`、`last_token_check`
- `POST /v1/accounts/{id}/cpa-toggle`
  - body: `{"action":"up"}` 或 `{"action":"down"}`（不传则自动切换）
- `POST /v1/accounts/{id}/otp-fetch`
  - body: `{"timeout_seconds":120}`（可选）
- `GET /v1/accounts/{id}/mail-list?limit=30`

## 4. 前端页面（内置）

- `GET /ui/`：账号面板 + Team 管理（MVP）

## 5. 兼容旧插件基础接口

- `GET /accounts`
- `POST /mark-subscribed`
  - 兼容旧逻辑，写入 `accounts_pool.status`（`team_subscribed/normal`）
- `GET /logs`
- `POST /run` / `POST /run-existing` / `POST /login-existing`：当前返回 `501`（仅占位）

## 6. 已确认的协议边界

- 被邀请普号的“协议化 accept invite”未实现；接口返回 `accept_invite_protocol_supported=false`。
