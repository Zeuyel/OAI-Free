# scripts

Utility scripts for migration and operations.

## migrate_local_pg_to_supabase.ps1

Migrate legacy local docker postgres data into Supabase.

### Requirements

- `pg_dump` and `psql` available in PATH
- Supabase database DSN

### Example

```powershell
powershell -ExecutionPolicy Bypass -File .\infra\scripts\migrate_local_pg_to_supabase.ps1 `
  -SupabaseDsn "postgresql://postgres.<project-ref>:<password>@aws-0-<region>.pooler.supabase.com:6543/postgres?sslmode=require&default_query_exec_mode=simple_protocol"
```

Default local source DSN:

`postgres://teamuser:teampass@127.0.0.1:55432/teamdb?sslmode=disable`

The script will:
1. Apply `infra/supabase/schema.sql`
2. Apply `infra/supabase/legacy_tables_for_import.sql`
3. Dump legacy data tables from local postgres
4. Import dump into Supabase
5. Backfill `accounts -> accounts_pool` using `infra/supabase/migrate_legacy_accounts_to_pool.sql`
6. Cleanup legacy tables using `infra/supabase/cleanup_legacy_tables.sql` (keeps `teams` and `team_invitations`)
7. Print verification counts
