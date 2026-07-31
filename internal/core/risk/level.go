package risk

// Level 是一次操作的风险等级（REQ-RISK-001 的输出）。
//
// 只有三级，且由确定性规则算出。等级与 Adapter 声明的风险标签
// （registry.RiskLabel）取值域相同但含义不同：标签是声明，等级是计算结果
type Level string

const (
	// LevelLow 对应 PRD §12 的读取类操作。
	LevelLow Level = "low"
	// LevelMedium 对应有限范围的写操作。
	LevelMedium Level = "medium"
	// LevelHigh 对应删除、不可逆、权限、Secret、账单类操作。
	// 高风险永远需要人工确认，不存在任何配置组合能改变这一点（REQ-DECIDE-003 AC3）。
	LevelHigh Level = "high"
)
