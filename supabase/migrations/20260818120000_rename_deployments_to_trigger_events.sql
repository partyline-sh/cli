-- Rename `deployments` to `trigger_events` — the name was the only deploy-specific thing about it.
--
-- The table records one row per trigger fire that reported an outcome, and a trigger is not a
-- deploy: ticket intake, an alert webhook, a form submission all land here the moment they say how
-- something turned out. Every column already generalises — `provider`, `outcome`, `ref` (the
-- original comment calls it "a git sha, a build id"), `url`, `duration_ms`, `run_id`. Only the NAME
-- claimed otherwise, and a name that lies is how the DORA widget ended up counting non-deploys.
--
-- `environment` stays exactly as it is: nullable free text. A non-deploy trigger simply leaves it
-- null, and that null is what the deploy-health query filters on to keep the DORA numbers about
-- deploys (see web/src/lib/api/deploy-health.ts).
--
-- Postgres carries indexes, constraints and policies across a table rename, so everything below the
-- first statement is cosmetic-but-correct cleanup: nothing should be left named after a table that
-- no longer exists.
alter table if exists public.deployments rename to trigger_events;

alter index if exists public.deployments_pkey       rename to trigger_events_pkey;
alter index if exists public.deployments_org_recent rename to trigger_events_org_recent;
alter index if exists public.deployments_env_recent rename to trigger_events_env_recent;

-- Constraint renames have no IF EXISTS, and the FK/check names are Postgres-generated — so probe
-- pg_constraint rather than assume a name that a differently-created database may not carry.
do $$
declare
  c record;
begin
  for c in
    select conname
      from pg_constraint
     where conrelid = 'public.trigger_events'::regclass
       and conname like 'deployments\_%'
  loop
    execute format(
      'alter table public.trigger_events rename constraint %I to %I',
      c.conname,
      'trigger_events_' || substring(c.conname from length('deployments_') + 1)
    );
  end loop;
end $$;

do $$
begin
  if exists (
    select 1 from pg_policies
     where schemaname = 'public' and tablename = 'trigger_events'
       and policyname = 'deployments: team read'
  ) then
    alter policy "deployments: team read" on public.trigger_events rename to "trigger_events: team read";
  end if;
end $$;

comment on table public.trigger_events is
  'One row per trigger fire that reported an outcome (#819). Deploy-shaped rows — those carrying an environment — power deploy frequency, change failure rate and MTTR.';
