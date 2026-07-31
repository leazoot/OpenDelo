// Package intent 把工具调用或 HTTP 请求解析为结构化意图。
//
// 只走确定性映射 + JSON Schema 校验。无模型调用、无网络、无 I/O。解析歧义时上抛而非猜测。
package intent
