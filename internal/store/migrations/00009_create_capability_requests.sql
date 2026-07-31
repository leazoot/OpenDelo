-- +goose Up
-- capability_requests 是 Agent 提交的一次能力请求（PRD §9.5、REQ-CAP-001）。
-- 请求描述的是能力而不是凭据：这里没有任何字段能表达「把 Token 给我」。
CREATE TABLE capability_requests (
    id             TEXT NOT NULL PRIMARY KEY,
    -- 贯穿日志与审计的追溯号。
    operation_id   TEXT NOT NULL,
    agent_id       TEXT NOT NULL REFERENCES agents (id) ON DELETE RESTRICT,
    -- PRD §9.5 的 workspace 是一个路径字符串；这里指向 workspaces，
    -- 项目指纹因此只有一份。
    workspace_id   TEXT NOT NULL REFERENCES workspaces (id) ON DELETE RESTRICT,
    service        TEXT NOT NULL,
    operation      TEXT NOT NULL,
    -- 请求作用的资源坐标，JSON 对象（PRD §9.5 的 resource）。
    resource       TEXT NOT NULL CHECK (json_valid(resource)),
    -- 只有写操作才有期望变更；读操作为 NULL 而不是空对象，
    -- 「没有变更」与「变更为空」在审批页面上是两句不同的话。
    desired_change TEXT CHECK (desired_change IS NULL OR json_valid(desired_change)),
    reason         TEXT NOT NULL,
    -- REQ-CAP-001 的状态机全部取值。CHECK 只挡住取值本身，
    -- 哪些转移合法由 S3 的状态机实现负责（AC3）。
    status         TEXT NOT NULL CHECK (status IN (
        'received', 'resolving', 'deciding',
        'auto_allowed', 'awaiting_approval', 'denied',
        'approved', 'rejected', 'expired', 'cancelled',
        'executing', 'succeeded', 'failed'
    )),
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);

-- 服务 Gate 的待审批列表：按状态过滤后按到达时间排序。
-- 带上 created_at 是为了让排序也走索引，否则 SQLite 会为 ORDER BY 建临时 B 树，
-- 请求量上去后就守不住 P95 < 20ms。
CREATE INDEX idx_capability_requests_status_created_at
    ON capability_requests (status, created_at);

-- +goose Down
DROP INDEX idx_capability_requests_status_created_at;
DROP TABLE capability_requests;
