// Package proxy 实现 Agent 的 HTTP 代理接入面：请求拦截、Lease 匹配、触发凭据注入。
//
// 每个请求独立校验，不缓存放行结论；无有效 Lease 时返回 403 且不产生任何出站流量。
// 只接受绝对形式的代理请求；CONNECT 一律拒绝，理由见 handler.go 的 refuseTunnel。
package proxy
