-- name: CreateAgent :one
INSERT INTO agents (
    id, name, type, version,
    executable_hash, executable_path, pid, parent_pid, os_user,
    device_id, workspace_id, started_at, session_key_hash, session_expires_at,
    trust_level, status, last_seen_at, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id, name, type, version,
    executable_hash, executable_path, pid, parent_pid, os_user,
    device_id, workspace_id, started_at, session_key_hash, session_expires_at,
    trust_level, status, last_seen_at, created_at, updated_at;

-- name: GetAgentByID :one
SELECT id, name, type, version,
    executable_hash, executable_path, pid, parent_pid, os_user,
    device_id, workspace_id, started_at, session_key_hash, session_expires_at,
    trust_level, status, last_seen_at, created_at, updated_at
FROM agents
WHERE id = ?;

-- name: GetAgentBySessionKeyHash :one
SELECT id, name, type, version,
    executable_hash, executable_path, pid, parent_pid, os_user,
    device_id, workspace_id, started_at, session_key_hash, session_expires_at,
    trust_level, status, last_seen_at, created_at, updated_at
FROM agents
WHERE session_key_hash = ?;

-- name: GetAgentByBinding :one
SELECT id, name, type, version,
    executable_hash, executable_path, pid, parent_pid, os_user,
    device_id, workspace_id, started_at, session_key_hash, session_expires_at,
    trust_level, status, last_seen_at, created_at, updated_at
FROM agents
WHERE device_id = ?
    AND workspace_id = ?
    AND executable_path = ?
    AND os_user = ?
    AND executable_hash = ?
ORDER BY id DESC
LIMIT 1;

-- name: RebindAgent :one
UPDATE agents
SET pid = ?, parent_pid = ?, started_at = ?,
    session_key_hash = ?, session_expires_at = ?,
    status = 'active', last_seen_at = ?, updated_at = ?
WHERE id = ?
RETURNING id, name, type, version,
    executable_hash, executable_path, pid, parent_pid, os_user,
    device_id, workspace_id, started_at, session_key_hash, session_expires_at,
    trust_level, status, last_seen_at, created_at, updated_at;

-- name: UpdateAgentTrustLevel :one
UPDATE agents
SET trust_level = ?, updated_at = ?
WHERE id = ?
RETURNING id, name, type, version,
    executable_hash, executable_path, pid, parent_pid, os_user,
    device_id, workspace_id, started_at, session_key_hash, session_expires_at,
    trust_level, status, last_seen_at, created_at, updated_at;

-- name: UpdateAgentStatus :one
UPDATE agents
SET status = ?, updated_at = ?
WHERE id = ?
RETURNING id, name, type, version,
    executable_hash, executable_path, pid, parent_pid, os_user,
    device_id, workspace_id, started_at, session_key_hash, session_expires_at,
    trust_level, status, last_seen_at, created_at, updated_at;

-- name: UpdateAgentLastSeen :one
UPDATE agents
SET last_seen_at = ?, updated_at = ?
WHERE id = ?
RETURNING id, name, type, version,
    executable_hash, executable_path, pid, parent_pid, os_user,
    device_id, workspace_id, started_at, session_key_hash, session_expires_at,
    trust_level, status, last_seen_at, created_at, updated_at;

-- name: ListAgents :many
-- Serves the Identities page's Agents column (design doc section 05).
-- Newest first: the agent that just connected is the one the user is looking for.
SELECT id, name, type, version,
    executable_hash, executable_path, pid, parent_pid, os_user,
    device_id, workspace_id, started_at, session_key_hash, session_expires_at,
    trust_level, status, last_seen_at, created_at, updated_at
FROM agents
ORDER BY last_seen_at DESC, id DESC
LIMIT ?;
