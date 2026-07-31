package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * 路由表（REQ-API-001 的前 11 个端点，路径与方法照抄 PRD §27）。
 *
 * 写成一张能被逐条核对的表：用例拿着 Routes() 与 PRD §27 的清单比对，
 * 多一个端点或少一个方法都会失败。
 */

// Route 是一个已注册的端点。
type Route struct {
	Method string
	// Pattern 是 net/http 的路径模式，:id 在这里写作 {id}。
	Pattern string
}

// Routes 返回本包注册的全部业务端点，按注册顺序。
//
// 导出是为了让契约测试逐条核对 PRD §27，而不是靠人去读 registerBusinessRoutes。
func Routes() []Route {
	routes := []Route{
		{Method: http.MethodPost, Pattern: "/v1/capability-requests"},
		{Method: http.MethodGet, Pattern: "/v1/capability-requests/{id}"},
		{Method: http.MethodPost, Pattern: "/v1/capability-requests/{id}/cancel"},
		{Method: http.MethodGet, Pattern: "/v1/approvals"},
	}
	for _, settle := range settleActions {
		routes = append(routes, Route{
			Method:  http.MethodPost,
			Pattern: "/v1/approvals/{id}/" + settle.path,
		})
	}
	return append(routes,
		Route{Method: http.MethodGet, Pattern: "/v1/leases"},
		Route{Method: http.MethodPost, Pattern: "/v1/leases/{id}/shorten"},
		Route{Method: http.MethodDelete, Pattern: "/v1/leases/{id}"},

		Route{Method: http.MethodGet, Pattern: "/v1/identities"},
		Route{Method: http.MethodPost, Pattern: "/v1/identities/connect"},
		Route{Method: http.MethodPost, Pattern: "/v1/identities/{id}/verify"},
		Route{Method: http.MethodDelete, Pattern: "/v1/identities/{id}"},

		Route{Method: http.MethodGet, Pattern: "/v1/trust-memories"},
		Route{Method: http.MethodPatch, Pattern: "/v1/trust-memories/{id}"},
		Route{Method: http.MethodDelete, Pattern: "/v1/trust-memories/{id}"},

		Route{Method: http.MethodGet, Pattern: "/v1/audit-events"},
		// 导出排在 :id 之前：它是一个固定段，不是一个 id。
		Route{Method: http.MethodGet, Pattern: "/v1/audit-events/export"},
		Route{Method: http.MethodGet, Pattern: "/v1/audit-events/{id}"},

		Route{Method: http.MethodGet, Pattern: "/v1/agents"},
		// register 是固定段，注册得比 {id} 更具体，因此优先命中。
		Route{Method: http.MethodPost, Pattern: "/v1/agents/register"},
		Route{Method: http.MethodPost, Pattern: "/v1/agents/{id}/trust"},
		Route{Method: http.MethodPost, Pattern: "/v1/agents/{id}/disconnect"},
		Route{Method: http.MethodGet, Pattern: "/v1/preferences"},
		Route{Method: http.MethodPatch, Pattern: "/v1/preferences"},
		Route{Method: http.MethodPost, Pattern: "/v1/vault"},
		Route{Method: http.MethodPost, Pattern: "/v1/vault/unlock"},
		Route{Method: http.MethodPost, Pattern: "/v1/vault/lock"},
		Route{Method: http.MethodGet, Pattern: "/v1/events"},
	)
}

// NewBusinessHandler 返回 REQ-API-001 前 11 个端点的处理器。
//
// **不含会话校验**：调用方负责先完成认证，再用 WithCaller 说明这次请求代表谁。
// 8787 面装的是 Console，MCP（8789）与 Proxy（8788）两个面装各自认证出来的 Agent。
// 路由表由 registerBusinessRoutes 一处给出，两条装配路径不会分叉。
func NewBusinessHandler(services Services, logger *slog.Logger) (http.Handler, error) {
	if err := services.validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		return nil, apperr.New(apperr.CodeInvalidConfiguration).
			WithDetail("业务端点缺少日志器")
	}

	broker := services.Events
	if broker == nil {
		broker = NewBroker(logger)
	}

	mux := http.NewServeMux()
	registerBusinessRoutes(mux,
		&endpoints{services: services, events: broker, logger: logger}, logger)
	mux.Handle("/", notFoundHandler(logger))

	// 每个请求都要带 operation_id：审计写入以它为必填，错误响应也要靠它
	// 让用户在账本里定位这次请求（REQ-API-003 AC3）。上游已经给过就沿用。
	return withOperationID(services.IDs, logger, mux), nil
}

// registerBusinessRoutes 把处理器挂到 mux 上。
//
// 方法校验走 allowMethods 而不是 ServeMux 的方法模式：后者的 405 正文是裸文本，
// 与 REQ-API-003 的错误契约不一致（见 respond.go）。
func registerBusinessRoutes(mux *http.ServeMux, e *endpoints, logger *slog.Logger) {
	mux.Handle("/v1/capability-requests",
		allowMethods(logger, http.HandlerFunc(e.submit), http.MethodPost))
	mux.Handle("/v1/capability-requests/{id}",
		allowMethods(logger, http.HandlerFunc(e.show), http.MethodGet, http.MethodHead))
	mux.Handle("/v1/capability-requests/{id}/cancel",
		allowMethods(logger, http.HandlerFunc(e.cancel), http.MethodPost))

	mux.Handle("/v1/approvals",
		allowMethods(logger, http.HandlerFunc(e.listApprovals), http.MethodGet, http.MethodHead))
	for _, settle := range settleActions {
		mux.Handle("/v1/approvals/{id}/"+settle.path,
			allowMethods(logger, e.settle(settle.action), http.MethodPost))
	}

	mux.Handle("/v1/leases",
		allowMethods(logger, http.HandlerFunc(e.listLeases), http.MethodGet, http.MethodHead))
	mux.Handle("/v1/leases/{id}/shorten",
		allowMethods(logger, http.HandlerFunc(e.shorten), http.MethodPost))
	mux.Handle("/v1/leases/{id}",
		allowMethods(logger, http.HandlerFunc(e.revoke), http.MethodDelete))

	mux.Handle("/v1/identities",
		allowMethods(logger, http.HandlerFunc(e.listIdentities), http.MethodGet, http.MethodHead))
	mux.Handle("/v1/identities/connect",
		allowMethods(logger, http.HandlerFunc(e.connectIdentity), http.MethodPost))
	mux.Handle("/v1/identities/{id}/verify",
		allowMethods(logger, http.HandlerFunc(e.verifyIdentity), http.MethodPost))
	mux.Handle("/v1/identities/{id}",
		allowMethods(logger, http.HandlerFunc(e.disconnectIdentity), http.MethodDelete))

	mux.Handle("/v1/trust-memories",
		allowMethods(logger, http.HandlerFunc(e.listMemories), http.MethodGet, http.MethodHead))
	mux.Handle("/v1/trust-memories/{id}",
		allowMethods(logger, http.HandlerFunc(e.memoryByID), http.MethodPatch, http.MethodDelete))

	mux.Handle("/v1/audit-events",
		allowMethods(logger, http.HandlerFunc(e.listEvents), http.MethodGet, http.MethodHead))
	// export 是固定段，注册得比 {id} 更具体，因此优先命中。
	mux.Handle("/v1/audit-events/export",
		allowMethods(logger, http.HandlerFunc(e.exportEvents), http.MethodGet, http.MethodHead))
	mux.Handle("/v1/audit-events/{id}",
		allowMethods(logger, http.HandlerFunc(e.showEvent), http.MethodGet, http.MethodHead))

	mux.Handle("/v1/agents",
		allowMethods(logger, http.HandlerFunc(e.listAgents), http.MethodGet, http.MethodHead))
	// register 是固定段，注册得比 {id} 更具体，因此优先命中。
	mux.Handle("/v1/agents/register",
		allowMethods(logger, http.HandlerFunc(e.registerAgent), http.MethodPost))
	mux.Handle("/v1/agents/{id}/trust",
		allowMethods(logger, http.HandlerFunc(e.trustAgent), http.MethodPost))
	mux.Handle("/v1/agents/{id}/disconnect",
		allowMethods(logger, http.HandlerFunc(e.disconnectAgent), http.MethodPost))
	mux.Handle("/v1/preferences",
		allowMethods(logger, http.HandlerFunc(e.preferences),
			http.MethodGet, http.MethodHead, http.MethodPatch))
	mux.Handle("/v1/vault",
		allowMethods(logger, http.HandlerFunc(e.createVault), http.MethodPost))
	mux.Handle("/v1/vault/unlock",
		allowMethods(logger, http.HandlerFunc(e.unlockVault), http.MethodPost))
	mux.Handle("/v1/vault/lock",
		allowMethods(logger, http.HandlerFunc(e.lockVault), http.MethodPost))
	// SSE 是长连接，不走 allowMethods 之外的任何包装。
	mux.Handle("/v1/events",
		allowMethods(logger, http.HandlerFunc(e.stream), http.MethodGet))
}
