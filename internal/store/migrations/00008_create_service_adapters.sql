-- +goose Up
-- service_adapters 保存 Adapter 的能力声明（PRD §18、REQ-ADAPTER-001）。
-- Adapter 的代码在编译期注册（ADR-009），这张表存的是「这个 Adapter 被允许做什么」，
-- 其中 Generic HTTP 的声明完全由用户给出（REQ-ADAPTER-005）。
--
-- 与 credential_references 一样，这张表不存任何凭据：认证方式只记形式，
-- 实际的 Secret 由 identities → credential_references → Provider 实时取用。
CREATE TABLE service_adapters (
    id                 TEXT NOT NULL PRIMARY KEY,
    -- 与 capability_requests.service、identities.service 对齐的服务名。
    service            TEXT NOT NULL,
    -- REQ-ADAPTER-006 AC1：注册表只含本期实现的四种。写入未实现的种类没有意义，
    -- 它在界面上出现却无法执行。新增实现时往 CHECK 里加取值即可（非破坏性）。
    kind               TEXT NOT NULL CHECK (kind IN ('github', 'cloudflare', 'model', 'generic-http')),
    display_name       TEXT NOT NULL,
    base_url           TEXT NOT NULL,
    -- 认证方式的形式，不是凭据本身。none 用于不需要认证的只读端点。
    auth_scheme        TEXT NOT NULL CHECK (auth_scheme IN ('none', 'bearer', 'header')),
    -- REQ-ADAPTER-001 的九项声明：操作、输入 Schema、最小 Scope、风险标签、
    -- 请求方法、幂等性、回滚能力逐个操作声明，合起来是一个 JSON 数组。
    capabilities       TEXT NOT NULL CHECK (json_valid(capabilities)),
    -- REQ-ADAPTER-005：路径与方法白名单。不在白名单内的请求不发出（AC1）。
    allowed_paths      TEXT NOT NULL CHECK (json_valid(allowed_paths)),
    allowed_methods    TEXT NOT NULL CHECK (json_valid(allowed_methods)),
    -- REQ-ADAPTER-007：脱敏规则与响应过滤。
    redaction_rules    TEXT NOT NULL CHECK (json_valid(redaction_rules)),
    -- REQ-ADAPTER-005 AC2：未声明 Risk Level 的配置无法保存。非空即是这条 AC
    -- 在存储层的实现——单个操作可以声明更高的等级，但兜底等级必须先有。
    default_risk_level TEXT NOT NULL CHECK (default_risk_level IN ('low', 'medium', 'high')),
    -- 停用的 Adapter 不参与决策。删除会让审计里的历史请求失去解释，所以用状态而不是删行。
    status             TEXT NOT NULL CHECK (status IN ('enabled', 'disabled')),
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL
);

-- 一个服务名只能有一个 Adapter：决策链路按 service 反查能力声明，
-- 两条同名声明会让「这个操作被允许吗」有两个答案。
CREATE UNIQUE INDEX uq_service_adapters_service ON service_adapters (service);

-- +goose Down
DROP INDEX uq_service_adapters_service;
DROP TABLE service_adapters;
