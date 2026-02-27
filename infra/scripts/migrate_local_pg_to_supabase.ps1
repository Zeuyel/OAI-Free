param(
  [string]$LocalDsn = "postgres://teamuser:teampass@127.0.0.1:55432/teamdb?sslmode=disable",
  [Parameter(Mandatory = $true)]
  [string]$SupabaseDsn,
  [string]$WorkspaceRoot = "",
  [string]$DumpFile = "",
  [switch]$SkipDump
)

$ErrorActionPreference = "Stop"

function Run-Or-Fail {
  param(
    [Parameter(Mandatory = $true)] [string]$Label,
    [Parameter(Mandatory = $true)] [scriptblock]$Action
  )
  Write-Host ""
  Write-Host "==> $Label"
  & $Action
  if ($LASTEXITCODE -ne 0) {
    throw "Step failed: $Label (exit=$LASTEXITCODE)"
  }
}

if ([string]::IsNullOrWhiteSpace($WorkspaceRoot)) {
  $WorkspaceRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
}

if ([string]::IsNullOrWhiteSpace($DumpFile)) {
  $DumpDir = Join-Path $WorkspaceRoot "artifacts"
  New-Item -ItemType Directory -Path $DumpDir -Force | Out-Null
  $DumpFile = Join-Path $DumpDir "legacy_data.sql"
}

$SchemaFile = Join-Path $WorkspaceRoot "infra\supabase\schema.sql"
$LegacySchemaFile = Join-Path $WorkspaceRoot "infra\supabase\legacy_tables_for_import.sql"
$BackfillFile = Join-Path $WorkspaceRoot "infra\supabase\migrate_legacy_accounts_to_pool.sql"
$CleanupFile = Join-Path $WorkspaceRoot "infra\supabase\cleanup_legacy_tables.sql"

if (-not (Test-Path $SchemaFile)) {
  throw "schema file not found: $SchemaFile"
}
if (-not (Test-Path $LegacySchemaFile)) {
  throw "legacy schema file not found: $LegacySchemaFile"
}
if (-not (Test-Path $BackfillFile)) {
  throw "backfill file not found: $BackfillFile"
}
if (-not (Test-Path $CleanupFile)) {
  throw "cleanup file not found: $CleanupFile"
}

Write-Host "WorkspaceRoot: $WorkspaceRoot"
Write-Host "LocalDsn:      $LocalDsn"
Write-Host "SupabaseDsn:   (hidden)"
Write-Host "DumpFile:      $DumpFile"
Write-Host "SkipDump:      $SkipDump"

Run-Or-Fail -Label "Apply infra/supabase/schema.sql" -Action {
  psql "$SupabaseDsn" -v ON_ERROR_STOP=1 -f "$SchemaFile"
}

Run-Or-Fail -Label "Apply infra/supabase/legacy_tables_for_import.sql" -Action {
  psql "$SupabaseDsn" -v ON_ERROR_STOP=1 -f "$LegacySchemaFile"
}

if (-not $SkipDump) {
  Run-Or-Fail -Label "Dump legacy data from local postgres" -Action {
    pg_dump "$LocalDsn" `
      --data-only `
      --table=public.accounts `
      --table=public.account_tags `
      --table=public.teams `
      --table=public.team_invitations `
      -f "$DumpFile"
  }
}
else {
  if (-not (Test-Path $DumpFile)) {
    throw "SkipDump is set but dump file not found: $DumpFile"
  }
}

Run-Or-Fail -Label "Import legacy dump into supabase" -Action {
  psql "$SupabaseDsn" -v ON_ERROR_STOP=1 -f "$DumpFile"
}

Run-Or-Fail -Label "Backfill legacy accounts -> accounts_pool" -Action {
  psql "$SupabaseDsn" -v ON_ERROR_STOP=1 -f "$BackfillFile"
}

Run-Or-Fail -Label "Cleanup legacy tables (keep teams/team_invitations)" -Action {
  psql "$SupabaseDsn" -v ON_ERROR_STOP=1 -f "$CleanupFile"
}

Run-Or-Fail -Label "Verification counts" -Action {
  psql "$SupabaseDsn" -v ON_ERROR_STOP=1 -c "select count(*) as accounts_pool_count from public.accounts_pool;"
}

Write-Host ""
Write-Host "Migration finished successfully."
