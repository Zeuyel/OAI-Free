-- Convert legacy team references from old accounts.id to accounts_pool.email
-- and remove legacy tables that are no longer used by go-team-api.

begin;

-- Drop legacy foreign key constraints if present
alter table if exists public.teams drop constraint if exists teams_owner_account_id_fkey;
alter table if exists public.team_invitations drop constraint if exists team_invitations_account_id_fkey;

-- Map owner account id -> email for teams
update public.teams t
set owner_account_id = lower(a.email)
from public.accounts a
where t.owner_account_id = a.id;

-- Map invitation account id -> email
update public.team_invitations i
set account_id = lower(a.email)
from public.accounts a
where i.account_id = a.id;

-- Normalize to lowercase email format where possible
update public.teams
set owner_account_id = lower(owner_account_id)
where owner_account_id is not null and owner_account_id <> '';

update public.team_invitations
set account_id = lower(account_id)
where account_id is not null and account_id <> '';

-- Remove legacy tables (replaced by accounts_pool)
drop table if exists public.account_tags;
drop table if exists public.accounts;

commit;
