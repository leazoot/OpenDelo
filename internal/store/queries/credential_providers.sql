-- name: CreateCredentialProvider :one
INSERT INTO credential_providers (id, kind, label, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)
RETURNING id, kind, label, created_at, updated_at;

-- name: GetCredentialProviderByID :one
SELECT id, kind, label, created_at, updated_at
FROM credential_providers
WHERE id = ?;

-- name: GetCredentialProviderByKindAndLabel :one
SELECT id, kind, label, created_at, updated_at
FROM credential_providers
WHERE kind = ? AND label = ?;
