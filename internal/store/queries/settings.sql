-- name: GetSetting :one
SELECT id, name, value, created_at, updated_at
FROM settings
WHERE name = ?;

-- name: ListSettings :many
SELECT id, name, value, created_at, updated_at
FROM settings
ORDER BY name;

-- name: CreateSetting :one
INSERT INTO settings (id, name, value, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)
RETURNING id, name, value, created_at, updated_at;

-- name: UpdateSettingValue :one
UPDATE settings
SET value = ?, updated_at = ?
WHERE name = ?
RETURNING id, name, value, created_at, updated_at;

-- name: DeleteSetting :exec
DELETE FROM settings
WHERE name = ?;
