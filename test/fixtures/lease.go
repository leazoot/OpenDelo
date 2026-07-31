package fixtures

import (
	"time"

	"github.com/Runcoor/opendelo/internal/core/approval"
	"github.com/Runcoor/opendelo/internal/core/lease"
)

// ApprovalOption 调整审批项夹具。
type ApprovalOption func(*approval.Approval)

// WithApprovalID 换掉审批项主键。
func WithApprovalID(id string) ApprovalOption {
	return func(item *approval.Approval) { item.ID = id }
}

// WithApprovalDecisionID 换掉审批项指向的决策。
func WithApprovalDecisionID(decisionID string) ApprovalOption {
	return func(item *approval.Approval) { item.DecisionID = decisionID }
}

// WithApprovalExpiresAt 换掉超时时刻。
func WithApprovalExpiresAt(expiresAt time.Time) ApprovalOption {
	return func(item *approval.Approval) { item.ExpiresAt = expiresAt }
}

// WithApprovalSettled 把审批项构造成已决状态。
func WithApprovalSettled(action approval.Action, status approval.Status, at time.Time) ApprovalOption {
	return func(item *approval.Approval) {
		item.Action = action
		item.Status = status
		item.DecidedAt = at
	}
}

// WithApprovalStatus 只换状态，不动结果，用于验证「同进同出」约束。
func WithApprovalStatus(status approval.Status) ApprovalOption {
	return func(item *approval.Approval) { item.Status = status }
}

// Approval 构造一个合法的待决审批项。默认超时时刻为 Instant 之后 5 分钟。
func Approval(options ...ApprovalOption) approval.Approval {
	item := approval.Approval{
		ID:         DefaultApprovalID,
		DecisionID: DefaultDecisionID,
		Status:     approval.StatusPending,
		ExpiresAt:  Instant.Add(5 * time.Minute),
		CreatedAt:  Instant,
		UpdatedAt:  Instant,
	}
	for _, apply := range options {
		apply(&item)
	}
	return item
}

// LeaseOption 调整 Lease 夹具。
type LeaseOption func(*lease.Lease)

// WithLeaseID 换掉 Lease 主键。
func WithLeaseID(id string) LeaseOption {
	return func(issued *lease.Lease) { issued.ID = id }
}

// WithLeaseApprovalID 换掉签发该 Lease 的审批项。空串表示自动放行。
func WithLeaseApprovalID(approvalID string) LeaseOption {
	return func(issued *lease.Lease) { issued.ApprovalID = approvalID }
}

// WithLeaseRequestLimit 换掉次数上限。lease.Unlimited 表示不限次数。
func WithLeaseRequestLimit(limit int) LeaseOption {
	return func(issued *lease.Lease) { issued.RequestLimit = limit }
}

// WithLeaseUsedRequests 换掉已用次数，用于验证上限约束。
func WithLeaseUsedRequests(used int) LeaseOption {
	return func(issued *lease.Lease) { issued.UsedRequests = used }
}

// WithLeaseExpiresAt 换掉到期时刻。
func WithLeaseExpiresAt(expiresAt time.Time) LeaseOption {
	return func(issued *lease.Lease) { issued.ExpiresAt = expiresAt }
}

// WithLeaseStatus 换掉状态。
func WithLeaseStatus(status lease.Status) LeaseOption {
	return func(issued *lease.Lease) { issued.Status = status }
}

// WithLeaseSessionBound 标记这条 Lease 绑定 Agent Session。
func WithLeaseSessionBound(bound bool) LeaseOption {
	return func(issued *lease.Lease) { issued.IsSessionBound = bound }
}

// WithLeaseScope 换掉资源范围文本，用于验证 JSON 约束。
func WithLeaseScope(scope string) LeaseOption {
	return func(issued *lease.Lease) { issued.ResourceScope = scope }
}

// Lease 构造一条合法的生效中 Lease。
//
// 默认时长 15 分钟（REQ-LEASE-001 AC2），次数上限 3，尚未使用，
// 由默认的审批项签发。
func Lease(options ...LeaseOption) lease.Lease {
	issued := lease.Lease{
		ID:            DefaultLeaseID,
		AgentID:       DefaultAgentID,
		IdentityID:    DefaultIdentityID,
		Service:       DefaultServiceLabel,
		ResourceScope: `{"repo":"Runcoor/opendelo"}`,
		Capabilities:  `["pull_request.create"]`,
		ExpiresAt:     Instant.Add(15 * time.Minute),
		RequestLimit:  3,
		UsedRequests:  0,
		Status:        lease.StatusActive,
		ApprovalID:    DefaultApprovalID,
		CreatedAt:     Instant,
		UpdatedAt:     Instant,
	}
	for _, apply := range options {
		apply(&issued)
	}
	return issued
}
