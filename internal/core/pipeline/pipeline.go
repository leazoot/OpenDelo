package pipeline

import (
	"context"
	"encoding/json"

	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/core/approval"
	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/intent"
	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/risk"
	"github.com/Runcoor/opendelo/internal/core/scope"
	"github.com/Runcoor/opendelo/internal/core/trust"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/audit"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
)

/*
 * 全链路编排：意图 → 身份 → Scope → 风险 → 决策 → 审计 → 放行或等人。
 *
 * 两条不变式在本文件里必须一眼可查：
 *
 *  1. **放行出口唯一**。整个包里只有 issue 一处调用 leases.Issue，
 *     而 issue 只从 Handle 的一个分支进入。
 *  2. **审计写在放行之前**（ADR-004）。写不进去就不放行 —— 一次没有留下记录的
 *     执行，事后无从追溯，那正是本产品不允许存在的东西。
 *
 * 前面每一步失败都不直接返回错误，而是折成一个 decision.Blocker 交给决策引擎，
 * 由它给出唯一的结论。这样「哪些情况会拒绝」只有一处答案。
 */

// Options 是 Pipeline 的依赖，全部必填。
type Options struct {
	Requests   CapabilityRequestRepository
	Decisions  decision.DecisionRepository
	Identities IdentityRepository
	// Agents 结束 Agent 会话，服务 REQ-CLI-002 AC3 的级联。
	Agents    AgentSessions
	Approvals *approval.Manager
	Leases    *lease.Manager
	Memories  *trust.Manager
	Intents   *intent.Resolver
	Scopes    *scope.Resolver
	Audit     *audit.Recorder
	Clock     clock.Clock
	IDs       *ulid.Generator
	// Mode 是自动化等级，默认平衡模式。
	Mode decision.Mode
}

// Pipeline 把一次能力请求跑完决策链路。
type Pipeline struct {
	requests   CapabilityRequestRepository
	decisions  decision.DecisionRepository
	identities IdentityRepository
	agents     AgentSessions
	approvals  *approval.Manager
	leases     *lease.Manager
	memories   *trust.Manager
	intents    *intent.Resolver
	scopes     *scope.Resolver
	audit      *audit.Recorder
	clock      clock.Clock
	ids        *ulid.Generator
	mode       decision.Mode
}

// New 校验依赖并构造 Pipeline。
func New(options Options) (*Pipeline, error) {
	mode := options.Mode
	if mode == "" {
		mode = decision.ModeBalanced
	}

	missing := map[string]bool{
		"能力请求仓储":        options.Requests == nil,
		"决策仓储":          options.Decisions == nil,
		"身份仓储":          options.Identities == nil,
		"Agent 会话服务":    options.Agents == nil,
		"审批 Manager":    options.Approvals == nil,
		"Lease Manager": options.Leases == nil,
		"记忆 Manager":    options.Memories == nil,
		"意图解析器":         options.Intents == nil,
		"Scope 收敛器":     options.Scopes == nil,
		"审计写入器":         options.Audit == nil,
		"时钟":            options.Clock == nil,
		"ID 生成器":        options.IDs == nil,
	}
	for name, absent := range missing {
		if absent {
			return nil, apperr.New(apperr.CodeInternal).WithDetail("Pipeline 缺少" + name)
		}
	}

	return &Pipeline{
		requests: options.Requests, decisions: options.Decisions,
		identities: options.Identities, agents: options.Agents,
		approvals: options.Approvals, leases: options.Leases, memories: options.Memories,
		intents: options.Intents, scopes: options.Scopes, audit: options.Audit,
		clock: options.Clock, ids: options.IDs, mode: mode,
	}, nil
}

// OperationFacts 是 Adapter 对一个操作性质的声明，风险计算的输入之一。
//
// 由调用方从能力声明里取出后传入，`core` 自己不去读注册表（依赖方向）。
// 全为零值时等级不会低于 Adapter 声明的标签，只是拿不到额外的上调。
type OperationFacts struct {
	Destructive           bool
	PermissionChange      bool
	SecretAccess          bool
	Billing               bool
	ExternalCommunication bool
	// ResourceCount 是本次影响的资源数量，0 表示无法确定。
	ResourceCount int
}

// Inputs 是一次请求所需的、由调用方加载好的只读数据（ADR-003）。
type Inputs struct {
	Request CapabilityRequest
	Call    intent.Call
	Catalog *intent.Catalog

	Identities        []matcher.Identity
	WorkspaceBindings []matcher.Binding
	ResourceBindings  []matcher.Binding
	ManualSelection   string

	AgentTrust  agentauth.TrustLevel
	DeviceTrust agentauth.DeviceTrust
	Facts       OperationFacts

	// 谨慎与自动模式的上下文（PRD §11）。
	ReadOnlyAutoAllow bool
	NewService        bool
	IdentityChanged   bool
	NewIdentity       bool
	NewResource       bool

	// Blockers 是调用方已经发现的阻断（Gateway 离线、凭据源不可用等）。
	Blockers []decision.Blocker
}

// Result 是一次编排的结论。
type Result struct {
	Request  CapabilityRequest
	Decision decision.Decision
	Outcome  decision.Outcome
	// Approval 只在结论为 require_approval 时非空。
	Approval *approval.Approval
	// Lease 只在结论为 auto_allow 时非空 —— 这是整条链路唯一的放行凭证。
	Lease *lease.Lease
}

// Handle 把一次能力请求跑完决策链路。
//
// 不返回「链路内部的失败」：认不出、算不出、定不了的情况都折成阻断交给决策引擎，
// 由它给出拒绝。返回错误只有两种情形 —— 输入本身不成立，或者账本写不进去。
// 后者按 ADR-004 必须让整个请求失败。
func (p *Pipeline) Handle(ctx context.Context, inputs Inputs) (result Result, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result, err = p.failClosed(ctx, inputs, decision.BlockerPolicyEngineFailure)
		}
	}()

	if inputs.Request.ID == "" || inputs.Request.OperationID == "" {
		return Result{}, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("能力请求缺少主键或 operation_id")
	}

	resolved, blocker := p.resolve(ctx, inputs)
	if blocker != "" {
		return p.failClosed(ctx, inputs, blocker)
	}

	outcome := decision.Decide(resolved.input)
	return p.conclude(ctx, inputs, resolved, outcome)
}

// resolved 是链路前半段算出来的中间结果。
type resolved struct {
	match  matcher.Result
	scope  scope.Result
	risk   risk.Assessment
	input  decision.Input
	grants []trust.Memory
}

// resolve 跑完意图 → 身份 → Scope → 风险 → 已学习授权，并装配决策输入。
//
// 任何一步失败都换成一个阻断而不是错误：决策引擎是唯一给结论的地方，
// 让每一步各自决定「这算不算拒绝」会把 Fail Closed 的判断散到五个包里。
func (p *Pipeline) resolve(ctx context.Context, inputs Inputs) (resolved, decision.Blocker) {
	var out resolved

	parsed, err := p.intents.Resolve(inputs.Catalog, inputs.Call)
	if err != nil {
		return out, decision.BlockerCapabilityNotOffered
	}
	// 记忆要在匹配身份**之前**取：它是身份匹配的第三级（REQ-IDENT-002）。
	// 放到匹配之后就只剩「这个项目里有几个候选」这一个依据，于是同一服务下
	// 连了第二个账号之后，学过的那条记忆再也没有机会说话 —— 每一次调用都变回歧义。
	grants, blocker := p.learned(ctx,
		inputs.Request.AgentID, inputs.Request.WorkspaceID, parsed.Service)
	if blocker != "" {
		return out, blocker
	}
	out.grants = grants

	matched, err := matcher.Match(matcher.Request{
		Service:     parsed.Service,
		WorkspaceID: inputs.Request.WorkspaceID,
		ResourceKey: parsed.ResourceKey,
	}, matcher.Inputs{
		WorkspaceBindings: inputs.WorkspaceBindings,
		ResourceBindings:  inputs.ResourceBindings,
		MemoryIdentityIDs: identitiesOf(grants),
		Identities:        inputs.Identities,
		ManualSelection:   inputs.ManualSelection,
	})
	if err != nil {
		return out, decision.BlockerIdentityAmbiguityUnresolvable
	}
	out.match = matched

	converged, err := p.scopes.Resolve(scope.Input{
		Intent:      parsed,
		AgentID:     inputs.Request.AgentID,
		WorkspaceID: inputs.Request.WorkspaceID,
		Identity:    matched.Identity,
	})
	if err != nil {
		return out, decision.BlockerScopeUndeterminable
	}
	out.scope = converged

	// 已签发的授权要在装配决策输入之前取好：`core` 不做 I/O（ADR-003），
	// 它只比较范围。
	standing, blocker := p.standing(ctx, inputs.Request.AgentID, parsed.Service)
	if blocker != "" {
		return out, blocker
	}

	assessment, err := risk.Evaluate(p.factors(inputs, parsed, converged.Scope, grants))
	if err != nil {
		return out, decision.BlockerRiskUnknown
	}
	out.risk = assessment

	out.input = decision.Input{
		Mode:              p.mode,
		AgentID:           inputs.Request.AgentID,
		AgentTrust:        inputs.AgentTrust,
		Write:             parsed.DesiredChange != "",
		Match:             matched,
		Scope:             converged,
		Assessment:        assessment,
		Learned:           toGrants(grants),
		Active:            standing,
		Blockers:          inputs.Blockers,
		ReadOnlyAutoAllow: inputs.ReadOnlyAutoAllow,
		NewService:        inputs.NewService,
		IdentityChanged:   inputs.IdentityChanged,
		NewIdentity:       inputs.NewIdentity,
		NewResource:       inputs.NewResource,
	}
	return out, ""
}

// learned 取出与本次请求相关、仍然可用的授权记忆。
//
// 只按 Agent、项目、服务三项取，不需要身份 —— 身份正是它要帮着定的那一项。
func (p *Pipeline) learned(
	ctx context.Context, agentID, workspaceID, service string,
) ([]trust.Memory, decision.Blocker) {
	memories, err := p.memories.Match(ctx, agentID, workspaceID, service, memoryLimit)
	if err != nil {
		return nil, decision.BlockerPolicyEngineFailure
	}
	return memories, ""
}

// standing 取出这个 Agent 在这个服务上仍然生效的已签发授权。
//
// 「允许到任务结束」签的正是这样一条：它不生成记忆，因此不在 learned 里。
// 少了这一步，同一会话里下一次同样的调用会被再问一遍人（R-39）。
//
// **只把还活着的传给决策引擎**：已过期的在这里就滤掉 —— 决策引擎不做 I/O，
// 判不了「现在几点」，而一条过期的授权在范围比较里与没过期的长得一模一样。
// 读不懂范围的同样丢掉：罩不住任何东西的授权留着只会让排查变难。
func (p *Pipeline) standing(
	ctx context.Context, agentID, service string,
) ([]decision.ActiveLease, decision.Blocker) {
	active, err := p.leases.Active(ctx, leaseLimit)
	if err != nil {
		return nil, decision.BlockerPolicyEngineFailure
	}

	now := p.clock.Now()
	covering := make([]decision.ActiveLease, 0, len(active))
	for _, candidate := range active {
		if candidate.AgentID != agentID || candidate.Service != service {
			continue
		}
		if !candidate.ExpiresAt.After(now) {
			continue
		}
		granted, scopeErr := lease.ScopeOf(candidate)
		if scopeErr != nil {
			continue
		}
		covering = append(covering, decision.ActiveLease{LeaseID: candidate.ID, Scope: granted})
	}
	return covering, ""
}

// leaseLimit 是一次匹配最多考虑多少条已签发授权。无界查询在仓储层就被拒。
const leaseLimit = 100

// identitiesOf 取出这些记忆各自认下的身份，供身份匹配的第三级使用。
//
// 不去重：`matcher.selectIdentities` 按候选身份取交集，重复的主键不会多出一个候选。
func identitiesOf(memories []trust.Memory) []string {
	pinned := make([]string, 0, len(memories))
	for _, memory := range memories {
		pinned = append(pinned, memory.IdentityID)
	}
	return pinned
}

// memoryLimit 是一次匹配最多取多少条记忆。无界查询在仓储层就被拒。
const memoryLimit = 100

// toGrants 把记忆换成决策引擎能比较的形状。读不懂范围的记忆被丢掉 ——
// 它覆盖不了任何东西，留着只会让「为什么没命中」查不出来，所以不参与匹配。
//
// 不再按服务过滤一遍：MatchMemories 的查询本来就带服务条件，
// 在这里重复一次只会让同一条判断有两处答案。
func toGrants(memories []trust.Memory) []decision.Grant {
	grants := make([]decision.Grant, 0, len(memories))
	for _, memory := range memories {
		learned, err := trust.ScopeOf(memory)
		if err != nil {
			continue
		}
		grants = append(grants, decision.Grant{
			MemoryID:  memory.ID,
			Scope:     learned,
			AlwaysAsk: memory.Behavior == trust.BehaviorAlwaysAsk,
		})
	}
	return grants
}

// factors 装配十三个风险因子。
func (p *Pipeline) factors(
	inputs Inputs, parsed intent.Intent, converged scope.Scope, grants []trust.Memory,
) risk.Factors {
	count := inputs.Facts.ResourceCount
	if count == 0 && !parsed.ResourceAmbiguous {
		count = 1
	}

	return risk.Factors{
		Write:                 parsed.DesiredChange != "",
		Production:            converged.Environment == matcher.EnvironmentProduction,
		Reversible:            parsed.Reversible,
		ResourceCount:         count,
		Destructive:           inputs.Facts.Destructive,
		PermissionChange:      inputs.Facts.PermissionChange,
		SecretAccess:          inputs.Facts.SecretAccess || parsed.SensitiveData,
		Billing:               inputs.Facts.Billing,
		ExternalCommunication: inputs.Facts.ExternalCommunication,
		FirstSeen:             len(grants) == 0,
		BeyondHistory:         beyondHistory(grants, converged),
		AgentTrust:            inputs.AgentTrust,
		DeviceTrust:           inputs.DeviceTrust,
		DeclaredLabel:         parsed.RiskLabel,
	}
}

// beyondHistory 报告本次范围是否超出了全部已学习的授权。
// 一条都没学过时不算超出 —— 那是「第一次」，由 FirstSeen 表达。
func beyondHistory(memories []trust.Memory, converged scope.Scope) bool {
	if len(memories) == 0 {
		return false
	}
	for _, memory := range memories {
		learned, err := trust.ScopeOf(memory)
		if err != nil {
			continue
		}
		if learned.CoversIgnoringWindow(converged) {
			return false
		}
	}
	return true
}

// conclude 把决策结论落成账本记录与后续动作。
//
// 顺序是固定的：先写决策记录，再写审计，最后才动 Lease。
// 审计写不进去就在这里返回错误，放行那一步根本走不到（ADR-004）。
func (p *Pipeline) conclude(
	ctx context.Context, inputs Inputs, out resolved, outcome decision.Outcome,
) (Result, error) {
	record, err := p.recordDecision(ctx, inputs, out, outcome)
	if err != nil {
		return Result{}, err
	}

	result := Result{Decision: record, Outcome: outcome}

	var next RequestStatus
	switch outcome.Verdict {
	case decision.VerdictDeny:
		if writeErr := p.write(
			ctx, inputs, out, record, audit.EventDenied, audit.OutcomeBlocked,
		); writeErr != nil {
			return Result{}, writeErr
		}
		next = StatusDenied

	case decision.VerdictRequireApproval:
		if writeErr := p.write(
			ctx, inputs, out, record, audit.EventDenied, audit.OutcomeBlocked,
		); writeErr != nil {
			return Result{}, writeErr
		}
		item, openErr := p.approvals.Open(ctx, record.ID)
		if openErr != nil {
			return Result{}, openErr
		}
		result.Approval = &item
		next = StatusAwaitingApproval

	default:
		// 唯一的放行分支。审计已经在 issue 里先于签发写入。
		issued, issueErr := p.issue(ctx, inputs, out, record, outcome)
		if issueErr != nil {
			return Result{}, issueErr
		}
		result.Lease = issued
		next = StatusAutoAllowed
	}

	advanced, err := p.advance(ctx, inputs.Request, next)
	if err != nil {
		return Result{}, err
	}
	result.Request = advanced
	return result, nil
}

// issue 是自动放行那条分支的落点。
//
// 审计先写：ADR-004 要求「未审计的执行 = 0」，所以账本写不进去时这里直接返回，
// issueLease 根本不会被调用。
func (p *Pipeline) issue(
	ctx context.Context, inputs Inputs, out resolved, record decision.Decision,
	outcome decision.Outcome,
) (*lease.Lease, error) {
	if err := p.write(
		ctx, inputs, out, record, audit.EventAutoAllowed, audit.OutcomeSucceeded,
	); err != nil {
		return nil, err
	}

	// 据一条已签发的授权放行时**复用它，不另签一条**：为同一次请求签出第二条
	// 授权，等于把一次人工确认换成了两份权限，而计次上限也随之翻倍（D-17）。
	if outcome.MatchedLeaseID != "" {
		reused, err := p.leases.ByID(ctx, outcome.MatchedLeaseID)
		if err != nil {
			return nil, err
		}
		return &reused, nil
	}

	return p.issueLease(ctx, lease.IssueRequest{
		Granted:        out.scope.Scope,
		SourceMemoryID: record.TrustMemoryID,
	})
}

// issueLease 是整条链路唯一签发 Lease 的地方。
//
// 自动放行（issue）与人工放行（grant）都在写完审计之后汇到这里 ——
// 「哪里会产生一条授权」因此只有一处答案，审查时不必去数调用点
func (p *Pipeline) issueLease(
	ctx context.Context, request lease.IssueRequest,
) (*lease.Lease, error) {
	issued, err := p.leases.Issue(ctx, request)
	if err != nil {
		return nil, err
	}
	return &issued, nil
}

func (p *Pipeline) recordDecision(
	ctx context.Context, inputs Inputs, out resolved, outcome decision.Outcome,
) (decision.Decision, error) {
	id, err := p.ids.NewID()
	if err != nil {
		return decision.Decision{}, err
	}
	factors, err := json.Marshal(out.risk.Factors)
	if err != nil {
		return decision.Decision{}, apperr.Wrap(apperr.CodeInternal, err).
			WithDetail("风险因子无法编码")
	}
	resolvedScope, err := json.Marshal(out.scope.Scope)
	if err != nil {
		return decision.Decision{}, apperr.Wrap(apperr.CodeInternal, err).
			WithDetail("Scope 无法编码")
	}

	return p.decisions.CreateDecision(ctx, decision.Decision{
		ID:                  id,
		CapabilityRequestID: inputs.Request.ID,
		Verdict:             outcome.Verdict,
		RiskLevel:           out.risk.Level,
		RiskFactors:         string(factors),
		IdentityID:          out.match.Identity.ID,
		MatchLevel:          out.match.Level,
		ResolvedScope:       string(resolvedScope),
		ApprovalRequirement: outcome.ApprovalRequirement,
		ReasonCode:          string(outcome.Reason),
		TrustMemoryID:       outcome.MatchedMemoryID,
		CreatedAt:           p.clock.Now(),
	})
}

func (p *Pipeline) write(
	ctx context.Context, inputs Inputs, out resolved,
	record decision.Decision, eventType audit.EventType, result audit.Outcome,
) error {
	_, err := p.audit.Record(ctx, audit.Event{
		OperationID:   inputs.Request.OperationID,
		Type:          eventType,
		AgentID:       inputs.Request.AgentID,
		WorkspaceID:   inputs.Request.WorkspaceID,
		IdentityID:    out.match.Identity.ID,
		Service:       out.scope.Scope.Service,
		Operation:     out.scope.Scope.Operation,
		Resource:      resourceOf(inputs.Request),
		ResolvedScope: record.ResolvedScope,
		Verdict:       audit.Verdict(record.Verdict),
		RiskLevel:     audit.RiskLevel(record.RiskLevel),
		DecisionID:    record.ID,
		Outcome:       result,
		Metadata:      `{"reason":"` + record.ReasonCode + `"}`,
	})
	return err
}

// failClosed 是链路前半段走不下去时的落点：记一条 error 级事件后拒绝。
//
// 审计同样是前置条件 —— 一次「因为认不出发起方而被拒」的请求也必须留下记录，
// 否则「无未审计路径」（REQ-AUDIT-001 AC1）就不成立。
func (p *Pipeline) failClosed(
	ctx context.Context, inputs Inputs, blocker decision.Blocker,
) (Result, error) {
	if _, err := p.audit.Record(ctx, audit.Event{
		OperationID:   inputs.Request.OperationID,
		Type:          audit.EventError,
		AgentID:       inputs.Request.AgentID,
		WorkspaceID:   inputs.Request.WorkspaceID,
		Service:       inputs.Request.Service,
		Operation:     inputs.Request.Operation,
		Resource:      resourceOf(inputs.Request),
		ResolvedScope: "{}",
		Verdict:       audit.Verdict(decision.VerdictDeny),
		Outcome:       audit.OutcomeBlocked,
		Metadata:      `{"blocker":"` + string(blocker) + `"}`,
	}); err != nil {
		return Result{}, err
	}

	request, err := p.advance(ctx, inputs.Request, StatusDenied)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Request: request,
		Outcome: decision.Outcome{
			Verdict:             decision.VerdictDeny,
			ApprovalRequirement: decision.ApprovalNone,
			Reason:              decision.ReasonFailClosed,
			Blocker:             blocker,
		},
	}, nil
}

// resourceOf 取请求携带的资源字段。空串换成空对象 ——
// 审计的 resource 列有 json_valid 约束，而「这次请求没有资源字段」
// 本身也是一条要如实记下的事实。
func resourceOf(request CapabilityRequest) string {
	if request.Resource == "" {
		return "{}"
	}
	return request.Resource
}

func (p *Pipeline) advance(
	ctx context.Context, request CapabilityRequest, to RequestStatus,
) (CapabilityRequest, error) {
	return p.requests.AdvanceRequest(ctx, request.ID, request.Status, to, p.clock.Now())
}
