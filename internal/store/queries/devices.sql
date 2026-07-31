-- name: CreateDevice :one
INSERT INTO devices (id, fingerprint, name, trust_status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING id, fingerprint, name, trust_status, created_at, updated_at;

-- name: GetDeviceByID :one
SELECT id, fingerprint, name, trust_status, created_at, updated_at
FROM devices
WHERE id = ?;

-- name: GetDeviceByFingerprint :one
SELECT id, fingerprint, name, trust_status, created_at, updated_at
FROM devices
WHERE fingerprint = ?;

-- name: UpdateDeviceTrustStatus :one
UPDATE devices
SET trust_status = ?, updated_at = ?
WHERE id = ?
RETURNING id, fingerprint, name, trust_status, created_at, updated_at;
