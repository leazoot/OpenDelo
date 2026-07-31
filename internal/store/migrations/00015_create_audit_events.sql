-- +goose Up
-- audit_events 是账本本身（PRD §9.9、§16.4、REQ-AUDIT-001）。
--
-- 只追加：没有任何 UPDATE 入口，删除只由保留期清理按时间条件执行
-- （REQ-AUDIT-005）。审计写入是执行的前置条件，写不进去请求就失败（ADR-004）。
--
-- 默认不记录完整请求正文、完整响应正文、Secret、文件内容与用户敏感数据
-- （PRD §22.1）。这张表里没有任何能装下这些东西的列。
CREATE TABLE audit_events (
    id                     TEXT NOT NULL PRIMARY KEY,
    -- 贯穿日志、错误与账本的追溯号。用户拿到一个 operation_id 就能在这里定位。
    operation_id           TEXT NOT NULL,
    -- REQ-AUDIT-002 的十类事件，加上需求里点名的三个 security / 清理事件。
    -- 封闭枚举：新增类型要同时更新前端过滤器（AC2），CHECK 让人不得不改这里。
    event_type             TEXT NOT NULL CHECK (event_type IN (
        'decision.auto_allowed', 'decision.user_allowed', 'decision.denied',
        'lease.created', 'lease.expired', 'lease.revoked',
        'adapter.executed', 'error', 'identity.matched', 'risk.changed',
        'security.scope_injection_ignored', 'security.secret_request_blocked',
        'audit.pruned'
    )),

    -- 请求者与来处。全部可空：认不出 Agent 正是要拒绝并记录的情况之一，
    -- 那时这些字段本来就没有答案，填一个假的比留空更糟。
    agent_id               TEXT REFERENCES agents (id) ON DELETE RESTRICT,
    device_id              TEXT REFERENCES devices (id) ON DELETE RESTRICT,
    workspace_id           TEXT REFERENCES workspaces (id) ON DELETE RESTRICT,

    -- 用的是哪个身份、凭据来自哪个来源。记的是来源本身，不是凭据。
    identity_id            TEXT REFERENCES identities (id) ON DELETE RESTRICT,
    credential_provider_id TEXT REFERENCES credential_providers (id) ON DELETE RESTRICT,

    -- 目标：哪个服务、什么操作、动了什么资源。
    service                TEXT NOT NULL,
    operation              TEXT NOT NULL,
    resource               TEXT NOT NULL CHECK (json_valid(resource)),
    -- 最终生效的 Scope。与 decisions 重复一份是有意的：账本要能独立解释一条记录，
    -- 不能依赖去别的表拼装。
    resolved_scope         TEXT NOT NULL CHECK (json_valid(resolved_scope)),

    -- 决策过程与经手的记录。
    verdict                TEXT CHECK (verdict IS NULL OR verdict IN ('auto_allow', 'require_approval', 'deny')),
    risk_level             TEXT CHECK (risk_level IS NULL OR risk_level IN ('low', 'medium', 'high')),
    decision_id            TEXT REFERENCES decisions (id) ON DELETE RESTRICT,
    approval_id            TEXT REFERENCES approvals (id) ON DELETE RESTRICT,
    lease_id               TEXT REFERENCES leases (id) ON DELETE RESTRICT,
    lease_status           TEXT CHECK (lease_status IS NULL OR lease_status IN (
        'active', 'expired', 'exhausted', 'revoked'
    )),

    -- 执行结果与耗时。
    outcome                TEXT NOT NULL CHECK (outcome IN ('succeeded', 'failed', 'blocked')),
    -- 外部服务返回的状态码，没有发出请求时为 NULL。
    response_status        INTEGER CHECK (response_status IS NULL OR response_status BETWEEN 100 AND 599),
    duration_ms            INTEGER NOT NULL CHECK (duration_ms >= 0),
    -- 这次返回给 Agent 的内容是否经过脱敏（REQ-AUDIT-001 的必记项之一）。
    is_redacted            INTEGER NOT NULL CHECK (is_redacted IN (0, 1)),

    -- 元数据，JSON 对象。放的是可展示的键值明细，不是正文。
    metadata               TEXT NOT NULL CHECK (json_valid(metadata)),
    created_at             TEXT NOT NULL
);

-- Ledger 默认视图：按时间倒序翻页（REQ-AUDIT-003）。
CREATE INDEX idx_audit_events_created_at ON audit_events (created_at);

-- Ledger 的两个主过滤维度。带上 created_at 让过滤后的倒序也走索引，
-- 否则十万条记录下要为排序整集建临时 B 树（REQ-NFR-001：P95 < 300ms）。
CREATE INDEX idx_audit_events_agent_id_created_at ON audit_events (agent_id, created_at);
CREATE INDEX idx_audit_events_service_created_at ON audit_events (service, created_at);

-- +goose Down
DROP INDEX idx_audit_events_service_created_at;
DROP INDEX idx_audit_events_agent_id_created_at;
DROP INDEX idx_audit_events_created_at;
DROP TABLE audit_events;
