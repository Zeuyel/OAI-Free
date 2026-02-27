-- Legacy compatibility tables for importing local pg_dump data.
-- These tables exist only to make legacy_data.sql importable and to support older team endpoints.

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
  account_id text not null references public.accounts(id) on delete cascade,
  tag text not null,
  created_at timestamptz not null default now(),
  primary key (account_id, tag)
);

create table if not exists public.teams (
  id text primary key,
  name text not null,
  status text not null default 'active',
  owner_account_id text null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  email text not null default '',
  account_id text not null default '',
  team_name text not null default '',
  subscription_plan text not null default '',
  expires_at timestamptz null,
  max_members integer not null default 6,
  current_members integer not null default 0,
  account_role text not null default 'account-owner',
  access_token text not null default '',
  refresh_token text not null default '',
  session_token text not null default '',
  client_id text not null default '',
  last_sync timestamptz null
);

create table if not exists public.team_invitations (
  id text primary key,
  team_id text not null references public.teams(id) on delete cascade,
  account_id text not null,
  status text not null default 'invited',
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  unique (team_id, account_id)
);

create index if not exists idx_accounts_email on public.accounts(email);
create index if not exists idx_accounts_type on public.accounts(account_type);
create index if not exists idx_invites_team on public.team_invitations(team_id, status);
create index if not exists idx_tags_tag on public.account_tags(tag);
