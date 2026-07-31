-- +goose Up
-- identities 是外部服务中的一个身份，例如 GitHub Work、Cloudflare Production
-- （PRD §9.4、REQ-IDENT-001）。
CREATE TABLE identities (
    id                      TEXT NOT NULL PRIMARY KEY,
    service                 TEXT NOT NULL,
    account_label           TEXT NOT NULL,
    -- REQ-INTENT-001 的环境只有生产与非生产两种取值；无法确定时就高不就低，
    -- 一律按 production 处理（REQ-INTENT-002 AC2），所以这里不需要「未知」。
    environment             TEXT NOT NULL CHECK (environment IN ('production', 'non-production')),
    -- 同一 service 下的默认身份（REQ-IDENT-001）。SQLite 没有布尔类型，
    -- 用 0/1 并以 CHECK 挡住其它整数。
    is_default              INTEGER NOT NULL CHECK (is_default IN (0, 1)),
    -- REQ-IDENT-004：检测到外部 Scope 扩大时转为 needs_review，
    -- 相关自动授权暂停，下一次请求进入审批。
    status                  TEXT NOT NULL CHECK (status IN ('ok', 'needs_review')),
    -- 身份必须有凭据来源才可用。允许为空会造出「匹配成功但取不到凭据」的状态，
    -- 那是执行期才会暴露的失败，与 Fail Closed 相悖。
    credential_reference_id TEXT NOT NULL REFERENCES credential_references (id) ON DELETE RESTRICT,
    created_at              TEXT NOT NULL,
    updated_at              TEXT NOT NULL
);

-- 唯一索引：同一服务下的账户名不重复，
-- 否则审批页面列出的候选身份无从区分。
CREATE UNIQUE INDEX uq_identities_service_label ON identities (service, account_label);

-- +goose Down
DROP INDEX uq_identities_service_label;
DROP TABLE identities;
