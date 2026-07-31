-- name: CreateDecision :one
INSERT INTO decisions (
    id, capability_request_id, verdict, risk_level, risk_factors,
    identity_id, match_level, resolved_scope, approval_requirement,
    reason_code, created_at, trust_memory_id
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id, capability_request_id, verdict, risk_level, risk_factors,
    identity_id, match_level, resolved_scope, approval_requirement,
    reason_code, created_at, trust_memory_id;

-- name: GetDecisionByID :one
SELECT id, capability_request_id, verdict, risk_level, risk_factors,
    identity_id, match_level, resolved_scope, approval_requirement,
    reason_code, created_at, trust_memory_id
FROM decisions
WHERE id = ?;

-- name: GetDecisionByCapabilityRequestID :one
SELECT id, capability_request_id, verdict, risk_level, risk_factors,
    identity_id, match_level, resolved_scope, approval_requirement,
    reason_code, created_at, trust_memory_id
FROM decisions
WHERE capability_request_id = ?;
