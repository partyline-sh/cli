-- Token accounting that tells the truth. `run_tasks.tokens` stored claude's Usage.Total() —
-- input + output + cache_creation + cache_read — which made a 12-minute agentic run read as
-- "9.8M tokens": the same cached context re-read every turn, summed. cache_read is a re-read of an
-- already-paid prefix, not new work, so it's the wrong number to headline.
--
-- Carry the honest breakdown instead:
--   fresh_tokens       = input + output + cache_creation (the genuinely-new spend — what we display)
--   cache_read_tokens  = cache_read only (a muted "+N cached" detail)
--   cost_usd           = claude's own total_cost_usd (the unambiguous figure)
-- `tokens` (Total) stays for back-compat / the O.5 ceiling; the UI switches to fresh_tokens + cost_usd.
alter table public.run_tasks add column if not exists fresh_tokens      int;
alter table public.run_tasks add column if not exists cache_read_tokens bigint; -- re-reads across a long run can exceed int32
alter table public.run_tasks add column if not exists cost_usd          numeric(12, 6);
