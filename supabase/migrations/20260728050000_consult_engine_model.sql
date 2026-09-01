-- Consult answers get their own engine + model, alongside the existing per-phase profile.
--
-- runConsultAnswer passed NO model at all, so a peer's question was answered by whatever that
-- machine's CLI happened to default to. That is the one output in the product with no gate behind
-- it: a crank build's weak model is caught by the verify gate (checks + adversarial reviewer), but a
-- consult answer goes straight into the asking agent's context and steers its work unverified.
--
-- So this is a QUALITY setting, not a cost one. The daily cap already bounds how often a peer can
-- make this machine think (24/project, 48/machine); within that budget the answer should be as good
-- as the project can make it, which is exactly the argument plan_model was created for.
--
-- Both are NULL-by-default and resolve like the rest of the profile:
--   consult_model  → the daemon passes it to the engine; empty = the engine's own default.
--   consult_engine → suggestion only. preferEngine() falls back to the machine's local per-project
--                    engine when the value is unknown, because which CLI is installed and logged in
--                    is a fact only the answering box has.
alter table public.projects add column if not exists consult_engine text;
alter table public.projects add column if not exists consult_model  text;

comment on column public.projects.consult_engine is
  'Engine for answering a peer consult. A suggestion — the daemon falls back to its local per-project engine when this names one it cannot run.';
comment on column public.projects.consult_model is
  'Model for answering a peer consult. Quality matters more here than anywhere else: a consult answer is the only agent output with no verify gate behind it.';
