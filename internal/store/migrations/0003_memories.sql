-- Phase 2 schema: shared semantic memory with provenance and a review queue.
--
-- Visibility rule (enforced by every read path):
--   private  -> visible only to its owner
--   shared   -> visible to the workspace only once status = 'approved'
-- Shared writes land as 'pending' and must be approved by a human reviewer
-- (the anti-poisoning quarantine). tsv powers ranked full-text search; the
-- schema is vector-ready: an embedding column can be added in a later
-- migration behind the same memory_search contract.

CREATE TABLE IF NOT EXISTS memories (
    id          text        PRIMARY KEY,
    workspace   text        NOT NULL,
    scope       text        NOT NULL,
    owner       text        NOT NULL DEFAULT '',
    status      text        NOT NULL,
    content     text        NOT NULL,
    source      text        NOT NULL DEFAULT '',
    created_by  text        NOT NULL,
    reviewed_by text        NOT NULL DEFAULT '',
    review_note text        NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL,
    reviewed_at timestamptz,
    tsv tsvector GENERATED ALWAYS AS (to_tsvector('english', content)) STORED
);

CREATE INDEX IF NOT EXISTS idx_memories_tsv ON memories USING GIN (tsv);
-- Search hot path: the visibility predicate.
CREATE INDEX IF NOT EXISTS idx_memories_visibility
    ON memories (workspace, scope, status, owner);
-- Review-queue hot path: pending shared items oldest-first.
CREATE INDEX IF NOT EXISTS idx_memories_queue
    ON memories (workspace, created_at) WHERE status = 'pending';
