package decision

import (
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/risk"
	"github.com/Runcoor/opendelo/internal/core/scope"
)

/*
 * 白盒用例：决策过程本身出错时的落点（REQ-DECIDE-002 AC2）。
 *
 * panic 只能从内部制造 —— 从外部构造不出一个会让 Decide 崩掉的输入，
 * 那正是好事。用例临时换掉分支表，跑完立刻装回去。
 */

func healthyInput() Input {
	return Input{
		Mode:       ModeBalanced,
		AgentID:    "01K1AGENT0000000000000MAIN",
		AgentTrust: agentauth.TrustKnown,
		Match: matcher.Result{
			Identity: matcher.Identity{ID: "01K1IDENTITY000000000MAIN"},
			Level:    matcher.MatchSoleIdentity,
		},
		Scope: scope.Result{Scope: scope.Scope{
			AgentID:      "01K1AGENT0000000000000MAIN",
			WorkspaceID:  "01K1WORKSPACE00000000MAIN",
			Service:      "github",
			IdentityID:   "01K1IDENTITY000000000MAIN",
			Account:      "work",
			Resource:     map[string]string{"repo": "Runcoor/opendelo"},
			ResourceKey:  "repo=Runcoor/opendelo",
			Operation:    "repo.read",
			NotBefore:    time.Unix(0, 0).UTC(),
			ExpiresAt:    time.Unix(900, 0).UTC(),
			RequestLimit: 1,
			Environment:  matcher.EnvironmentProduction,
			RiskCeiling:  risk.LevelLow,
		}},
		Assessment: risk.Assessment{
			Level:   risk.LevelLow,
			Factors: []risk.Factor{FactorForTest},
		},
	}
}

// FactorForTest 只是一条占位原因：validAssessment 要求结论至少有一条解释。
const FactorForTest risk.Factor = "read_only"

func TestDecide_PanicInsideTheEngine_BecomesDeny(t *testing.T) {
	// 先确认这个输入本来会被自动放行，否则下面的断言换成什么实现都成立。
	if healthy := Decide(healthyInput()); healthy.Verdict != VerdictAutoAllow {
		t.Fatalf("对照输入的结论为 %s，期望 auto_allow", healthy.Verdict)
	}

	original := branches
	t.Cleanup(func() { branches = original })

	branches = []branch{{
		reason:  ReasonHighRisk,
		hits:    func(Input, bool) bool { panic("策略引擎炸了") },
		verdict: VerdictRequireApproval,
	}}

	outcome := Decide(healthyInput())
	if outcome.Verdict != VerdictDeny {
		t.Errorf("结论为 %s，期望 deny", outcome.Verdict)
	}
	if outcome.Reason != ReasonFailClosed || outcome.Blocker != BlockerPolicyEngineFailure {
		t.Errorf("原因为 %s/%s，期望 fail_closed/policy_engine_failure",
			outcome.Reason, outcome.Blocker)
	}
	if outcome.ApprovalRequirement != ApprovalNone {
		t.Errorf("确认强度为 %s，崩掉的决策不该产生审批项", outcome.ApprovalRequirement)
	}
}

func TestBranches_AreTheOrderedRowsFromThePRD(t *testing.T) {
	// 分支表的顺序就是 PRD §10.6 的顺序。第一分支（禁止列表）在 Decide 里单独求值，
	// 最后一个分支是默认返回，所以表里是中间这几行。
	//
	// active_lease 是 PRD 之外新增的一行（D-17，关闭 R-39），插在身份歧义与低风险
	// 之间。**它的位置本身就是安全属性**：往前挪一格越过身份歧义，比对的就是一个
	// 猜出来的身份；再往前越过高风险，「高风险永远要人确认」就有了第一个例外。
	expected := []Reason{
		ReasonHighRisk,
		ReasonBeyondLearnedScope,
		ReasonIdentityAmbiguous,
		ReasonActiveLease,
		ReasonLowRisk,
		ReasonTrustMemoryMatch,
	}

	if len(branches) != len(expected) {
		t.Fatalf("分支表有 %d 行，期望 %d 行", len(branches), len(expected))
	}
	for index, reason := range expected {
		if branches[index].reason != reason {
			t.Errorf("第 %d 行为 %s，期望 %s", index+1, branches[index].reason, reason)
		}
	}
}

func TestActiveLeaseBranch_ExcludesHighRiskOnItsOwn(t *testing.T) {
	// 分支表里高风险排在前面，因此 active_lease 那一行的 `!= LevelHigh`
	// 从 Decide 走过去永远碰不到 —— 把它删掉，外部用例一条都不会红。
	//
	// 直接调那一行的谓词，让这道防线自己被测到：它挡的是「有人把这一行往前挪」
	// 之后的那一瞬，而那正是最容易在改动中发生、又最难被发现的一步。
	var predicate func(Input, bool) bool
	for _, row := range branches {
		if row.reason == ReasonActiveLease {
			predicate = row.hits
		}
	}
	if predicate == nil {
		t.Fatal("分支表里没有 active_lease 那一行")
	}

	input := healthyInput()
	input.Active = []ActiveLease{{LeaseID: "01K1LEASE0000000000000ACTV", Scope: input.Scope.Scope}}

	if !predicate(input, true) {
		t.Fatal("范围完全覆盖时这一行没有命中 —— 下面的断言会因为别的原因通过")
	}

	input.Assessment.Level = risk.LevelHigh
	if predicate(input, true) {
		t.Error("高风险时这一行仍然命中：一条 Lease 不得豁免人工确认")
	}
}
