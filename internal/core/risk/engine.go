package risk

import "github.com/Runcoor/opendelo/internal/core/agentauth"

/*
 * Risk Engine：十三个因子 → 等级 + 触发的因子列表（REQ-RISK-001）。
 *
 * 纯函数。规则写成一张字面上的表，每条规则各自决定「把等级抬到哪里」，
 * 最终取全部结论中最高的一个。抬升永远相对**基线**计算而不是相对上一条规则的结果，
 * 因此表的顺序只影响列出的顺序，不影响算出的等级。
 */

// Factor 是一条被触发的风险原因（REQ-RISK-001 AC3）。
//
// 用码而不是句子：Console 按码做中英文，账本导出后也不会锁死语言，
// 与 decisions.reason_code 的做法一致。码本身取的是能读懂的英文短语。
type Factor string

const (
	// FactorDeclaredLabel 恒出现：等级的基线来自 Adapter 的声明。
	FactorDeclaredLabel Factor = "adapter_declared_label"
	// FactorReadOnly 说明这次不改变任何东西，是低风险最常见的原因。
	FactorReadOnly Factor = "read_only"

	// FactorDestructive 删除类操作（PRD §12.3）。
	FactorDestructive Factor = "destructive"
	// FactorIrreversible 写操作且 Adapter 声明不可逆。
	FactorIrreversible Factor = "irreversible"
	// FactorPermissionChange 修改权限、成员或认证方式。
	FactorPermissionChange Factor = "permission_change"
	// FactorSecretAccess 读取 Secret 或签发凭证。
	FactorSecretAccess Factor = "secret_access"
	// FactorBilling 涉及账单、支付或转账。
	FactorBilling Factor = "billing"
	// FactorBulkChange 写操作影响的资源不止一个，或数量无法确定。
	FactorBulkChange Factor = "bulk_change"

	// FactorProductionWrite 生产环境下的写操作（REQ-INTENT-003 AC1）。
	FactorProductionWrite Factor = "production_write"
	// FactorBeyondHistory 本次范围超出已学习的授权（REQ-RISK-003）。
	FactorBeyondHistory Factor = "beyond_history"
	// FactorFirstSeen 该写操作首次出现（PRD §12.2「首次确认」）。
	FactorFirstSeen Factor = "first_seen"
	// FactorAgentUnverified 发起方尚未被用户确认（REQ-AGENT-002）。
	FactorAgentUnverified Factor = "agent_unverified"
	// FactorUntrustedDevice 设备尚未被用户确认。
	FactorUntrustedDevice Factor = "untrusted_device"
	// FactorExternalCommunication 会向外部发出可见的通信。
	FactorExternalCommunication Factor = "external_communication"
)

// Assessment 是一次风险计算的结论。
type Assessment struct {
	Level Level
	// Factors 至少有一条（REQ-RISK-001 AC3），第一条恒为 FactorDeclaredLabel。
	Factors []Factor
}

// rule 是一条风险规则：命中时把等级抬到 raise(baseline) 给出的高度。
type rule struct {
	factor    Factor
	triggered func(Factors) bool
	raise     func(baseline Level) Level
}

// toHigh 是「恒为 high」：命中即封顶，不受基线、Trust Memory 与自动化等级影响
// （REQ-RISK-002 AC3）。
func toHigh(Level) Level { return LevelHigh }

// atLeastMedium 把等级抬到至少 medium，已经更高的保持不变。
func atLeastMedium(baseline Level) Level {
	if rank(baseline) > rank(LevelMedium) {
		return baseline
	}
	return LevelMedium
}

// oneLevelUp 在基线之上抬一级，high 已经到顶。
func oneLevelUp(baseline Level) Level {
	switch baseline {
	case LevelLow:
		return LevelMedium
	default:
		return LevelHigh
	}
}

// rules 是全部风险规则。前六条封顶到 high，后六条在基线之上抬。
//
// 后六条都要求 Write：PRD §11.2 与 §12.1 明确低风险读取自动允许，
// 读一个没读过的资源仍然只是读。抬升只发生在会改变外部状态的请求上。
var rules = []rule{
	{FactorDestructive, func(f Factors) bool { return f.Destructive }, toHigh},
	{FactorIrreversible, func(f Factors) bool { return f.Write && !f.Reversible }, toHigh},
	{FactorPermissionChange, func(f Factors) bool { return f.PermissionChange }, toHigh},
	{FactorSecretAccess, func(f Factors) bool { return f.SecretAccess }, toHigh},
	{FactorBilling, func(f Factors) bool { return f.Billing }, toHigh},
	{FactorBulkChange, func(f Factors) bool { return f.Write && f.ResourceCount != 1 }, toHigh},

	{FactorProductionWrite, func(f Factors) bool { return f.Write && f.Production }, atLeastMedium},
	{FactorBeyondHistory, func(f Factors) bool { return f.Write && f.BeyondHistory }, oneLevelUp},
	{FactorFirstSeen, func(f Factors) bool { return f.Write && f.FirstSeen }, atLeastMedium},
	{
		FactorAgentUnverified,
		func(f Factors) bool { return f.Write && f.AgentTrust == agentauth.TrustUnverified },
		atLeastMedium,
	},
	{
		FactorUntrustedDevice,
		func(f Factors) bool { return f.Write && f.DeviceTrust == agentauth.DeviceUntrusted },
		atLeastMedium,
	},
	{
		FactorExternalCommunication,
		func(f Factors) bool { return f.Write && f.ExternalCommunication },
		atLeastMedium,
	},
}

// Evaluate 计算风险等级。
//
// 因子认不出来时返回错误而不是给一个等级：「风险等级未知」是 Fail Closed 的十种
// 情况之一（PRD §6.3），必须让调用方拒绝，不能让它拿到一个看起来能用的结论。
func Evaluate(factors Factors) (Assessment, error) {
	if err := factors.validate(); err != nil {
		return Assessment{}, err
	}

	baseline := levelOf(factors.DeclaredLabel)
	level := baseline

	reasons := []Factor{FactorDeclaredLabel}
	if !factors.Write {
		reasons = append(reasons, FactorReadOnly)
	}

	for _, current := range rules {
		if !current.triggered(factors) {
			continue
		}
		reasons = append(reasons, current.factor)
		if raised := current.raise(baseline); rank(raised) > rank(level) {
			level = raised
		}
	}

	return Assessment{Level: level, Factors: reasons}, nil
}

// rank 把等级排成可比较的序。认不出的等级为 0，因此抬不高任何东西。
func rank(level Level) int {
	switch level {
	case LevelLow:
		return 1
	case LevelMedium:
		return 2
	case LevelHigh:
		return 3
	default:
		return 0
	}
}
