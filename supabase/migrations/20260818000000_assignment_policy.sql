-- Run mode (policy) on a project assignment, so it can be set from an LLM session or the web.
--
-- WHY IT NEEDS A COLUMN. A project's policy — "auto" runs dispatched work unattended, "ask" queues
-- it for the owner to approve at the daemon console — lives ONLY in the machine's local registry
-- (daemon.go daemonProject.Policy). That is correct: it is a standing grant to execute code on
-- someone's box, and the server has no business holding the authoritative copy. But it also meant
-- the grant could only ever be changed by typing on that machine, so "set this project to manual" or
-- "add this machine as a node and have it ask before running" was not expressible remotely at all.
--
-- This carries the operator's INTENT alongside the existing bind assignment. The daemon still
-- decides: it re-derives the directory from a handle it advertised, and applies the policy itself.
-- Reference-not-command holds — the server sends a word from a fixed vocabulary, never a command.
--
-- NULL means LEAVE IT ALONE, and that default is load-bearing: an assignment that only meant to
-- re-point a directory must never silently widen (or narrow) who may run code on that machine. Only
-- an explicit 'auto' or 'ask' moves it, which is why the CHECK permits exactly those two.
alter table daemon_project_assignments
  add column if not exists policy text
  check (policy is null or policy in ('auto', 'ask'));
