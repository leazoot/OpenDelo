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

func TestBranches_AreTheSixOrderedRowsFromThePRD(t *testing.T) {
	// 分支表的顺序就是 PRD §10.6 的顺序。第一分支（禁止列表）在 Decide 里单独求值，
	// 第七分支是默认返回，所以表里是中间五行。
	expected := []Reason{
		ReasonHighRisk,
		ReasonBeyondLearnedScope,
		ReasonIdentityAmbiguous,
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
