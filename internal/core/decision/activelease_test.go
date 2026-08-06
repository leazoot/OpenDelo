package decision_test

import (
	"testing"

	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/risk"
	"github.com/Runcoor/opendelo/internal/core/scope"
)

/*
 * 已签发的授权参与后续决策（D-17，关闭 R-39）。
 *
 * 「允许到任务结束」签出一条会话绑定的 Lease，但**不生成记忆**。此前决策链路
 * 只读记忆，于是同一会话里下一次同样的调用又要重新问人 —— 用户明明已经为这个
 * 确切范围点过头了。
 *
 * 这一分支是决策链路上新增的**放行出口**，因此本文件的重心全在它不该放行的时候：
 * 高风险、身份不明、范围差一维、授权不在场。
 */

const leaseID = "01K1LEASE0000000000000ACTV"

func leaseCovering(covered scope.Scope) decision.ActiveLease {
	return decision.ActiveLease{LeaseID: leaseID, Scope: covered}
}

func TestDecide_ActiveLeaseCoversTheRequest_IsAllowedWithoutAskingAgain(t *testing.T) {
	// R-39 本身：写操作、中风险、没有任何记忆 —— 唯一的依据就是那条 Lease。
	input := mediumWrite()
	input.Active = []decision.ActiveLease{leaseCovering(baseScope())}

	outcome := decision.Decide(input)

	assertOutcome(t, outcome, decision.VerdictAutoAllow, decision.ReasonActiveLease)
	if outcome.MatchedLeaseID != leaseID {
		// 报不出是哪一条，调用方就只能另签一条 —— 一次人工确认换出两份权限。
		t.Errorf("命中的 Lease 为 %q，期望 %q", outcome.MatchedLeaseID, leaseID)
	}
}

func TestDecide_HighRisk_IsNotExemptedByAnActiveLease(t *testing.T) {
	// 「高风险操作永远需要人工确认」这句话里没有例外分支（REQ-DECIDE-003 AC3、
	// 不可协商约束第 3 条）。一条 Lease 说的是「这一次可以」，不是「以后都不必问」。
	input := mediumWrite()
	input.Assessment = risk.Assessment{
		Level:   risk.LevelHigh,
		Factors: []risk.Factor{risk.FactorDeclaredLabel, risk.FactorIrreversible},
	}
	covering := baseScope()
	covering.RiskCeiling = risk.LevelHigh
	input.Scope.Scope.RiskCeiling = risk.LevelHigh
	input.Active = []decision.ActiveLease{leaseCovering(covering)}

	outcome := decision.Decide(input)

	assertOutcome(t, outcome, decision.VerdictRequireApproval, decision.ReasonHighRisk)
	if outcome.MatchedLeaseID != "" {
		t.Errorf("高风险的结论里报出了可复用的 Lease %q", outcome.MatchedLeaseID)
	}
}

func TestDecide_IdentityAmbiguous_IsNotResolvedByAnActiveLease(t *testing.T) {
	// 身份定不下来时，这次请求 Scope 里的那一维本身就不可信 ——
	// 拿它去比对任何授权，比的都是一个猜出来的值。
	//
	// 结论是 deny 而不是 require_approval：无法收敛的身份歧义落在 Fail Closed
	// 的十种情况里，比分支表更早一步。写这条用例时先期望的是分支表那一行，
	// 实测才发现它根本走不到那里 —— 结论比预期更严，方向是对的。
	input := mediumWrite()
	input.Match.Ambiguous = true
	input.Active = []decision.ActiveLease{leaseCovering(baseScope())}

	outcome := decision.Decide(input)

	assertOutcome(t, outcome, decision.VerdictDeny, decision.ReasonFailClosed)
	if outcome.MatchedLeaseID != "" {
		t.Errorf("身份不明的结论里报出了可复用的 Lease %q", outcome.MatchedLeaseID)
	}
}

func TestDecide_ActiveLeaseOnAnotherResource_StillRequiresApproval(t *testing.T) {
	// 差一维就是不覆盖。这条与「Learn Without Expanding」是同一条规矩：
	// 一份授权罩得住的只有它签发时收敛出的那个范围。
	another := baseScope()
	another.Resource = map[string]string{"zone": "tele-call.cn", "record": otherRecord}
	another.ResourceKey = "record=" + otherRecord + ";zone=tele-call.cn"

	input := mediumWrite()
	input.Active = []decision.ActiveLease{leaseCovering(another)}

	outcome := decision.Decide(input)

	assertOutcome(t, outcome, decision.VerdictRequireApproval, decision.ReasonRequiresConfirmation)
	if outcome.MatchedLeaseID != "" {
		t.Errorf("范围不覆盖却报出了可复用的 Lease %q", outcome.MatchedLeaseID)
	}
}

func TestDecide_ActiveLeaseWithALowerRiskCeiling_DoesNotCoverAHigherRequest(t *testing.T) {
	// 风险上限是 Scope 的一个维度。一条只批到低风险的授权罩不住一次中风险请求 ——
	// 否则「批一次读」就能顺带把写也放出去。
	lower := baseScope()
	lower.RiskCeiling = risk.LevelLow

	input := mediumWrite()
	input.Active = []decision.ActiveLease{leaseCovering(lower)}

	outcome := decision.Decide(input)

	assertOutcome(t, outcome, decision.VerdictRequireApproval, decision.ReasonRequiresConfirmation)
}

func TestDecide_ActiveLeaseButMemoryDoesNotCover_StillRequiresApproval(t *testing.T) {
	// 用户特意收紧过的记忆先说话：有记忆而这次落在它之外时，结论是问人。
	// 分支顺序在这里体现为「多问一次」，方向永远是安全的那一侧。
	input := mediumWrite()
	input.Learned = []decision.Grant{grantForAnotherRecord()}
	input.Active = []decision.ActiveLease{leaseCovering(baseScope())}

	outcome := decision.Decide(input)

	assertOutcome(t, outcome, decision.VerdictRequireApproval, decision.ReasonBeyondLearnedScope)
	if outcome.MatchedLeaseID != "" {
		t.Errorf("超出已学范围的结论里报出了可复用的 Lease %q", outcome.MatchedLeaseID)
	}
}

func TestDecide_UnconfirmedAgentWriting_IsNotWavedThroughByAnActiveLease(t *testing.T) {
	// 未确认的 Agent 发起写操作那道门与「有没有授权」无关：它问的是
	// 「这个 Agent 是不是人认下来的那一个」。一条 Lease 回答不了这个问题。
	input := mediumWrite()
	input.AgentTrust = agentauth.TrustUnverified
	input.Active = []decision.ActiveLease{leaseCovering(baseScope())}

	outcome := decision.Decide(input)

	if outcome.Verdict == decision.VerdictAutoAllow {
		t.Errorf("未确认的 Agent 靠一条 Lease 被自动放行了：%+v", outcome)
	}
	if outcome.MatchedLeaseID != "" {
		t.Errorf("结论里报出了可复用的 Lease %q", outcome.MatchedLeaseID)
	}
}

func TestDecide_ActiveLeaseOfAnotherAgent_DoesNotCover(t *testing.T) {
	// 授权是签给某一个 Agent 的。同一台机器上的第二个 Agent 不该白拿
	// 第一个刚获得的权限 —— Agent 是 Scope 的第一个维度。
	elsewhere := baseScope()
	elsewhere.AgentID = "01K1AGENT000000000000OTHER"

	input := mediumWrite()
	input.Active = []decision.ActiveLease{leaseCovering(elsewhere)}

	outcome := decision.Decide(input)

	assertOutcome(t, outcome, decision.VerdictRequireApproval, decision.ReasonRequiresConfirmation)
	if outcome.MatchedLeaseID != "" {
		t.Errorf("另一个 Agent 的授权被当成了可复用的：%q", outcome.MatchedLeaseID)
	}
}

func TestDecide_NoActiveLease_BehavesExactlyAsBefore(t *testing.T) {
	// 新分支不得改变没有 Lease 时的任何结论 —— 这是它「只加不减」的对照组。
	input := mediumWrite()

	outcome := decision.Decide(input)

	assertOutcome(t, outcome, decision.VerdictRequireApproval, decision.ReasonRequiresConfirmation)
}
