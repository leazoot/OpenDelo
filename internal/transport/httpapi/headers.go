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
	headerOperationID           = "X-Operation-ID"
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
// 上游已经给过就沿用：一次请求在整条链路上只能有一个 operation_id，
// 换一个等于把「同一次请求」在账本里切成了两半。
func withOperationID(ids *ulid.Generator, logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if existing := logging.OperationIDFrom(r.Context()); existing != "" {
			w.Header().Set(headerOperationID, existing)
			next.ServeHTTP(w, r)
			return
		}

		operationID, err := ids.NewID()
		if err != nil {
			// 拿不到 ID 意味着这次请求无法被审计追溯，按 Fail Closed 直接拒绝（ADR-004）。
			logger.ErrorContext(r.Context(), "生成 operation_id 失败", slog.String("error", err.Error()))
			writeError(w, r, logger, http.StatusInternalServerError, apperr.Wrap(apperr.CodeInternal, err))
			return
		}
		w.Header().Set(headerOperationID, operationID)
		next.ServeHTTP(w, r.WithContext(logging.WithOperationID(r.Context(), operationID)))
	})
}
