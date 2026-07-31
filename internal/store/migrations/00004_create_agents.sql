-- +goose Up
-- agents 是发起请求的执行主体（PRD §9.1、REQ-AGENT-001）。
--
-- 这张表的每一列都是身份的一部分：REQ-AGENT-001 明确禁止仅凭名字识别 Agent，
-- 所以 name 与 type 只用于展示，能证明「是我启动的那个进程」的是哈希、路径、
-- 进程号、父进程、系统用户、设备、工作区、启动时间与 Session Key 这九项，
-- 它们全部 NOT NULL（AC3）。
CREATE TABLE agents (
    id                 TEXT NOT NULL PRIMARY KEY,

    -- 展示用。REQ-AGENT-003 AC2：篡改这两列不影响身份校验结果。
    name               TEXT NOT NULL,
    type               TEXT NOT NULL CHECK (
                           type IN ('claude-code', 'codex', 'gemini-cli', 'opencode', 'generic')
                       ),
    -- 唯一可空列：Agent 不一定上报版本，空表示「未上报」，不表示 0 或未知错误。
    version            TEXT,

    -- 以下九列是 REQ-AGENT-001 的身份绑定。
    executable_hash    TEXT    NOT NULL,
    executable_path    TEXT    NOT NULL,
    pid                INTEGER NOT NULL CHECK (pid > 0),
    -- 无父进程时为 0（REQ-AGENT-001 AC3 显式允许），所以下界是 0 而不是 1。
    parent_pid         INTEGER NOT NULL CHECK (parent_pid >= 0),
    os_user            TEXT    NOT NULL,
    device_id          TEXT    NOT NULL REFERENCES devices (id) ON DELETE RESTRICT,
    workspace_id       TEXT    NOT NULL REFERENCES workspaces (id) ON DELETE RESTRICT,
    started_at         TEXT    NOT NULL,
    -- 存的是 Session Key 的哈希而不是 Key 本身：库里存下的明文一旦泄漏就能直接冒充
    -- 该 Agent，而校验只需要比对，不需要还原。加密不能替代哈希 —— 带随机 nonce 的
    -- 密文无法用作查找键（会话密钥材料不得明文落库）。
    session_key_hash   TEXT    NOT NULL,
    -- Session Key 不自动续期（REQ-AGENT-001 异常状态），过期时刻必须可判定。
    session_expires_at TEXT    NOT NULL,

    -- REQ-AGENT-002：首次注册为 unverified，用户确认后升为 known。
    trust_level        TEXT NOT NULL CHECK (trust_level IN ('unverified', 'known', 'trusted')),
    -- 进程还在跑（active）还是已经退出/会话失效（disconnected）。
    status             TEXT NOT NULL CHECK (status IN ('active', 'disconnected')),
    last_seen_at       TEXT NOT NULL,

    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL
);

-- 服务 Session Key 校验（每个 Agent 请求都要走一次），同时保证一个 Session Key
-- 不可能同时属于两个 Agent。
CREATE UNIQUE INDEX uq_agents_session_key_hash ON agents (session_key_hash);

-- +goose Down
DROP INDEX uq_agents_session_key_hash;
DROP TABLE agents;
