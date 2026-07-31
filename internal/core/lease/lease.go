package lease

import (
	"context"
	"time"
)

// Status 是 Lease 的生命周期状态（REQ-LEASE-001/002）。
type Status string

const (
	// StatusActive 生效中。
	StatusActive Status = "active"
	// StatusExpired 到期失效。到期不续签。
	StatusExpired Status = "expired"
	// StatusExhausted 次数用尽（AC3）。
	StatusExhausted Status = "exhausted"
	// StatusRevoked 被用户收回。
	StatusRevoked Status = "revoked"
)

// Unlimited 用作 Lease.RequestLimit 的「不限次数」取值。
//
// 不限次数不等于不限时间：ExpiresAt 始终存在，两者是独立的两把锁。
const Unlimited = 0

// Lease 是一次临时授权（PRD §9.7）。
//
// ResourceScope 与 Capabilities 在签发后不可修改（REQ-LEASE-004）：
// 这个类型没有改它们的方法，仓储也没有对应的更新入口。需要更大范围时
// 只能重新审批并签发新的 Lease。
type Lease struct {
	ID         string
	AgentID    string
	IdentityID string
	Service    string
	// ResourceScope 与 Capabilities 是签发时收敛好的范围与能力，JSON。
	ResourceScope string
	Capabilities  string
	// ExpiresAt 恒非空。「永久授权」在这个产品里不可表达。
	ExpiresAt time.Time
	// RequestLimit 为 Unlimited 时表示不限次数。
	RequestLimit int
	UsedRequests int
	Status       Status
	// ApprovalID 为空表示这条 Lease 由自动放行签发，没有对应的审批项。
	ApprovalID string
	// IsSessionBound 为真时 Agent Session 结束即失效（REQ-APPROVAL-002 AC3）。
	IsSessionBound bool
	// SourceMemoryID 是 PRD §9.7 的 source_memory_id：由哪条记忆自动签发。
	// 为空表示这次是人工确认或首次放行。记忆失效时据此找到要一并收回的 Lease。
	SourceMemoryID string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Repository 读写 Lease。
//
// 没有修改 Scope 与 Capabilities 的方法（REQ-LEASE-004 AC1），
// 也没有延长有效期的方法：Shorten 只接受更早的到期时刻。
type Repository interface {
	// IssueLease 签发一条 Lease。同一个审批项签发第二条会得到冲突错误。
	IssueLease(ctx context.Context, issued Lease) (Lease, error)
	LeaseByID(ctx context.Context, id string) (Lease, error)
	// LeaseByApprovalID 反查某个审批项签发出来的 Lease，不存在时返回
	// apperr.CodeNotFound。它支撑审批决策端点的幂等（REQ-API-004）：
	// 重复提交同一个决策要拿回首次签发的那一条，而不是再签一条。
	LeaseByApprovalID(ctx context.Context, approvalID string) (Lease, error)
	// LeasesByStatus 按到期先后列出，服务 Gate 缝内侧的 Active leases。limit 必须为正。
	LeasesByStatus(ctx context.Context, status Status, limit int) ([]Lease, error)
	// ActiveLeasesByIdentity 列出某个身份名下仍生效的 Lease，
	// 支撑身份断开时的级联撤销（REQ-IDENT-001 AC2）。limit 必须为正。
	ActiveLeasesByIdentity(ctx context.Context, identityID string, limit int) ([]Lease, error)
	// ActiveSessionBoundLeasesByAgent 列出某个 Agent 名下仍生效且绑定会话的 Lease，
	// 支撑会话结束时的级联撤销（REQ-CLI-002 AC3）。limit 必须为正。
	//
	// 只列绑定会话的那些：「仅这一次」与「到项目结束」的授权不随进程退出而消失，
	// 在这里一并收回等于悄悄缩小用户已经允许的范围。
	ActiveSessionBoundLeasesByAgent(
		ctx context.Context, agentID string, limit int,
	) ([]Lease, error)
	// ActiveLeasesDueBefore 列出到点仍处于生效中的 Lease，供到期清扫使用。
	ActiveLeasesDueBefore(ctx context.Context, deadline time.Time, limit int) ([]Lease, error)
	// Consume 记一次使用。只有仍生效、未到期且未达次数上限时才成功，
	// 三个条件在同一条语句里判定，因此并发调用不会超发。
	Consume(ctx context.Context, id string, now time.Time) (Lease, error)
	// Close 把生效中的 Lease 置为 expired / exhausted / revoked。
	Close(ctx context.Context, id string, status Status, at time.Time) (Lease, error)
	// Shorten 提前到期时刻。新的时刻必须早于原有时刻，否则不生效 ——
	// 这个方法不能被用来延长授权。
	Shorten(ctx context.Context, id string, expiresAt, at time.Time) (Lease, error)
}
