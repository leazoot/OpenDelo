package matcher

// MatchLevel 是身份匹配命中的层级（REQ-IDENT-002）。
//
// 五个取值即五级匹配顺序，序号越小越优先。命中层级必须与匹配结果一并
// 写入 Decision 与审计事件（AC3）—— 只知道匹配到了哪个身份，无法解释
// 为什么是它。
type MatchLevel string

const (
	// MatchWorkspaceBinding 是用户为该项目显式建立的绑定。
	MatchWorkspaceBinding MatchLevel = "workspace_binding"
	// MatchResourceBinding 是用户为该资源显式建立的绑定。
	MatchResourceBinding MatchLevel = "resource_binding"
	// MatchTrustMemory 来自历史选择形成的记忆。
	MatchTrustMemory MatchLevel = "trust_memory"
	// MatchSoleIdentity 表示该服务下只有一个可用身份。
	MatchSoleIdentity MatchLevel = "sole_identity"
	// MatchManualSelection 表示由用户在审批时当场选定。
	MatchManualSelection MatchLevel = "manual_selection"
)
