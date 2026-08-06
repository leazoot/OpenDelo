package pipeline

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/Runcoor/opendelo/internal/core/approval"
	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/core/scope"
	"github.com/Runcoor/opendelo/internal/core/trust"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/audit"
)

/*
 * 审批落地：人点了头之后该发生什么（REQ-APPROVAL-002、REQ-API-004）。
 *
 * 与自动放行那条路走的是同一套不变式：
 *
 *  1. **放行出口仍然唯一**。这里不直接调用 leases.Issue，而是与自动放行走同一个
 *     issueLease，因此「哪里会签发授权」只有一处答案。
 *  2. **审计写在放行之前**（ADR-004）。写不进去就不签发。
 *  3. **重复提交不产生第二次后果**（REQ-API-004）：审批仓储的条件更新拦下第二次
 *     写入，本方法据此把首次的 Lease 原样返回，而不是再签一条。
 */

// SettleInput 是一次人工审批落地的输入。
type SettleInput struct {
	ApprovalID string
	Action     approval.Action
	// StrongAuthCompleted 由接入面填：决策要求 strong_auth 时它必须为真。
	StrongAuthCompleted bool
	// Confirmations 是这次提交已经完成的确认次数。
	Confirmations int
}

// SettleResult 是一次审批落地的结果。
type SettleResult struct {
	Approval approval.Approval
	Decision decision.Decision
	Request  CapabilityRequest
	// Lease 只在放行时非空。重复提交时它是首次签发的那一条。
	Lease *lease.Lease
	// Memory 只在这次提交真的学到了一条记忆时非空。
	Memory *trust.Memory
	// Replayed 为真表示这次提交重放了之前已经做出的同一个决定，没有产生新的后果。
	Replayed bool
}

// SettleApproval 把用户给出的审批结果落成账本记录、Lease 与记忆。
//
// 风险等级与确认强度取自当时写下的决策记录，不由调用方传入：让接入面自报
// 「这次是低风险」，等于把「哪些操作可以只点一次头」交给了请求方（REQ-DECIDE-004）。
func (p *Pipeline) SettleApproval(ctx context.Context, input SettleInput) (SettleResult, error) {
	item, err := p.approvals.ByID(ctx, input.ApprovalID)
	if err != nil {
		return SettleResult{}, err
	}
	record, err := p.decisions.DecisionByID(ctx, item.DecisionID)
	if err != nil {
		return SettleResult{}, err
	}
	request, err := p.requests.RequestByID(ctx, record.CapabilityRequestID)
	if err != nil {
		return SettleResult{}, err
	}

	// 放行之前先看授权窗口还在不在（R-43）。
	//
	// 收敛出来的窗口是**决策那一刻**算的，而人可以隔更久才点头。窗口已经过去时
	// `lease.Issue` 会正确拒签，但那时审批项已经被 Settle 消费、账本上已经写着
	// 「用户放行」—— 一件没有发生的事。所以这道检查必须在消费之前，
	// 而且只挡放行：拒绝一条过期的请求仍然是有意义的。
	if approval.Allows(input.Action) {
		granted, scopeErr := decodeScope(record.ResolvedScope)
		if scopeErr != nil {
			return SettleResult{}, scopeErr
		}
		if !granted.ExpiresAt.After(p.clock.Now()) {
			return SettleResult{}, apperr.New(apperr.CodeApprovalTimeout).
				WithDetail("这次授权的时间窗已经过去，签不出 Lease 了。" +
					"让 Agent 重新发起一次请求，缝前会再出现一条待决项")
		}
	}

	settlement, err := p.approvals.Settle(ctx, approval.SettleRequest{
		ApprovalID:            input.ApprovalID,
		Action:                input.Action,
		RiskLevel:             record.RiskLevel,
		StrongAuthCompleted:   input.StrongAuthCompleted,
		Requirement:           record.ApprovalRequirement,
		RequiredConfirmations: decision.RequiredConfirmations(p.mode, record.RiskLevel),
		Confirmations:         input.Confirmations,
	})
	if err != nil {
		return SettleResult{}, err
	}

	result := SettleResult{
		Approval: settlement.Approval,
		Decision: record,
		Request:  request,
		Replayed: settlement.Replayed,
	}
	if settlement.Replayed {
		return p.replayed(ctx, result, settlement)
	}
	return p.applySettlement(ctx, result, settlement, request, record)
}

// replayed 把首次决定的结果原样取回。
//
// 不写审计、不签发、不学记忆：那些在首次提交时就已经发生了，再做一次
// 会让账本上出现两条「用户放行」而实际只点过一次头。
func (p *Pipeline) replayed(
	ctx context.Context, result SettleResult, settlement approval.Settlement,
) (SettleResult, error) {
	if !settlement.Allowed {
		return result, nil
	}

	issued, err := p.leases.IssuedFor(ctx, settlement.Approval.ID)
	if err != nil {
		// 放行过却找不到 Lease，说明首次提交在签发那一步就失败了。
		// 返回冲突而不是补签一条：补签会绕开「审计先于放行」那道顺序。
		if apperr.Is(err, apperr.CodeNotFound) {
			return SettleResult{}, apperr.New(apperr.CodeConflict).
				WithDetail("审批项 " + settlement.Approval.ID + " 已放行但没有对应的 Lease")
		}
		return SettleResult{}, err
	}
	result.Lease = &issued
	return result, nil
}

// applySettlement 执行一次新的审批结论。
func (p *Pipeline) applySettlement(
	ctx context.Context, result SettleResult, settlement approval.Settlement,
	request CapabilityRequest, record decision.Decision,
) (SettleResult, error) {
	granted, err := decodeScope(record.ResolvedScope)
	if err != nil {
		return SettleResult{}, err
	}

	if !settlement.Allowed {
		if writeErr := p.recordSettlement(
			ctx, request, record, granted, settlement.Approval.ID,
			audit.EventDenied, audit.OutcomeBlocked,
		); writeErr != nil {
			return SettleResult{}, writeErr
		}
		advanced, advanceErr := p.advance(ctx, request, StatusRejected)
		if advanceErr != nil {
			return SettleResult{}, advanceErr
		}
		result.Request = advanced
		return result, nil
	}

	issued, err := p.grant(ctx, request, record, granted, settlement)
	if err != nil {
		return SettleResult{}, err
	}
	result.Lease = issued

	learned, err := p.learn(ctx, record, granted, settlement)
	if err != nil {
		return SettleResult{}, err
	}
	result.Memory = learned

	advanced, err := p.advance(ctx, request, StatusApproved)
	if err != nil {
		return SettleResult{}, err
	}
	result.Request = advanced
	return result, nil
}

// grant 是人工放行这条路上的签发出口。
//
// 审计先写：与自动放行的 issue 一样，账本写不进去时 leases.Issue 走不到（ADR-004）。
func (p *Pipeline) grant(
	ctx context.Context, request CapabilityRequest, record decision.Decision,
	granted scope.Scope, settlement approval.Settlement,
) (*lease.Lease, error) {
	if err := p.recordSettlement(
		ctx, request, record, granted, settlement.Approval.ID,
		audit.EventUserAllowed, audit.OutcomeSucceeded,
	); err != nil {
		return nil, err
	}

	// 「仅允许这一次」把次数上限收紧到 1。这是收紧，不是放宽：
	// 决策收敛出来的上限只会更大。
	if settlement.RequestLimit > 0 && settlement.RequestLimit < granted.RequestLimit {
		granted.RequestLimit = settlement.RequestLimit
	}

	return p.issueLease(ctx, lease.IssueRequest{
		Granted:        granted,
		ApprovalID:     settlement.Approval.ID,
		SourceMemoryID: record.TrustMemoryID,
		IsSessionBound: settlement.SessionBound,
	})
}

// learn 在用户选择了学习类操作时生成一条记忆。
//
// 记忆的范围就是这次收敛出来的范围，一个维度都不放大（REQ-TRUST-002）——
// 收敛校验在 trust.Manager 里做，这里不给它任何可以绕开的入口。
func (p *Pipeline) learn(
	ctx context.Context, record decision.Decision,
	granted scope.Scope, settlement approval.Settlement,
) (*trust.Memory, error) {
	if settlement.Learn == "" {
		return nil, nil
	}

	memory, err := p.memories.Generate(ctx, trust.GenerateRequest{
		Approved:   granted,
		Learned:    granted,
		RiskLevel:  record.RiskLevel,
		Behavior:   settlement.Learn,
		ApprovalID: settlement.Approval.ID,
	})
	if err != nil {
		return nil, err
	}
	return &memory, nil
}

func (p *Pipeline) recordSettlement(
	ctx context.Context, request CapabilityRequest, record decision.Decision,
	granted scope.Scope, approvalID string,
	eventType audit.EventType, outcome audit.Outcome,
) error {
	_, err := p.audit.Record(ctx, audit.Event{
		OperationID:   request.OperationID,
		Type:          eventType,
		AgentID:       request.AgentID,
		WorkspaceID:   request.WorkspaceID,
		IdentityID:    record.IdentityID,
		Service:       granted.Service,
		Operation:     granted.Operation,
		Resource:      resourceOf(request),
		ResolvedScope: record.ResolvedScope,
		Verdict:       audit.Verdict(record.Verdict),
		RiskLevel:     audit.RiskLevel(record.RiskLevel),
		DecisionID:    record.ID,
		ApprovalID:    approvalID,
		Outcome:       outcome,
		Metadata:      `{"reason":"` + record.ReasonCode + `"}`,
	})
	return err
}

// BlockedRequest 是一次被入口直接拦下、还没来得及落库的请求。
type BlockedRequest struct {
	OperationID string
	AgentID     string
	WorkspaceID string
	Service     string
	Operation   string
}

// RecordSecretRequestBlocked 记下一次「Agent 直接索取凭据」（REQ-CAP-001 AC2）。
//
// 这类请求在入口就被拒，不进决策链路，因此没有决策记录可以挂。但它是
// 安全事件，账本上必须留下痕迹 —— 写不进去就让整个请求失败（ADR-004），
// 而不是拒绝完就算了。
func (p *Pipeline) RecordSecretRequestBlocked(
	ctx context.Context, blocked BlockedRequest,
) error {
	_, err := p.audit.Record(ctx, audit.Event{
		OperationID:   blocked.OperationID,
		Type:          audit.EventSecretRequestBlocked,
		AgentID:       blocked.AgentID,
		WorkspaceID:   blocked.WorkspaceID,
		Service:       blocked.Service,
		Operation:     blocked.Operation,
		Resource:      "{}",
		ResolvedScope: "{}",
		Verdict:       audit.Verdict(decision.VerdictDeny),
		Outcome:       audit.OutcomeBlocked,
		Metadata:      `{"blocker":"` + string(decision.BlockerCapabilityNotOffered) + `"}`,
	})
	return err
}

// RecordStrongAuthLocked 记下一次「强认证连续失败后进入锁定」（REQ-APPROVAL-005 AC2）。
//
// 与 RecordSecretRequestBlocked 同理：这件事发生在决策链路之外，没有请求也没有
// 决策可以挂，但它是安全事件 —— 有人在这台设备上连着试了三次主密码。
// 写不进账本就让这次尝试失败（ADR-004），而不是锁完就算了。
//
// **不记任何与主密码有关的东西**：这个结构里没有能装下它的字段，
// Metadata 只写锁定时长。
func (p *Pipeline) RecordStrongAuthLocked(
	ctx context.Context, operationID string, lockoutSeconds int,
) error {
	_, err := p.audit.Record(ctx, audit.Event{
		OperationID:   operationID,
		Type:          audit.EventStrongAuthLocked,
		Service:       "opendelo",
		Operation:     "vault.unlock",
		Resource:      "{}",
		ResolvedScope: "{}",
		Outcome:       audit.OutcomeBlocked,
		Metadata:      `{"lockout_seconds":` + strconv.Itoa(lockoutSeconds) + `}`,
	})
	return err
}

// ClearTrustMemory 清除一条学过的授权，并把这件事记进账本（REQ-UI-007 AC3）。
//
// **先记账本再删行**：删掉之后那条记忆的 service、身份与项目就再也读不回来了，
// 账本上只会剩一个主键。写不进去就不删（ADR-004）—— 一次不在账本上的
// 破坏性操作，比一次没做成的破坏性操作糟得多。
func (p *Pipeline) ClearTrustMemory(ctx context.Context, id, operationID string) error {
	cleared, err := p.memories.ByID(ctx, id)
	if err != nil {
		return err
	}

	if _, err = p.audit.Record(ctx, audit.Event{
		OperationID:   operationID,
		Type:          audit.EventTrustCleared,
		AgentID:       cleared.AgentID,
		WorkspaceID:   cleared.WorkspaceID,
		IdentityID:    cleared.IdentityID,
		Service:       cleared.Service,
		Operation:     "trust.clear",
		Resource:      cleared.ResourceScope,
		ResolvedScope: "{}",
		RiskLevel:     audit.RiskLevel(cleared.RiskCeiling),
		Outcome:       audit.OutcomeSucceeded,
		Metadata:      `{"approval_behavior":"` + string(cleared.Behavior) + `"}`,
	}); err != nil {
		return err
	}
	return p.memories.Delete(ctx, id)
}

// decodeScope 读回决策当时收敛好的范围。
//
// 读不懂就返回错误而不是签发一条范围不明的 Lease：一条说不清覆盖什么的授权
// 事后也校验不了请求在不在其中（Fail Closed）。
func decodeScope(resolved string) (scope.Scope, error) {
	var granted scope.Scope
	if err := json.Unmarshal([]byte(resolved), &granted); err != nil {
		return scope.Scope{}, apperr.Wrap(apperr.CodeInternal, err).
			WithDetail("决策记录里的范围无法解析")
	}
	if err := granted.Validate(); err != nil {
		return scope.Scope{}, err
	}
	return granted, nil
}

// RecordAgentTrusted 记下一次「用户把这个 Agent 升为已知」（REQ-AGENT-002 AC3）。
//
// 与 RecordStrongAuthLocked 同理：这件事发生在决策链路之外，没有请求也没有决策
// 可以挂，但它改变的是**风险引擎的输入** —— 从这一刻起，这个 Agent 的写操作
// 有了被自动放行的可能。写不进账本就不升级（ADR-004）：一次不在账本上的
// 信任提升，比一次没做成的糟得多。
func (p *Pipeline) RecordAgentTrusted(ctx context.Context, agentID, operationID string) error {
	_, err := p.audit.Record(ctx, audit.Event{
		OperationID:   operationID,
		Type:          audit.EventAgentTrusted,
		AgentID:       agentID,
		Service:       "opendelo",
		Operation:     "agent.trust",
		Resource:      "{}",
		ResolvedScope: "{}",
		Outcome:       audit.OutcomeSucceeded,
		Metadata:      `{"trust_level":"known"}`,
	})
	return err
}
