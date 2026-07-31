package proxy

import (
	"log/slog"
	"net/http"
)

/*
 * 访问日志（REQ-PROXY-001 AC2）。
 *
 * 日志里**记下** Agent 送来的 Authorization 与 Proxy-Authorization 两个头，
 * 由 platform/logging 的词表把值换成 [redacted]。
 *
 * 记而不是不记，是因为这两个头的出现本身是要给运维看的信号：L1 之下 Agent
 * 的环境已经被清理过，它还能掏出一个 Authorization，说明凭据是从别处来的。
 * 值一个字节都不会落到输出里 —— 脱敏没有开关，
 * 哨兵用例在 access_test.go 里逐字节扫过输出。
 *
 * 这两个头也**不会**被转发：Exchange 的签名里根本没有请求头
 * （REQ-PROXY-001 AC1 的前半句）。凭据由 Adapter 在出站时注入。
 */

// logAccess 记一条访问日志。
//
// query 不进日志（记 URL 时只留 path）——
// 有些服务把令牌放在 query 里，那不是本网关能预料的。
func (p *Proxy) logAccess(
	r *http.Request,
	host, path string,
	route Route, grant Grant,
	status int, outcome string,
) {
	p.logger.LogAttrs(r.Context(), slog.LevelInfo, "agent proxy request",
		slog.String("method", r.Method),
		slog.String("host", host),
		slog.String("path", path),
		slog.String("service", route.Service),
		slog.String("operation", route.Operation),
		slog.String("lease_id", grant.LeaseID),
		slog.Int("status", status),
		slog.String("outcome", outcome),
		slog.String("authorization", r.Header.Get("Authorization")),
		slog.String("proxy_authorization", r.Header.Get(sessionHeader)),
	)
}
