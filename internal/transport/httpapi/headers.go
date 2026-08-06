package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/logging"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
)

// ContentSecurityPolicy 与安全规则里的策略逐字一致。
//
// 没有 'unsafe-inline' 与 'unsafe-eval'：Console 的构建产物不含内联脚本与内联样式，
// 由 scripts/check-csp.mjs 在每次前端构建后校验。
const ContentSecurityPolicy = "default-src 'self'; connect-src 'self'; img-src 'self' data:; " +
	"font-src 'self'; script-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'"

const (
	headerContentSecurityPolicy = "Content-Security-Policy"
	headerContentTypeOptions    = "X-Content-Type-Options"
	headerReferrerPolicy        = "Referrer-Policy"
)

// withSecurityHeaders 给每个响应加上安全响应头。
//
// 放在最外层，静态资源与 /v1 端点一视同仁 —— 漏掉任何一条路径都等于这些头没生效。
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set(headerContentSecurityPolicy, ContentSecurityPolicy)
		// 关掉 MIME 嗅探。Console 的资源一律由 consoleHandler 显式给出 Content-Type。
		header.Set(headerContentTypeOptions, "nosniff")
		// 本地优先：不向任何地方带出 Referer。
		header.Set(headerReferrerPolicy, "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// withOperationID 给每个请求生成一个 operation_id，放进 context 供日志与错误响应取用。
//
// 生成逻辑在 `platform/logging`，三个接入面共用一份（见那里的说明）；
// 本函数只补上 8787 自己的那半：拒绝时按 REQ-API-003 的错误契约写出。
func withOperationID(ids *ulid.Generator, logger *slog.Logger, next http.Handler) http.Handler {
	return logging.WithHTTPOperationID(ids, func(w http.ResponseWriter, r *http.Request, err error) {
		// 拿不到 ID 意味着这次请求无法被审计追溯，按 Fail Closed 直接拒绝（ADR-004）。
		logger.ErrorContext(r.Context(), "生成 operation_id 失败", slog.String("error", err.Error()))
		writeError(w, r, logger, http.StatusInternalServerError, apperr.Wrap(apperr.CodeInternal, err))
	}, next)
}
