-- Agent templates (#794): a saved persona a trigger can wake.
--
-- THE SHAPE OF THE PROBLEM. A webhook fires and something has to run. Today a trigger carries an
-- inline task string, which is fine for "rebuild the docs" and useless for "triage this ticket the
-- way a good support engineer would" — that second one is a PERSONA, it is reused across many
-- triggers, and it is worth authoring once properly rather than retyping into every webhook.
--
-- ORG LEVEL, not project. One "ticket triager" gets pointed at several projects; making it
-- project-scoped would mean re-authoring the same judgement per project, which is the thing this
-- exists to stop. Same scoping as the skill library, for the same reason.
--
-- WHAT IS *NOT* HERE, and this is the important part: no tool list, no MCP servers, no repo. An
-- agent woken by a trigger runs on a daemon, inside a PROJECT, and inherits that project's tools,
-- MCP config and context exactly like any other run. A template that also declared tools would be a
-- second, quieter source of truth for the same question — and the two would disagree the first time
-- someone changed one. The template says WHO the agent is and WHAT it is for; the project says what
-- it can reach.
create table if not exists public.agent_templates (
  id         uuid primary key default gen_random_uuid(),
  org_id     uuid not null references public.orgs(id) on delete cascade,
  name       text not null,
  -- The authored instruction document — persona, the job, how to read the payload, and when to
  -- stop. Prose, deliberately: it is injected into the agent's prompt, and the thing being described
  -- ("a ticket usually has an id, a subject and a body; field names vary") is far easier to write as
  -- a sentence than as a schema, which was the whole reason the door accepts any payload.
  body       text not null,
  -- When it must refuse. Its own column rather than a paragraph inside body because it is the ONE
  -- field the runtime and the UI both need to reason about, and because an unattended agent with no
  -- instruction to stop will improvise around a missing precondition instead of saying so. Enforced
  -- non-empty at the application layer when a template is approved.
  stop_rule  text not null default '',
  -- draft → approved. A template is strictly more powerful than a skill (it IS the agent), so it
  -- gets at least the skill library's discipline: authored freely, but nothing runs it until a human
  -- deliberately approves it.
  status     text not null default 'draft' check (status in ('draft', 'approved', 'archived')),
  -- The planning conversation this came out of, so "how did we decide this?" is one click away and
  -- re-opening to revise it lands in the same place. Null for a template written by hand.
  party_id   uuid references public.parties(id) on delete set null,
  created_by uuid not null references auth.users(id) on delete cascade,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

-- Names are how a human picks one when binding a trigger, so they have to be unambiguous inside a
-- team. Case-insensitive: "Ticket triage" and "ticket triage" being two different templates is a
-- trap, not a feature.
create unique index if not exists agent_templates_name_unique
  on public.agent_templates (org_id, lower(name));

create index if not exists agent_templates_org on public.agent_templates (org_id, status, updated_at desc);

alter table public.agent_templates enable row level security;

-- Read: any member of the org. Writes go through the API under the service role, which is where the
-- owner/admin check and the approval rules live — same posture as projects and skills.
create policy "agent_templates: org read"
  on public.agent_templates for select to authenticated
  using (public.is_org_member(org_id));

comment on table public.agent_templates is
  '#794: an org-level persona a trigger can wake. Tools, MCP and context come from the PROJECT the run lands in — never from here.';
