-- name: CreateIdentity :one
INSERT INTO identities (
    id, service, account_label, environment, is_default,
    status, credential_reference_id, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id, service, account_label, environment, is_default,
    status, credential_reference_id, created_at, updated_at;

-- name: GetIdentityByID :one
SELECT id, service, account_label, environment, is_default,
    status, credential_reference_id, created_at, updated_at
FROM identities
WHERE id = ?;

-- name: GetIdentityByServiceAndAccountLabel :one
SELECT id, service, account_label, environment, is_default,
    status, credential_reference_id, created_at, updated_at
FROM identities
WHERE service = ? AND account_label = ?;

-- name: ListIdentitiesByService :many
SELECT id, service, account_label, environment, is_default,
    status, credential_reference_id, created_at, updated_at
FROM identities
WHERE service = ?
ORDER BY account_label
LIMIT ?;

-- name: UpdateIdentityStatus :one
UPDATE identities
SET status = ?, updated_at = ?
WHERE id = ?
RETURNING id, service, account_label, environment, is_default,
    status, credential_reference_id, created_at, updated_at;

-- name: UpdateIdentityDefault :one
UPDATE identities
SET is_default = ?, updated_at = ?
WHERE id = ?
RETURNING id, service, account_label, environment, is_default,
    status, credential_reference_id, created_at, updated_at;

-- name: ListIdentities :many
-- Serves the Identities page (REQ-UI-005). Ordered by (service, account_label)
-- so it walks uq_identities_service_label instead of sorting a scan result.
SELECT id, service, account_label, environment, is_default,
    status, credential_reference_id, created_at, updated_at
FROM identities
ORDER BY service, account_label
LIMIT ?;
