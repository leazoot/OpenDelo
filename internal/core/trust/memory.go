package trust

import (
	"context"
	"time"

	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/risk"
)

// Behavior 是记忆命中后的行为（PRD §9.8 的 approval_behavior）。
type Behavior string

const (
	// BehaviorAutoAllow 命中即自动放行，前提是风险不超过 RiskCeiling。
	BehaviorAutoAllow Behavior = "auto_allow"
	// BehaviorAlwaysAsk 命中也仍然询问（REQ-APPROVAL-002 AC5）。
	BehaviorAlwaysAsk Behavior = "always_ask"
)

// Status 是记忆当前是否参与匹配。
type Status string

const (
	// StatusActive 生效中。
	StatusActive Status = "active"
	// StatusInvalidated 已失效。失效的记忆不删除，Automation 页面要显示
	// 它为什么失效（REQ-TRUST-004 AC2）。
	StatusInvalidated Status = "invalidated"
)

// InvalidationReason 是 REQ-TRUST-004 列出的八个失效条件。
type InvalidationReason string

const (
	// ReasonProviderDisconnected 凭据来源断开。
	ReasonProviderDisconnected InvalidationReason = "provider_disconnected"
	// ReasonIdentityScopeChanged 身份在外部服务上的权限范围变了。
	ReasonIdentityScopeChanged InvalidationReason = "identity_scope_changed"
	// ReasonAgentExecutableChanged Agent 可执行文件哈希变了。
	ReasonAgentExecutableChanged InvalidationReason = "agent_executable_changed"
	// ReasonProjectFingerprintChanged 项目指纹变了。
	ReasonProjectFingerprintChanged InvalidationReason = "project_fingerprint_changed"
	// ReasonDeviceUntrusted 设备不再可信。
	ReasonDeviceUntrusted InvalidationReason = "device_untrusted"
	// ReasonUnusedTooLong 长期未使用（默认 30 天）。
	ReasonUnusedTooLong InvalidationReason = "unused_too_long"
	// ReasonCautiousModeSelected 用户切换到谨慎模式。
	ReasonCautiousModeSelected InvalidationReason = "cautious_mode_selected"
	// ReasonAdapterRiskUpgraded Adapter 把某个操作的风险定义调高了。
	ReasonAdapterRiskUpgraded InvalidationReason = "adapter_risk_upgraded"
)

// Memory 是一条可复用的授权记忆（PRD §9.8）。
//
// REQ-TRUST-002 的七个维度在这里各占一个字段，且都必须有值：
// 资源(ResourceScope) · 操作(CapabilityScope) · 时间(ExpiresAt) ·
// Agent(AgentID) · 项目(WorkspaceID) · 身份(IdentityID) · 环境(Environment)。
// 任何一维留空都等于「这一维不限」，那正是扩大。
//
// RiskCeiling 只能是 low 或 medium：高风险永远需要人工确认，
// 所以「能自动放行高风险的记忆」这个东西不存在（REQ-TRUST-003）。
type Memory struct {
	ID              string
	AgentID         string
	WorkspaceID     string
	IdentityID      string
	Service         string
	ResourceScope   string
	CapabilityScope string
	Environment     matcher.Environment
	RiskCeiling     risk.Level
	Behavior        Behavior
	// CreatedFrom 指向产生这条记忆的审批（REQ-TRUST-001 AC2）。
	CreatedFrom        string
	Status             Status
	InvalidationReason InvalidationReason
	// LastUsedAt 为零值表示从未使用过。零值时间不能落库 ——
	// 它会让「长期未使用」的判断立刻命中。
	LastUsedAt time.Time
	ExpiresAt  time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Repository 读写授权记忆。
//
// 没有修改 Scope 的方法：改范围就是重新学习，只能由一次新的审批产生新记忆。
// 这样「不得扩大」不需要在更新路径上再检查一遍 —— 那条路径不存在。
type Repository interface {
	CreateMemory(ctx context.Context, memory Memory) (Memory, error)
	MemoryByID(ctx context.Context, id string) (Memory, error)
	// MatchMemories 是决策链路的匹配入口：只返回生效中的记忆。
	// 失效的记忆读得到，但匹配不到（REQ-TRUST-004 AC3）。limit 必须为正。
	MatchMemories(ctx context.Context, agentID, workspaceID, service string, limit int) ([]Memory, error)
	MemoriesByStatus(ctx context.Context, status Status, limit int) ([]Memory, error)
	// ActiveMemoriesByIdentity 列出某个身份名下仍生效的记忆，
	// 支撑身份断开时的级联失效（REQ-IDENT-001 AC2）。limit 必须为正。
	ActiveMemoriesByIdentity(ctx context.Context, identityID string, limit int) ([]Memory, error)
	// Touch 记一次使用，刷新 last_used_at。
	Touch(ctx context.Context, id string, at time.Time) (Memory, error)
	// TightenBehavior 把一条生效中的记忆从 auto_allow 改成 always_ask。
	// 只有这一个方向 —— 放宽只能由一次新的审批产生（REQ-TRUST-002）。
	TightenBehavior(ctx context.Context, id string, at time.Time) (Memory, error)
	// Invalidate 使一条记忆失效并记下原因。原因是数据的一部分，不是日志。
	Invalidate(ctx context.Context, id string, reason InvalidationReason, at time.Time) (Memory, error)
	// DeleteMemory 是用户在 Automation 页面上的删除（REQ-TRUST-005 AC1）。
	// 与 Invalidate 不同：失效是系统行为且保留记录，删除是用户行为。
	DeleteMemory(ctx context.Context, id string) error
}
