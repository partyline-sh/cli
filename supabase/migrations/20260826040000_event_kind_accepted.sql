-- The board's last column is called Accepted, not Shipped, and the outbound event follows it.
--
-- "Shipped" claimed something partyline cannot know: every customer's deploy pipeline is different,
-- and two projects on ONE board can differ. What the product can honestly claim is that a human
-- accepted the work — which is literally what raises this event (runs.accepted_at).
--
-- Renaming a PUBLIC event name is normally a breaking change and would need both names carried
-- through a deprecation window. It is free right now because nothing is subscribed to it, and it
-- stops being free the day something is.
--
-- Order matters: widen the constraint to accept BOTH names, migrate the rows, then narrow it to the
-- new name only. Dropping the old check first would leave a window where any kind could land, and
-- narrowing before migrating would reject the rows we are about to rewrite.

alter table public.events drop constraint if exists events_kind_check;

alter table public.events add constraint events_kind_check check (kind in (
  'run.completed','run.failed','run.killed','run.needs_approval',
  'work_item.shipped','work_item.accepted','trigger.fired'
));

update public.events set kind = 'work_item.accepted' where kind = 'work_item.shipped';

alter table public.events drop constraint events_kind_check;

alter table public.events add constraint events_kind_check check (kind in (
  'run.completed','run.failed','run.killed','run.needs_approval',
  'work_item.accepted','trigger.fired'
));
