package decision

import (
	"context"
	"time"

	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/risk"
)

// Verdict 是决策的结论（PRD §9.6）。
//
// 只有三个取值。多一个取值就意味着多一条放行路径，而整条链路只允许有
// 一个放行出口。
type Verdict string

const (
	// VerdictAutoAllow 直接放行并签发 Lease。
	VerdictAutoAllow Verdict = "auto_allow"
	// VerdictRequireApproval 交给人确认。这是默认分支：放行必须显式。
	VerdictRequireApproval Verdict = "require_approval"
	// VerdictDeny 拒绝，不产生任何出站流量。
	VerdictDeny Verdict = "deny"
)

// ApprovalRequirement 是这次决策对确认强度的要求（PRD §9.6 的 approval_requirement）。
type ApprovalRequirement string

const (
	// ApprovalNone 表示不会产生审批项。auto_allow 与 deny 都用它 ——
	// 前者不需要问，后者不提供审批入口（REQ-DECIDE-004 AC1）。
	ApprovalNone ApprovalRequirement = "none"
	// ApprovalStandard 表示在 Console 上确认即可。
	ApprovalStandard ApprovalRequirement = "standard"
	// ApprovalStrongAuth 表示还需要 Passkey / Touch ID / 主密码（REQ-APPROVAL-005）。
	ApprovalStrongAuth ApprovalRequirement = "strong_auth"
)

// Decision 是决策引擎对一次请求给出的结论（PRD §9.6）。
//
// 记录不可变：改写一条决策等于改写「当时为什么放行」，账本会因此不可信。
// 仓储接口据此不提供任何更新方法。
type Decision struct {
	ID                  string
	CapabilityRequestID string
	Verdict             Verdict
	RiskLevel           risk.Level
	// RiskFactors 是触发该等级的因子列表（JSON 数组），
	// Access Folio 用它解释「为什么是这个等级」（REQ-RISK-001 AC3）。
	RiskFactors string
	// IdentityID 与 MatchLevel 同时存在或同时为空。没匹配到身份正是
	// Fail Closed 要拒绝的情况之一，决策记录必须能如实表达它。
	IdentityID string
	MatchLevel matcher.MatchLevel
	// ResolvedScope 是收敛后的十个维度（REQ-SCOPE-001），JSON 对象。
	ResolvedScope       string
	ApprovalRequirement ApprovalRequirement
	// ReasonCode 是结论原因的封闭标识，不是句子：Console 按码做中英文，
	// 账本导出后也不会锁死语言。
	ReasonCode string
	// TrustMemoryID 是 PRD §9.6 的 matched_memory：命中的记忆，未命中时为空。
	// 它与 MatchLevel 为 trust_memory 是同一件事的两种表述，
	// 前者说是哪一条，后者说命中在第几级。
	TrustMemoryID string
	CreatedAt     time.Time
}

// DecisionRepository 写入与读取决策记录。
//
// 只有写入与读取，没有更新与删除：决策是账本的事实，不接受事后修改。
type DecisionRepository interface {
	// CreateDecision 为一个请求写入唯一的决策。同一请求写第二次会得到
	// 冲突错误 —— REQ-API-004 要求决策类端点幂等，重复调用返回首次结果
	// 而不是产生第二个结论。
	CreateDecision(ctx context.Context, decision Decision) (Decision, error)
	DecisionByID(ctx context.Context, id string) (Decision, error)
	DecisionByRequestID(ctx context.Context, requestID string) (Decision, error)
}
