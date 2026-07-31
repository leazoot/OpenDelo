-- name: CreateWorkspace :one
INSERT INTO workspaces (id, path, project_fingerprint, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)
RETURNING id, path, project_fingerprint, created_at, updated_at;

-- name: GetWorkspaceByID :one
SELECT id, path, project_fingerprint, created_at, updated_at
FROM workspaces
WHERE id = ?;

-- name: GetWorkspaceByPath :one
SELECT id, path, project_fingerprint, created_at, updated_at
FROM workspaces
WHERE path = ?;

-- name: UpdateWorkspaceFingerprint :one
UPDATE workspaces
SET project_fingerprint = ?, updated_at = ?
WHERE id = ?
RETURNING id, path, project_fingerprint, created_at, updated_at;
