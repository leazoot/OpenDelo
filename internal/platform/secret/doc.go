// Package secret 定义 Value —— 凭据明文在本产品中的唯一载体（ADR-002）。
//
// Value 的 String、MarshalJSON 与 Format 一律返回 [redacted]，使误打印在类型层面无法泄漏。
// Value 只允许出现在 credential 与 adapter 两个包的签名中，由架构测试强制。
package secret
