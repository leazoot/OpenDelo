package pipeline

import (
	"context"

	"github.com/Runcoor/opendelo/internal/platform/audit"
)

/*
 * 等不到人的审批（REQ-CAP-003）。
 *
 * 超时的结论是**拒绝**，状态是 `expired`：前者说的是最终没有放行，后者说的是
 * 为什么 —— 没人来处理。两者分开记，是因为「你拒绝了」与「你没看见」
 * 在账本上不该长成同一件事。
 *
 * 这条路上**永远不签发 Lease**（AC2）。它只关闭审批项、推进请求、记一笔账。
 */

// ExpireApprovals 关闭已经超时的审批项，逐条记账，返回关闭的条数。
//
// 记账失败即中止：账本写不进去时不能继续关闭下一条，否则会出现
// 「审批项已经关了、账本上却没有」的记录（ADR-004 的同一条理由）。
// 已经关掉并记好账的那些不回滚 —— 它们各自都是完整的。
func (p *Pipeline) ExpireApprovals(ctx context.Context, limit int) (int, error) {
	expired, err := p.approvals.Expire(ctx, limit)
	if err != nil {
		return 0, err
	}

	closed := 0
	for _, item := range expired {
		record, decisionErr := p.decisions.DecisionByID(ctx, item.DecisionID)
		if decisionErr != nil {
			return closed, decisionErr
		}
		request, requestErr := p.requests.RequestByID(ctx, record.CapabilityRequestID)
		if requestErr != nil {
			return closed, requestErr
		}
		granted, scopeErr := decodeScope(record.ResolvedScope)
		if scopeErr != nil {
			return closed, scopeErr
		}

		if writeErr := p.recordSettlement(
			ctx, request, record, granted, item.ID,
			audit.EventApprovalExpired, audit.OutcomeBlocked,
		); writeErr != nil {
			return closed, writeErr
		}
		if _, advanceErr := p.advance(ctx, request, StatusRejected); advanceErr != nil {
			return closed, advanceErr
		}
		closed++
	}
	return closed, nil
}
