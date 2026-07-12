-- M1.4 schema: invites and per-room policy.
--
-- invites are single- or multi-use codes a moderator mints so a named-or-not
-- principal of a given kind can join the room (optionally arriving with the
-- moderator role). Only the SHA-256 hash of the code is stored; redemption is
-- atomic (uses < max_uses), revocation is soft.
--
-- join_policy: open (anyone may join) | invite (a valid invite code required).
-- who_may_broadcast: anyone | moderators (human owner/moderators only).

CREATE TABLE IF NOT EXISTS invites (
    id         text        PRIMARY KEY,
    code_hash  text        NOT NULL UNIQUE,
    workspace  text        NOT NULL,
    kind       text        NOT NULL,
    role       text        NOT NULL DEFAULT 'member',
    max_uses   int         NOT NULL,
    uses       int         NOT NULL DEFAULT 0,
    created_by text        NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz,
    revoked_at timestamptz
);

CREATE INDEX IF NOT EXISTS idx_invites_workspace ON invites (workspace);

ALTER TABLE workspaces ADD COLUMN IF NOT EXISTS join_policy text NOT NULL DEFAULT 'open';
ALTER TABLE workspaces ADD COLUMN IF NOT EXISTS who_may_broadcast text NOT NULL DEFAULT 'anyone';
