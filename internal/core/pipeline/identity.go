package pipeline

import (
	"context"
	"time"

	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/trust"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/audit"
)

/*
 * 身份断开的级联（REQ-IDENT-001 AC2、REQ-IDENT-004）。
 *
 * 顺序不能调换：**先收回 Lease，再失效记忆，最后才改身份状态**。
 * 反过来的话，中间存在一个窗口 —— 身份已经被标记为不可自动使用，
 * 而依赖它的 Lease 还活着，此时正好进来的请求会被放行。
 * 凭据来源断开时（`internal/credential/registry`）的顺序出于同一条理由。
 */

// cascadeLimit 是一次级联最多处理多少条。
//
// 无界查询在仓储层就被拒。上限取得足够大：
// 一个身份名下同时生效上千条 Lease 本身就说明有别的问题。
const cascadeLimit = 1000

// IdentityRevocation 是一次身份断开的结果。
type IdentityRevocation struct {
	Identity matcher.Identity
	// RevokedLeases 与 InvalidatedMemories 是这次真正被处理掉的那些。
	RevokedLeases       []lease.Lease
	InvalidatedMemories []trust.Memory
}

// IdentityRepository 是 pipeline 读写身份所需的最小接口。
type IdentityRepository interface {
	IdentityByID(ctx context.Context, id string) (matcher.Identity, error)
	Identities(ctx context.Context, limit int) ([]matcher.Identity, error)
	SetIdentityStatus(
		ctx context.Context, id string, status matcher.IdentityStatus, at time.Time,
	) (matcher.Identity, error)
}

// DisconnectIdentity 断开一个身份：收回它名下的全部 Lease、失效它名下的
// 全部记忆，并把它标记为需要检查（REQ-IDENT-001 AC2）。
//
// **不删除行**：身份被审计引用着（外键为 RESTRICT），删掉它会让账本里
// 「这次用的是谁」答不出来。断开之后自动授权全部停止，下一次请求进入审批 ——
// 这正是 needs_review 这个状态的含义（REQ-IDENT-004）。
func (p *Pipeline) DisconnectIdentity(
	ctx context.Context, identityID, operationID string,
) (IdentityRevocation, error) {
	if identityID == "" || operationID == "" {
		return IdentityRevocation{}, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("断开身份需要身份主键与 operation_id")
	}

	identity, err := p.identities.IdentityByID(ctx, identityID)
	if err != nil {
		return IdentityRevocation{}, err
	}

	revoked, err := p.revokeLeasesOf(ctx, identity, operationID)
	if err != nil {
		return IdentityRevocation{}, err
	}

	invalidated, err := p.memories.InvalidateForIdentity(
		ctx, identityID, trust.ReasonIdentityScopeChanged, cascadeLimit)
	if err != nil {
		return IdentityRevocation{}, err
	}

	updated, err := p.identities.SetIdentityStatus(
		ctx, identityID, matcher.StatusNeedsReview, p.clock.Now())
	if err != nil {
		return IdentityRevocation{}, err
	}

	return IdentityRevocation{
		Identity:            updated,
		RevokedLeases:       revoked,
		InvalidatedMemories: invalidated,
	}, nil
}

// revokeLeasesOf 逐条收回一个身份名下仍生效的 Lease，每条都记一次审计。
//
// 逐条走 Close 的条件更新而不是一条批量 UPDATE：后者在并发下会把刚被别处
// 收回的 Lease 再写一次，让账本上出现两条互相矛盾的记录。
func (p *Pipeline) revokeLeasesOf(
	ctx context.Context, identity matcher.Identity, operationID string,
) ([]lease.Lease, error) {
	active, err := p.leases.IssuedTo(ctx, identity.ID, cascadeLimit)
	if err != nil {
		return nil, err
	}

	revoked := make([]lease.Lease, 0, len(active))
	for _, granted := range active {
		if writeErr := p.recordLeaseRevoked(ctx, identity, granted, operationID); writeErr != nil {
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

func (p *Pipeline) recordLeaseRevoked(
	ctx context.Context, identity matcher.Identity, granted lease.Lease, operationID string,
) error {
	_, err := p.audit.Record(ctx, audit.Event{
		OperationID:   operationID,
		Type:          audit.EventLeaseRevoked,
		AgentID:       granted.AgentID,
		IdentityID:    identity.ID,
		Service:       granted.Service,
		Resource:      "{}",
		ResolvedScope: granted.ResourceScope,
		Verdict:       audit.Verdict(decision.VerdictDeny),
		LeaseID:       granted.ID,
		LeaseStatus:   audit.LeaseStatus(lease.StatusRevoked),
		Outcome:       audit.OutcomeBlocked,
		Metadata:      `{"reason":"identity_disconnected"}`,
	})
	return err
}

// Identities 列出全部身份，服务 Identities 页面。
func (p *Pipeline) Identities(ctx context.Context, limit int) ([]matcher.Identity, error) {
	return p.identities.Identities(ctx, limit)
}

// Identity 读取一个身份，不存在时返回 apperr.CodeNotFound。
func (p *Pipeline) Identity(ctx context.Context, id string) (matcher.Identity, error) {
	return p.identities.IdentityByID(ctx, id)
}
