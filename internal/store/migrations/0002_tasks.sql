-- Phase 1 schema: shared task board.
--
-- Tasks decouple "what needs doing" from "who does it". Claiming uses
-- SELECT ... FOR UPDATE SKIP LOCKED so concurrent agents never claim the same
-- task. A claim carries a lease (lease_expires_at); when it lapses the task is
-- eligible again (work-stealing).

CREATE TABLE IF NOT EXISTS tasks (
    id              text        PRIMARY KEY,
    workspace       text        NOT NULL,
    title           text        NOT NULL,
    details         text        NOT NULL DEFAULT '',
    status          text        NOT NULL,
    created_by      text        NOT NULL,
    assigned_agent  text        NOT NULL DEFAULT '',
    result          text        NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL,
    claimed_at      timestamptz,
    lease_expires_at timestamptz
);

-- The claim hot path scans a workspace's pending tasks oldest-first.
CREATE INDEX IF NOT EXISTS idx_tasks_workspace_status_created
    ON tasks (workspace, status, created_at);

-- task_deps holds the dependency edges: task_id depends on depends_on_id. A
-- task is claimable only when every dependency is completed. The FK on
-- depends_on_id (with the composite PK) rejects dangling dependencies at
-- create time.
CREATE TABLE IF NOT EXISTS task_deps (
    task_id       text NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    depends_on_id text NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    PRIMARY KEY (task_id, depends_on_id)
);
CREATE INDEX IF NOT EXISTS idx_task_deps_depends_on ON task_deps (depends_on_id);
