-- +goose Up
-- approvals 是一次等待人工确认的审批项（PRD §13、REQ-APPROVAL-001/002）。
-- 决策判为 require_approval 时创建，用户在 Access Folio 上给出结果。
CREATE TABLE approvals (
    id           TEXT NOT NULL PRIMARY KEY,
    decision_id  TEXT NOT NULL REFERENCES decisions (id) ON DELETE RESTRICT,
    -- REQ-APPROVAL-002 的五种操作，未决时为 NULL。
    -- 「拒绝」既是一种操作也决定了 status，两者一起写入。
    action       TEXT CHECK (action IS NULL OR action IN (
        'deny', 'allow_once', 'allow_until_task_end',
        'auto_allow_in_project', 'always_ask'
    )),
    status       TEXT NOT NULL CHECK (status IN (
        'pending', 'approved', 'rejected', 'expired', 'cancelled'
    )),
    -- 审批超时（默认 5 分钟）。非空：一个永远等下去的审批项等于一条永久授权的入口。
    expires_at   TEXT NOT NULL,
    -- 未决时为 NULL。决出结果的时刻要能与超时时刻比对，不能用零值冒充。
    decided_at   TEXT,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    -- 未决的审批不能带结果，已决的审批必须有结果与时刻。
    -- 只写了一半的审批项会让「这次是谁放行的」答不出来。
    CHECK ((status = 'pending') = (action IS NULL)),
    CHECK ((status = 'pending') = (decided_at IS NULL))
);

-- 一个决策只产生一个审批项：REQ-API-004 要求重复决策返回首次结果，
-- 而不是让同一次请求出现两个可以分别放行的入口。
CREATE UNIQUE INDEX uq_approvals_decision_id ON approvals (decision_id);

-- 服务超时清扫：找出 pending 且已过期的审批项（REQ-APPROVAL-002 的超时行为）。
CREATE INDEX idx_approvals_status_expires_at ON approvals (status, expires_at);

-- +goose Down
DROP INDEX idx_approvals_status_expires_at;
DROP INDEX uq_approvals_decision_id;
DROP TABLE approvals;
