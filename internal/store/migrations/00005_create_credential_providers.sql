-- +goose Up
-- credential_providers 是凭据的来源（PRD §9.2）。
--
-- 这张表里没有、也不会有任何凭据本身：Provider 描述的是「去哪里取」，
-- 取到的明文只在请求期间以 secret.Value 存在于内存中（REQ-CRED-001）。
CREATE TABLE credential_providers (
    id         TEXT NOT NULL PRIMARY KEY,
    -- 只收本期真正实现的三种（REQ-CRED-006 AC1）。PRD §9.2 还列了 Bitwarden、
    -- Vaultwarden、HashiCorp Vault、Windows Credential Manager 与 Environment Import，
    -- 本期不实现，因此也不允许存进来 —— 存了也取不出凭据。
    -- 将来实现哪一种就往 CHECK 里加哪一种，加值是非破坏性变更。
    kind       TEXT NOT NULL CHECK (kind IN ('1password', 'macos-keychain', 'local-vault')),
    -- 用户可读的名字，Identities 页面用它区分同一种类的多个来源。
    label      TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- 同一种类下的名字必须唯一，否则界面上两个来源无从区分。
CREATE UNIQUE INDEX uq_credential_providers_kind_label ON credential_providers (kind, label);

-- +goose Down
DROP INDEX uq_credential_providers_kind_label;
DROP TABLE credential_providers;
