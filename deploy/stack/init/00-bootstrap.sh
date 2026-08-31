#!/bin/bash
# Postgres bootstrap — runs ONCE, on an empty data dir, before any partyline migration.
# Deliverable of task #177 (the portability gate). Everything here is something hosted Supabase
# provides implicitly and plain Postgres does not; the 114 migrations assume all of it exists.
#
# Measured, not guessed: with none of this, `psql -f` fails at 0001_core.sql line 11
# ("schema auth does not exist") and 113 of 114 migrations cascade-fail behind it.
#
# The migrations use exactly four auth symbols — auth.uid (67 uses), auth.users (57),
# auth.jwt (4), auth.admin (1) — and grant to `authenticated` (76) and `service_role` (2).
#
# There is no GoTrue. This script creates the auth schema, OUR auth.users table, and the claim
# helpers, all before the first migration runs — 0001_core.sql FKs to auth.users on line 8.
set -euo pipefail

# ── Keycloak's database ────────────────────────────────────────────────────────────────────────
# partyline BUNDLES an identity provider, because it has no local accounts: with no IdP there is no
# way to create the first user, and standing up Keycloak yourself is a bigger job than installing
# partyline. Keycloak keeps its own schema, in its own database, on this same Postgres — so it is
# covered by the same backup as everything else rather than being a second thing to remember.
#
# CREATE DATABASE cannot run inside a transaction block, and psql's heredoc below is one script, so
# this is its own invocation. `IF NOT EXISTS` has no CREATE DATABASE form, hence the SELECT guard.
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" -tAc \
  "select 1 from pg_database where datname = 'keycloak'" | grep -q 1 ||
  psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" -c "create database keycloak"

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-SQL  # UNQUOTED: ${AUTHENTICATOR_PASSWORD} below is expanded by the SHELL, so NO BACKTICKS
	-- ── Roles ──────────────────────────────────────────────────────────────────────────────
	-- PostgREST logs in as 'authenticator' and SET ROLEs to anon/authenticated per request; that
	-- switch is what makes RLS + auth.uid() work exactly as on hosted Supabase.
	do \$\$
	begin
	  if not exists (select from pg_roles where rolname = 'anon') then
	    create role anon nologin noinherit;
	  end if;
	  if not exists (select from pg_roles where rolname = 'authenticated') then
	    create role authenticated nologin noinherit;
	  end if;
	  if not exists (select from pg_roles where rolname = 'service_role') then
	    create role service_role nologin noinherit bypassrls;
	  end if;
	  if not exists (select from pg_roles where rolname = 'authenticator') then
	    execute format('create role authenticator login noinherit password %L', '${AUTHENTICATOR_PASSWORD}');
	  end if;
	end
	\$\$;

	grant anon, authenticated, service_role to authenticator;

	-- ── auth schema — OURS, not GoTrue's ───────────────────────────────────────────────────
	-- There is no GoTrue here. An OIDC provider authenticates; partyline owns identity storage
	-- and sessions (thread contract #334).
	create schema if not exists auth;
	grant usage on schema auth to anon, authenticated, service_role;

	-- auth.users keeps its NAME on purpose. 57 references and 46 foreign keys across 22 migration
	-- files point at it; renaming to public.users would mean repointing every one of them for a
	-- purely cosmetic gain. It is our table, in our database — the schema name is just a name.
	-- Lives in the bootstrap rather than a migration because 0001_core.sql:8 already FKs to it.
	create table if not exists auth.users (
	  id                 uuid primary key default gen_random_uuid(),
	  -- The identity provider's own user id (an OIDC 'sub'). A STRING, never used as the uuid —
	  -- this column is the only place the mapping exists, so swapping IdP touches one column. The
	  -- NAME is history: it held WorkOS ids first, and renaming it would be a migration on the one
	  -- column every existing account signs in through.
	  workos_user_id     text unique,
	  email              text unique not null,
	  email_confirmed_at timestamptz,
	  raw_user_meta_data jsonb not null default '{}'::jsonb,
	  created_at         timestamptz not null default now(),
	  updated_at         timestamptz not null default now()
	);
	create index if not exists users_workos_idx on auth.users (workos_user_id);
	-- NO grants to 'authenticated' here, deliberately. An earlier version granted SELECT, which
	-- would have exposed every user's email had the auth schema ever been added to
	-- PGRST_DB_SCHEMAS. The app reaches auth.users through public.resolve_workos_user() instead —
	-- SECURITY DEFINER, service_role only.
	grant select, insert, update, delete on auth.users to service_role;

	-- Claim readers. Formerly Supabase-provided; now ours. Each is a thin read of the per-request
	-- JWT claims GUC that PostgREST sets from OUR session token — which is exactly why all 60 RLS
	-- policies survive the provider swap unchanged (docs/epics/supabase-exit.md).
	create or replace function auth.uid() returns uuid
	  language sql stable
	  as \$\$ select nullif(current_setting('request.jwt.claims', true)::jsonb->>'sub', '')::uuid \$\$;

	create or replace function auth.jwt() returns jsonb
	  language sql stable
	  as \$\$ select coalesce(current_setting('request.jwt.claims', true)::jsonb, '{}'::jsonb) \$\$;

	create or replace function auth.role() returns text
	  language sql stable
	  as \$\$ select nullif(current_setting('request.jwt.claims', true)::jsonb->>'role', '')::text \$\$;

	create or replace function auth.email() returns text
	  language sql stable
	  as \$\$ select nullif(current_setting('request.jwt.claims', true)::jsonb->>'email', '')::text \$\$;

	grant execute on function auth.uid(), auth.jwt(), auth.role(), auth.email()
	  to anon, authenticated, service_role;

	-- ── Realtime publication ───────────────────────────────────────────────────────────────
	-- 0001_core.sql:212 does an unguarded 'alter publication supabase_realtime add table …'.
	-- Later migrations wrap theirs in an exception handler, but the first one doesn't, so the
	-- publication has to exist. Empty is fine — nothing subscribes to it here, and P2 of the exit
	-- epic deletes Realtime entirely; this just keeps the historical migrations replayable.
	do \$\$
	begin
	  if not exists (select from pg_publication where pubname = 'supabase_realtime') then
	    create publication supabase_realtime;
	  end if;
	end
	\$\$;

	-- ── storage schema ─────────────────────────────────────────────────────────────────────
	-- Migrations insert bucket rows (e.g. 20260718000000_work_item_attachments.sql). We are
	-- moving object storage to R2 in P2, so this is a minimal stand-in that lets the historical
	-- migrations replay — NOT a reimplementation of Supabase Storage.
	create schema if not exists storage;
	create table if not exists storage.buckets (
	  id                 text primary key,
	  name               text not null,
	  public             boolean default false,
	  file_size_limit    bigint,
	  allowed_mime_types text[],
	  created_at         timestamptz default now()
	);
	create table if not exists storage.objects (
	  id         uuid primary key default gen_random_uuid(),
	  bucket_id  text references storage.buckets(id),
	  name       text,
	  owner      uuid,
	  metadata   jsonb,
	  created_at timestamptz default now()
	);
	grant usage on schema storage to anon, authenticated, service_role;

	-- ── Table privileges ───────────────────────────────────────────────────────────────────
	-- THE MODEL: grants are permissive, RLS is the boundary. That is how hosted Supabase works,
	-- and every one of our 60+ policies was written assuming it.
	--
	-- An earlier version of this file granted to service_role ONLY. PostgREST connects as
	-- 'authenticator' and SET ROLEs to 'authenticated' per request, so every authenticated read
	-- failed with "permission denied for table orgs" — before RLS was even consulted. The app
	-- rendered a shell with no data and looked like an RLS or session problem. It stayed invisible
	-- until there was real data to notice missing.
	--
	-- Safe because 48 of 49 public tables have RLS enabled; the one that does not is
	-- schema_migrations, our own tracking table, which is excluded below. Any NEW table must have
	-- RLS enabled — the default privileges here will grant on it automatically.
	grant usage on schema public to anon, authenticated, service_role;
	grant all on all tables in schema public to authenticated, service_role;
	grant all on all sequences in schema public to authenticated, service_role;
	grant execute on all functions in schema public to authenticated, service_role;
	-- GUARDED, because this script runs BEFORE the first migration and schema_migrations is created
	-- BY apply-migrations.sh. Unguarded it raised "relation public.schema_migrations does not exist"
	-- on every fresh install, and with ON_ERROR_STOP=1 that aborted the rest of this file — while
	-- the container still reported healthy. The default privileges below cover the table when it is
	-- created later; this revoke only matters on a re-run against a database that already has it.
	do \$\$
	begin
	  if to_regclass('public.schema_migrations') is not null then
	    revoke all on public.schema_migrations from authenticated;
	  end if;
	end
	\$\$;

	alter default privileges in schema public
	  grant all on tables to authenticated, service_role;
	alter default privileges in schema public
	  grant all on sequences to authenticated, service_role;
	alter default privileges in schema public
	  grant execute on functions to authenticated, service_role;

	-- anon gets schema usage only. Supabase grants it table access too, but our browser no longer
	-- reads PostgREST (the session cookie is httpOnly), so an unauthenticated request has no
	-- legitimate reason to reach a table. Add grants deliberately if that ever changes.
SQL

echo "bootstrap: roles, auth schema and auth helpers created"
