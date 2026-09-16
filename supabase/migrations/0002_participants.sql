-- presence on the line: engine reports participant names via heartbeat
alter table public.sessions add column participants jsonb not null default '[]';
