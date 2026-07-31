-- name: CreateCapabilityRequest :one
INSERT INTO capability_requests (
    id, operation_id, agent_id, workspace_id, service, operation,
    resource, desired_change, reason, status, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id, operation_id, agent_id, workspace_id, service, operation,
    resource, desired_change, reason, status, created_at, updated_at, change_preview;

-- name: GetCapabilityRequestByID :one
SELECT id, operation_id, agent_id, workspace_id, service, operation,
    resource, desired_change, reason, status, created_at, updated_at, change_preview
FROM capability_requests
WHERE id = ?;

-- name: ListCapabilityRequestsByStatus :many
SELECT id, operation_id, agent_id, workspace_id, service, operation,
    resource, desired_change, reason, status, created_at, updated_at, change_preview
FROM capability_requests
WHERE status = ?
ORDER BY created_at
LIMIT ?;

-- name: UpdateCapabilityRequestStatus :one
UPDATE capability_requests
SET status = ?, updated_at = ?
WHERE id = ? AND status = ?
RETURNING id, operation_id, agent_id, workspace_id, service, operation,
    resource, desired_change, reason, status, created_at, updated_at, change_preview;

-- name: UpdateCapabilityRequestChangePreview :execrows
-- The preview only lands on a request that is still waiting for a person. Once a
-- request has a verdict, the old value shown in the folio is the one the decision
-- was made against, and writing over it afterwards rewrites that evidence. The
-- status lives in the WHERE clause rather than in a read-then-check, so a
-- concurrent decision is not overwritten by this update.
UPDATE capability_requests
SET change_preview = ?, updated_at = ?
WHERE id = ? AND status = ?;
