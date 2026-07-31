package decision_test

import (
	"reflect"
	"testing"

	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/risk"
)

/*
 * 三种自动化等级（PRD §11、REQ-DECIDE-003）。
 *
 * 模式是决策链路的输入参数，只改变两个自动放行分支的附加条件。禁止列表、
 * 高风险、范围扩大与身份歧义四条在分支表里，与模式无关 ——
 * 「不允许关闭所有高风险保护」因此是结构上做不到，而不是一条要记得执行的规则。
 */

func TestModes_AreTheThreeFromThePRD(t *testing.T) {
	// 顺序是严格程度从紧到松，单调性用例依赖它。
	expected := []decision.Mode{"cautious", "balanced", "automatic"}

	if got := decision.Modes(); !reflect.DeepEqual(got, expected) {
		t.Fatalf("模式清单为 %v，期望 %v", got, expected)
	}
}

// ——— 谨慎模式：PRD §11.1 的五条规则逐条断言 ———

func TestCautiousMode_CoversEveryRuleFromThePRD(t *testing.T) {
	cautious := func(apply func(*decision.Input)) decision.Input {
		input := lowRiskRead()
		input.Mode = decision.ModeCautious
		// 开关打开，这样每个用例失败的原因只可能是它自己制造的那一条。
		input.ReadOnlyAutoAllow = true
		apply(&input)
		return input
	}

	cases := []struct {
		rule    string
		input   decision.Input
		verdict decision.Verdict
	}{
		{"只读且开关打开时自动允许", cautious(func(*decision.Input) {}), decision.VerdictAutoAllow},
		{"只读但开关默认关闭时询问", cautious(func(i *decision.Input) {
			i.ReadOnlyAutoAllow = false
		}), decision.VerdictRequireApproval},
		{"所有写操作询问", cautious(func(i *decision.Input) {
			i.Write = true
		}), decision.VerdictRequireApproval},
		{"新服务首次询问", cautious(func(i *decision.Input) {
			i.NewService = true
		}), decision.VerdictRequireApproval},
		{"身份变化询问", cautious(func(i *decision.Input) {
			i.IdentityChanged = true
		}), decision.VerdictRequireApproval},
	}

	for _, testCase := range cases {
		t.Run(testCase.rule, func(t *testing.T) {
			if got := decision.Decide(testCase.input).Verdict; got != testCase.verdict {
				t.Errorf("结论为 %s，期望 %s", got, testCase.verdict)
			}
		})
	}
}

func TestCautiousMode_HighRiskNeedsTwoConfirmations(t *testing.T) {
	// PRD §11.1「高风险二次确认」。另两种模式为单次确认。
	cases := []struct {
		mode     decision.Mode
		expected int
	}{
		{decision.ModeCautious, 2},
		{decision.ModeBalanced, 1},
		{decision.ModeAutomatic, 1},
	}

	for _, testCase := range cases {
		t.Run(string(testCase.mode)+" 下高风险确认 "+string(rune('0'+testCase.expected))+" 次", func(t *testing.T) {
			input := mediumWrite()
			input.Mode = testCase.mode
			input.Assessment.Level = risk.LevelHigh

			outcome := decision.Decide(input)
			if outcome.Verdict != decision.VerdictRequireApproval {
				t.Fatalf("结论为 %s，期望 require_approval", outcome.Verdict)
			}
			if outcome.Confirmations != testCase.expected {
				t.Errorf("确认次数为 %d，期望 %d", outcome.Confirmations, testCase.expected)
			}
			if outcome.ApprovalRequirement != decision.ApprovalStrongAuth {
				t.Errorf("确认强度为 %s，高风险恒为 strong_auth", outcome.ApprovalRequirement)
			}
		})
	}
}

func TestCautiousMode_NonHighRisk_StillNeedsOnlyOneConfirmation(t *testing.T) {
	// 二次确认是「高风险」的规则，不是「谨慎模式」的规则。
	// 谨慎模式下一次普通的中风险写操作仍然只确认一次。
	input := mediumWrite()
	input.Mode = decision.ModeCautious

	outcome := decision.Decide(input)
	if outcome.Verdict != decision.VerdictRequireApproval {
		t.Fatalf("结论为 %s，期望 require_approval", outcome.Verdict)
	}
	if outcome.Confirmations != 1 {
		t.Errorf("确认次数为 %d，期望 1", outcome.Confirmations)
	}
	if outcome.ApprovalRequirement != decision.ApprovalStandard {
		t.Errorf("确认强度为 %s，期望 standard", outcome.ApprovalRequirement)
	}
}

func TestCautiousMode_WriteWithinLearnedScope_StillAsks(t *testing.T) {
	// REQ-DECIDE-003 AC4：谨慎模式下写操作即使命中 Trust Memory 也仍然询问。
	input := mediumWrite()
	input.Mode = decision.ModeCautious
	input.ReadOnlyAutoAllow = true
	input.Learned = []decision.Grant{grantCovering(baseScope())}

	assertOutcome(t, decision.Decide(input),
		decision.VerdictRequireApproval, decision.ReasonRequiresConfirmation)
}

// ——— 平衡模式：PRD §11.2 ———

func TestBalancedMode_CoversEveryRuleFromThePRD(t *testing.T) {
	balanced := func(apply func(*decision.Input)) decision.Input {
		input := lowRiskRead()
		apply(&input)
		return input
	}

	cases := []struct {
		rule    string
		input   decision.Input
		verdict decision.Verdict
	}{
		{"低风险读取自动允许", balanced(func(*decision.Input) {}), decision.VerdictAutoAllow},
		{"中风险首次询问", balanced(func(i *decision.Input) {
			*i = mediumWrite()
		}), decision.VerdictRequireApproval},
		{"完全匹配 Trust Memory 时自动允许", balanced(func(i *decision.Input) {
			*i = mediumWrite()
			i.Learned = []decision.Grant{grantCovering(baseScope())}
		}), decision.VerdictAutoAllow},
		{"高风险始终询问", balanced(func(i *decision.Input) {
			*i = mediumWrite()
			i.Assessment.Level = risk.LevelHigh
			i.Learned = []decision.Grant{grantCovering(baseScope())}
		}), decision.VerdictRequireApproval},
		{"范围扩大重新询问", balanced(func(i *decision.Input) {
			*i = mediumWrite()
			i.Learned = []decision.Grant{grantForAnotherRecord()}
		}), decision.VerdictRequireApproval},
	}

	for _, testCase := range cases {
		t.Run(testCase.rule, func(t *testing.T) {
			if got := decision.Decide(testCase.input).Verdict; got != testCase.verdict {
				t.Errorf("结论为 %s，期望 %s", got, testCase.verdict)
			}
		})
	}
}

// ——— 自动模式：PRD §11.3 ———

func TestAutomaticMode_CoversEveryRuleFromThePRD(t *testing.T) {
	automatic := func(apply func(*decision.Input)) decision.Input {
		input := lowRiskRead()
		input.Mode = decision.ModeAutomatic
		apply(&input)
		return input
	}

	cases := []struct {
		rule    string
		input   decision.Input
		verdict decision.Verdict
	}{
		{"低风险读取自动执行", automatic(func(*decision.Input) {}), decision.VerdictAutoAllow},
		{"低风险写操作也自动执行", automatic(func(i *decision.Input) {
			i.Write = true
		}), decision.VerdictAutoAllow},
		{"已学习的中风险操作自动执行", automatic(func(i *decision.Input) {
			*i = mediumWrite()
			i.Mode = decision.ModeAutomatic
			i.Learned = []decision.Grant{grantCovering(baseScope())}
		}), decision.VerdictAutoAllow},
		{"新身份仍然询问", automatic(func(i *decision.Input) {
			i.Write = true
			i.NewIdentity = true
		}), decision.VerdictRequireApproval},
		{"新资源仍然询问", automatic(func(i *decision.Input) {
			i.Write = true
			i.NewResource = true
		}), decision.VerdictRequireApproval},
		{"高风险仍然询问", automatic(func(i *decision.Input) {
			*i = mediumWrite()
			i.Mode = decision.ModeAutomatic
			i.Assessment.Level = risk.LevelHigh
			i.Learned = []decision.Grant{grantCovering(baseScope())}
		}), decision.VerdictRequireApproval},
	}

	for _, testCase := range cases {
		t.Run(testCase.rule, func(t *testing.T) {
			if got := decision.Decide(testCase.input).Verdict; got != testCase.verdict {
				t.Errorf("结论为 %s，期望 %s", got, testCase.verdict)
			}
		})
	}
}

func TestAutomaticMode_NewIdentityOrResource_DoesNotBlockReads(t *testing.T) {
	// 「新身份或新资源询问」收紧的是自动模式**多出来**的那部分（低风险写）。
	// 若它也拦住读操作，自动模式就会比平衡模式更严，单调性不成立。
	for _, apply := range []func(*decision.Input){
		func(i *decision.Input) { i.NewIdentity = true },
		func(i *decision.Input) { i.NewResource = true },
	} {
		input := lowRiskRead()
		input.Mode = decision.ModeAutomatic
		apply(&input)

		assertOutcome(t, decision.Decide(input), decision.VerdictAutoAllow, decision.ReasonLowRisk)
	}
}

// ——— 跨模式的不可协商约束（REQ-DECIDE-003 AC3/AC6）———

// strictness 把结论排成可比较的序：拒绝最严，自动放行最松。
func strictness(outcome decision.Outcome) int {
	switch outcome.Verdict {
	case decision.VerdictDeny:
		return 3
	case decision.VerdictRequireApproval:
		return 2
	default:
		return 1
	}
}

// enumerateAllModes 穷举会影响结论的开关，每组输入在三种模式下各跑一次。
func enumerateAllModes(t *testing.T, visit func(cautious, balanced, automatic decision.Input)) {
	t.Helper()

	switches := []func(*decision.Input){
		func(i *decision.Input) { i.Write = true },
		func(i *decision.Input) { i.ReadOnlyAutoAllow = true },
		func(i *decision.Input) { i.NewService = true },
		func(i *decision.Input) { i.IdentityChanged = true },
		func(i *decision.Input) { i.NewIdentity = true },
		func(i *decision.Input) { i.NewResource = true },
		func(i *decision.Input) { i.AgentTrust = agentauth.TrustUnverified },
		func(i *decision.Input) { i.Match.NeedsReview = true },
		func(i *decision.Input) { i.Match = ambiguousMatch() },
		func(i *decision.Input) { i.Scope.Ambiguous = true },
		func(i *decision.Input) { i.Scope.Scope = baseScope() },
		func(i *decision.Input) { i.Learned = []decision.Grant{grantCovering(baseScope())} },
		func(i *decision.Input) { i.Learned = append(i.Learned, grantForAnotherRecord()) },
	}
	levels := []risk.Level{risk.LevelLow, risk.LevelMedium, risk.LevelHigh}

	for mask := 0; mask < 1<<len(switches); mask++ {
		for _, level := range levels {
			build := func(mode decision.Mode) decision.Input {
				input := lowRiskRead()
				input.Mode = mode
				input.Assessment.Level = level
				for index, apply := range switches {
					if mask&(1<<index) != 0 {
						apply(&input)
					}
				}
				return input
			}
			visit(
				build(decision.ModeCautious),
				build(decision.ModeBalanced),
				build(decision.ModeAutomatic),
			)
		}
	}
}

func TestModes_AreMonotonicInStrictness(t *testing.T) {
	// AC6：同一请求在三种模式下的结论构成单调关系 ——
	// 谨慎不比平衡宽松，平衡不比自动宽松。确认次数同样单调。
	enumerateAllModes(t, func(cautious, balanced, automatic decision.Input) {
		strict := decision.Decide(cautious)
		middle := decision.Decide(balanced)
		loose := decision.Decide(automatic)

		if strictness(strict) < strictness(middle) {
			t.Fatalf("谨慎模式比平衡模式宽松：%s < %s（%+v）",
				strict.Verdict, middle.Verdict, cautious)
		}
		if strictness(middle) < strictness(loose) {
			t.Fatalf("平衡模式比自动模式宽松：%s < %s（%+v）",
				middle.Verdict, loose.Verdict, balanced)
		}
		if strict.Confirmations < middle.Confirmations ||
			middle.Confirmations < loose.Confirmations {
			t.Fatalf("确认次数不单调：%d / %d / %d（%+v）",
				strict.Confirmations, middle.Confirmations, loose.Confirmations, balanced)
		}
	})
}

func TestModes_NoConfigurationLetsHighRiskExecuteAutomatically(t *testing.T) {
	// AC3：三种模式 × 全部配置项穷举后，高风险无一自动执行。
	enumerateAllModes(t, func(cautious, balanced, automatic decision.Input) {
		for _, input := range []decision.Input{cautious, balanced, automatic} {
			outcome := decision.Decide(input)
			if input.Assessment.Level != risk.LevelHigh {
				continue
			}
			if outcome.Verdict == decision.VerdictAutoAllow {
				t.Fatalf("%s 模式下高风险被自动放行：%+v", input.Mode, input)
			}
			if outcome.Verdict == decision.VerdictRequireApproval &&
				outcome.ApprovalRequirement != decision.ApprovalStrongAuth {
				t.Fatalf("%s 模式下高风险的确认强度为 %s：%+v",
					input.Mode, outcome.ApprovalRequirement, input)
			}
			// 原因也必须如实说是高风险：落到默认分支的话，Gate 页面上
			// 一次高风险操作会显示成「需要确认」，用户看不出它高在哪
			// （REQ-DECIDE-001 AC3）。前两个分支比它更靠前，允许覆盖它。
			switch outcome.Reason {
			case decision.ReasonFailClosed, decision.ReasonForbidden, decision.ReasonHighRisk:
			default:
				t.Fatalf("%s 模式下高风险的原因为 %s：%+v", input.Mode, outcome.Reason, input)
			}
		}
	})
}

func TestModes_DoNotChangeForbiddenListOrScope(t *testing.T) {
	// 模式只能收紧或放开两个自动放行分支，动不了禁止列表与 Scope 收敛结果。
	for _, mode := range decision.Modes() {
		t.Run(string(mode)+" 下禁止列表不变", func(t *testing.T) {
			input := lowRiskRead()
			input.Mode = mode
			input.ReadOnlyAutoAllow = true
			input.Scope.Scope.Service = decision.SelfService
			input.Scope.Scope.Operation = "settings.update"

			outcome := decision.Decide(input)
			assertOutcome(t, outcome, decision.VerdictDeny, decision.ReasonForbidden)
			if outcome.Forbidden != decision.ForbiddenSelfConfiguration {
				t.Errorf("类别为 %s，期望 self_configuration", outcome.Forbidden)
			}
		})

		t.Run(string(mode)+" 下 Scope 收敛结果不变", func(t *testing.T) {
			input := lowRiskRead()
			input.Mode = mode
			input.ReadOnlyAutoAllow = true
			before := input.Scope.Scope

			decision.Decide(input)

			if !reflect.DeepEqual(input.Scope.Scope, before) {
				t.Errorf("决策改动了 Scope：%+v", input.Scope.Scope)
			}
		})
	}
}

func TestModes_DoNotChangeFailClosedOutcomes(t *testing.T) {
	// 十种 Fail Closed 在三种模式下结论一致：模式收紧得了放行，放松不了拒绝。
	for _, mode := range decision.Modes() {
		for _, blocker := range decision.Blockers() {
			if blocker == decision.BlockerPolicyEngineFailure {
				// 它同时是「模式认不出」的落点，用合法模式构造不出对照。
				continue
			}
			input := lowRiskRead()
			input.Mode = mode
			input.ReadOnlyAutoAllow = true
			input.Blockers = []decision.Blocker{blocker}

			outcome := decision.Decide(input)
			if outcome.Verdict != decision.VerdictDeny || outcome.Blocker != blocker {
				t.Errorf("%s 模式下 %s 的结论为 %s/%s，期望 deny/%s",
					mode, blocker, outcome.Verdict, outcome.Blocker, blocker)
			}
		}
	}
}
