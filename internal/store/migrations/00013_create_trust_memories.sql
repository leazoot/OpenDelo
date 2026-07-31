-- +goose Up
-- trust_memories 是用户历史授权形成的可复用记忆（PRD §9.8、REQ-TRUST-001）。
--
-- REQ-TRUST-002「不得扩大」的七个维度在这张表里各有一列：
-- 资源(resource_scope) · 操作(capability_scope) · 时间(expires_at) ·
-- Agent(agent_id) · 项目(workspace_id) · 身份(identity_id) · 环境(environment)。
-- 七列全部非空 —— 任何一维留空都等于「这一维不限」，那正是扩大。
CREATE TABLE trust_memories (
    id                  TEXT NOT NULL PRIMARY KEY,
    -- Agent 与项目各自恰好一个。记忆不能覆盖「这台机器上的所有 Agent」
    -- 或「所有项目」，所以这两维是外键而不是模式串。
    agent_id            TEXT NOT NULL REFERENCES agents (id) ON DELETE RESTRICT,
    workspace_id        TEXT NOT NULL REFERENCES workspaces (id) ON DELETE RESTRICT,
    identity_id         TEXT NOT NULL REFERENCES identities (id) ON DELETE RESTRICT,
    service             TEXT NOT NULL,
    -- 精确到原审批的那一个资源与那一组操作，JSON。PRD §14.2 的反例：
    -- 批准「修改 api.tele-call.cn 的 A 记录」不得存成「修改 tele-call.cn 任意 DNS」。
    resource_scope      TEXT NOT NULL CHECK (json_valid(resource_scope)),
    capability_scope    TEXT NOT NULL CHECK (json_valid(capability_scope)),
    environment         TEXT NOT NULL CHECK (environment IN ('production', 'non-production')),
    -- REQ-TRUST-003：高风险永远需要人工确认，不存在 risk_ceiling = 'high' 的记忆。
    -- 这里不写 high，因此「学出一条能自动放行高风险的记忆」在 schema 层面不可表达。
    risk_ceiling        TEXT NOT NULL CHECK (risk_ceiling IN ('low', 'medium')),
    approval_behavior   TEXT NOT NULL CHECK (approval_behavior IN ('auto_allow', 'always_ask')),
    -- REQ-TRUST-001 AC2：指向产生它的那次审批，Automation 页面据此显示来源。
    created_from        TEXT NOT NULL REFERENCES approvals (id) ON DELETE RESTRICT,
    status              TEXT NOT NULL CHECK (status IN ('active', 'invalidated')),
    -- REQ-TRUST-004 的八个失效条件。失效的记忆显示原因而不是直接消失（AC2），
    -- 所以原因是数据的一部分，不是日志。
    invalidation_reason TEXT CHECK (invalidation_reason IS NULL OR invalidation_reason IN (
        'provider_disconnected', 'identity_scope_changed', 'agent_executable_changed',
        'project_fingerprint_changed', 'device_untrusted', 'unused_too_long',
        'cautious_mode_selected', 'adapter_risk_upgraded'
    )),
    -- 从未使用过时为 NULL。零值时间会让「长期未使用」的判断立刻命中。
    last_used_at        TEXT,
    -- 时间维度同样有界：记忆也会到期，默认 30 天未使用即失效。
    expires_at          TEXT NOT NULL,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    -- 生效中的记忆不能带失效原因，失效的记忆必须说得出原因。
    CHECK ((status = 'active') = (invalidation_reason IS NULL))
);

-- 一次审批只学出一条记忆：同一次确认被学两遍会让同一个范围有两处来源，
-- 用户在 Automation 页面删掉一条也止不住另一条。
CREATE UNIQUE INDEX uq_trust_memories_created_from ON trust_memories (created_from);

-- 服务决策链路中的记忆匹配查询（REQ-IDENT-002，P95 < 10ms）。
--
-- 尾列 id 不参与查找，只让排序也走索引。没有它时，如果同一个
-- (agent, workspace, service) 下积累了大量记忆，SQLite 要把命中集整个取出来
-- 排完序再 LIMIT —— 实测 10 万条同属一个三元组时 P95 为 43ms，加上尾列后是 1.1ms。
CREATE INDEX idx_trust_memories_agent_workspace_service_id
    ON trust_memories (agent_id, workspace_id, service, id);

-- +goose Down
DROP INDEX idx_trust_memories_agent_workspace_service_id;
DROP INDEX uq_trust_memories_created_from;
DROP TABLE trust_memories;
