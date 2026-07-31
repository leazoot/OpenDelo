-- +goose Up
-- 服务注册时的「这个进程之前注册过吗」查询（REQ-AGENT-001 行为要求 3）。
--
-- 前五列是同一个 Agent 跨重启保持不变的部分：设备、工作区、可执行文件路径、
-- 系统用户与可执行文件哈希。PID 与启动时间每次重启都变，因此不在键里。
-- 末列 id 让 ORDER BY id DESC 直接走索引，不再需要临时 B 树排序。
CREATE INDEX idx_agents_binding
    ON agents (device_id, workspace_id, executable_path, os_user, executable_hash, id);

-- +goose Down
DROP INDEX idx_agents_binding;
