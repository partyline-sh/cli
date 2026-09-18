-- ask_peer · the project-wide AUTO-ANSWER allowance. Until now the daily cap on auto-answered peer
-- consults lived only in each machine's environment (PARTYLINE_CONSULT_AUTO_DAILY), which meant
-- changing it was a launchd/systemd edit on every box. This makes it ONE setting per project, edited
-- in the web, honoured by every daemon that advertises the label.
--
-- SECURITY / SHAPE: this is a NUMBER, nothing else — reference-not-command, exactly like
-- visual_verify_enabled. It rides to the daemon on the consult event and the daemon RE-VALIDATES it,
-- clamping to its own compiled hard ceiling (200/project, 400/machine) so a compromised control plane
-- can raise the allowance only within a bound the machine owns. A NULL or unusable value falls back to
-- the daemon's built-in default (24) — never to unlimited.
--
-- 0 IS MEANINGFUL: "never auto-answer in this project; every consult waits for a human."
--
-- The CAP is project-wide; the SPEND is still counted per machine (each daemon keeps its own daily
-- ledger). Three machines in a project at a cap of 24 can therefore answer 72 between them. A shared
-- cross-machine counter would need server-side counting and is deliberately out of scope.

alter table public.projects
  add column if not exists consult_auto_daily int;

alter table public.projects
  drop constraint if exists projects_consult_auto_daily_range;

alter table public.projects
  add constraint projects_consult_auto_daily_range
  check (consult_auto_daily is null or (consult_auto_daily >= 0 and consult_auto_daily <= 200));

comment on column public.projects.consult_auto_daily is
  'ask_peer: per-day auto-answered consult allowance for this project, honoured by every daemon advertising the label. NULL = the daemon default (24). 0 = never auto-answer. Daemon clamps to its own hard ceiling; spend is counted per machine.';
