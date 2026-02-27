-- Supabase schema (keygen + CPA feed + token health)
-- Updated: 2026-02-25

create or replace function public.set_updated_at()
returns trigger
language plpgsql
as $$
begin
  new.updated_at = now();
  return new;
end;
$$;

create table if not exists public.accounts_pool (
  email text primary key,
  password text,
  account_identity text not null default 'normal',
  status text not null default 'unknown',
  token_len int not null default 0,
  cpa_filename text,
  error text,
  access_token text,
  refresh_token text,
  id_token text,
  token_alive boolean not null default false,
  token_check_method text,
  token_expired_at timestamptz,
  token_last_refresh timestamptz,
  last_token_check timestamptz,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint accounts_pool_token_len_non_negative check (token_len >= 0)
);

create table if not exists public.keygen_runs (
  id bigint generated always as identity primary key,
  run_started_at timestamptz,
  run_finished_at timestamptz,
  mode text,
  register_total int not null default 0,
  feed_ok int not null default 0,
  feed_fail int not null default 0,
  verify_found int not null default 0,
  result_json text,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint keygen_runs_register_total_non_negative check (register_total >= 0),
  constraint keygen_runs_feed_ok_non_negative check (feed_ok >= 0),
  constraint keygen_runs_feed_fail_non_negative check (feed_fail >= 0),
  constraint keygen_runs_verify_found_non_negative check (verify_found >= 0)
);

alter table public.accounts_pool add column if not exists access_token text;
alter table public.accounts_pool add column if not exists refresh_token text;
alter table public.accounts_pool add column if not exists id_token text;
alter table public.accounts_pool add column if not exists account_identity text not null default 'normal';
alter table public.accounts_pool add column if not exists token_alive boolean not null default false;
alter table public.accounts_pool add column if not exists token_check_method text;
alter table public.accounts_pool add column if not exists token_expired_at timestamptz;
alter table public.accounts_pool add column if not exists token_last_refresh timestamptz;
alter table public.accounts_pool add column if not exists last_token_check timestamptz;
alter table public.accounts_pool add column if not exists created_at timestamptz not null default now();

alter table public.keygen_runs add column if not exists created_at timestamptz not null default now();
alter table public.keygen_runs add column if not exists updated_at timestamptz not null default now();

create index if not exists idx_accounts_pool_status on public.accounts_pool (status);
create index if not exists idx_accounts_pool_identity on public.accounts_pool (account_identity);
create index if not exists idx_accounts_pool_token_alive on public.accounts_pool (token_alive);
create index if not exists idx_accounts_pool_last_token_check on public.accounts_pool (last_token_check desc);
create index if not exists idx_keygen_runs_started_at on public.keygen_runs (run_started_at desc);

drop trigger if exists trg_accounts_pool_set_updated_at on public.accounts_pool;
create trigger trg_accounts_pool_set_updated_at
before update on public.accounts_pool
for each row execute function public.set_updated_at();

drop trigger if exists trg_keygen_runs_set_updated_at on public.keygen_runs;
create trigger trg_keygen_runs_set_updated_at
before update on public.keygen_runs
for each row execute function public.set_updated_at();
