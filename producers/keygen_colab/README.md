# keygen_colab

账号生产者目录，当前主 notebook：

- `register_and_cpa_feed_single.ipynb`

主要职责：
- 批量注册账号
- 可选执行 OAuth/CPA 喂养
- 将账号和 token 信息推送到 Supabase（`accounts_pool` / `keygen_runs`）

与其他组件关系：
- 输出数据给 `apps/go_team_api` 管理层和外部 worker 消费
- 紧急模式可直接上号，常规模式由 worker 后续补上号
