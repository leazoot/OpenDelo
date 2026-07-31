-- name: AppendAuditEvent :one
INSERT INTO audit_events (
    id, operation_id, event_type, agent_id, device_id, workspace_id,
    identity_id, credential_provider_id, service, operation, resource,
    resolved_scope, verdict, risk_level, decision_id, approval_id,
    lease_id, lease_status, outcome, response_status, duration_ms,
    is_redacted, metadata, created_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id, operation_id, event_type, agent_id, device_id, workspace_id,
    identity_id, credential_provider_id, service, operation, resource,
    resolved_scope, verdict, risk_level, decision_id, approval_id,
    lease_id, lease_status, outcome, response_status, duration_ms,
    is_redacted, metadata, created_at;

-- name: GetAuditEventByID :one
SELECT id, operation_id, event_type, agent_id, device_id, workspace_id,
    identity_id, credential_provider_id, service, operation, resource,
    resolved_scope, verdict, risk_level, decision_id, approval_id,
    lease_id, lease_status, outcome, response_status, duration_ms,
    is_redacted, metadata, created_at
FROM audit_events
WHERE id = ?;

-- name: ListAuditEvents :many
SELECT id, operation_id, event_type, agent_id, device_id, workspace_id,
    identity_id, credential_provider_id, service, operation, resource,
    resolved_scope, verdict, risk_level, decision_id, approval_id,
    lease_id, lease_status, outcome, response_status, duration_ms,
    is_redacted, metadata, created_at
FROM audit_events
WHERE created_at < ?
ORDER BY created_at DESC
LIMIT ?;

-- name: ListAuditEventsByAgent :many
SELECT id, operation_id, event_type, agent_id, device_id, workspace_id,
    identity_id, credential_provider_id, service, operation, resource,
    resolved_scope, verdict, risk_level, decision_id, approval_id,
    lease_id, lease_status, outcome, response_status, duration_ms,
    is_redacted, metadata, created_at
FROM audit_events
WHERE agent_id = ? AND created_at < ?
ORDER BY created_at DESC
LIMIT ?;

-- name: ListAuditEventsByService :many
SELECT id, operation_id, event_type, agent_id, device_id, workspace_id,
    identity_id, credential_provider_id, service, operation, resource,
    resolved_scope, verdict, risk_level, decision_id, approval_id,
    lease_id, lease_status, outcome, response_status, duration_ms,
    is_redacted, metadata, created_at
FROM audit_events
WHERE service = ? AND created_at < ?
ORDER BY created_at DESC
LIMIT ?;

-- name: CountAuditEventsBefore :one
SELECT count(*) FROM audit_events
WHERE created_at < ?;

-- name: PruneAuditEventsBefore :execrows
DELETE FROM audit_events
WHERE created_at < ?;
