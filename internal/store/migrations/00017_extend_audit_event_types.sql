-- 00017 · Add the two agent identity event types to audit_events.
--
-- Change: widen the event_type CHECK with 'agent.identity_mismatch' and
-- 'agent.trusted'. REQ-AGENT-001 and REQ-AGENT-002 AC3 name both events, but
-- neither existed in the closed enum, so core had no way to record them.
--
-- Breaking: no. Up only widens the accepted set, so every existing row stays
-- valid and no data is rewritten.
--
-- Rollback: Down rewrites rows of the two new types to 'error' before
-- tightening the CHECK. Deleting them would lose audit rows, and audit rows are
-- never dropped by a cascade. The
-- rewrite keeps the row, its operation_id and its metadata; only the type
-- becomes coarser. Approved by the user as decision D-07 on 2026-07-29.
--
-- SQLite cannot alter a CHECK in place, so both directions rebuild the table
-- following the official procedure: create, copy, drop, rename, recreate
-- indexes. Column list and constraints are copied verbatim from 00015 apart
-- from the CHECK being changed.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE audit_events_rebuilt (
    id                     TEXT NOT NULL PRIMARY KEY,
    operation_id           TEXT NOT NULL,
    event_type             TEXT NOT NULL CHECK (event_type IN (
        'decision.auto_allowed', 'decision.user_allowed', 'decision.denied',
        'lease.created', 'lease.expired', 'lease.revoked',
        'adapter.executed', 'error', 'identity.matched', 'risk.changed',
        'security.scope_injection_ignored', 'security.secret_request_blocked',
        'audit.pruned',
        'agent.identity_mismatch', 'agent.trusted'
    )),

    agent_id               TEXT REFERENCES agents (id) ON DELETE RESTRICT,
    device_id              TEXT REFERENCES devices (id) ON DELETE RESTRICT,
    workspace_id           TEXT REFERENCES workspaces (id) ON DELETE RESTRICT,

    identity_id            TEXT REFERENCES identities (id) ON DELETE RESTRICT,
    credential_provider_id TEXT REFERENCES credential_providers (id) ON DELETE RESTRICT,

    service                TEXT NOT NULL,
    operation              TEXT NOT NULL,
    resource               TEXT NOT NULL CHECK (json_valid(resource)),
    resolved_scope         TEXT NOT NULL CHECK (json_valid(resolved_scope)),

    verdict                TEXT CHECK (verdict IS NULL OR verdict IN ('auto_allow', 'require_approval', 'deny')),
    risk_level             TEXT CHECK (risk_level IS NULL OR risk_level IN ('low', 'medium', 'high')),
    decision_id            TEXT REFERENCES decisions (id) ON DELETE RESTRICT,
    approval_id            TEXT REFERENCES approvals (id) ON DELETE RESTRICT,
    lease_id               TEXT REFERENCES leases (id) ON DELETE RESTRICT,
    lease_status           TEXT CHECK (lease_status IS NULL OR lease_status IN (
        'active', 'expired', 'exhausted', 'revoked'
    )),

    outcome                TEXT NOT NULL CHECK (outcome IN ('succeeded', 'failed', 'blocked')),
    response_status        INTEGER CHECK (response_status IS NULL OR response_status BETWEEN 100 AND 599),
    duration_ms            INTEGER NOT NULL CHECK (duration_ms >= 0),
    is_redacted            INTEGER NOT NULL CHECK (is_redacted IN (0, 1)),

    metadata               TEXT NOT NULL CHECK (json_valid(metadata)),
    created_at             TEXT NOT NULL
);

INSERT INTO audit_events_rebuilt SELECT
    id, operation_id, event_type,
    agent_id, device_id, workspace_id,
    identity_id, credential_provider_id,
    service, operation, resource, resolved_scope,
    verdict, risk_level, decision_id, approval_id, lease_id, lease_status,
    outcome, response_status, duration_ms, is_redacted,
    metadata, created_at
FROM audit_events;

DROP INDEX idx_audit_events_service_created_at;
DROP INDEX idx_audit_events_agent_id_created_at;
DROP INDEX idx_audit_events_created_at;
DROP TABLE audit_events;
ALTER TABLE audit_events_rebuilt RENAME TO audit_events;

CREATE INDEX idx_audit_events_created_at ON audit_events (created_at);
CREATE INDEX idx_audit_events_agent_id_created_at ON audit_events (agent_id, created_at);
CREATE INDEX idx_audit_events_service_created_at ON audit_events (service, created_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
UPDATE audit_events
SET event_type = 'error'
WHERE event_type IN ('agent.identity_mismatch', 'agent.trusted');

CREATE TABLE audit_events_rebuilt (
    id                     TEXT NOT NULL PRIMARY KEY,
    operation_id           TEXT NOT NULL,
    event_type             TEXT NOT NULL CHECK (event_type IN (
        'decision.auto_allowed', 'decision.user_allowed', 'decision.denied',
        'lease.created', 'lease.expired', 'lease.revoked',
        'adapter.executed', 'error', 'identity.matched', 'risk.changed',
        'security.scope_injection_ignored', 'security.secret_request_blocked',
        'audit.pruned'
    )),

    agent_id               TEXT REFERENCES agents (id) ON DELETE RESTRICT,
    device_id              TEXT REFERENCES devices (id) ON DELETE RESTRICT,
    workspace_id           TEXT REFERENCES workspaces (id) ON DELETE RESTRICT,

    identity_id            TEXT REFERENCES identities (id) ON DELETE RESTRICT,
    credential_provider_id TEXT REFERENCES credential_providers (id) ON DELETE RESTRICT,

    service                TEXT NOT NULL,
    operation              TEXT NOT NULL,
    resource               TEXT NOT NULL CHECK (json_valid(resource)),
    resolved_scope         TEXT NOT NULL CHECK (json_valid(resolved_scope)),

    verdict                TEXT CHECK (verdict IS NULL OR verdict IN ('auto_allow', 'require_approval', 'deny')),
    risk_level             TEXT CHECK (risk_level IS NULL OR risk_level IN ('low', 'medium', 'high')),
    decision_id            TEXT REFERENCES decisions (id) ON DELETE RESTRICT,
    approval_id            TEXT REFERENCES approvals (id) ON DELETE RESTRICT,
    lease_id               TEXT REFERENCES leases (id) ON DELETE RESTRICT,
    lease_status           TEXT CHECK (lease_status IS NULL OR lease_status IN (
        'active', 'expired', 'exhausted', 'revoked'
    )),

    outcome                TEXT NOT NULL CHECK (outcome IN ('succeeded', 'failed', 'blocked')),
    response_status        INTEGER CHECK (response_status IS NULL OR response_status BETWEEN 100 AND 599),
    duration_ms            INTEGER NOT NULL CHECK (duration_ms >= 0),
    is_redacted            INTEGER NOT NULL CHECK (is_redacted IN (0, 1)),

    metadata               TEXT NOT NULL CHECK (json_valid(metadata)),
    created_at             TEXT NOT NULL
);

INSERT INTO audit_events_rebuilt SELECT
    id, operation_id, event_type,
    agent_id, device_id, workspace_id,
    identity_id, credential_provider_id,
    service, operation, resource, resolved_scope,
    verdict, risk_level, decision_id, approval_id, lease_id, lease_status,
    outcome, response_status, duration_ms, is_redacted,
    metadata, created_at
FROM audit_events;

DROP INDEX idx_audit_events_service_created_at;
DROP INDEX idx_audit_events_agent_id_created_at;
DROP INDEX idx_audit_events_created_at;
DROP TABLE audit_events;
ALTER TABLE audit_events_rebuilt RENAME TO audit_events;

CREATE INDEX idx_audit_events_created_at ON audit_events (created_at);
CREATE INDEX idx_audit_events_agent_id_created_at ON audit_events (agent_id, created_at);
CREATE INDEX idx_audit_events_service_created_at ON audit_events (service, created_at);
-- +goose StatementEnd
