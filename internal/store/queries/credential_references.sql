-- name: CreateCredentialReference :one
INSERT INTO credential_references (
    id, provider_id, provider_item_ref, field,
    service, account_label, metadata, capabilities,
    health_status, last_verified_at, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id, provider_id, provider_item_ref, field,
    service, account_label, metadata, capabilities,
    health_status, last_verified_at, created_at, updated_at;

-- name: GetCredentialReferenceByCoordinates :one
-- Serves the reuse lookup during registration. The coordinates carry a unique
-- index, so one credential is one row no matter how many identities lean on it.
SELECT id, provider_id, provider_item_ref, field,
    service, account_label, metadata, capabilities,
    health_status, last_verified_at, created_at, updated_at
FROM credential_references
WHERE provider_id = ? AND provider_item_ref = ? AND field = ?;

-- name: GetCredentialReferenceByID :one
SELECT id, provider_id, provider_item_ref, field,
    service, account_label, metadata, capabilities,
    health_status, last_verified_at, created_at, updated_at
FROM credential_references
WHERE id = ?;

-- name: UpdateCredentialReferenceHealth :one
UPDATE credential_references
SET health_status = ?, last_verified_at = ?, updated_at = ?
WHERE id = ?
RETURNING id, provider_id, provider_item_ref, field,
    service, account_label, metadata, capabilities,
    health_status, last_verified_at, created_at, updated_at;
