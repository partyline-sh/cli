-- Loops.so audience sync: stamp when a new account was pushed to Loops.
-- The auth callback claims this atomically (update … where welcomed_at is null)
-- so racing callbacks / re-logins sync a user to the audience exactly once.
alter table public.profiles add column if not exists welcomed_at timestamptz;
