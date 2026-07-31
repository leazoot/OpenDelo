// Package httpapi 提供 Web Console 的 HTTP 接入面：REST 端点、SSE 推送与静态资源。
//
// 只做协议转换（解析、认证、调用 core、序列化）。任何「是否允许」的判断出现在本包即违规。
package httpapi
