-- M6.1 schema: usage ledger (token metering, docs/token-metering.md §4).
--
-- usage_events is the append-only audit trail of metered coordination traffic;
-- usage_daily is the per-(workspace, member, UTC day) rollup maintained by
-- upsert in the same transaction as each event batch. Bytes are the immutable
-- ground truth; there is deliberately NO est_tokens column — token estimates
-- are derived at display time with a configurable ratio (§3) so history
-- re-renders correctly under recalibration.

CREATE TABLE IF NOT EXISTS usage_events (
    id                         bigserial PRIMARY KEY,
    ts                         timestamptz NOT NULL,
    workspace                  text NOT NULL,
    member                     text NOT NULL,
    kind                       text NOT NULL,
    tool                       text NOT NULL,
    direction                  text NOT NULL,
    bytes                      bigint NOT NULL DEFAULT 0,
    authenticated              boolean NOT NULL DEFAULT false,
    reported_prompt_tokens     bigint,
    reported_completion_tokens bigint,
    vendor                     text,
    model                      text
);

CREATE INDEX IF NOT EXISTS idx_usage_events_ws_ts
    ON usage_events (workspace, ts);
CREATE INDEX IF NOT EXISTS idx_usage_events_ws_member_ts
    ON usage_events (workspace, member, ts);

CREATE TABLE IF NOT EXISTS usage_daily (
    workspace                  text NOT NULL,
    member                     text NOT NULL,
    day                        date NOT NULL,
    ingress_bytes              bigint NOT NULL DEFAULT 0,
    egress_bytes               bigint NOT NULL DEFAULT 0,
    events                     bigint NOT NULL DEFAULT 0,
    reported_prompt_tokens     bigint NOT NULL DEFAULT 0,
    reported_completion_tokens bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (workspace, member, day)
);
