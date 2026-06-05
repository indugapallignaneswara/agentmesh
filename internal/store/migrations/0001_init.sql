-- Phase 0 schema: members (presence), messages + deliveries (inbox),
-- events (append-only episodic log).

CREATE TABLE IF NOT EXISTS members (
    workspace  text        NOT NULL,
    name       text        NOT NULL,
    kind       text        NOT NULL,
    agent_card jsonb,
    joined_at  timestamptz NOT NULL,
    last_seen  timestamptz NOT NULL,
    PRIMARY KEY (workspace, name)
);

-- Message IDs are opaque caller-supplied strings (the service layer assigns a
-- UUID, but the store contract treats the ID as an opaque token), so id is text
-- rather than uuid.
CREATE TABLE IF NOT EXISTS messages (
    id         text        PRIMARY KEY,
    workspace  text        NOT NULL,
    sender     text        NOT NULL,
    kind       text        NOT NULL,
    body       text        NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_workspace_created
    ON messages (workspace, created_at);

CREATE TABLE IF NOT EXISTS deliveries (
    message_id   text        NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    workspace    text        NOT NULL,
    recipient    text        NOT NULL,
    delivered_at timestamptz,
    PRIMARY KEY (message_id, recipient)
);
-- Partial index over the hot path: a member's undelivered inbox.
CREATE INDEX IF NOT EXISTS idx_deliveries_inbox
    ON deliveries (workspace, recipient)
    WHERE delivered_at IS NULL;

CREATE TABLE IF NOT EXISTS events (
    seq        bigserial   PRIMARY KEY,
    workspace  text        NOT NULL,
    source     text        NOT NULL,
    type       text        NOT NULL,
    payload    jsonb,
    created_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_workspace_seq
    ON events (workspace, seq);
