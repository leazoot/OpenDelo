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
