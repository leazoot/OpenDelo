-- +goose Up
-- leases 是一次临时授权（PRD §9.7、REQ-LEASE-001）。
-- 字段与 PRD 列出的一致，source_memory_id 随 trust_memories 一起在后续迁移中补上。
CREATE TABLE leases (
    id               TEXT NOT NULL PRIMARY KEY,
    agent_id         TEXT NOT NULL REFERENCES agents (id) ON DELETE RESTRICT,
    -- 没有身份就取不到凭据，这样的授权签发出来也执行不了。
    identity_id      TEXT NOT NULL REFERENCES identities (id) ON DELETE RESTRICT,
    service          TEXT NOT NULL,
    -- 签发时收敛好的范围与能力，JSON。REQ-LEASE-004：签发后不可修改，
    -- 需要更大范围只能重新审批并签发新的 Lease，所以没有改这两列的入口。
    resource_scope   TEXT NOT NULL CHECK (json_valid(resource_scope)),
    capabilities     TEXT NOT NULL CHECK (json_valid(capabilities)),
    -- REQ-LEASE-001 AC1：不存在「永久」。非空由 schema 表达，
    -- 这条约束不依赖任何应用层代码记得去设置它。
    expires_at       TEXT NOT NULL,
    -- 次数上限。NULL 表示不限次数 —— 但期限仍然存在，两者是独立的两把锁。
    -- 「仅允许这一次」签发 request_limit = 1 的 Lease（REQ-APPROVAL-002 AC2）。
    request_limit    INTEGER CHECK (request_limit IS NULL OR request_limit > 0),
    used_requests    INTEGER NOT NULL CHECK (used_requests >= 0),
    status           TEXT NOT NULL CHECK (status IN ('active', 'expired', 'exhausted', 'revoked')),
    -- 自动放行的 Lease 没有审批项，所以可空。
    approval_id      TEXT REFERENCES approvals (id) ON DELETE RESTRICT,
    -- REQ-APPROVAL-002 AC3：「允许到任务结束」绑定 Agent Session，
    -- Session 结束即失效。
    is_session_bound INTEGER NOT NULL CHECK (is_session_bound IN (0, 1)),
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL,
    -- 用掉的次数永远不能超过上限。计数递增用条件更新防并发，
    -- 这条 CHECK 是它的后盾：任何绕过条件更新的写入路径也超发不了。
    CHECK (request_limit IS NULL OR used_requests <= request_limit)
);

-- 唯一索引：一个 approval 只能签发一个 Lease。
-- SQLite 的唯一索引把多个 NULL 视为互不相同，自动放行的 Lease 因此不受影响。
CREATE UNIQUE INDEX uq_leases_approval_id ON leases (approval_id);

-- 服务到期扫描（REQ-LEASE-002）与 Gate 缝内侧的 Active leases 列表（REQ-LEASE-003）：
-- 两者都是「按状态过滤后按到期时间排序」。
CREATE INDEX idx_leases_status_expires_at ON leases (status, expires_at);

-- +goose Down
DROP INDEX idx_leases_status_expires_at;
DROP INDEX uq_leases_approval_id;
DROP TABLE leases;
