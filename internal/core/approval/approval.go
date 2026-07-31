package approval

import (
	"context"
	"time"
)

// Action 是用户在 Access Folio 上给出的操作（REQ-APPROVAL-002）。
//
// 五种，没有第六种。高风险审批不提供 ActionAutoAllowInProject，
// 这条限制在决策与界面两侧执行，存储层只负责如实记下用户选了什么。
type Action string

const (
	// ActionDeny 拒绝这次请求。
	ActionDeny Action = "deny"
	// ActionAllowOnce 只允许一次，签发 request_limit = 1 的 Lease（AC2）。
	ActionAllowOnce Action = "allow_once"
	// ActionAllowUntilTaskEnd 允许到任务结束，签发绑定 Agent Session 的 Lease（AC3）。
	ActionAllowUntilTaskEnd Action = "allow_until_task_end"
	// ActionAutoAllowInProject 今后在当前项目自动允许，生成一条 Trust Memory（AC4）。
	ActionAutoAllowInProject Action = "auto_allow_in_project"
	// ActionAlwaysAsk 始终要求确认，生成 approval_behavior = always_ask 的记忆（AC5）。
	ActionAlwaysAsk Action = "always_ask"
)

// Status 是审批项的生命周期状态。
type Status string

const (
	// StatusPending 等待用户处理。
	StatusPending Status = "pending"
	// StatusApproved 用户已放行。
	StatusApproved Status = "approved"
	// StatusRejected 用户已拒绝。
	StatusRejected Status = "rejected"
	// StatusExpired 超时未处理。超时的审批不产生 Lease。
	StatusExpired Status = "expired"
	// StatusCancelled 由 Agent 撤回请求导致。
	StatusCancelled Status = "cancelled"
)

// Approval 是一次等待人工确认的审批项（PRD §13）。
//
// Action 与 DecidedAt 只在离开 StatusPending 时才有值，且必须同时有值 ——
// 只写了一半的审批项会让「这次是谁放行的」答不出来。
type Approval struct {
	ID         string
	DecisionID string
	Action     Action
	Status     Status
	// ExpiresAt 是审批超时时刻，恒非空。一个永远等下去的审批项
	// 等于一条永久授权的入口。
	ExpiresAt time.Time
	DecidedAt time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Repository 读写审批项。
//
// 没有删除方法：审批是账本里「谁放行的」那一环。
type Repository interface {
	CreateApproval(ctx context.Context, item Approval) (Approval, error)
	ApprovalByID(ctx context.Context, id string) (Approval, error)
	// ApprovalByDecisionID 支撑幂等：同一个决策重复请求审批时返回首次创建的那一条。
	ApprovalByDecisionID(ctx context.Context, decisionID string) (Approval, error)
	// ApprovalsByStatus 按超时先后列出，服务 Gate 的待审批列表。limit 必须为正。
	ApprovalsByStatus(ctx context.Context, status Status, limit int) ([]Approval, error)
	// PendingApprovalsDueBefore 列出到点仍未处理的审批项，供超时清扫使用。
	PendingApprovalsDueBefore(ctx context.Context, deadline time.Time, limit int) ([]Approval, error)
	// Settle 只在审批项仍为 pending 时写入结果。并发的两次决策只有一次成功，
	// 另一次得到冲突错误 —— 同一个审批项不能被放行两次。
	Settle(ctx context.Context, id string, action Action, status Status, at time.Time) (Approval, error)
}
