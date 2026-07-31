package pipeline

import (
	"context"

	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/audit"
)

/*
 * 会话结束的级联（REQ-CLI-002 AC3）。
 *
 * 顺序与身份断开**相反**：先失效会话，再收回 Lease。
 *
 * 理由是两者的把关点不同。身份断开时，Lease 是绕过身份状态的那条路 ——
 * 先改状态就会留下一个「身份已停用而授权还活着」的窗口。会话断开时，
 * Session Key 才是第一道门：两个 Agent 面都先认会话再谈 Lease，所以会话一断，
 * 名下的 Lease 立刻就没有出口。反过来先收 Lease 的话，中间那一瞬会话仍然有效，
 * 这时进来的请求会照常走完决策链路并**签出一条新的会话绑定 Lease** ——
 * 而那一条不在我们刚刚列出的那批里，会一直活到过期。
 */

// AgentSessions 是结束一个 Agent 会话所需的最小接口。
//
// 只有 Disconnect 一个方法：这里不需要读 Agent，也不该有能力改它的信任等级。
type AgentSessions interface {
	Disconnect(ctx context.Context, agentID string) (agentauth.Agent, error)
}

// SessionRevocation 是一次会话结束的结果。
type SessionRevocation struct {
	Agent agentauth.Agent
	// RevokedLeases 是这次真正被收回的那些，只含绑定会话的 Lease。
	RevokedLeases []lease.Lease
}

// DisconnectAgent 结束一个 Agent 会话：Session Key 立即失效，
// 名下「允许到任务结束」的 Lease 一并收回（REQ-CLI-002 AC3）。
//
// 重复调用是安全的：已断开的会话再断一次仍然返回该 Agent，且没有可收的 Lease。
// `opendelo run` 在子进程退出后无条件调用它，而那时会话可能已经因为过期而不可用。
func (p *Pipeline) DisconnectAgent(
	ctx context.Context, agentID, operationID string,
) (SessionRevocation, error) {
	if agentID == "" || operationID == "" {
		return SessionRevocation{}, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("结束会话需要 Agent 主键与 operation_id")
	}

	disconnected, err := p.agents.Disconnect(ctx, agentID)
	if err != nil {
		return SessionRevocation{}, err
	}

	revoked, err := p.revokeSessionLeasesOf(ctx, agentID, operationID)
	if err != nil {
		return SessionRevocation{}, err
	}
	return SessionRevocation{Agent: disconnected, RevokedLeases: revoked}, nil
}

// revokeSessionLeasesOf 逐条收回一个 Agent 名下绑定会话的 Lease，每条都先记审计。
//
// 与身份断开同一条理由：逐条走 Close 的条件更新而不是一条批量 UPDATE，
// 否则并发下会把刚被别处收回的 Lease 再写一次。
func (p *Pipeline) revokeSessionLeasesOf(
	ctx context.Context, agentID, operationID string,
) ([]lease.Lease, error) {
	bound, err := p.leases.BoundToSessionOf(ctx, agentID, cascadeLimit)
	if err != nil {
		return nil, err
	}

	revoked := make([]lease.Lease, 0, len(bound))
	for _, granted := range bound {
		if writeErr := p.recordSessionLeaseRevoked(ctx, granted, operationID); writeErr != nil {
			return revoked, writeErr
		}
		closed, closeErr := p.leases.Revoke(ctx, granted.ID)
		if closeErr != nil {
			return revoked, closeErr
		}
		revoked = append(revoked, closed)
	}
	return revoked, nil
}

func (p *Pipeline) recordSessionLeaseRevoked(
	ctx context.Context, granted lease.Lease, operationID string,
) error {
	_, err := p.audit.Record(ctx, audit.Event{
		OperationID:   operationID,
		Type:          audit.EventLeaseRevoked,
		AgentID:       granted.AgentID,
		IdentityID:    granted.IdentityID,
		Service:       granted.Service,
		Resource:      "{}",
		ResolvedScope: granted.ResourceScope,
		Verdict:       audit.Verdict(decision.VerdictDeny),
		LeaseID:       granted.ID,
		LeaseStatus:   audit.LeaseStatus(lease.StatusRevoked),
		Outcome:       audit.OutcomeBlocked,
		Metadata:      `{"reason":"agent_session_ended"}`,
	})
	return err
}
