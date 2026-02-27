# supabase

数据库结构目录。

文件：
- `schema.sql`：当前主 schema（`accounts_pool`、`keygen_runs` 等）
- `legacy_tables_for_import.sql`：导入本地 legacy dump 所需旧表结构
- `migrate_legacy_accounts_to_pool.sql`：旧 `accounts/account_tags` 回填到 `accounts_pool`
- `cleanup_legacy_tables.sql`：迁移后清理 `accounts/account_tags`，仅保留 `teams/team_invitations`

用途：
- 新环境初始化建表
- 与 notebook / `go_team_api` 的字段契约对齐

字段约定（`accounts_pool`）：
- `account_identity`：账号身份（`normal` / `plus` / `team_owner` / `team_member`）
- `status`：流程状态（例如 `fed`、`login_failed`、`registered_pending_login` 等）

建议：
- 生产环境开启 RLS，并限制插件直连表
- 让插件通过 `go_team_api` 访问数据，避免暴露 service role key

迁移入口脚本：
- `infra/scripts/migrate_local_pg_to_supabase.ps1`
