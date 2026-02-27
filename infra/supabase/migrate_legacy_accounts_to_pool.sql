-- Backfill legacy local-postgres tables into accounts_pool.
-- Run this after importing legacy data tables (accounts/account_tags/teams/team_invitations) to Supabase.

begin;

-- Keep this script runnable even if legacy tables were not imported yet.
create table if not exists public.accounts (
  id text primary key,
  email text not null unique,
  password text not null default '',
  account_type text not null default 'normal',
  team_subscribed boolean not null default false,
  access_token text not null default '',
  refresh_token text not null default '',
  id_token text not null default '',
  token_expired_at timestamptz null,
  token_last_refresh timestamptz null,
  token_alive boolean not null default false,
  last_token_check timestamptz null,
  status_source text not null default '',
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table if not exists public.account_tags (
  account_id text not null,
  tag text not null,
  created_at timestamptz not null default now(),
  primary key (account_id, tag)
);

insert into public.accounts_pool (
  email,
  password,
  account_identity,
  status,
  token_len,
  cpa_filename,
  error,
  access_token,
  refresh_token,
  id_token,
  token_alive,
  token_check_method,
  token_expired_at,
  token_last_refresh,
  last_token_check,
  created_at,
  updated_at
)
select
  lower(trim(a.email)) as email,
  nullif(a.password, '') as password,
  case
    when exists (
      select 1
      from public.account_tags t
      where t.account_id = a.id and lower(t.tag) = 'team_owner'
    ) then 'team_owner'
    when coalesce(a.team_subscribed, false) then 'team_member'
    when lower(coalesce(a.account_type, '')) = 'team' then 'team_member'
    else 'normal'
  end as account_identity,
  'imported_legacy' as status,
  char_length(coalesce(a.access_token, '')) as token_len,
  null::text as cpa_filename,
  null::text as error,
  nullif(a.access_token, '') as access_token,
  nullif(a.refresh_token, '') as refresh_token,
  nullif(a.id_token, '') as id_token,
  coalesce(a.token_alive, false) as token_alive,
  case
    when nullif(a.refresh_token, '') is not null then 'legacy_refresh_migrated'
    when nullif(a.access_token, '') is not null then 'legacy_access_migrated'
    else null
  end as token_check_method,
  a.token_expired_at,
  a.token_last_refresh,
  a.last_token_check,
  coalesce(a.created_at, now()) as created_at,
  coalesce(a.updated_at, now()) as updated_at
from public.accounts a
where nullif(trim(a.email), '') is not null
on conflict (email) do update
set
  password = coalesce(excluded.password, public.accounts_pool.password),
  account_identity = case
    when coalesce(public.accounts_pool.account_identity, 'normal') in ('team_owner', 'team_member', 'plus')
      then public.accounts_pool.account_identity
    else excluded.account_identity
  end,
  token_len = excluded.token_len,
  error = null,
  access_token = coalesce(excluded.access_token, public.accounts_pool.access_token),
  refresh_token = coalesce(excluded.refresh_token, public.accounts_pool.refresh_token),
  id_token = coalesce(excluded.id_token, public.accounts_pool.id_token),
  token_alive = excluded.token_alive,
  token_check_method = coalesce(excluded.token_check_method, public.accounts_pool.token_check_method),
  token_expired_at = coalesce(excluded.token_expired_at, public.accounts_pool.token_expired_at),
  token_last_refresh = coalesce(excluded.token_last_refresh, public.accounts_pool.token_last_refresh),
  last_token_check = coalesce(excluded.last_token_check, public.accounts_pool.last_token_check),
  updated_at = greatest(public.accounts_pool.updated_at, excluded.updated_at);

commit;

-- Quick verify
select
  (select count(*) from public.accounts) as legacy_accounts,
  (select count(*) from public.accounts_pool) as pool_accounts;
