-- +goose Up
-- decisions 是决策引擎对一次请求给出的结论（PRD §9.6）。
-- 这张表只追加不修改：改写一条决策等于改写「当时为什么放行」，账本会因此不可信。
CREATE TABLE decisions (
    id                    TEXT NOT NULL PRIMARY KEY,
    capability_request_id TEXT NOT NULL
        REFERENCES capability_requests (id) ON DELETE RESTRICT,
    -- PRD §9.6 的三种结论。放行出口唯一，
    -- 取值多一个就意味着多一条放行路径。
    verdict               TEXT NOT NULL CHECK (verdict IN ('auto_allow', 'require_approval', 'deny')),
    -- REQ-RISK-001 的输出：等级 + 触发的因子列表（JSON 数组），
    -- 后者是 Access Folio 用来解释「为什么是这个等级」的依据（AC3）。
    risk_level            TEXT NOT NULL CHECK (risk_level IN ('low', 'medium', 'high')),
    risk_factors          TEXT NOT NULL CHECK (json_valid(risk_factors)),
    -- 没有匹配到身份时为 NULL —— 那正是 Fail Closed 要拒绝的情况之一，
    -- 决策记录必须能表达它，不能用一个假的身份填上。
    identity_id           TEXT REFERENCES identities (id) ON DELETE RESTRICT,
    -- REQ-IDENT-002 AC3：命中的匹配层级与匹配结果一并写入 Decision。
    match_level           TEXT CHECK (match_level IS NULL OR match_level IN (
        'workspace_binding', 'resource_binding', 'trust_memory',
        'sole_identity', 'manual_selection'
    )),
    -- REQ-SCOPE-001 收敛出的十个维度，JSON 对象。Lease 据此签发。
    resolved_scope        TEXT NOT NULL CHECK (json_valid(resolved_scope)),
    -- PRD §9.6 的 approval_requirement。strong_auth 对应 REQ-APPROVAL-005。
    approval_requirement  TEXT NOT NULL CHECK (approval_requirement IN ('none', 'standard', 'strong_auth')),
    -- 结论的原因用码而不是句子：Console 按码做中英文，账本导出后也不会锁死语言。
    reason_code           TEXT NOT NULL,
    created_at            TEXT NOT NULL,
    -- 匹配层级与身份必须同时存在或同时不存在：只有其中一个说明决策记录本身是坏的。
    CHECK ((identity_id IS NULL) = (match_level IS NULL))
);

-- 一个请求只有一个决策。REQ-API-004 要求决策类端点幂等，
-- 重复调用返回首次结果而不是产生第二个结论，这条唯一索引是它的存储层保证。
CREATE UNIQUE INDEX uq_decisions_capability_request_id
    ON decisions (capability_request_id);

-- +goose Down
DROP INDEX uq_decisions_capability_request_id;
DROP TABLE decisions;
