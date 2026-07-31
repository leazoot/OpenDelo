-- name: CreateServiceAdapter :one
INSERT INTO service_adapters (
    id, service, kind, display_name, base_url, auth_scheme,
    capabilities, allowed_paths, allowed_methods, redaction_rules,
    default_risk_level, status, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id, service, kind, display_name, base_url, auth_scheme,
    capabilities, allowed_paths, allowed_methods, redaction_rules,
    default_risk_level, status, created_at, updated_at;

-- name: GetServiceAdapterByID :one
SELECT id, service, kind, display_name, base_url, auth_scheme,
    capabilities, allowed_paths, allowed_methods, redaction_rules,
    default_risk_level, status, created_at, updated_at
FROM service_adapters
WHERE id = ?;

-- name: GetServiceAdapterByService :one
SELECT id, service, kind, display_name, base_url, auth_scheme,
    capabilities, allowed_paths, allowed_methods, redaction_rules,
    default_risk_level, status, created_at, updated_at
FROM service_adapters
WHERE service = ?;

-- name: ListEnabledServiceAdapters :many
SELECT id, service, kind, display_name, base_url, auth_scheme,
    capabilities, allowed_paths, allowed_methods, redaction_rules,
    default_risk_level, status, created_at, updated_at
FROM service_adapters
WHERE status = 'enabled'
ORDER BY service
LIMIT ?;

-- name: UpdateServiceAdapterStatus :one
UPDATE service_adapters
SET status = ?, updated_at = ?
WHERE id = ?
RETURNING id, service, kind, display_name, base_url, auth_scheme,
    capabilities, allowed_paths, allowed_methods, redaction_rules,
    default_risk_level, status, created_at, updated_at;

-- name: UpdateServiceAdapterDeclaration :one
UPDATE service_adapters
SET base_url = ?, auth_scheme = ?, capabilities = ?, allowed_paths = ?,
    allowed_methods = ?, redaction_rules = ?, default_risk_level = ?, updated_at = ?
WHERE id = ?
RETURNING id, service, kind, display_name, base_url, auth_scheme,
    capabilities, allowed_paths, allowed_methods, redaction_rules,
    default_risk_level, status, created_at, updated_at;
