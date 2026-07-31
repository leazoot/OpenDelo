package cli

import (
	"context"
	"encoding/json"

	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/intent"
	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/core/scope"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/audit"
	"github.com/Runcoor/opendelo/internal/platform/logging"
	"github.com/Runcoor/opendelo/internal/transport/proxy"
)

/*
 * Proxy 的授权与拦截记账（REQ-PROXY-002）。
 *
 * 8788 不跑决策链路。它只回答一个问题：**这次请求有没有一条已经签发的 Lease 罩着**。
 * 没有就拒绝，且不发出任何出站流量（成功标准 S10）。
 *
 * 这条边界很容易被写松。「没找到 Lease 就去走一次决策」听起来更好用，但那样
 * Agent 只要直接发 HTTP 就能触发授权流程，`opendelo run` 注入的代理配置就从
 * 一道闸变成了一个入口。要新的授权得走 MCP 或 Console，那里有人看着。
 */

// proxyAudits 把被拦下的请求记进账本。
type proxyAudits struct {
	recorder *audit.Recorder
}

var _ proxy.Audits = (*proxyAudits)(nil)

// RecordBlocked 记一条拒绝。
//
// 记的是元数据：谁、去哪个服务、做什么、为什么被拒。**不记** URL 的查询串、
// 请求正文与任何请求头 —— 它们可能带凭据。
// Outcome 为 blocked、ResponseStatus 为 0，后者是「没有发出过外部请求」的意思，
// 也是这条记录最要紧的一个字段：账本据此能证明拦截确实发生在出站之前。
func (a *proxyAudits) RecordBlocked(ctx context.Context, blocked proxy.Blocked) error {
	_, err := a.recorder.Record(ctx, audit.Event{
		// operation_id 从 context 取：Proxy 的处理链在最外层就为每个请求生成了
		// 一个，账本与运行日志因此指向同一次请求。漏掉它这条记录根本写不进去
		// （ADR-004：审计写不进去，请求就得失败），而那种失败在代码里看不出来。
		OperationID: logging.OperationIDFrom(ctx),
		Type:        audit.EventDenied,
		AgentID:     blocked.Caller.AgentID,
		WorkspaceID: blocked.Caller.WorkspaceID,
		Service:     blocked.Route.Service,
		Operation:   blocked.Route.Operation,
		Resource:    resourceText(blocked.Route.Resource),
		// 被拦下的请求没有收敛出 Scope —— 拦截发生在决策之前。空对象是
		// 「这次没有 Scope」的如实记录，账本只接受合法 JSON。
		ResolvedScope: "{}",
		Verdict:       audit.Verdict(decision.VerdictDeny),
		Outcome:       audit.OutcomeBlocked,
		Metadata:      blockedMetadata(blocked),
	})
	return err
}

// blockedMetadata 只带方法、主机与路径。查询串不进来：REQ 的脱敏规则要求
// 记录 URL 时只保留 path。
func blockedMetadata(blocked proxy.Blocked) string {
	encoded, err := json.Marshal(map[string]string{
		"method": blocked.Target.Method,
		"host":   blocked.Target.Host,
		"path":   blocked.Target.Path,
		"reason": blocked.Reason,
	})
	if err != nil {
		// 三个都是普通字符串，marshal 不会失败；真失败了也不能让记账因此中断 ——
		// 记不上元数据总好过整条拒绝记录都写不进去。
		return ""
	}
	return string(encoded)
}

// resourceText 把资源维度编码成账本要求的 JSON 对象。
//
// 空也要编码成 `{}` 而不是空串：账本只接受合法 JSON，空串会让整条拒绝记录
// 写不进去 —— 而按 ADR-004 那意味着请求失败，且失败原因与真正的拒绝理由无关。
func resourceText(resource map[string]string) string {
	encoded, err := json.Marshal(resource)
	if err != nil {
		return "{}"
	}
	if len(resource) == 0 {
		return "{}"
	}
	return string(encoded)
}

// proxyLeases 用已签发的 Lease 授权一次代理请求。
type proxyLeases struct {
	leases *lease.Manager
}

var _ proxy.Leases = (*proxyLeases)(nil)

// maxActiveLeases 是一次匹配最多考虑的活跃 Lease 数。
//
// 与分页上限一致。同一台机器上同时活跃两百条 Lease 已经不正常了，
// 那时该报错而不是逐条扫下去。
const maxActiveLeases = 200

// Authorize 找一条罩得住这次请求的活跃 Lease，并记一次使用。
//
// 匹配要求 Agent、服务、操作、资源四项全中。前两项是 Lease 自己的列，
// 后两项要落在它签发时收敛好的 Scope 之内 —— 这一步由 lease.Manager.Use 判定，
// 本方法不自己比较，否则「范围够不够」就有了第二个答案。
func (l *proxyLeases) Authorize(
	ctx context.Context, caller proxy.Caller, route proxy.Route,
) (proxy.Grant, error) {
	active, err := l.leases.Active(ctx, maxActiveLeases)
	if err != nil {
		return proxy.Grant{}, err
	}

	for _, candidate := range active {
		// 先按列筛一道只是为了少走几次 Use。**安全不靠这一步**：Agent 与服务
		// 同样是 Scope 的两个维度，真正的比较在下面的 Use 里。去掉这个 if
		// 结果不变，只是会多消耗几次比较 —— 别把它读成权限检查。
		if candidate.AgentID != caller.AgentID || candidate.Service != route.Service {
			continue
		}
		requested, buildErr := requestedScope(candidate, caller, route)
		if buildErr != nil {
			continue
		}
		used, useErr := l.leases.Use(ctx, candidate.ID, requested)
		if useErr != nil {
			// 超范围、已过期、已用满都在这里落地。继续看下一条：同一个 Agent
			// 对同一个服务可以有多条 Lease，覆盖不同的资源。
			continue
		}
		return proxy.Grant{LeaseID: used.ID, IdentityID: used.IdentityID}, nil
	}

	return proxy.Grant{}, apperr.New(apperr.CodeCredentialNotAuthorized).
		WithDetail("没有罩得住这次请求的 Lease")
}

// requestedScope 拼出这次请求要求的 Scope。
//
// 十个维度里，Proxy 能观察到的只有 Agent、工作区、服务、操作、资源五项；
// 身份、账户、环境、有效期、次数是**签发时**定下的，不是这次 HTTP 请求的属性 ——
// 从 Lease 上取回来，让它们在比较中恒等。这不是放宽：真正决定「这次请求算不算
// 越界」的恰恰是那五项可观察维度，而它们逐一参与比较。
//
// 反过来写（让 Proxy 自己猜环境或身份）才是危险的：猜错了会让一条本该覆盖不到的
// Lease 匹配上，而账本上看不出这次匹配是猜出来的。
func requestedScope(
	granted lease.Lease, caller proxy.Caller, route proxy.Route,
) (scope.Scope, error) {
	var issued scope.Scope
	if err := json.Unmarshal([]byte(granted.ResourceScope), &issued); err != nil {
		return scope.Scope{}, apperr.Wrap(apperr.CodeInternal, err).
			WithDetail("Lease " + granted.ID + " 的 Scope 不是合法 JSON")
	}

	requested := issued
	requested.AgentID = caller.AgentID
	requested.WorkspaceID = caller.WorkspaceID
	requested.Service = route.Service
	requested.Operation = route.Operation
	requested.Resource = route.Resource
	requested.ResourceKey = intent.ResourceKeyOf(route.Resource)
	return requested, nil
}
