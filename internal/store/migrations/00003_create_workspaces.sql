-- +goose Up
-- workspaces 是「请求为哪个项目发起」的记录（REQ-AGENT-001 的第 7 项绑定）。
--
-- project_fingerprint 放在这里而不是 agents 上（PRD §9.1 把它列在 Agent 名下）：
-- 指纹描述的是项目而不是进程，同一个项目下的多个 Agent 必须看到同一个值，
-- 否则 REQ-IDENT-003 AC3「指纹变化后绑定失效」会因为副本不同步而漏判。
CREATE TABLE workspaces (
    id                  TEXT NOT NULL PRIMARY KEY,
    -- 绝对路径，写入前由上层规范化。
    path                TEXT NOT NULL,
    project_fingerprint TEXT NOT NULL,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
);

-- 服务「按路径找回已注册工作区」这一注册路径上的唯一查询，同时保证一个路径只有一行。
CREATE UNIQUE INDEX uq_workspaces_path ON workspaces (path);

-- +goose Down
DROP INDEX uq_workspaces_path;
DROP TABLE workspaces;
