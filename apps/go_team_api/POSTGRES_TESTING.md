# PostgreSQL Docker 测试配置

本项目用于 Go API 的本地测试 PostgreSQL 容器配置如下。

## Compose 文件

路径：`apps/go_team_api/docker-compose.postgres.yml`

关键配置：

- image: `postgres:16-alpine`
- container_name: `go-team-postgres`
- POSTGRES_USER: `teamuser`
- POSTGRES_PASSWORD: `teampass`
- POSTGRES_DB: `teamdb`
- host port: `55432` -> container `5432`
- volume: `team_postgres_data`

## 启动

```bash
docker compose -f apps/go_team_api/docker-compose.postgres.yml up -d
```

## 停止

```bash
docker compose -f apps/go_team_api/docker-compose.postgres.yml down
```

## 停止并删除数据卷（重置数据库）

```bash
docker compose -f apps/go_team_api/docker-compose.postgres.yml down -v
```

## 健康检查

```bash
docker inspect --format "{{.State.Health.Status}}" go-team-postgres
```

## 手动连库测试

```bash
docker exec go-team-postgres psql -U teamuser -d teamdb -c "select version();"
```

## Go 服务连接串

```text
postgres://teamuser:teampass@127.0.0.1:55432/teamdb?sslmode=disable
```

## Windows PowerShell 快速设置

```powershell
$env:DATABASE_URL="postgres://teamuser:teampass@127.0.0.1:55432/teamdb?sslmode=disable"
$env:API_KEYS="admin:adminkey,operator:opkey,viewer:viewkey"
cd apps/go_team_api
go run .
```
