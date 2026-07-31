package decision

import (
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/risk"
)

/*
 * Fail Closed（PRD §6.3、REQ-DECIDE-002）。
 *
 * 十种情况一律拒绝。其中五种本包自己看得出来（输入里就缺），另五种由调用方发现后
 * 以 Blockers 交进来 —— core 不做 I/O，「凭据源连不上」这种事只有上层知道。
 *
 * 阻断在七个分支之前求值：认不出发起方的时候，连「这是不是禁止操作」都问得不对。
 */

// Blocker 是使决策无法安全进行的情况。
type Blocker string

const (
	// BlockerAgentUnidentified 无法识别 Agent。
	BlockerAgentUnidentified Blocker = "agent_unidentified"
	// BlockerServiceUndetermined 无法确定服务。
	BlockerServiceUndetermined Blocker = "service_undetermined"
	// BlockerIdentityAmbiguityUnresolvable 身份匹配歧义且无法交给用户解决。
	//
	// 与第四分支（身份匹配不唯一 → 询问）的区别在于「问得出来吗」：
	// 有两个以上候选就问，一个候选都拿不出来时问也没有意义。
	BlockerIdentityAmbiguityUnresolvable Blocker = "identity_ambiguity_unresolvable"
	// BlockerScopeUndeterminable Scope 的十个维度不完整。
	BlockerScopeUndeterminable Blocker = "scope_undeterminable"
	// BlockerCapabilityNotOffered Adapter 未声明该能力。
	BlockerCapabilityNotOffered Blocker = "capability_not_offered"
	// BlockerRiskUnknown 风险等级未知。
	BlockerRiskUnknown Blocker = "risk_unknown"
	// BlockerPolicyEngineFailure 决策过程本身出错或 panic。
	BlockerPolicyEngineFailure Blocker = "policy_engine_failure"
	// BlockerGatewayOffline Gateway 离线。
	BlockerGatewayOffline Blocker = "gateway_offline"
	// BlockerCredentialSourceUnavailable 凭据来源不可用。
	//
	//nolint:gosec // G101 按标识符名里的 credential 一词误报：这是阻断原因的枚举标签，
	// 会出现在账本与 API 响应里，不是任何形式的凭据。
	BlockerCredentialSourceUnavailable Blocker = "credential_source_unavailable"
	// BlockerAuditWriteFailed 审计写入失败（ADR-004：审计是执行的前置条件）。
	BlockerAuditWriteFailed Blocker = "audit_write_failed"
)

// blockers 是十种情况的封闭清单，顺序与常量声明一致。
var blockers = []Blocker{
	BlockerAgentUnidentified,
	BlockerServiceUndetermined,
	BlockerIdentityAmbiguityUnresolvable,
	BlockerScopeUndeterminable,
	BlockerCapabilityNotOffered,
	BlockerRiskUnknown,
	BlockerPolicyEngineFailure,
	BlockerGatewayOffline,
	BlockerCredentialSourceUnavailable,
	BlockerAuditWriteFailed,
}

// Blockers 返回十种 Fail Closed 情况的副本，供调用方与用例逐条核对。
func Blockers() []Blocker {
	return append([]Blocker(nil), blockers...)
}

// selfDetected 是本包自己能看出来的阻断，按声明顺序求值，命中即返回。
//
// 调用方交来的 Blockers 优先于它们：上层已经知道凭据源连不上时，
// 再去挑剔某个维度是否完整没有意义，也会让账本上的原因偏离真正的起因。
func (i Input) selfDetected() (Blocker, bool) {
	checks := []struct {
		blocker Blocker
		tripped bool
	}{
		{BlockerAgentUnidentified, i.AgentID == ""},
		{BlockerServiceUndetermined, i.Scope.Scope.Service == ""},
		{BlockerScopeUndeterminable, i.Scope.Scope.Validate() != nil},
		{BlockerIdentityAmbiguityUnresolvable, !resolvableMatch(i.Match)},
		{BlockerRiskUnknown, !validAssessment(i.Assessment)},
	}

	for _, check := range checks {
		if check.tripped {
			return check.blocker, true
		}
	}
	return "", false
}

// resolvableMatch 报告这次匹配能不能往下走。
//
// 有歧义时至少要有两个候选，否则审批页面上没有可选的东西，问了也解决不了；
// 没有歧义时必须匹配到一个身份，且命中层级与身份同进同出 ——
// 后者与 decisions 表上的 CHECK 是同一条约束（REQ-IDENT-002 AC3）。
func resolvableMatch(result matcher.Result) bool {
	if result.Ambiguous {
		return len(result.Candidates) >= 2 && result.Identity.ID == "" && result.Level == ""
	}
	return result.Identity.ID != "" && result.Level != ""
}

// validAssessment 要求等级合法**且**至少给出一条原因。
//
// 没有原因的结论在 Access Folio 上解释不了「为什么是这个等级」（REQ-RISK-001 AC3），
// 而一个解释不了的等级与未知等级没有区别。
func validAssessment(assessment risk.Assessment) bool {
	if len(assessment.Factors) == 0 {
		return false
	}
	switch assessment.Level {
	case risk.LevelLow, risk.LevelMedium, risk.LevelHigh:
		return true
	default:
		return false
	}
}

func validBlocker(blocker Blocker) bool {
	for _, known := range blockers {
		if blocker == known {
			return true
		}
	}
	return false
}
