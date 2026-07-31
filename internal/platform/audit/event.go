package audit

import (
	"context"
	"time"
)

// EventType 是账本条目的类型（REQ-AUDIT-002）。
//
// 封闭枚举：前十个来自 PRD §16.4 的展示清单，其余三个由需求点名
// （REQ-SCOPE-002、REQ-CAP-001 AC2、REQ-AUDIT-005）。新增类型必须同步
// 更新前端过滤器（AC2）。
type EventType string

const (
	// EventAutoAllowed 决策直接放行。
	EventAutoAllowed EventType = "decision.auto_allowed"
	// EventUserAllowed 用户在审批中放行。
	EventUserAllowed EventType = "decision.user_allowed"
	// EventDenied 请求被拒绝。
	EventDenied EventType = "decision.denied"
	// EventLeaseCreated 签发了一条 Lease。
	EventLeaseCreated EventType = "lease.created"
	// EventLeaseExpired Lease 到期失效。
	EventLeaseExpired EventType = "lease.expired"
	// EventLeaseRevoked Lease 被收回。
	EventLeaseRevoked EventType = "lease.revoked"
	// EventAdapterExecuted Adapter 执行了一次出站请求。
	EventAdapterExecuted EventType = "adapter.executed"
	// EventError 需要人工关注的错误。
	EventError EventType = "error"
	// EventIdentityMatched 身份匹配的结果与命中层级。
	EventIdentityMatched EventType = "identity.matched"
	// EventRiskChanged 风险等级相对历史发生了变化。
	EventRiskChanged EventType = "risk.changed"
	// EventScopeInjectionIgnored 请求里带了越权字段，已忽略并记录（REQ-SCOPE-002）。
	EventScopeInjectionIgnored EventType = "security.scope_injection_ignored"
	// EventSecretRequestBlocked Agent 试图直接索取凭据（REQ-CAP-001 AC2）。
	EventSecretRequestBlocked EventType = "security.secret_request_blocked"
	// EventIdentityMismatch 记「出示的身份与注册时对不上」（REQ-AGENT-001 异常状态）。
	EventIdentityMismatch EventType = "agent.identity_mismatch"
	// EventAgentTrusted 记「用户把这个 Agent 升为已知」（REQ-AGENT-002 AC3）。
	EventAgentTrusted EventType = "agent.trusted"
	// EventTrustCleared 一条学过的授权被清除（REQ-UI-007 AC3）。
	EventTrustCleared EventType = "trust.cleared"
	// EventStrongAuthLocked 强认证连续失败后进入锁定（REQ-APPROVAL-005 AC2）。
	EventStrongAuthLocked EventType = "security.strong_auth_locked"
	// EventPruned 保留期清理删除了超期记录（REQ-AUDIT-005 AC1）。
	EventPruned EventType = "audit.pruned"
)

// Verdict 与 RiskLevel 在这里各自定义，而不是引用 core 的同名类型：
// platform 不得依赖任何业务包。
// 账本记的是当时写下的字面结论，本来也不该随 core 的类型演化而改变含义。
type (
	// Verdict 是当时的决策结论。
	Verdict string
	// RiskLevel 是当时算出的风险等级。
	RiskLevel string
	// LeaseStatus 是当时的 Lease 状态。
	LeaseStatus string
	// Outcome 是这次请求最终发生了什么。
	Outcome string
)

const (
	// OutcomeSucceeded 请求被执行且成功。
	OutcomeSucceeded Outcome = "succeeded"
	// OutcomeFailed 请求被执行但失败。
	OutcomeFailed Outcome = "failed"
	// OutcomeBlocked 请求没有被执行 —— 拒绝、超时、Fail Closed 都算这一类。
	OutcomeBlocked Outcome = "blocked"
)

// Event 是一条账本记录（PRD §9.9、REQ-AUDIT-001）。
//
// 记的是元数据：请求者、目标、身份、Scope、决策、审批、执行结果、凭据来源、
// 是否脱敏、Lease 状态、耗时。**不记**完整请求正文、完整响应正文、Secret、
// 文件内容与用户敏感数据（PRD §22.1）—— 这个结构里没有能装下它们的字段。
//
// 大量字段可空是有意的：认不出 Agent、定不了服务身份这些情况本身就要被
// 记录下来，那时这些字段没有答案，填一个假的比留空更糟。
type Event struct {
	ID          string
	OperationID string
	Type        EventType

	AgentID              string
	DeviceID             string
	WorkspaceID          string
	IdentityID           string
	CredentialProviderID string

	Service       string
	Operation     string
	Resource      string
	ResolvedScope string

	Verdict     Verdict
	RiskLevel   RiskLevel
	DecisionID  string
	ApprovalID  string
	LeaseID     string
	LeaseStatus LeaseStatus

	Outcome Outcome
	// ResponseStatus 为 0 表示没有发出过外部请求。
	ResponseStatus int
	Duration       time.Duration
	IsRedacted     bool

	Metadata  string
	CreatedAt time.Time
}

// Reader 是账本的只读面。
//
// 单独拆出来是为了让展示与导出那一侧拿不到 Append 与 PruneBefore：
// 账本只在决策链路上追加，接入面没有理由能写它，也没有理由能删它。
type Reader interface {
	EventByID(ctx context.Context, id string) (Event, error)
	// Events 按时间倒序翻页。before 是游标（取更早的记录），零值表示从最新开始。
	Events(ctx context.Context, before time.Time, limit int) ([]Event, error)
	EventsByAgent(ctx context.Context, agentID string, before time.Time, limit int) ([]Event, error)
	EventsByService(ctx context.Context, service string, before time.Time, limit int) ([]Event, error)
}

// Repository 读写账本。
//
// **没有任何更新方法。** 账本只追加：改写一条记录等于改写「当时发生了什么」，
// 那样它就不再是账本了（REQ-AUDIT-001 AC1）。唯一的删除入口是
// PruneBefore，且只按时间条件删除并要求调用方随后记下一条 audit.pruned
// 事件（REQ-AUDIT-005 AC1）。
type Repository interface {
	Reader
	// Append 写入一条记录。写入失败必须导致整个请求失败（ADR-004）。
	Append(ctx context.Context, event Event) (Event, error)
	// CountBefore 数出早于 cutoff 的记录条数，供清理任务在删除前如实报数。
	CountBefore(ctx context.Context, cutoff time.Time) (int, error)
	// PruneBefore 删除早于 cutoff 的记录，并在**同一个事务**里写下这条清理事件，
	// 返回删除条数与落库后的事件（REQ-AUDIT-005 AC1）。
	//
	// 两件事必须一起成功或一起失败：删掉了却没记下来，账本就少了一段
	// 无法追溯的历史；而接口里不存在「只删不记」的入口，这种状态因此
	// 不是靠调用方自觉避免的。
	// cutoff 必须是一个真实时刻：零值会被拒绝，否则一次误调用就能清空账本。
	PruneBefore(ctx context.Context, cutoff time.Time, record Event) (int, Event, error)
}
