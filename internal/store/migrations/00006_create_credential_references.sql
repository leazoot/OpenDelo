-- +goose Up
-- credential_references 是「去哪个来源、取哪一项、取哪个字段」的引用（PRD §9.3）。
--
-- REQ-CRED-001 AC1：这张表不存在任何保存凭据明文的列。三元组
-- (provider_id, provider_item_ref, field) 只是坐标，凭它无法离线还原出任何 Secret；
-- 明文永远来自 Provider 的实时取用。新增列时必须先确认它不是凭据本身。
CREATE TABLE credential_references (
    id                TEXT NOT NULL PRIMARY KEY,
    provider_id       TEXT NOT NULL REFERENCES credential_providers (id) ON DELETE RESTRICT,
    -- Provider 内部的条目坐标，形式由 Provider 自己定义（1Password 是 Item ID，
    -- Keychain 是 service+account）。
    provider_item_ref TEXT NOT NULL,
    -- 条目里的哪个字段。REQ-CRED-001 把它列为引用的第三个组成部分。
    field             TEXT NOT NULL,
    service           TEXT NOT NULL,
    account_label     TEXT NOT NULL,
    -- 展示与匹配用的附加信息，JSON 对象。
    metadata          TEXT NOT NULL CHECK (json_valid(metadata)),
    -- 这份凭据被声明可以做什么，JSON 数组。Adapter 的能力校验会用到。
    capabilities      TEXT NOT NULL CHECK (json_valid(capabilities)),
    -- REQ-CRED-005：needs_reauth 会暂停依赖它的 Trust Memory，
    -- unavailable 会让请求直接被拒（Fail Closed）。
    health_status     TEXT NOT NULL CHECK (health_status IN ('ok', 'needs_reauth', 'unavailable')),
    -- 从未验证过时为 NULL。这里不能用零值时间冒充「很久以前验证过」。
    last_verified_at  TEXT,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
);

-- 同一个来源里的同一个字段只登记一次，避免同一份凭据被两条引用指向后状态不一致。
CREATE UNIQUE INDEX uq_credential_references_provider_item_field
    ON credential_references (provider_id, provider_item_ref, field);

-- +goose Down
DROP INDEX uq_credential_references_provider_item_field;
DROP TABLE credential_references;
