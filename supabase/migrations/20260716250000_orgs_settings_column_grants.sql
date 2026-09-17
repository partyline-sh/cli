-- 0012_authz_hardening revoked table-wide UPDATE on orgs and re-granted only (name). Two org-settings
-- columns added since — require_review (Review gate) and git_provider (active repo provider) — are
-- edited through the same owner/admin PATCH /api/v1/orgs/[slug] via the caller's RLS client, but were
-- never added to the column grant. Result: "permission denied for table orgs" (SQLSTATE 42501) when
-- writing them (reproduced switching the git provider).
--
-- Grant the column-level UPDATE. Authorization is unchanged and still enforced twice: the orgs UPDATE
-- RLS policy restricts WHICH org (org_role in owner/admin), and the PATCH handler re-checks the role.
-- This only widens which COLUMNS an already-authorized editor may set — matching how (name) works.
grant update (require_review, git_provider) on public.orgs to authenticated;
