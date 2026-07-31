-- name: CreateApproval :one
INSERT INTO approvals (
    id, decision_id, action, status, expires_at, decided_at, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id, decision_id, action, status, expires_at, decided_at, created_at, updated_at;

-- name: GetApprovalByID :one
SELECT id, decision_id, action, status, expires_at, decided_at, created_at, updated_at
FROM approvals
WHERE id = ?;

-- name: GetApprovalByDecisionID :one
SELECT id, decision_id, action, status, expires_at, decided_at, created_at, updated_at
FROM approvals
WHERE decision_id = ?;

-- name: ListApprovalsByStatus :many
SELECT id, decision_id, action, status, expires_at, decided_at, created_at, updated_at
FROM approvals
WHERE status = ?
ORDER BY expires_at
LIMIT ?;

-- name: ListPendingApprovalsDueBefore :many
SELECT id, decision_id, action, status, expires_at, decided_at, created_at, updated_at
FROM approvals
WHERE status = 'pending' AND expires_at <= ?
ORDER BY expires_at
LIMIT ?;

-- name: SettleApproval :one
UPDATE approvals
SET action = ?, status = ?, decided_at = ?, updated_at = ?
WHERE id = ? AND status = 'pending'
RETURNING id, decision_id, action, status, expires_at, decided_at, created_at, updated_at;
