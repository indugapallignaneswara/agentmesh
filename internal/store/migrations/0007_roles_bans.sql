-- M1.2 schema: member roles and room bans.
--
-- role: owner | moderator | member. The room creator becomes owner on join;
-- SetMemberRole is the only other mutator (rejoin preserves the role).
-- bans are name-based per room: a banned name cannot rejoin until unbanned.

ALTER TABLE members ADD COLUMN IF NOT EXISTS role text NOT NULL DEFAULT 'member';

CREATE TABLE IF NOT EXISTS bans (
    workspace  text        NOT NULL,
    name       text        NOT NULL,
    banned_by  text        NOT NULL,
    reason     text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    PRIMARY KEY (workspace, name)
);
