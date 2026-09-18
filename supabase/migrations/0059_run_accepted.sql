-- Human "Accept" of a Review item (board CTA + drag-to-Shipped). accepted_at records that the owner
-- reviewed a run and accepted its output as shipped — even a `done`-with-no-PR run or a `failed` run
-- they judged good enough. The board routes any run with accepted_at to the Shipped column (it wins
-- over the "done but no PR → back to Review" reroute). The /accept endpoint also sets status=done, so
-- accepted work is genuinely terminal. No behavior change from the column alone; reads are tolerant.
alter table public.runs add column if not exists accepted_at timestamptz;
