-- +goose Up
-- settings 是结构化偏好的键值存储（PRD §26）。
--
-- 取值统一存 JSON 文本：布尔、数值、对象三类偏好因此共用一张表，新增一个偏好
-- 不需要加列，也就不需要一次迁移。
CREATE TABLE settings (
    -- SQLite 的 TEXT PRIMARY KEY 出于历史兼容允许存 NULL，NOT NULL 必须显式写出。
    id         TEXT NOT NULL PRIMARY KEY,
    name       TEXT NOT NULL,
    value      TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- 服务「按名字读写单个偏好」这一唯一的访问方式，同时保证一个偏好只有一行。
CREATE UNIQUE INDEX uq_settings_name ON settings (name);

-- +goose Down
DROP INDEX uq_settings_name;
DROP TABLE settings;
