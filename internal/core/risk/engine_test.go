package risk_test

import (
	"reflect"
	"testing"

	adapters "github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/core/risk"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * core/risk 的行为用例（REQ-RISK-001/002/003）。
 *
 * 本包是纯函数：十三个因子由调用方算好后传入。REQ-RISK-001 AC2 的「不发起网络请求、
 * 不调用模型」由 test/arch 的 TestCore_DoesNotReachOutsideItself 守住 ——
 * 一个包只要导入了 net/http 就已经有能力发请求了，用导入关系判定比用调用点更可靠。
 */

// baseFactors 是一次读操作的合法因子集：其余用例只写自己关心的差异。
func baseFactors() risk.Factors {
	return risk.Factors{
		Write:         false,
		Reversible:    true,
		ResourceCount: 1,
		AgentTrust:    agentauth.TrustKnown,
		DeviceTrust:   agentauth.DeviceTrusted,
		DeclaredLabel: adapters.RiskLabelLow,
	}
}

func evaluate(t *testing.T, factors risk.Factors) risk.Assessment {
	t.Helper()

	assessment, err := risk.Evaluate(factors)
	if err != nil {
		t.Fatalf("计算风险失败：%v", err)
	}
	return assessment
}

func hasFactor(assessment risk.Assessment, wanted risk.Factor) bool {
	for _, factor := range assessment.Factors {
		if factor == wanted {
			return true
		}
	}
	return false
}

// ——— PRD §12 的分级基线（REQ-RISK-002 AC1）———

func TestEvaluate_EveryExampleFromThePRD_LandsOnItsDocumentedLevel(t *testing.T) {
	// PRD §12 逐条列出的二十个示例操作。因子按该操作的实际形状填写，
	// 声明标签取 PRD 给的那一级 —— 两者算出来必须落在同一格。
	write := func(apply func(*risk.Factors)) risk.Factors {
		factors := baseFactors()
		factors.Write = true
		factors.DeclaredLabel = adapters.RiskLabelMedium
		apply(&factors)
		return factors
	}
	read := func(apply func(*risk.Factors)) risk.Factors {
		factors := baseFactors()
		apply(&factors)
		return factors
	}

	cases := []struct {
		operation string
		factors   risk.Factors
		expected  risk.Level
	}{
		// PRD §12.1 低风险。
		{"读取仓库", read(func(*risk.Factors) {}), risk.LevelLow},
		{"查询日志", read(func(*risk.Factors) {}), risk.LevelLow},
		{"查看部署", read(func(*risk.Factors) {}), risk.LevelLow},
		{"查询 DNS", read(func(f *risk.Factors) { f.Production = true }), risk.LevelLow},
		{"获取模型列表", read(func(f *risk.Factors) { f.ResourceCount = 0 }), risk.LevelLow},
		{"查询资源状态", read(func(*risk.Factors) {}), risk.LevelLow},
		{"读取非敏感配置", read(func(*risk.Factors) {}), risk.LevelLow},

		// PRD §12.2 中风险。
		{"创建 Pull Request", write(func(*risk.Factors) {}), risk.LevelMedium},
		{"修改单条 DNS", write(func(f *risk.Factors) { f.Production = true }), risk.LevelMedium},
		{"创建测试部署", write(func(*risk.Factors) {}), risk.LevelMedium},
		{"发送测试邮件", write(func(f *risk.Factors) { f.ExternalCommunication = true }), risk.LevelMedium},
		{"更新限定配置", write(func(*risk.Factors) {}), risk.LevelMedium},
		{"创建 Issue", write(func(*risk.Factors) {}), risk.LevelMedium},
		{"上传非敏感文件", write(func(*risk.Factors) {}), risk.LevelMedium},

		// PRD §12.3 高风险。
		{"删除资源", write(func(f *risk.Factors) {
			f.Destructive = true
			f.Reversible = false
			f.DeclaredLabel = adapters.RiskLabelHigh
		}), risk.LevelHigh},
		{"生产发布", write(func(f *risk.Factors) {
			f.Production = true
			f.DeclaredLabel = adapters.RiskLabelHigh
		}), risk.LevelHigh},
		{"修改账户权限", write(func(f *risk.Factors) {
			f.PermissionChange = true
			f.DeclaredLabel = adapters.RiskLabelHigh
		}), risk.LevelHigh},
		{"邀请成员", write(func(f *risk.Factors) {
			f.PermissionChange = true
			f.ExternalCommunication = true
			f.DeclaredLabel = adapters.RiskLabelHigh
		}), risk.LevelHigh},
		{"创建 API Token", write(func(f *risk.Factors) {
			f.SecretAccess = true
			f.DeclaredLabel = adapters.RiskLabelHigh
		}), risk.LevelHigh},
		{"读取 Secret", read(func(f *risk.Factors) {
			f.SecretAccess = true
			f.DeclaredLabel = adapters.RiskLabelHigh
		}), risk.LevelHigh},
		{"修改认证", write(func(f *risk.Factors) {
			f.PermissionChange = true
			f.Reversible = false
			f.DeclaredLabel = adapters.RiskLabelHigh
		}), risk.LevelHigh},
		{"修改账单", write(func(f *risk.Factors) {
			f.Billing = true
			f.DeclaredLabel = adapters.RiskLabelHigh
		}), risk.LevelHigh},
		{"支付", write(func(f *risk.Factors) {
			f.Billing = true
			f.Reversible = false
			f.DeclaredLabel = adapters.RiskLabelHigh
		}), risk.LevelHigh},
		{"转账", write(func(f *risk.Factors) {
			f.Billing = true
			f.Reversible = false
			f.DeclaredLabel = adapters.RiskLabelHigh
		}), risk.LevelHigh},
		{"删除数据库", write(func(f *risk.Factors) {
			f.Destructive = true
			f.Reversible = false
			f.DeclaredLabel = adapters.RiskLabelHigh
		}), risk.LevelHigh},
		{"批量修改", write(func(f *risk.Factors) {
			f.ResourceCount = 40
			f.DeclaredLabel = adapters.RiskLabelHigh
		}), risk.LevelHigh},
		{"不可逆操作", write(func(f *risk.Factors) {
			f.Reversible = false
			f.DeclaredLabel = adapters.RiskLabelHigh
		}), risk.LevelHigh},
	}

	if len(cases) != 27 {
		t.Fatalf("示例操作有 %d 条，PRD §12 列了 27 条（7 低 + 7 中 + 13 高）", len(cases))
	}

	for _, testCase := range cases {
		t.Run(testCase.operation+" 为 "+string(testCase.expected), func(t *testing.T) {
			if got := evaluate(t, testCase.factors).Level; got != testCase.expected {
				t.Errorf("算出 %s，PRD §12 列为 %s", got, testCase.expected)
			}
		})
	}
}

// ——— 删除与不可逆恒为 high（REQ-RISK-002 AC3）———

func TestEvaluate_DestructiveOrIrreversible_IsHighEvenWhenEverythingElseIsFavourable(t *testing.T) {
	// 最有利的上下文：声明为低风险、已受信 Agent、已确认设备、非生产、
	// 不是首次、没有超出历史。Trust Memory 与自动化等级根本不是本包的输入。
	favourable := func() risk.Factors {
		factors := baseFactors()
		factors.Write = true
		factors.AgentTrust = agentauth.TrustTrusted
		factors.DeclaredLabel = adapters.RiskLabelLow
		return factors
	}

	cases := []struct {
		name   string
		apply  func(*risk.Factors)
		factor risk.Factor
	}{
		{"删除", func(f *risk.Factors) { f.Destructive = true }, risk.FactorDestructive},
		{"不可逆", func(f *risk.Factors) { f.Reversible = false }, risk.FactorIrreversible},
		{"改权限", func(f *risk.Factors) { f.PermissionChange = true }, risk.FactorPermissionChange},
		{"碰 Secret", func(f *risk.Factors) { f.SecretAccess = true }, risk.FactorSecretAccess},
		{"碰账单", func(f *risk.Factors) { f.Billing = true }, risk.FactorBilling},
		{"批量", func(f *risk.Factors) { f.ResourceCount = 2 }, risk.FactorBulkChange},
		{"数量不确定", func(f *risk.Factors) { f.ResourceCount = 0 }, risk.FactorBulkChange},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"恒为高风险", func(t *testing.T) {
			factors := favourable()
			testCase.apply(&factors)

			assessment := evaluate(t, factors)
			if assessment.Level != risk.LevelHigh {
				t.Errorf("算出 %s，期望 high", assessment.Level)
			}
			if !hasFactor(assessment, testCase.factor) {
				t.Errorf("原因里没有 %s：%v", testCase.factor, assessment.Factors)
			}
		})
	}
}

func TestFactors_HasNoInputForModeOrTrustMemory(t *testing.T) {
	// REQ-RISK-002 AC3：删除与不可逆不受 Trust Memory 与自动化等级影响。
	// 让两者根本不是输入，比在代码里记得不去读它们可靠。
	expected := []string{
		"Write", "Production", "Reversible", "ResourceCount",
		"Destructive", "PermissionChange", "SecretAccess", "Billing",
		"ExternalCommunication", "FirstSeen", "BeyondHistory",
		"AgentTrust", "DeviceTrust", "DeclaredLabel",
	}

	structType := reflect.TypeOf(risk.Factors{})
	names := make([]string, 0, structType.NumField())
	for index := 0; index < structType.NumField(); index++ {
		names = append(names, structType.Field(index).Name)
	}

	if !reflect.DeepEqual(names, expected) {
		t.Fatalf("Factors 的字段为 %v，期望 %v", names, expected)
	}
}

// ——— 上下文抬升 ———

func TestEvaluate_ProductionWrite_IsAtLeastMedium(t *testing.T) {
	// REQ-INTENT-003 AC1：生产环境身份发起的任何写操作，风险不低于中风险。
	factors := baseFactors()
	factors.Write = true
	factors.Production = true

	assessment := evaluate(t, factors)
	if assessment.Level != risk.LevelMedium {
		t.Errorf("算出 %s，期望 medium", assessment.Level)
	}
	if !hasFactor(assessment, risk.FactorProductionWrite) {
		t.Errorf("原因里没有 production_write：%v", assessment.Factors)
	}
}

func TestEvaluate_BeyondHistory_RaisesOneLevelAboveTheBaseline(t *testing.T) {
	// REQ-RISK-003：范围超出已学习授权时上调。
	// AC1（api → www）与 AC2（TeleCall → Finance）在本层都表现为同一个因子。
	cases := []struct {
		label    adapters.RiskLabel
		expected risk.Level
	}{
		{adapters.RiskLabelLow, risk.LevelMedium},
		{adapters.RiskLabelMedium, risk.LevelHigh},
		{adapters.RiskLabelHigh, risk.LevelHigh},
	}

	for _, testCase := range cases {
		t.Run(string(testCase.label)+" 上调为 "+string(testCase.expected), func(t *testing.T) {
			factors := baseFactors()
			factors.Write = true
			factors.BeyondHistory = true
			factors.DeclaredLabel = testCase.label

			assessment := evaluate(t, factors)
			if assessment.Level != testCase.expected {
				t.Errorf("算出 %s，期望 %s", assessment.Level, testCase.expected)
			}
			if !hasFactor(assessment, risk.FactorBeyondHistory) {
				t.Errorf("原因里没有 beyond_history：%v", assessment.Factors)
			}
		})
	}
}

func TestEvaluate_Escalations_AreRelativeToTheBaselineNotToEachOther(t *testing.T) {
	// 每条规则各自决定「把等级抬到哪里」，抬升相对基线计算，最终取最高的一个。
	// 若改成相对上一条规则的结果，规则表的顺序就会改变算出来的等级 ——
	// 下面两组都会变成 high，而它们本来只是「一次超出历史的普通写操作」。
	cases := []struct {
		name  string
		apply func(*risk.Factors)
	}{
		{"生产写 + 超出历史", func(f *risk.Factors) {
			f.Production = true
			f.BeyondHistory = true
		}},
		{"首次出现 + 超出历史", func(f *risk.Factors) {
			f.FirstSeen = true
			f.BeyondHistory = true
		}},
		{"Agent 未确认 + 超出历史", func(f *risk.Factors) {
			f.AgentTrust = agentauth.TrustUnverified
			f.BeyondHistory = true
		}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"仍为中风险", func(t *testing.T) {
			factors := baseFactors()
			factors.Write = true
			testCase.apply(&factors)

			if got := evaluate(t, factors).Level; got != risk.LevelMedium {
				t.Errorf("算出 %s，期望 medium —— 两条规则都从 low 起算，不该叠加", got)
			}
		})
	}
}

func TestEvaluate_FirstSeenOrUnverifiedOrUntrusted_RaisesWritesToMedium(t *testing.T) {
	cases := []struct {
		name   string
		apply  func(*risk.Factors)
		factor risk.Factor
	}{
		{"首次出现", func(f *risk.Factors) { f.FirstSeen = true }, risk.FactorFirstSeen},
		{"Agent 未确认", func(f *risk.Factors) {
			f.AgentTrust = agentauth.TrustUnverified
		}, risk.FactorAgentUnverified},
		{"设备未确认", func(f *risk.Factors) {
			f.DeviceTrust = agentauth.DeviceUntrusted
		}, risk.FactorUntrustedDevice},
		{"对外通信", func(f *risk.Factors) {
			f.ExternalCommunication = true
		}, risk.FactorExternalCommunication},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"把写操作抬到中风险", func(t *testing.T) {
			factors := baseFactors()
			factors.Write = true
			testCase.apply(&factors)

			assessment := evaluate(t, factors)
			if assessment.Level != risk.LevelMedium {
				t.Errorf("算出 %s，期望 medium", assessment.Level)
			}
			if !hasFactor(assessment, testCase.factor) {
				t.Errorf("原因里没有 %s：%v", testCase.factor, assessment.Factors)
			}
		})
	}
}

func TestEvaluate_ReadStaysLow_InTheMostHostileContext(t *testing.T) {
	// PRD §11.2 与 §12.1：低风险读取自动允许。读一个没读过的资源仍然只是读，
	// 所以上下文抬升全部要求写操作 —— 否则「GitHub 仓库读取不弹审批」
	// （REQ-DECIDE-003 AC1）在第一次读时就不成立了。
	factors := baseFactors()
	factors.Production = true
	factors.FirstSeen = true
	factors.BeyondHistory = true
	factors.ExternalCommunication = true
	factors.AgentTrust = agentauth.TrustUnverified
	factors.DeviceTrust = agentauth.DeviceUntrusted
	factors.ResourceCount = 0

	assessment := evaluate(t, factors)
	if assessment.Level != risk.LevelLow {
		t.Errorf("算出 %s，期望 low", assessment.Level)
	}
	if !hasFactor(assessment, risk.FactorReadOnly) {
		t.Errorf("原因里没有 read_only：%v", assessment.Factors)
	}
}

// ——— 确定性与 Fail Closed ———

func TestEvaluate_SameFactors_ProduceTheSameAssessment(t *testing.T) {
	// AC1：相同输入必须产生相同风险等级。等级与原因列表都逐项比对 ——
	// 只比等级的话，原因顺序漂移不会被发现，而它会让审批页面每次显示得不一样。
	factors := baseFactors()
	factors.Write = true
	factors.Production = true
	factors.FirstSeen = true
	factors.BeyondHistory = true
	factors.AgentTrust = agentauth.TrustUnverified

	first := evaluate(t, factors)
	for round := 0; round < 1000; round++ {
		if again := evaluate(t, factors); !reflect.DeepEqual(again, first) {
			t.Fatalf("第 %d 轮算出 %+v，首轮为 %+v", round, again, first)
		}
	}
}

func TestEvaluate_AlwaysGivesAtLeastOneReason(t *testing.T) {
	// AC3：输出至少一条人类可读的原因，供 Access Folio 展示。
	// 什么都没触发时基线本身就是原因，第一条恒为 adapter_declared_label。
	for _, label := range []adapters.RiskLabel{
		adapters.RiskLabelLow, adapters.RiskLabelMedium, adapters.RiskLabelHigh,
	} {
		factors := baseFactors()
		factors.DeclaredLabel = label

		assessment := evaluate(t, factors)
		if len(assessment.Factors) == 0 {
			t.Fatalf("标签 %s 的结论没有给出任何原因", label)
		}
		if assessment.Factors[0] != risk.FactorDeclaredLabel {
			t.Errorf("第一条原因为 %s，期望 adapter_declared_label", assessment.Factors[0])
		}
	}
}

func TestEvaluate_UnknownInput_IsRefused(t *testing.T) {
	// REQ-RISK-002 AC2 与 PRD §6.3：风险等级未知一律拒绝，不落到某个默认值上。
	cases := []struct {
		name  string
		apply func(*risk.Factors)
	}{
		{"没有声明风险标签", func(f *risk.Factors) { f.DeclaredLabel = "" }},
		{"风险标签认不出来", func(f *risk.Factors) { f.DeclaredLabel = "critical" }},
		{"Agent 信任等级为空", func(f *risk.Factors) { f.AgentTrust = "" }},
		{"Agent 信任等级认不出来", func(f *risk.Factors) { f.AgentTrust = "root" }},
		{"设备信任状态为空", func(f *risk.Factors) { f.DeviceTrust = "" }},
		{"设备信任状态认不出来", func(f *risk.Factors) { f.DeviceTrust = "maybe" }},
		{"资源数量为负", func(f *risk.Factors) { f.ResourceCount = -1 }},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"时拒绝", func(t *testing.T) {
			factors := baseFactors()
			testCase.apply(&factors)

			assessment, err := risk.Evaluate(factors)
			if err == nil {
				t.Fatalf("期望拒绝，实际算出 %+v", assessment)
			}
			if !apperr.Is(err, apperr.CodeInvalidRequest) {
				t.Errorf("错误码为 %s，期望 invalid_request", apperr.CodeOf(err))
			}
			if assessment.Level != "" {
				t.Errorf("拒绝时仍返回了等级 %s", assessment.Level)
			}
		})
	}
}

// ——— 穷举出来的不变量 ———

// enumerate 穷举九个布尔因子 × 三个声明标签 × 三种资源数量 ×
// 三个信任等级 × 两种设备状态，共 512 × 3 × 3 × 3 × 2 = 27648 组输入。
func enumerate(t *testing.T, visit func(risk.Factors)) {
	t.Helper()

	booleans := []func(*risk.Factors, bool){
		func(f *risk.Factors, on bool) { f.Write = on },
		func(f *risk.Factors, on bool) { f.Production = on },
		func(f *risk.Factors, on bool) { f.Reversible = on },
		func(f *risk.Factors, on bool) { f.Destructive = on },
		func(f *risk.Factors, on bool) { f.PermissionChange = on },
		func(f *risk.Factors, on bool) { f.SecretAccess = on },
		func(f *risk.Factors, on bool) { f.Billing = on },
		func(f *risk.Factors, on bool) { f.ExternalCommunication = on },
		func(f *risk.Factors, on bool) { f.FirstSeen = on },
		func(f *risk.Factors, on bool) { f.BeyondHistory = on },
	}
	labels := []adapters.RiskLabel{
		adapters.RiskLabelLow, adapters.RiskLabelMedium, adapters.RiskLabelHigh,
	}
	counts := []int{0, 1, 7}
	trusts := []agentauth.TrustLevel{
		agentauth.TrustUnverified, agentauth.TrustKnown, agentauth.TrustTrusted,
	}
	devices := []agentauth.DeviceTrust{agentauth.DeviceTrusted, agentauth.DeviceUntrusted}

	for mask := 0; mask < 1<<len(booleans); mask++ {
		for _, label := range labels {
			for _, count := range counts {
				for _, trust := range trusts {
					for _, device := range devices {
						factors := risk.Factors{
							ResourceCount: count,
							AgentTrust:    trust,
							DeviceTrust:   device,
							DeclaredLabel: label,
						}
						for index, set := range booleans {
							set(&factors, mask&(1<<index) != 0)
						}
						visit(factors)
					}
				}
			}
		}
	}
}

func TestEvaluate_NeverFallsBelowTheDeclaredLabel(t *testing.T) {
	// 计算结果只会等于或高于 Adapter 的声明。低于它意味着一个被声明为高风险的
	// 操作可以因为「上下文看起来很安全」而被降级 —— 那正是不该存在的路径。
	order := map[risk.Level]int{risk.LevelLow: 1, risk.LevelMedium: 2, risk.LevelHigh: 3}
	declared := map[adapters.RiskLabel]risk.Level{
		adapters.RiskLabelLow:    risk.LevelLow,
		adapters.RiskLabelMedium: risk.LevelMedium,
		adapters.RiskLabelHigh:   risk.LevelHigh,
	}

	enumerate(t, func(factors risk.Factors) {
		assessment, err := risk.Evaluate(factors)
		if err != nil {
			t.Fatalf("合法输入被拒绝：%+v（%v）", factors, err)
		}
		if order[assessment.Level] < order[declared[factors.DeclaredLabel]] {
			t.Fatalf("声明为 %s 的操作算出 %s：%+v",
				factors.DeclaredLabel, assessment.Level, factors)
		}
	})
}

func TestEvaluate_TurningAnyFactorOn_NeverLowersTheLevel(t *testing.T) {
	// 单调性：每个因子都是风险的来源，打开它不该让等级下降。
	// 这条不变量不重算一遍规则，因此不会跟着实现一起错。
	order := map[risk.Level]int{risk.LevelLow: 1, risk.LevelMedium: 2, risk.LevelHigh: 3}
	switches := []struct {
		name string
		on   func(*risk.Factors)
	}{
		{"production", func(f *risk.Factors) { f.Production = true }},
		{"destructive", func(f *risk.Factors) { f.Destructive = true }},
		{"permission_change", func(f *risk.Factors) { f.PermissionChange = true }},
		{"secret_access", func(f *risk.Factors) { f.SecretAccess = true }},
		{"billing", func(f *risk.Factors) { f.Billing = true }},
		{"external_communication", func(f *risk.Factors) { f.ExternalCommunication = true }},
		{"first_seen", func(f *risk.Factors) { f.FirstSeen = true }},
		{"beyond_history", func(f *risk.Factors) { f.BeyondHistory = true }},
		{"irreversible", func(f *risk.Factors) { f.Reversible = false }},
		{"untrusted_device", func(f *risk.Factors) { f.DeviceTrust = agentauth.DeviceUntrusted }},
		{"agent_unverified", func(f *risk.Factors) { f.AgentTrust = agentauth.TrustUnverified }},
	}

	enumerate(t, func(factors risk.Factors) {
		before, err := risk.Evaluate(factors)
		if err != nil {
			t.Fatalf("合法输入被拒绝：%+v（%v）", factors, err)
		}
		for _, current := range switches {
			raised := factors
			current.on(&raised)

			after, err := risk.Evaluate(raised)
			if err != nil {
				t.Fatalf("合法输入被拒绝：%+v（%v）", raised, err)
			}
			if order[after.Level] < order[before.Level] {
				t.Fatalf("打开 %s 后等级从 %s 降到 %s：%+v",
					current.name, before.Level, after.Level, factors)
			}
		}
	})
}

func TestEvaluate_EveryReportedFactor_IsADeclaredCode(t *testing.T) {
	// 原因列表里不能出现码表之外的取值，也不能重复 ——
	// 审批页面按码做 i18n，认不出的码会显示成空白。
	declared := map[risk.Factor]bool{
		risk.FactorDeclaredLabel: true, risk.FactorReadOnly: true,
		risk.FactorDestructive: true, risk.FactorIrreversible: true,
		risk.FactorPermissionChange: true, risk.FactorSecretAccess: true,
		risk.FactorBilling: true, risk.FactorBulkChange: true,
		risk.FactorProductionWrite: true, risk.FactorBeyondHistory: true,
		risk.FactorFirstSeen: true, risk.FactorAgentUnverified: true,
		risk.FactorUntrustedDevice: true, risk.FactorExternalCommunication: true,
	}
	if len(declared) != 14 {
		t.Fatalf("码表有 %d 个取值，期望 14", len(declared))
	}

	enumerate(t, func(factors risk.Factors) {
		assessment, err := risk.Evaluate(factors)
		if err != nil {
			t.Fatalf("合法输入被拒绝：%+v（%v）", factors, err)
		}

		seen := make(map[risk.Factor]bool, len(assessment.Factors))
		for _, factor := range assessment.Factors {
			if !declared[factor] {
				t.Fatalf("原因 %s 不在码表里：%+v", factor, factors)
			}
			if seen[factor] {
				t.Fatalf("原因 %s 出现了两次：%v", factor, assessment.Factors)
			}
			seen[factor] = true
		}
		if len(assessment.Factors) == 0 {
			t.Fatalf("结论没有给出任何原因：%+v", factors)
		}
	})
}

func TestEvaluate_EveryCeilingFactor_MeansHigh(t *testing.T) {
	// 六个封顶因子出现在原因列表里时，等级必然是 high。
	ceilings := map[risk.Factor]bool{
		risk.FactorDestructive: true, risk.FactorIrreversible: true,
		risk.FactorPermissionChange: true, risk.FactorSecretAccess: true,
		risk.FactorBilling: true, risk.FactorBulkChange: true,
	}

	enumerate(t, func(factors risk.Factors) {
		assessment, err := risk.Evaluate(factors)
		if err != nil {
			t.Fatalf("合法输入被拒绝：%+v（%v）", factors, err)
		}
		for _, factor := range assessment.Factors {
			if ceilings[factor] && assessment.Level != risk.LevelHigh {
				t.Fatalf("命中 %s 却算出 %s：%+v", factor, assessment.Level, factors)
			}
		}
	})
}
