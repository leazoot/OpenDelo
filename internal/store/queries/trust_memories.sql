-- name: CreateTrustMemory :one
INSERT INTO trust_memories (
    id, agent_id, workspace_id, identity_id, service,
    resource_scope, capability_scope, environment, risk_ceiling,
    approval_behavior, created_from, status, invalidation_reason,
    last_used_at, expires_at, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id, agent_id, workspace_id, identity_id, service,
    resource_scope, capability_scope, environment, risk_ceiling,
    approval_behavior, created_from, status, invalidation_reason,
    last_used_at, expires_at, created_at, updated_at;

-- name: GetTrustMemoryByID :one
SELECT id, agent_id, workspace_id, identity_id, service,
    resource_scope, capability_scope, environment, risk_ceiling,
    approval_behavior, created_from, status, invalidation_reason,
    last_used_at, expires_at, created_at, updated_at
FROM trust_memories
WHERE id = ?;

-- name: MatchTrustMemories :many
SELECT id, agent_id, workspace_id, identity_id, service,
    resource_scope, capability_scope, environment, risk_ceiling,
    approval_behavior, created_from, status, invalidation_reason,
    last_used_at, expires_at, created_at, updated_at
FROM trust_memories
WHERE agent_id = ? AND workspace_id = ? AND service = ? AND status = 'active'
ORDER BY id
LIMIT ?;

-- name: ListTrustMemoriesByStatus :many
SELECT id, agent_id, workspace_id, identity_id, service,
    resource_scope, capability_scope, environment, risk_ceiling,
    approval_behavior, created_from, status, invalidation_reason,
    last_used_at, expires_at, created_at, updated_at
FROM trust_memories
WHERE status = ?
ORDER BY id
LIMIT ?;

-- name: TouchTrustMemory :one
UPDATE trust_memories
SET last_used_at = ?, updated_at = ?
WHERE id = ? AND status = 'active'
RETURNING id, agent_id, workspace_id, identity_id, service,
    resource_scope, capability_scope, environment, risk_ceiling,
    approval_behavior, created_from, status, invalidation_reason,
    last_used_at, expires_at, created_at, updated_at;

-- name: InvalidateTrustMemory :one
UPDATE trust_memories
SET status = 'invalidated', invalidation_reason = ?, updated_at = ?
WHERE id = ? AND status = 'active'
RETURNING id, agent_id, workspace_id, identity_id, service,
    resource_scope, capability_scope, environment, risk_ceiling,
    approval_behavior, created_from, status, invalidation_reason,
    last_used_at, expires_at, created_at, updated_at;

-- name: DeleteTrustMemory :execrows
DELETE FROM trust_memories
WHERE id = ?;

-- name: ListTrustMemoriesByIdentity :many
-- Feeds the cascade that invalidates an identity's memories on disconnect
-- (REQ-IDENT-001 AC2).
SELECT id, agent_id, workspace_id, identity_id, service,
    resource_scope, capability_scope, environment, risk_ceiling,
    approval_behavior, created_from, status, invalidation_reason,
    last_used_at, expires_at, created_at, updated_at
FROM trust_memories
WHERE identity_id = ? AND status = 'active'
ORDER BY id
LIMIT ?;

-- name: TightenTrustMemoryBehavior :one
-- Only auto_allow -> always_ask (REQ-TRUST-005). The direction is pinned in the
-- WHERE clause, so widening cannot be expressed here at all.
UPDATE trust_memories
SET approval_behavior = 'always_ask', updated_at = ?
WHERE id = ? AND status = 'active' AND approval_behavior = 'auto_allow'
RETURNING id, agent_id, workspace_id, identity_id, service,
    resource_scope, capability_scope, environment, risk_ceiling,
    approval_behavior, created_from, status, invalidation_reason,
    last_used_at, expires_at, created_at, updated_at;
