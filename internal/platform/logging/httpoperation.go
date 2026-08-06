package logging

import (
	"net/http"
)

/*
 * operation_id 的 HTTP 入口。
 *
 * 三个接入面（8787 / 8788 / 8789）都必须给每个请求一个 operation_id：
 * 它是日志的必填字段、错误响应里用户定位这次请求的唯一凭据，也是审计写入的前提
 * （ADR-004）。少了它，`core/pipeline` 会把请求判为不成立而拒绝 ——
 * 那不是「日志少一个字段」，是这个面上的每一次调用都走不通。
 *
 * 中间件放在这里而不是某个 transport 包里：三个面各写一份的话，
 * 新加的那个面漏掉它不会有任何编译错误，症状要到第一次真实调用时才出现。
 */

// HeaderOperationID 是 operation_id 在响应上的位置。
//
// 每个面都带：用户拿到一次失败之后，凭它就能在账本里定位那次请求，
// 而不必先分辨这次失败来自哪个端口。
const HeaderOperationID = "X-Operation-ID"

// IDSource 生成一个 operation_id。
//
// 接口在这里、实现在 platform/ulid：本包不该为了一个 ID 去依赖另一个包，
// 而调用方本来就持有生成器。
type IDSource interface {
	NewID() (string, error)
}

// RefuseFunc 在拿不到 operation_id 时写出拒绝。
//
// 由各面自己给：三个面的错误契约不同（JSON 错误体 / JSON-RPC / 代理响应），
// 在这里统一写一种会让其中两个面返回它的客户端读不懂的东西。
type RefuseFunc func(w http.ResponseWriter, r *http.Request, err error)

// WithHTTPOperationID 给每个请求生成 operation_id 并放进 context。
//
// 上游已经给过就沿用：一次请求在整条链路上只能有一个 operation_id，
// 换一个等于把「同一次请求」在账本里切成两半。
//
// 生成失败时**拒绝这次请求**，不放它过去：拿不到 ID 意味着这次操作无法被审计
// 追溯，而审计是执行的前置条件（ADR-004）。
func WithHTTPOperationID(ids IDSource, refuse RefuseFunc, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if existing := OperationIDFrom(r.Context()); existing != "" {
			w.Header().Set(HeaderOperationID, existing)
			next.ServeHTTP(w, r)
			return
		}

		operationID, err := ids.NewID()
		if err != nil {
			refuse(w, r, err)
			return
		}
		w.Header().Set(HeaderOperationID, operationID)
		next.ServeHTTP(w, r.WithContext(WithOperationID(r.Context(), operationID)))
	})
}
