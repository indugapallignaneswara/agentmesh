-- M8 schema: per-room byte budgets as first-class room policy
-- (docs/token-metering.md §7).
--
-- Budgets are byte-denominated daily ceilings on AGENT coordination traffic
-- (ingress+egress); humans are exempt by design — a runaway agent must never
-- silence the humans who would stop it. Zero means unlimited: the same
-- "disabled by default, existing deployments unaffected" posture as the rate
-- limiter. Additive only — existing rows get 0/0 and behave exactly as before.

ALTER TABLE workspaces ADD COLUMN IF NOT EXISTS budget_daily_bytes bigint NOT NULL DEFAULT 0;
ALTER TABLE workspaces ADD COLUMN IF NOT EXISTS budget_member_daily_bytes bigint NOT NULL DEFAULT 0;
