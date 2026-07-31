package pipeline

import (
	"context"
	"time"
)

// RequestStatus 是能力请求的状态（REQ-CAP-001 的状态机）。
//
// 这里只是取值集合；哪些转移合法由状态机实现负责，`denied → executing`
// 这类路径必须不存在（AC3）。
type RequestStatus string

const (
	// StatusReceived 是刚收到、尚未解析。
	StatusReceived RequestStatus = "received"
	// StatusResolving 正在解析意图与身份。
	StatusResolving RequestStatus = "resolving"
	// StatusDeciding 正在求值决策分支。
	StatusDeciding RequestStatus = "deciding"
	// StatusAutoAllowed 由决策直接放行。
	StatusAutoAllowed RequestStatus = "auto_allowed"
	// StatusAwaitingApproval 等待人工确认。
	StatusAwaitingApproval RequestStatus = "awaiting_approval"
	// StatusDenied 被决策拒绝，是终态。
	StatusDenied RequestStatus = "denied"
	// StatusApproved 人工确认通过。
	StatusApproved RequestStatus = "approved"
	// StatusRejected 人工确认拒绝，是终态。
	StatusRejected RequestStatus = "rejected"
	// StatusExpired 审批超时，是终态且不产生 Lease。
	StatusExpired RequestStatus = "expired"
	// StatusCancelled 由 Agent 主动取消，是终态。
	StatusCancelled RequestStatus = "cancelled"
	// StatusExecuting 正在经 Adapter 执行。
	StatusExecuting RequestStatus = "executing"
	// StatusSucceeded 执行成功，是终态。
	StatusSucceeded RequestStatus = "succeeded"
	// StatusFailed 执行失败，是终态。
	StatusFailed RequestStatus = "failed"
)

// CapabilityRequest 是 Agent 提交的一次能力请求（PRD §9.5、REQ-CAP-001）。
//
// 请求描述能力而不是凭据：这个结构里没有任何字段能表达「把 Token 给我」。
// Resource 与 DesiredChange 是 JSON 文本，形状随服务与操作变化，
// 由 Adapter 声明的输入 Schema 在 transport 层校验。
type CapabilityRequest struct {
	ID          string
	OperationID string
	AgentID     string
	WorkspaceID string
	Service     string
	Operation   string
	Resource    string
	// DesiredChange 为空表示读操作。「没有变更」与「变更为空」在审批页面上
	// 是两句不同的话，所以空串落库为 NULL。
	DesiredChange string
	Reason        string
	Status        RequestStatus
	// ChangePreview 是执行前查出来的旧值，JSON 数组文本（REQ-APPROVAL-001 AC4）。
	//
	// 空串表示**没有查过**，不是「查过但没有可对照的字段」。审批页面对这两种
	// 情况说的话不一样，因此这里不把它们并成一个。
	ChangePreview string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// CapabilityRequestRepository 读写能力请求。
//
// 没有删除方法：请求是账本的起点，删掉它会让后续的决策、审批、Lease
// 全部失去出处。
type CapabilityRequestRepository interface {
	CreateRequest(ctx context.Context, request CapabilityRequest) (CapabilityRequest, error)
	RequestByID(ctx context.Context, id string) (CapabilityRequest, error)
	// RequestsByStatus 按到达顺序列出某个状态下的请求，服务 Gate 的待审批列表。
	// limit 必须为正。
	RequestsByStatus(ctx context.Context, status RequestStatus, limit int) ([]CapabilityRequest, error)
	// AdvanceRequest 只在当前状态等于 from 时才写入 to，返回更新后的请求。
	// 并发的两次推进只有一次成功，另一次得到冲突错误
	AdvanceRequest(
		ctx context.Context, id string, from, to RequestStatus, at time.Time,
	) (CapabilityRequest, error)
	// SaveChangePreview 记下执行前查出来的旧值，只在请求仍处于 expected 时写入。
	//
	// 带上 expected 而不是无条件覆盖：查勘发生在人做决定之前，两者是并行的 ——
	// 请求已经被决定时这次写入必须落空，否则卷宗上的旧值会在决定之后被换掉。
	// 落空按冲突返回，调用方据此知道自己晚了一步，而不是以为写成功了。
	SaveChangePreview(
		ctx context.Context, id, preview string, expected RequestStatus, at time.Time,
	) error
}
