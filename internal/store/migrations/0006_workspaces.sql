-- M1 (v0.2) schema: rooms as first-class objects.
--
-- Before this, a workspace existed only implicitly (a member row referencing a
-- name). The workspaces table makes a room a durable, human-owned object with
-- a lifecycle: open rooms accept new content, closed rooms reject writes but
-- stay readable for review. Existing data keeps working — rows here are created
-- explicitly (room_create) or lazily on first join when implicit mode is on.

CREATE TABLE IF NOT EXISTS workspaces (
    name       text        PRIMARY KEY,
    status     text        NOT NULL DEFAULT 'open',
    created_by text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    closed_by  text        NOT NULL DEFAULT '',
    closed_at  timestamptz
);

CREATE INDEX IF NOT EXISTS idx_workspaces_status ON workspaces (status);
