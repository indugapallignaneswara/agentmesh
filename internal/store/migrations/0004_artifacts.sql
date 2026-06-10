-- Phase 3 schema: co-edited artifacts with optimistic concurrency.
-- version increments on every successful write; an UPDATE conditioned on the
-- caller's base version is the lost-update guard.

CREATE TABLE IF NOT EXISTS artifacts (
    workspace  text        NOT NULL,
    name       text        NOT NULL,
    content    text        NOT NULL,
    version    bigint      NOT NULL,
    created_by text        NOT NULL,
    updated_by text        NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (workspace, name)
);
