package risk

import (
	adapters "github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * 风险因子（PRD §10.5、REQ-RISK-001）。
 *
 * PRD 列出的十三个因子在这里各占一个字段，另加 Adapter 声明的标签作为基线。
 * 这里**没有**自动化等级、也没有 Trust Memory —— 两者都不该影响风险等级
 * （REQ-RISK-002 AC3），在类型上不存在比在代码里记得不去读更可靠。
 */

// Factors 是一次风险计算的全部输入。
type Factors struct {
	// Write 为真表示写操作。读操作对应 Intent.DesiredChange 为空。
	Write bool
	// Production 为真表示目标在生产环境。
	Production bool
	// Reversible 是 Adapter 声明的可逆性，不是猜出来的（REQ-INTENT-002）。
	Reversible bool
	// ResourceCount 是本次操作影响的资源数量。0 表示数量无法确定 ——
	// 写操作遇到它按批量处理（Fail Closed），不按「大概是一个」处理。
	ResourceCount int

	// Destructive 表示删除类操作。
	Destructive bool
	// PermissionChange 表示修改权限、成员或认证方式。
	PermissionChange bool
	// SecretAccess 表示读取 Secret 或签发凭证。
	SecretAccess bool
	// Billing 表示涉及账单、支付或转账。
	Billing bool
	// ExternalCommunication 表示会向外部发出可见的通信，例如发邮件。
	ExternalCommunication bool

	// FirstSeen 表示这个操作在该 Agent 与该项目下第一次出现。
	FirstSeen bool
	// BeyondHistory 表示本次 Scope 超出了已学习授权的范围（REQ-RISK-003）。
	// 由调用方用 scope.Covers 判定后传入。
	BeyondHistory bool

	AgentTrust  agentauth.TrustLevel
	DeviceTrust agentauth.DeviceTrust

	// DeclaredLabel 是 Adapter 为该操作声明的风险标签，作为计算的基线。
	// 计算结果只会等于或高于它，永远不会低于它。
	DeclaredLabel adapters.RiskLabel
}

// validate 是 REQ-RISK-002 AC2 的 Fail Closed 一侧：认不出来的输入一律拒绝，
// 不落到某个「看起来安全」的默认值上。
func (f Factors) validate() error {
	switch {
	case levelOf(f.DeclaredLabel) == "":
		return unknownRisk("Adapter 没有为该操作声明风险标签")
	case !validTrustLevel(f.AgentTrust):
		return unknownRisk("Agent 的信任等级认不出来：" + string(f.AgentTrust))
	case !validDeviceTrust(f.DeviceTrust):
		return unknownRisk("设备的信任状态认不出来：" + string(f.DeviceTrust))
	case f.ResourceCount < 0:
		return unknownRisk("影响的资源数量为负")
	}
	return nil
}

func unknownRisk(detail string) error {
	return apperr.New(apperr.CodeInvalidRequest).WithDetail("风险等级无法确定：" + detail)
}

func validTrustLevel(level agentauth.TrustLevel) bool {
	switch level {
	case agentauth.TrustUnverified, agentauth.TrustKnown, agentauth.TrustTrusted:
		return true
	default:
		return false
	}
}

func validDeviceTrust(trust agentauth.DeviceTrust) bool {
	switch trust {
	case agentauth.DeviceTrusted, agentauth.DeviceUntrusted:
		return true
	default:
		return false
	}
}

// levelOf 把 Adapter 声明的标签换成等级。认不出的标签落到零值，由 validate 拒绝。
func levelOf(label adapters.RiskLabel) Level {
	switch label {
	case adapters.RiskLabelLow:
		return LevelLow
	case adapters.RiskLabelMedium:
		return LevelMedium
	case adapters.RiskLabelHigh:
		return LevelHigh
	default:
		return ""
	}
}
