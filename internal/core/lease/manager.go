package lease

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Runcoor/opendelo/internal/core/scope"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
)

/*
 * Lease Manager：签发、计量、到期清扫、撤销、缩短（REQ-LEASE-001~004）。
 *
 * 签发出来的范围完全取自收敛好的 Scope —— 有效期、次数上限、资源与能力都不在
 * 这一层重新决定。在签发时再放大一次，等于绕开了 Scope 收敛那一步的全部约束。
 *
 * 这里没有任何延长有效期、扩大范围或修改 Capabilities 的方法（REQ-LEASE-004 AC1）。
 */

// Options 是 Manager 的依赖，全部必填。
type Options struct {
	Leases Repository
	Clock  clock.Clock
	IDs    *ulid.Generator
}

// Manager 管理 Lease 的生命周期。
type Manager struct {
	leases Repository
	clock  clock.Clock
	ids    *ulid.Generator
}

// NewManager 校验依赖并构造 Manager。
func NewManager(options Options) (*Manager, error) {
	switch {
	case options.Leases == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("Lease Manager 缺少仓储")
	case options.Clock == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("Lease Manager 缺少时钟")
	case options.IDs == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("Lease Manager 缺少 ID 生成器")
	}
	return &Manager{leases: options.Leases, clock: options.Clock, ids: options.IDs}, nil
}

// IssueRequest 是一次签发的输入。
//
// 没有「有效期」与「次数」字段：两者是 Scope 的两个维度，取自 Granted。
type IssueRequest struct {
	// Granted 是决策通过后收敛好的十个维度。
	Granted scope.Scope
	// ApprovalID 为空表示这条 Lease 由自动放行签发。
	ApprovalID string
	// SourceMemoryID 是命中的授权记忆，未命中时为空。
	SourceMemoryID string
	// IsSessionBound 为真时 Agent Session 结束即失效（REQ-APPROVAL-002 AC3）。
	IsSessionBound bool
}

// Issue 签发一条 Lease。
//
// Scope 十个维度不全时拒绝：一条范围说不清的授权签发出来也没法校验请求是否在其中
// （Fail Closed）。到期时刻已经过去时同样拒绝 —— 那样的 Lease 一签发就是死的，
// 让它进库只会在账本上留下一条永远用不上的授权。
func (m *Manager) Issue(ctx context.Context, request IssueRequest) (Lease, error) {
	if err := request.Granted.Validate(); err != nil {
		return Lease{}, err
	}

	now := m.clock.Now()
	if !request.Granted.ExpiresAt.After(now) {
		return Lease{}, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("签发的 Lease 到期时刻不晚于当前时刻")
	}

	id, err := m.ids.NewID()
	if err != nil {
		return Lease{}, err
	}
	resourceScope, capabilities, err := encodeGrant(request.Granted)
	if err != nil {
		return Lease{}, err
	}

	return m.leases.IssueLease(ctx, Lease{
		ID:             id,
		AgentID:        request.Granted.AgentID,
		IdentityID:     request.Granted.IdentityID,
		Service:        request.Granted.Service,
		ResourceScope:  resourceScope,
		Capabilities:   capabilities,
		ExpiresAt:      request.Granted.ExpiresAt,
		RequestLimit:   request.Granted.RequestLimit,
		UsedRequests:   0,
		Status:         StatusActive,
		ApprovalID:     request.ApprovalID,
		IsSessionBound: request.IsSessionBound,
		SourceMemoryID: request.SourceMemoryID,
		CreatedAt:      now,
		UpdatedAt:      now,
	})
}

// Use 校验一次请求是否落在 Lease 之内，并记一次使用。
//
// 校验每次都重做，不缓存结论。校验通过后
// 计量与状态判定在仓储的同一条语句里完成，因此并发使用不会超发。
//
// 请求超出 Lease 的范围时返回 credential_not_authorized 而不是悄悄放行或
// 悄悄扩大：REQ-LEASE-004 AC2 要求这样的请求重新进入决策，而不是复用这条 Lease。
func (m *Manager) Use(ctx context.Context, id string, requested scope.Scope) (Lease, error) {
	if err := requested.Validate(); err != nil {
		return Lease{}, err
	}

	current, err := m.leases.LeaseByID(ctx, id)
	if err != nil {
		return Lease{}, err
	}
	granted, err := decodeGrant(current.ResourceScope)
	if err != nil {
		return Lease{}, err
	}
	// 时间窗口不参与比较：这条 Lease 到没到期由它自己的 ExpiresAt 说了算，
	// 仓储在计量那一条语句里一并判定。其余九个维度（含次数上限 ——
	// 要的次数比签发时多就是扩大）逐一比较。
	if !granted.CoversIgnoringWindow(requested) {
		return Lease{}, apperr.New(apperr.CodeCredentialNotAuthorized).
			WithDetail("请求超出 Lease " + id + " 的范围")
	}

	used, err := m.leases.Consume(ctx, id, m.clock.Now())
	if err != nil {
		return Lease{}, err
	}

	// REQ-LEASE-001 AC3：用满即刻变为 exhausted，不等下一次请求来发现。
	if used.RequestLimit != Unlimited && used.UsedRequests >= used.RequestLimit {
		return m.leases.Close(ctx, id, StatusExhausted, m.clock.Now())
	}
	return used, nil
}

// Revoke 收回一条 Lease（REQ-LEASE-002）。
func (m *Manager) Revoke(ctx context.Context, id string) (Lease, error) {
	return m.leases.Close(ctx, id, StatusRevoked, m.clock.Now())
}

// Shorten 提前 Lease 的到期时刻（REQ-LEASE-002 AC3）。
//
// 只能缩短。新的时刻不早于原有时刻时返回 invalid_request（对应 400），
// 相等同样被拒 —— 把边界交给巧合不是安全边界该有的样子。
func (m *Manager) Shorten(ctx context.Context, id string, expiresAt time.Time) (Lease, error) {
	if expiresAt.IsZero() {
		return Lease{}, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("新的到期时刻为空")
	}
	return m.leases.Shorten(ctx, id, expiresAt, m.clock.Now())
}

// Sweep 把已过期但仍标记为生效的 Lease 逐条关闭（REQ-LEASE-002：到期自动失效，不续签）。
//
// 返回被关闭的 Lease，供调用方逐条记 lease.expired 审计事件。
// 关闭途中出错即返回：已关掉的那些保持关闭状态，下一轮清扫不会重复处理它们。
// 非正的条数上限由仓储拒绝，这一层不再重复判断一次。
func (m *Manager) Sweep(ctx context.Context, limit int) ([]Lease, error) {
	now := m.clock.Now()
	due, err := m.leases.ActiveLeasesDueBefore(ctx, now, limit)
	if err != nil {
		return nil, err
	}

	closed := make([]Lease, 0, len(due))
	for _, expiring := range due {
		result, err := m.leases.Close(ctx, expiring.ID, StatusExpired, now)
		if err != nil {
			return closed, err
		}
		closed = append(closed, result)
	}
	return closed, nil
}

// ByID 读取一条 Lease，不存在时返回 apperr.CodeNotFound。
func (m *Manager) ByID(ctx context.Context, id string) (Lease, error) {
	return m.leases.LeaseByID(ctx, id)
}

// IssuedFor 返回某个审批项已经签发出来的 Lease，没有则返回 apperr.CodeNotFound。
//
// 重复提交同一个审批决策时用它拿回首次的结果（REQ-API-004），
// 而不是再签一条 —— 一次点头只换一条授权。
func (m *Manager) IssuedFor(ctx context.Context, approvalID string) (Lease, error) {
	return m.leases.LeaseByApprovalID(ctx, approvalID)
}

// IssuedTo 列出某个身份名下仍生效的 Lease，服务身份断开时的级联撤销。
func (m *Manager) IssuedTo(ctx context.Context, identityID string, limit int) ([]Lease, error) {
	return m.leases.ActiveLeasesByIdentity(ctx, identityID, limit)
}

// BoundToSessionOf 列出某个 Agent 名下仍生效且绑定会话的 Lease，
// 服务会话结束时的级联撤销（REQ-CLI-002 AC3）。
func (m *Manager) BoundToSessionOf(
	ctx context.Context, agentID string, limit int,
) ([]Lease, error) {
	return m.leases.ActiveSessionBoundLeasesByAgent(ctx, agentID, limit)
}

// Active 列出生效中的 Lease，按到期先后（REQ-LEASE-003 的数据来源）。
//
// 无界列表查询由仓储拒绝。
func (m *Manager) Active(ctx context.Context, limit int) ([]Lease, error) {
	return m.leases.LeasesByStatus(ctx, StatusActive, limit)
}

// encodeGrant 把 Scope 编码成入库的两列。
//
// Capabilities 恒为单个操作：最小权限意味着一次授权只覆盖 Scope 里的那一个操作，
// 需要第二个操作就重新走一遍决策。
func encodeGrant(granted scope.Scope) (resourceScope, capabilities string, err error) {
	encodedScope, err := json.Marshal(granted)
	if err != nil {
		return "", "", apperr.Wrap(apperr.CodeInternal, err).WithDetail("Scope 无法编码")
	}
	encodedCapabilities, err := json.Marshal([]string{granted.Operation})
	if err != nil {
		return "", "", apperr.Wrap(apperr.CodeInternal, err).WithDetail("能力清单无法编码")
	}
	return string(encodedScope), string(encodedCapabilities), nil
}

// decodeGrant 读回签发时的范围。
//
// 解析失败或十个维度不全时返回错误而不是放行：读不懂自己签发的范围时，
// 唯一安全的结论是不知道这次请求在不在其中（Fail Closed）。
func decodeGrant(resourceScope string) (scope.Scope, error) {
	var granted scope.Scope
	if err := json.Unmarshal([]byte(resourceScope), &granted); err != nil {
		return scope.Scope{}, apperr.Wrap(apperr.CodeInternal, err).
			WithDetail("Lease 记录的范围无法解析")
	}
	if err := granted.Validate(); err != nil {
		return scope.Scope{}, err
	}
	return granted, nil
}
