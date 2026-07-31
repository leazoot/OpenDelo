// Package logging 提供基于 log/slog 的结构化日志与脱敏。
//
// 每条日志带 operation_id；命中脱敏词表的字段替换为 [redacted]，debug 级别同样脱敏。
// 日志是运维辅助，不能替代审计。
package logging
