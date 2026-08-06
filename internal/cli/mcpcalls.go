package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	adapters "github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/core/pipeline"
	"github.com/Runcoor/opendelo/internal/core/scope"
	"github.com/Runcoor/opendelo/internal/orchestration"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/audit"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/logging"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
	"github.com/Runcoor/opendelo/internal/transport/mcpsrv"
)

/*
 * MCP 工具调用的编排（REQ-MCP-003）。
 *
 * 这是 8789 与决策链路之间的那段接线，也是本产品里第一条**完整**的路：
 * 工具调用 → 能力请求 → 决策 → 放行则取凭据执行 → 脱敏后回话。
 *
 * 它落在组装根是依赖方向逼出来的：`core/pipeline` 与 `adapter/registry` 谁也不能
 * import 谁，而这段编排两边都要用。
 *
 * 两处地方容易写松，都在下面标了出来：
 *
 *  1. **执行用的是 Lease 上的 Scope，不是调用参数。** 参数只用来发起请求；
 *     真正出站的服务、操作与资源一律从签发出来的那条 Lease 上取回。两者本该
 *     一致，但「本该一致」不是一道检查 —— 从 Lease 取回来，它们就不可能不一致。
 *  2. **放行分支只认 auto_allow 且必须带着 Lease。** 少了任何一半都走拒绝，
 *     而不是「既然已经决定放行，Lease 大概只是没填」。
 */

// agentRecords 读取发起调用的 Agent。
//
// 只取 AgentByID 一个方法：认证已经在接入面完成，这里要的是信任等级与设备，
// 而不是再认一次。
type agentRecords interface {
	AgentByID(ctx context.Context, id string) (agentauth.Agent, error)
}

// deviceRecords 读取 Agent 所在的设备。
type deviceRecords interface {
	DeviceByID(ctx context.Context, id string) (agentauth.Device, error)
}

// mcpCalls 把一次 MCP 工具调用跑完决策链路。
type mcpCalls struct {
	// submissions 是三个面共用的那段：装配决策输入并跑完链路。
	submissions *orchestration.Submissions
	requests    pipeline.CapabilityRequestRepository
	leases      *lease.Manager
	registry    *adapters.Registry
	exchange    *adapters.Exchange
	recorder    *audit.Recorder
	clock       clock.Clock
	ids         *ulid.Generator
}

var _ mcpsrv.Calls = (*mcpCalls)(nil)

// newMCPCalls 校验依赖并构造编排。
//
// 缺任何一项都拒绝构造：一个「少了账本」的编排会一路跑到放行那一步才发现写不了
// 审计，而那时凭据已经取出来过一次。
func newMCPCalls(calls mcpCalls) (*mcpCalls, error) {
	missing := map[string]bool{
		"请求编排":          calls.submissions == nil,
		"能力请求仓储":        calls.requests == nil,
		"Lease Manager": calls.leases == nil,
		"Adapter 注册表":   calls.registry == nil,
		"执行面":           calls.exchange == nil,
		"账本":            calls.recorder == nil,
		"时钟":            calls.clock == nil,
		"ID 生成器":        calls.ids == nil,
	}
	for name, absent := range missing {
		if absent {
			return nil, apperr.New(apperr.CodeInternal).WithDetail("MCP 编排缺少" + name)
		}
	}
	return &calls, nil
}

// Call 跑完一次工具调用。
//
// 返回错误只在两种情形：调用本身不成立（参数不是合法 JSON、工具与注册表对不上），
// 或者账本写不进去（ADR-004）。**按策略拒绝不是错误** —— 它是产品的正常输出，
// 走 Refused 交给模型，模型因此知道该去找人而不是换个工具再试。
func (c *mcpCalls) Call(ctx context.Context, call mcpsrv.Call) (mcpsrv.CallOutcome, error) {
	request, err := c.open(ctx, call)
	if err != nil {
		return mcpsrv.CallOutcome{}, err
	}

	result, err := c.submissions.Decide(ctx, request)
	if err != nil {
		return mcpsrv.CallOutcome{}, err
	}

	switch result.Outcome.Verdict {
	case decision.VerdictAutoAllow:
		if result.Lease == nil {
			// 放行结论没有带着 Lease 意味着链路内部不自洽。此时不执行：
			// 没有 Lease 就没有可比对的范围，执行等于凭一个结论字符串出站。
			return mcpsrv.CallOutcome{}, apperr.New(apperr.CodeInternal).
				WithDetail("放行结论没有附带 Lease")
		}
		return c.execute(ctx, result.Request, *result.Lease)

	case decision.VerdictRequireApproval:
		return refusal("This call is waiting for a person to approve it in OpenDelo. " +
			"Nothing was sent to " + call.Service + "."), nil

	case decision.VerdictDeny:
		return refusal("Refused by policy (" + string(result.Outcome.Reason) + "). " +
			"Nothing was sent to " + call.Service + "."), nil

	default:
		// 取值集合是封闭的三项。多出来的一项只可能来自一次没写完的改动，
		// 而此处的默认值只能是拒绝。
		return refusal("Refused: the gateway could not reach a decision."), nil
	}
}

// open 把这次调用落成一条能力请求。
//
// 先落库再决策：账本上的每一条决策都要能指回一个具体的请求，
// 而「决策完了再补一条请求」在失败时会留下一个没有出处的结论。
//
// 参数在这里就拆成资源与变更两部分，因为拆分依据是**能力声明**里的资源维度，
// 而决策链路只认落库之后的那两个字段。
func (c *mcpCalls) open(
	ctx context.Context, call mcpsrv.Call,
) (pipeline.CapabilityRequest, error) {
	capability, err := c.registry.Capability(call.Service, call.Operation)
	if err != nil {
		return pipeline.CapabilityRequest{}, err
	}
	resource, change, err := splitArguments(call.Arguments, capability)
	if err != nil {
		return pipeline.CapabilityRequest{}, err
	}

	id, err := c.ids.NewID()
	if err != nil {
		return pipeline.CapabilityRequest{}, err
	}
	now := c.clock.Now()

	return c.requests.CreateRequest(ctx, pipeline.CapabilityRequest{
		ID:            id,
		OperationID:   logging.OperationIDFrom(ctx),
		AgentID:       call.Caller.AgentID,
		WorkspaceID:   call.Caller.WorkspaceID,
		Service:       call.Service,
		Operation:     call.Operation,
		Resource:      resource,
		DesiredChange: change,
		Reason:        "MCP tool call: " + call.Tool,
		Status:        pipeline.StatusReceived,
		CreatedAt:     now,
		UpdatedAt:     now,
	})
}

// execute 取凭据、出站、记账。只有 auto_allow 走到这里。
//
// 顺序：计量 → 出站 → 记账 → 回话。计量在前是因为它同时是一次范围校验
// （lease.Manager.Use 在超范围、已过期、已用满时拒绝）；把它放在出站之后，
// 一条已经用满的 Lease 还能再发一次请求出去。
func (c *mcpCalls) execute(
	ctx context.Context, request pipeline.CapabilityRequest, granted lease.Lease,
) (mcpsrv.CallOutcome, error) {
	authorized, err := scopeOfLease(granted)
	if err != nil {
		return mcpsrv.CallOutcome{}, err
	}

	running, err := c.requests.AdvanceRequest(ctx,
		request.ID, pipeline.StatusAutoAllowed, pipeline.StatusExecuting, c.clock.Now())
	if err != nil {
		return mcpsrv.CallOutcome{}, err
	}

	if _, useErr := c.leases.Use(ctx, granted.ID, authorized); useErr != nil {
		return c.failed(ctx, running, granted, authorized, 0, useErr)
	}

	started := c.clock.Now()
	reply, sendErr := c.exchange.Send(ctx, adapters.ExchangeRequest{
		Service:   authorized.Service,
		Operation: authorized.Operation,
		// 身份取自 Lease 而不是重新匹配一次：这条 Lease 就是「用这个身份做这件事」
		// 的那份授权，重新匹配等于让执行有机会用上另一个身份。
		IdentityID: authorized.IdentityID,
		Resource:   authorized.Resource,
		// 正文就是这次请求的期望变更 —— 也正是用户在卷宗上看到并批准的那份内容。
		// 曾经写死成 nil：风险照着它算、卷宗照着它显示、人照着它点头，
		// 然后出站的时候它掉了，外部服务收到一个空正文的写请求。
		Body:        []byte(running.DesiredChange),
		OperationID: request.OperationID,
	})
	if sendErr != nil {
		return c.failed(ctx, running, granted, authorized, c.clock.Now().Sub(started), sendErr)
	}

	// 上游没答成也是一次执行：它发生过、花了时间、可能已经改变了外部状态。
	// 一律记成 succeeded 会让账本看不出这次调用其实没成，而账本是唯一的事后依据。
	outcome, next := audit.OutcomeSucceeded, pipeline.StatusSucceeded
	if reply.StatusCode != http.StatusOK {
		outcome, next = audit.OutcomeFailed, pipeline.StatusFailed
	}

	// 记的是**上游真正答的**那个数字，不是给 Agent 的 200/502 映射。
	// 状态码不是报文：对外的错误码与消息一个字不变，而账本上少了它，
	// 排查一次失败时看得见的就只有「网关不可用」（R-44）。
	if err := c.record(ctx, running, granted, authorized,
		outcome, reply.UpstreamStatus, c.clock.Now().Sub(started)); err != nil {
		return mcpsrv.CallOutcome{}, err
	}
	if _, err := c.requests.AdvanceRequest(ctx,
		running.ID, pipeline.StatusExecuting, next, c.clock.Now()); err != nil {
		return mcpsrv.CallOutcome{}, err
	}

	// 回给模型的是 Adapter 脱敏之后的那份内容，本包不再加工 —— 失败的那份里
	// 装的是同样脱敏过的错误，模型据此知道是外部服务没答成，而不是它被拒了。
	return mcpsrv.CallOutcome{Text: string(reply.Body)}, nil
}

// failed 记下一次失败的执行并把请求推进到终态。
//
// 原始错误一定向上返回：执行失败与「记不上账」是两件事，把前者换成后者
// 会让排查从一开始就走错方向。
func (c *mcpCalls) failed(
	ctx context.Context, request pipeline.CapabilityRequest, granted lease.Lease,
	authorized scope.Scope, elapsed time.Duration, cause error,
) (mcpsrv.CallOutcome, error) {
	recordErr := c.record(ctx, request, granted, authorized, audit.OutcomeFailed, 0, elapsed)
	_, advanceErr := c.requests.AdvanceRequest(ctx,
		request.ID, pipeline.StatusExecuting, pipeline.StatusFailed, c.clock.Now())
	return mcpsrv.CallOutcome{}, errors.Join(cause, recordErr, advanceErr)
}

// record 写一条执行事件。
//
// 记的是元数据：谁、用哪个身份、对哪个资源做了什么、结果如何、花了多久。
// **不记**请求正文与响应正文。
func (c *mcpCalls) record(
	ctx context.Context, request pipeline.CapabilityRequest, granted lease.Lease,
	authorized scope.Scope, outcome audit.Outcome, status int, elapsed time.Duration,
) error {
	resolvedScope, err := json.Marshal(authorized)
	if err != nil {
		return apperr.Wrap(apperr.CodeInternal, err).WithDetail("Lease 的 Scope 无法编码")
	}

	_, err = c.recorder.Record(ctx, audit.Event{
		OperationID:    request.OperationID,
		Type:           audit.EventAdapterExecuted,
		AgentID:        request.AgentID,
		WorkspaceID:    request.WorkspaceID,
		IdentityID:     authorized.IdentityID,
		Service:        authorized.Service,
		Operation:      authorized.Operation,
		Resource:       resourceText(authorized.Resource),
		ResolvedScope:  string(resolvedScope),
		Verdict:        audit.Verdict(decision.VerdictAutoAllow),
		LeaseID:        granted.ID,
		Outcome:        outcome,
		ResponseStatus: status,
		Duration:       elapsed,
		IsRedacted:     true,
		// 记下是哪个面驱动的：同一条链路上，8788 与 8789 的执行在账本里
		// 除此之外看不出区别，而「Agent 是自己发的 HTTP 还是调的工具」
		// 正是排查时第一个要问的问题。
		Metadata: `{"face":"mcp"}`,
	})
	return err
}

// splitArguments 把工具参数拆成资源与变更两部分。
//
// 拆分依据是能力声明里的资源维度（REQ-ADAPTER-001 的 MinimumScope）：
// 声明为资源的字段进 Resource，其余进 DesiredChange。读操作没有变更部分 ——
// 那不是省略，`DesiredChange` 为空正是「这是一次读」在决策链路里的表达方式。
func splitArguments(
	arguments json.RawMessage, capability adapters.Capability,
) (string, string, error) {
	fields := map[string]json.RawMessage{}
	if len(arguments) > 0 {
		if err := json.Unmarshal(arguments, &fields); err != nil {
			return "", "", apperr.Wrap(apperr.CodeInvalidRequest, err).
				WithDetail("工具参数不是一个 JSON 对象")
		}
	}

	resource := map[string]json.RawMessage{}
	for _, key := range capability.MinimumScope.ResourceKeys {
		if value, present := fields[key]; present {
			resource[key] = value
			delete(fields, key)
		}
	}

	encodedResource, err := json.Marshal(resource)
	if err != nil {
		return "", "", apperr.Wrap(apperr.CodeInvalidRequest, err).
			WithDetail("工具参数中的资源字段无法编码")
	}
	if !capability.Write() {
		return string(encodedResource), "", nil
	}

	encodedChange, err := json.Marshal(fields)
	if err != nil {
		return "", "", apperr.Wrap(apperr.CodeInvalidRequest, err).
			WithDetail("工具参数中的变更字段无法编码")
	}
	return string(encodedResource), string(encodedChange), nil
}

// scopeOfLease 取回签发时收敛好的十个维度。
func scopeOfLease(granted lease.Lease) (scope.Scope, error) {
	var authorized scope.Scope
	if err := json.Unmarshal([]byte(granted.ResourceScope), &authorized); err != nil {
		return scope.Scope{}, apperr.Wrap(apperr.CodeInternal, err).
			WithDetail("Lease " + granted.ID + " 的 Scope 不是合法 JSON")
	}
	return authorized, nil
}

func refusal(text string) mcpsrv.CallOutcome {
	return mcpsrv.CallOutcome{Refused: true, Text: text}
}
