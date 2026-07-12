-- M2.1 schema: at-least-once inbox (ack mode).
--
-- A leased read marks deliveries in flight until a visibility deadline instead
-- of consuming them; unless acknowledged they reappear after the deadline.
-- delivered_at remains the terminal state set by plain reads or by AckInbox.

ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS in_flight_until timestamptz;

CREATE INDEX IF NOT EXISTS idx_deliveries_inflight
    ON deliveries (workspace, recipient) WHERE delivered_at IS NULL;
