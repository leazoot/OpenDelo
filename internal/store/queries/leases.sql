-- name: CreateLease :one
INSERT INTO leases (
    id, agent_id, identity_id, service, resource_scope, capabilities,
    expires_at, request_limit, used_requests, status, approval_id,
    is_session_bound, created_at, updated_at, source_memory_id
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id, agent_id, identity_id, service, resource_scope, capabilities,
    expires_at, request_limit, used_requests, status, approval_id,
    is_session_bound, created_at, updated_at, source_memory_id;

-- name: GetLeaseByID :one
SELECT id, agent_id, identity_id, service, resource_scope, capabilities,
    expires_at, request_limit, used_requests, status, approval_id,
    is_session_bound, created_at, updated_at, source_memory_id
FROM leases
WHERE id = ?;

-- name: ListLeasesByStatus :many
SELECT id, agent_id, identity_id, service, resource_scope, capabilities,
    expires_at, request_limit, used_requests, status, approval_id,
    is_session_bound, created_at, updated_at, source_memory_id
FROM leases
WHERE status = ?
ORDER BY expires_at
LIMIT ?;

-- name: ListActiveLeasesDueBefore :many
SELECT id, agent_id, identity_id, service, resource_scope, capabilities,
    expires_at, request_limit, used_requests, status, approval_id,
    is_session_bound, created_at, updated_at, source_memory_id
FROM leases
WHERE status = 'active' AND expires_at <= ?
ORDER BY expires_at
LIMIT ?;

-- name: ConsumeLease :one
UPDATE leases
SET used_requests = used_requests + 1, updated_at = ?
WHERE id = ?
    AND status = 'active'
    AND expires_at > ?
    AND (request_limit IS NULL OR used_requests < request_limit)
RETURNING id, agent_id, identity_id, service, resource_scope, capabilities,
    expires_at, request_limit, used_requests, status, approval_id,
    is_session_bound, created_at, updated_at, source_memory_id;

-- name: CloseLease :one
UPDATE leases
SET status = ?, updated_at = ?
WHERE id = ? AND status = 'active'
RETURNING id, agent_id, identity_id, service, resource_scope, capabilities,
    expires_at, request_limit, used_requests, status, approval_id,
    is_session_bound, created_at, updated_at, source_memory_id;

-- name: ShortenLease :one
UPDATE leases
SET expires_at = ?, updated_at = ?
WHERE id = ? AND status = 'active' AND expires_at > ?
RETURNING id, agent_id, identity_id, service, resource_scope, capabilities,
    expires_at, request_limit, used_requests, status, approval_id,
    is_session_bound, created_at, updated_at, source_memory_id;

-- name: ListActiveLeasesByCredentialReference :many
-- Serves the cascade revocation required when a credential is disconnected
-- (REQ-CRED-005 AC3). Walks leases -> identities -> credential_references.
SELECT l.id, l.agent_id, l.identity_id, l.service, l.resource_scope, l.capabilities,
    l.expires_at, l.request_limit, l.used_requests, l.status, l.approval_id,
    l.is_session_bound, l.created_at, l.updated_at, l.source_memory_id
FROM leases AS l
JOIN identities AS i ON i.id = l.identity_id
WHERE l.status = 'active' AND i.credential_reference_id = ?
ORDER BY l.expires_at
LIMIT ?;

-- name: GetLeaseByApprovalID :one
SELECT id, agent_id, identity_id, service, resource_scope, capabilities,
    expires_at, request_limit, used_requests, status, approval_id,
    is_session_bound, created_at, updated_at, source_memory_id
FROM leases
WHERE approval_id = ?;

-- name: ListActiveLeasesByIdentity :many
-- Feeds the cascade that revokes an identity's leases on disconnect
-- (REQ-IDENT-001 AC2).
SELECT id, agent_id, identity_id, service, resource_scope, capabilities,
    expires_at, request_limit, used_requests, status, approval_id,
    is_session_bound, created_at, updated_at, source_memory_id
FROM leases
WHERE identity_id = ? AND status = 'active'
ORDER BY expires_at
LIMIT ?;

-- name: ListActiveSessionBoundLeasesByAgent :many
-- Feeds the cascade that revokes an agent's task-scoped leases when its session
-- ends (REQ-CLI-002 AC3). Only session-bound leases are listed: a lease granted
-- for a single call or for the whole project outlives the process that asked
-- for it, and revoking those here would silently shrink what the user allowed.
SELECT id, agent_id, identity_id, service, resource_scope, capabilities,
    expires_at, request_limit, used_requests, status, approval_id,
    is_session_bound, created_at, updated_at, source_memory_id
FROM leases
WHERE agent_id = ? AND status = 'active' AND is_session_bound = 1
ORDER BY expires_at
LIMIT ?;
