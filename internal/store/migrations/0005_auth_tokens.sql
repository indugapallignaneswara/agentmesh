-- Phase 4 schema: bearer tokens binding a credential to one principal
-- (workspace + member + kind). Only the SHA-256 hash of the secret is stored;
-- revocation is a soft mark so the audit trail survives.

CREATE TABLE IF NOT EXISTS auth_tokens (
    id         text        PRIMARY KEY,
    token_hash text        NOT NULL UNIQUE,
    workspace  text        NOT NULL,
    member     text        NOT NULL,
    kind       text        NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz,
    revoked_at timestamptz
);

CREATE INDEX IF NOT EXISTS idx_auth_tokens_workspace ON auth_tokens (workspace, member);
