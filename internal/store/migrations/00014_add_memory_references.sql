-- +goose Up
-- 把 PRD §9.6 的 matched_memory 与 §9.7 的 source_memory_id 补上。
--
-- 这两列没有跟着各自的建表迁移一起写：trust_memories 那时还不存在，
-- 加一个没有外键的裸文本列会留下一段可以写入悬空引用的窗口。
ALTER TABLE decisions
    ADD COLUMN trust_memory_id TEXT REFERENCES trust_memories (id) ON DELETE RESTRICT;

ALTER TABLE leases
    ADD COLUMN source_memory_id TEXT REFERENCES trust_memories (id) ON DELETE RESTRICT;

-- +goose Down
ALTER TABLE leases DROP COLUMN source_memory_id;
ALTER TABLE decisions DROP COLUMN trust_memory_id;
