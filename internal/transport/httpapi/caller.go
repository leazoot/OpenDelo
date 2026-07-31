package httpapi

import (
	"context"
	"net/http"
)

/*
 * 调用方身份：这次请求代表谁在问（REQ-CAP-002 AC2）。
 *
 * 两种调用方，可见范围不同：
 *
 *  - **Console**（会话令牌）看得到全部到达。Gate 页面的职责就是把缝前所有等待的
 *    请求摆出来，按 Agent 过滤会让人漏看。
 *  - **Agent**（Session Key，由 MCP 与 Proxy 两个面填入）只看得到自己发起的请求。
 *    别人的请求一律 404 而不是 403 —— 403 等于承认「这个 id 存在」，
 *    那本身就是一次信息泄漏。
 */

type callerKey struct{}

// Caller 是这次请求背后的调用方。
type Caller struct {
	// AgentID 为空表示调用方是 Console；非空表示是那个 Agent 自己。
	AgentID string
}

// IsAgent 报告调用方是不是一个 Agent。
func (c Caller) IsAgent() bool { return c.AgentID != "" }

// maySee 报告这个调用方能否看到属于 ownerAgentID 的记录。
func (c Caller) maySee(ownerAgentID string) bool {
	return !c.IsAgent() || c.AgentID == ownerAgentID
}

// WithCaller 把调用方放进 context。
//
// 由完成认证的那一层调用：会话守卫放 Console，MCP 与 Proxy 两个面放各自认证出来的
// Agent。业务处理器只读不写，因此「这次请求代表谁」不会在链路中途被改。
func WithCaller(ctx context.Context, caller Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, caller)
}

// callerFrom 取出调用方。
//
// 取不到时返回零值，也就是 Console。这在生产里不会发生：/v1 之下的每个请求都要先过
// 会话守卫，而守卫一定会放一个进去。
func callerFrom(ctx context.Context) Caller {
	caller, present := ctx.Value(callerKey{}).(Caller)
	if !present {
		return Caller{}
	}
	return caller
}

// withCaller 是给处理器链用的中间件形态。
func withCaller(caller Caller, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(WithCaller(r.Context(), caller)))
	})
}
