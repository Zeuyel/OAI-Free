# OpenAI Account Pipeline Workspace

本仓库是一个账号生产与管理工作区，覆盖注册、上号、管理面板、自动补号和插件人工升级。

## 核心架构入口

- 全项目设计文档：`docs/PROJECT_ARCHITECTURE.md`
- 账号供给闭环设计：`docs/ARCHITECTURE_ACCOUNT_PIPELINE.md`
- API 管理层：`apps/go_team_api/README.md`
- 账号生产者：`producers/keygen_colab/README.md`
- 数据库结构：`infra/supabase/README.md`

## 顶层目录

- `apps/`：线上应用（当前包含 `go_team_api`）
- `extensions/`：浏览器插件（当前开发中）
- `producers/`：账号生产者（notebook）
- `infra/`：数据库 schema 与运维脚本
- `workers/`：外置 worker 脚本（手动/定时上号）
- `legacy/`：历史实现与迁移过渡材料
- `artifacts/`：运行产物与临时输出目录
- `config.json` / `mail_fetch_viewer.html`：根目录兼容配置与静态资源

## 当前推荐生产链路

1. `producers/keygen_colab` 注册账号并写入 `accounts_pool`
2. 外部 worker 定时/事件触发执行上号与补号
3. `apps/go_team_api` 作为管理面板与统一数据访问层
4. 浏览器插件执行人工 Plus/Team 升级并回写状态
