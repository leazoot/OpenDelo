-- +goose Up
-- devices 是「请求从哪台机器来」的记录（REQ-AGENT-001 的第 6 项绑定）。
--
-- 它单独成表而不是 agents 上的一列，因为同一台设备会注册很多个 Agent，
-- 而设备的信任状态是 Risk Engine 的输入之一（REQ-RISK-001），改一次要对
-- 该设备上全部 Agent 生效。
CREATE TABLE devices (
    -- SQLite 的 TEXT PRIMARY KEY 出于历史兼容允许存 NULL，NOT NULL 必须显式写出。
    id           TEXT NOT NULL PRIMARY KEY,
    -- 设备指纹是跨重启稳定的标识。主键是每次插入新生成的 ULID，认不出
    -- 「还是上次那台机器」，绑定就失去意义。
    fingerprint  TEXT NOT NULL,
    -- 展示用名字（REQ-UI-005 AC2 要求 Agent 卡片显示请求来自哪台设备）。
    name         TEXT NOT NULL,
    -- 设备不再可信会使 Trust Memory 失效（REQ-TRUST-004），所以取值必须封闭。
    trust_status TEXT NOT NULL CHECK (trust_status IN ('trusted', 'untrusted')),
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);

-- 服务「按指纹找回已注册设备」这一注册路径上的唯一查询，同时保证一台设备只有一行。
CREATE UNIQUE INDEX uq_devices_fingerprint ON devices (fingerprint);

-- +goose Down
DROP INDEX uq_devices_fingerprint;
DROP TABLE devices;
