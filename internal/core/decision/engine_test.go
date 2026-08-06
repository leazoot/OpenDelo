package decision_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/risk"
	"github.com/Runcoor/opendelo/internal/core/scope"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * core/decision 的行为用例（REQ-DECIDE-001/002/004）。
 *
 * 本包是纯函数：匹配、收敛与风险的结论都由调用方算好后传入（ADR-003）。
 * 本文件在平衡模式下验证分支、顺序与 Fail Closed；三种自动化等级的规则
 * 与单调性在 mode_test.go。
 */

const (
	agentID     = "01K1AGENT0000000000000MAIN"
	memoryID    = "01K1MEMORY000000000000MAIN"
	otherRecord = "www.tele-call.cn"
)

func baseScope() scope.Scope {
	return scope.Scope{
		AgentID:      agentID,
		WorkspaceID:  fixtures.DefaultWorkspaceID,
		Service:      "cloudflare",
		IdentityID:   fixtures.DefaultIdentityID,
		Account:      "cloudflare-production",
		Resource:     map[string]string{"zone": "tele-call.cn", "record": "api.tele-call.cn"},
		ResourceKey:  "record=api.tele-call.cn;zone=tele-call.cn",
		Operation:    "dns.record.update",
		NotBefore:    fixtures.Instant,
		ExpiresAt:    fixtures.Instant.Add(15 * time.Minute),
		RequestLimit: 1,
		Environment:  matcher.EnvironmentProduction,
		RiskCeiling:  risk.LevelMedium,
	}
}

// lowRiskRead 是平衡模式下唯一会被自动放行的形状：低风险、读操作、
// 身份唯一、Scope 完整、没有已学习的授权要比对。
func lowRiskRead() decision.Input {
	return decision.Input{
		Mode:       decision.ModeBalanced,
		AgentID:    agentID,
		AgentTrust: agentauth.TrustKnown,
		Write:      false,
		Match: matcher.Result{
			Identity: fixtures.Identity(),
			Level:    matcher.MatchSoleIdentity,
		},
		Scope: scope.Result{Scope: readScope()},
		Assessment: risk.Assessment{
			Level:   risk.LevelLow,
			Factors: []risk.Factor{risk.FactorDeclaredLabel, risk.FactorReadOnly},
		},
	}
}

func readScope() scope.Scope {
	resolved := baseScope()
	resolved.Operation = "dns.record.list"
	resolved.RiskCeiling = risk.LevelLow
	return resolved
}

// mediumWrite 是一次中风险写操作，命中已学习授权时会被自动放行。
func mediumWrite() decision.Input {
	input := lowRiskRead()
	input.Write = true
	input.Scope = scope.Result{Scope: baseScope()}
	input.Assessment = risk.Assessment{
		Level:   risk.LevelMedium,
		Factors: []risk.Factor{risk.FactorDeclaredLabel, risk.FactorProductionWrite},
	}
	return input
}

func grantCovering(covered scope.Scope) decision.Grant {
	return decision.Grant{MemoryID: memoryID, Scope: covered}
}

// alwaysAskGrant 是一条用户特意收紧成「始终要求确认」的记忆。
func alwaysAskGrant(covered scope.Scope) decision.Grant {
	grant := grantCovering(covered)
	grant.AlwaysAsk = true
	return grant
}

func grantForAnotherRecord() decision.Grant {
	another := baseScope()
	another.Resource = map[string]string{"zone": "tele-call.cn", "record": otherRecord}
	another.ResourceKey = "record=" + otherRecord + ";zone=tele-call.cn"
	return decision.Grant{MemoryID: "01K1MEMORY00000000000OTHR", Scope: another}
}

func assertOutcome(t *testing.T, got decision.Outcome, verdict decision.Verdict, reason decision.Reason) {
	t.Helper()

	if got.Verdict != verdict {
		t.Errorf("结论为 %s，期望 %s（原因 %s）", got.Verdict, verdict, got.Reason)
	}
	if got.Reason != reason {
		t.Errorf("原因为 %s，期望 %s", got.Reason, reason)
	}
}

// ——— 七个分支各一个用例（REQ-DECIDE-001 AC1）———

func TestDecide_ForbiddenOperation_IsDeniedWithoutApproval(t *testing.T) {
	// 分支一。
	input := lowRiskRead()
	input.Scope.Scope.Service = decision.SelfService
	input.Scope.Scope.Operation = "audit.disable"

	outcome := decision.Decide(input)
	assertOutcome(t, outcome, decision.VerdictDeny, decision.ReasonForbidden)
	if outcome.Forbidden != decision.ForbiddenAuditDisable {
		t.Errorf("类别为 %s，期望 audit_disable", outcome.Forbidden)
	}
	if outcome.ApprovalRequirement != decision.ApprovalNone {
		t.Errorf("确认强度为 %s，禁止列表不该产生审批项", outcome.ApprovalRequirement)
	}
}

func TestDecide_HighRisk_AlwaysRequiresApproval(t *testing.T) {
	// 分支二。
	input := mediumWrite()
	input.Assessment.Level = risk.LevelHigh

	outcome := decision.Decide(input)
	assertOutcome(t, outcome, decision.VerdictRequireApproval, decision.ReasonHighRisk)
	if outcome.ApprovalRequirement != decision.ApprovalStrongAuth {
		t.Errorf("确认强度为 %s，高风险要走强认证（REQ-APPROVAL-005）", outcome.ApprovalRequirement)
	}
}

func TestDecide_BeyondLearnedScope_RequiresApproval(t *testing.T) {
	// 分支三：学过 www，这次要改 api。
	input := mediumWrite()
	input.Learned = []decision.Grant{grantForAnotherRecord()}

	assertOutcome(t, decision.Decide(input),
		decision.VerdictRequireApproval, decision.ReasonBeyondLearnedScope)
}

func TestDecide_AmbiguousIdentity_RequiresApproval(t *testing.T) {
	// 分支四。
	input := lowRiskRead()
	input.Match = matcher.Result{
		Ambiguous: true,
		Candidates: []matcher.Identity{
			fixtures.Identity(fixtures.WithIdentityID("01K1IDENTITY0000000000WORK")),
			fixtures.Identity(fixtures.WithIdentityID("01K1IDENTITY0000000000PERS")),
		},
	}

	outcome := decision.Decide(input)
	assertOutcome(t, outcome, decision.VerdictRequireApproval, decision.ReasonIdentityAmbiguous)
	if outcome.ApprovalRequirement != decision.ApprovalStandard {
		t.Errorf("确认强度为 %s，期望 standard", outcome.ApprovalRequirement)
	}
}

func TestDecide_LowRiskRead_IsAutoAllowed(t *testing.T) {
	// 分支五。对应成功标准 S3：平衡模式下 GitHub 仓库读取与 DNS 查询不弹审批。
	outcome := decision.Decide(lowRiskRead())
	assertOutcome(t, outcome, decision.VerdictAutoAllow, decision.ReasonLowRisk)
	if outcome.ApprovalRequirement != decision.ApprovalNone {
		t.Errorf("确认强度为 %s，自动放行不该要求确认", outcome.ApprovalRequirement)
	}
}

func TestDecide_MediumRiskWithinLearnedScope_IsAutoAllowed(t *testing.T) {
	// 分支六。
	input := mediumWrite()
	input.Learned = []decision.Grant{grantCovering(baseScope())}

	outcome := decision.Decide(input)
	assertOutcome(t, outcome, decision.VerdictAutoAllow, decision.ReasonTrustMemoryMatch)
	if outcome.MatchedMemoryID != memoryID {
		t.Errorf("命中的记忆为 %q，期望 %q", outcome.MatchedMemoryID, memoryID)
	}
}

func TestDecide_WiderLearnedGrant_CoversTheRequest(t *testing.T) {
	// 已学习的授权比这次请求宽（窗口更长、次数更多、风险上限更高）时算命中：
	// 覆盖判定的方向是「已学习的 ⊇ 这次的」。方向反过来就成了「这次的必须比
	// 学过的更宽才算数」，那既拦不住扩大，也会把正常的复用当成越界。
	wider := baseScope()
	wider.ExpiresAt = wider.NotBefore.Add(time.Hour)
	wider.RequestLimit = 10
	wider.RiskCeiling = risk.LevelHigh

	input := mediumWrite()
	input.Learned = []decision.Grant{{MemoryID: memoryID, Scope: wider}}

	assertOutcome(t, decision.Decide(input),
		decision.VerdictAutoAllow, decision.ReasonTrustMemoryMatch)
}

func TestDecide_GrantLearnedEarlier_StillMatchesAFreshRequest(t *testing.T) {
	// 已学习的授权是过去某一刻记下的，这次请求的窗口从「现在」起算。
	// 若时间窗口参与匹配，任何记忆都会立刻显得「不够宽」——
	// 「完全匹配 Trust Memory 时自动允许」就永远不成立。
	learned := baseScope()
	learned.NotBefore = fixtures.Instant.Add(-24 * time.Hour)
	learned.ExpiresAt = fixtures.Instant.Add(-24*time.Hour + 15*time.Minute)

	input := mediumWrite()
	input.Learned = []decision.Grant{{MemoryID: memoryID, Scope: learned}}

	outcome := decision.Decide(input)
	assertOutcome(t, outcome, decision.VerdictAutoAllow, decision.ReasonTrustMemoryMatch)
	if outcome.MatchedMemoryID != memoryID {
		t.Errorf("命中的记忆为 %q，期望 %q", outcome.MatchedMemoryID, memoryID)
	}
}

func TestDecide_NarrowerLearnedGrant_DoesNotCoverTheRequest(t *testing.T) {
	// 反过来：这次请求要的比学过的多，就是范围扩大，必须重新问
	// （Learn Without Expanding 在决策侧的落点）。
	narrower := baseScope()
	narrower.RequestLimit = 1

	input := mediumWrite()
	input.Scope.Scope.RequestLimit = 10
	input.Learned = []decision.Grant{{MemoryID: memoryID, Scope: narrower}}

	assertOutcome(t, decision.Decide(input),
		decision.VerdictRequireApproval, decision.ReasonBeyondLearnedScope)
}

func TestDecide_EverythingElse_RequiresApproval(t *testing.T) {
	// 分支七：中风险写操作，没学过 —— PRD §12.2 的「首次确认」。
	assertOutcome(t, decision.Decide(mediumWrite()),
		decision.VerdictRequireApproval, decision.ReasonRequiresConfirmation)
}

func TestDecide_LowRiskWrite_IsNotAutoAllowedInBalancedMode(t *testing.T) {
	// PRD §11.2 说的是「低风险**读取**自动允许」，不是「低风险自动允许」。
	// 一个被声明为低风险的写操作在平衡模式下仍然要问。
	input := lowRiskRead()
	input.Write = true

	assertOutcome(t, decision.Decide(input),
		decision.VerdictRequireApproval, decision.ReasonRequiresConfirmation)
}

// ——— 顺序不可调换（REQ-DECIDE-001 AC1）———

func TestDecide_BranchesAreEvaluatedInOrder(t *testing.T) {
	// 每个用例都让两个分支同时成立，断言命中的是靠前的那个。
	// 调换任意两行求值顺序，对应的用例就会失败。
	forbidden := func(input decision.Input) decision.Input {
		input.Scope.Scope.Service = decision.SelfService
		input.Scope.Scope.Operation = "settings.update"
		return input
	}

	cases := []struct {
		name   string
		input  decision.Input
		reason decision.Reason
	}{
		{"禁止列表先于高风险", func() decision.Input {
			input := forbidden(mediumWrite())
			input.Assessment.Level = risk.LevelHigh
			return input
		}(), decision.ReasonForbidden},

		{"禁止列表先于低风险读取", forbidden(lowRiskRead()), decision.ReasonForbidden},

		{"高风险先于超出已学习", func() decision.Input {
			input := mediumWrite()
			input.Assessment.Level = risk.LevelHigh
			input.Learned = []decision.Grant{grantForAnotherRecord()}
			return input
		}(), decision.ReasonHighRisk},

		{"高风险先于命中记忆", func() decision.Input {
			input := mediumWrite()
			input.Assessment.Level = risk.LevelHigh
			input.Learned = []decision.Grant{grantCovering(baseScope())}
			return input
		}(), decision.ReasonHighRisk},

		{"超出已学习先于身份歧义", func() decision.Input {
			input := mediumWrite()
			input.Learned = []decision.Grant{grantForAnotherRecord()}
			input.Match = ambiguousMatch()
			return input
		}(), decision.ReasonBeyondLearnedScope},

		{"身份歧义先于低风险读取", func() decision.Input {
			input := lowRiskRead()
			input.Match = ambiguousMatch()
			return input
		}(), decision.ReasonIdentityAmbiguous},

		{"身份歧义先于命中记忆", func() decision.Input {
			input := mediumWrite()
			input.Learned = []decision.Grant{grantCovering(baseScope())}
			input.Match = ambiguousMatch()
			return input
		}(), decision.ReasonIdentityAmbiguous},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := decision.Decide(testCase.input).Reason; got != testCase.reason {
				t.Errorf("原因为 %s，期望 %s", got, testCase.reason)
			}
		})
	}
}

func ambiguousMatch() matcher.Result {
	return matcher.Result{
		Ambiguous: true,
		Candidates: []matcher.Identity{
			fixtures.Identity(fixtures.WithIdentityID("01K1IDENTITY0000000000WORK")),
			fixtures.Identity(fixtures.WithIdentityID("01K1IDENTITY0000000000PERS")),
		},
	}
}

// ——— Fail Closed 的十种情况（REQ-DECIDE-002 AC1）———

func TestBlockers_AreExactlyTheTenFromThePRD(t *testing.T) {
	expected := []decision.Blocker{
		"agent_unidentified", "service_undetermined",
		"identity_ambiguity_unresolvable", "scope_undeterminable",
		"capability_not_offered", "risk_unknown", "policy_engine_failure",
		"gateway_offline", "credential_source_unavailable", "audit_write_failed",
	}

	if got := decision.Blockers(); !reflect.DeepEqual(got, expected) {
		t.Fatalf("阻断清单为 %v，期望 %v", got, expected)
	}
}

func TestDecide_EveryFailClosedSituation_IsDenied(t *testing.T) {
	// PRD §6.3 的十种情况各一个用例，结果均为拒绝。
	// 用例把输入调成本来会被自动放行的形状，再制造那一种不确定。
	cases := []struct {
		blocker decision.Blocker
		apply   func(*decision.Input)
	}{
		{decision.BlockerAgentUnidentified, func(i *decision.Input) { i.AgentID = "" }},
		{decision.BlockerServiceUndetermined, func(i *decision.Input) { i.Scope.Scope.Service = "" }},
		{decision.BlockerIdentityAmbiguityUnresolvable, func(i *decision.Input) {
			i.Match = matcher.Result{}
		}},
		{decision.BlockerScopeUndeterminable, func(i *decision.Input) {
			i.Scope.Scope.RequestLimit = 0
		}},
		{decision.BlockerCapabilityNotOffered, reportedBy(decision.BlockerCapabilityNotOffered)},
		{decision.BlockerRiskUnknown, func(i *decision.Input) { i.Assessment.Level = "" }},
		{decision.BlockerPolicyEngineFailure, reportedBy(decision.BlockerPolicyEngineFailure)},
		{decision.BlockerGatewayOffline, reportedBy(decision.BlockerGatewayOffline)},
		{
			decision.BlockerCredentialSourceUnavailable,
			reportedBy(decision.BlockerCredentialSourceUnavailable),
		},
		{decision.BlockerAuditWriteFailed, reportedBy(decision.BlockerAuditWriteFailed)},
	}

	if len(cases) != len(decision.Blockers()) {
		t.Fatalf("用例覆盖了 %d 种情况，清单里有 %d 种", len(cases), len(decision.Blockers()))
	}

	for _, testCase := range cases {
		t.Run(string(testCase.blocker)+" 时拒绝", func(t *testing.T) {
			input := lowRiskRead()
			testCase.apply(&input)

			outcome := decision.Decide(input)
			assertOutcome(t, outcome, decision.VerdictDeny, decision.ReasonFailClosed)
			if outcome.Blocker != testCase.blocker {
				t.Errorf("阻断为 %s，期望 %s", outcome.Blocker, testCase.blocker)
			}
			if outcome.ApprovalRequirement != decision.ApprovalNone {
				t.Errorf("确认强度为 %s，Fail Closed 不该产生审批项", outcome.ApprovalRequirement)
			}
		})
	}
}

func reportedBy(blocker decision.Blocker) func(*decision.Input) {
	return func(input *decision.Input) {
		input.Blockers = []decision.Blocker{blocker}
	}
}

func TestDecide_RiskWithoutAnyReason_IsTreatedAsUnknown(t *testing.T) {
	// 一个解释不了的等级与未知等级没有区别：Access Folio 上它显示不出原因，
	// 而 REQ-RISK-001 AC3 要求每个结论都能解释。
	input := lowRiskRead()
	input.Assessment.Factors = nil

	outcome := decision.Decide(input)
	assertOutcome(t, outcome, decision.VerdictDeny, decision.ReasonFailClosed)
	if outcome.Blocker != decision.BlockerRiskUnknown {
		t.Errorf("阻断为 %s，期望 risk_unknown", outcome.Blocker)
	}
}

func TestDecide_UnknownBlockerFromTheCaller_IsTreatedAsPolicyFailure(t *testing.T) {
	input := lowRiskRead()
	input.Blockers = []decision.Blocker{"looks_fine_to_me"}

	outcome := decision.Decide(input)
	assertOutcome(t, outcome, decision.VerdictDeny, decision.ReasonFailClosed)
	if outcome.Blocker != decision.BlockerPolicyEngineFailure {
		t.Errorf("阻断为 %s，期望 policy_engine_failure", outcome.Blocker)
	}
}

func TestDecide_AmbiguityWithFewerThanTwoCandidates_IsDenied(t *testing.T) {
	// 有歧义却拿不出两个候选：审批页面上没有可选的东西，问了也解决不了。
	for _, candidates := range [][]matcher.Identity{nil, {fixtures.Identity()}} {
		input := lowRiskRead()
		input.Match = matcher.Result{Ambiguous: true, Candidates: candidates}

		outcome := decision.Decide(input)
		assertOutcome(t, outcome, decision.VerdictDeny, decision.ReasonFailClosed)
		if outcome.Blocker != decision.BlockerIdentityAmbiguityUnresolvable {
			t.Errorf("阻断为 %s，期望 identity_ambiguity_unresolvable", outcome.Blocker)
		}
	}
}

func TestDecide_IdentityWithoutMatchLevel_IsDenied(t *testing.T) {
	// 身份与命中层级必须同进同出，与 decisions 表上的 CHECK 是同一条约束。
	cases := []matcher.Result{
		{Identity: fixtures.Identity()},
		{Level: matcher.MatchSoleIdentity},
	}

	for _, match := range cases {
		input := lowRiskRead()
		input.Match = match

		outcome := decision.Decide(input)
		assertOutcome(t, outcome, decision.VerdictDeny, decision.ReasonFailClosed)
	}
}

func TestDecide_UnknownMode_IsDenied(t *testing.T) {
	// 认不出的模式一律拒绝，不落到某个「看起来温和」的默认值上。
	//
	// 高风险那一组是必要的：低风险读取会先走到自动放行分支、在取模式规则时崩掉，
	// 于是「碰巧」也落到拒绝上；高风险在第二分支就命中，取不到模式规则也照样
	// 返回 require_approval。少了它，「模式必须先校验」这条就没人守。
	for _, mode := range []decision.Mode{"", "off", "balanced-ish", "AUTOMATIC"} {
		for _, level := range []risk.Level{risk.LevelLow, risk.LevelHigh} {
			input := lowRiskRead()
			input.Mode = mode
			input.Assessment.Level = level

			outcome := decision.Decide(input)
			assertOutcome(t, outcome, decision.VerdictDeny, decision.ReasonFailClosed)
			if outcome.Blocker != decision.BlockerPolicyEngineFailure {
				t.Errorf("模式 %q + %s 的阻断为 %s，期望 policy_engine_failure",
					mode, level, outcome.Blocker)
			}
		}
	}
}

// ——— 禁止列表（REQ-DECIDE-004）———

/*
 * TestDecide_LegitimateOperationsAreNotMistakenForForbiddenOnes（回归）
 *
 * 关键词原本是**子串**匹配：`manage_token` 归一化成 `managetoken`，
 * 里面既有 `get`（横跨 manage 与 token 的接缝）也有 `token`，于是被判成
 * 「Agent 在索取凭据」而**永久拒绝、不提供审批入口**。
 *
 * Cloudflare 的 `manage_token` 是 PRD §12.3 明确列出的**高风险操作** ——
 * 要人点头，不是不许做。判进禁止列表等于把一个产品功能永久关掉，
 * 而用户在界面上连拒绝以外的选择都没有。
 *
 * 每一类禁止操作都配一组「不该命中」的相邻操作：
 * 只有该命中的用例时，把关键词表清空一样能全绿。
 */
func TestDecide_LegitimateOperationsAreNotMistakenForForbiddenOnes(t *testing.T) {
	cases := []struct {
		service   string
		operation string
		why       string
	}{
		// 造一个新的凭据不是读走现成的（credential_request 的边界）。
		{"cloudflare", "manage_token", "轮换 Token 是 PRD §12.3 的高风险操作，要人点头而不是永久拒绝"},
		{"cloudflare", "create_token", "同上：造出来的不是现成的"},
		{"github", "update_secret", "写入一个 Secret 不是读出它"},
		{"github", "rotate_credential", "轮换同样是写"},
		// 名词对但没有读动词。
		{"cloudflare", "list_zones", "list 是读动词，但 zone 不是凭据"},
		{"github", "get_repository", "同上"},
		// vault_export 的边界：读的不是保险库，而且服务不是 OpenDelo 自己。
		{"onepassword", "vault_backup_create", "备份是写，不是把库导出给 Agent"},
		// 自身服务之外的操作不该被后三类碰到。
		{"github", "audit_log.read", "GitHub 的审计日志不是 OpenDelo 的账本"},
		{"cloudflare", "policy.list", "Cloudflare 的策略不是 OpenDelo 的权限"},
		{"github", "settings.update", "改的是仓库设置，不是 OpenDelo 自己"},
	}

	for _, testCase := range cases {
		t.Run(testCase.service+"/"+testCase.operation, func(t *testing.T) {
			input := lowRiskRead()
			input.Scope.Scope.Service = testCase.service
			input.Scope.Scope.Operation = testCase.operation

			outcome := decision.Decide(input)
			if outcome.Reason == decision.ReasonForbidden {
				t.Errorf("被判进禁止列表的 %s —— %s", outcome.Forbidden, testCase.why)
			}
		})
	}
}

func TestDecide_EveryForbiddenCategory_IsDenied(t *testing.T) {
	cases := []struct {
		service   string
		operation string
		category  decision.Forbidden
	}{
		{"github", "credential.read", decision.ForbiddenCredentialRequest},
		{"github", "secret.get", decision.ForbiddenCredentialRequest},
		{"github", "token.export", decision.ForbiddenCredentialRequest},
		{"cloudflare", "api_key.reveal", decision.ForbiddenCredentialRequest},
		// 分段之后仍然要认出来的三种写法：复合词、复数、驼峰。
		{"github", "private_key.read", decision.ForbiddenCredentialRequest},
		{"github", "list_secrets", decision.ForbiddenCredentialRequest},
		{"github", "readCredential", decision.ForbiddenCredentialRequest},
		{"onepassword", "list_vaults", decision.ForbiddenVaultExport},
		{decision.SelfService, "vault.export", decision.ForbiddenVaultExport},
		{"onepassword", "vault.list", decision.ForbiddenVaultExport},
		{"macos", "keychain.dump", decision.ForbiddenVaultExport},
		{decision.SelfService, "lease.extend", decision.ForbiddenPrivilegeEscalation},
		{decision.SelfService, "scope.widen", decision.ForbiddenPrivilegeEscalation},
		{decision.SelfService, "trust_memory.create", decision.ForbiddenPrivilegeEscalation},
		{decision.SelfService, "approval.decide", decision.ForbiddenPrivilegeEscalation},
		{decision.SelfService, "audit.disable", decision.ForbiddenAuditDisable},
		{decision.SelfService, "audit_events.prune", decision.ForbiddenAuditDisable},
		{decision.SelfService, "ledger.clear", decision.ForbiddenAuditDisable},
		{decision.SelfService, "settings.update", decision.ForbiddenSelfConfiguration},
		{decision.SelfService, "preferences.write", decision.ForbiddenSelfConfiguration},
		{decision.SelfService, "gateway.restart", decision.ForbiddenSelfConfiguration},
	}

	seen := make(map[decision.Forbidden]bool)
	for _, testCase := range cases {
		t.Run(testCase.service+"/"+testCase.operation, func(t *testing.T) {
			input := lowRiskRead()
			input.Scope.Scope.Service = testCase.service
			input.Scope.Scope.Operation = testCase.operation

			outcome := decision.Decide(input)
			assertOutcome(t, outcome, decision.VerdictDeny, decision.ReasonForbidden)
			if outcome.Forbidden != testCase.category {
				t.Errorf("类别为 %s，期望 %s", outcome.Forbidden, testCase.category)
			}
		})
		seen[testCase.category] = true
	}

	if len(seen) != 5 {
		t.Fatalf("覆盖了 %d 类禁止操作，REQ-DECIDE-004 列了 5 类", len(seen))
	}
}

func TestDecide_ForbiddenList_IsMatchedInEveryWriting(t *testing.T) {
	// 只比全等的话，Credential-Read 与 vault.export_all 都会漏网。
	for _, operation := range []string{
		"Credential-Read", "CREDENTIAL_READ", "credentialRead",
		"vault.export_all", "Vault/Export", "v1.secrets.download",
	} {
		t.Run(operation+" 被认出", func(t *testing.T) {
			input := lowRiskRead()
			input.Scope.Scope.Operation = operation

			if got := decision.Decide(input); got.Reason != decision.ReasonForbidden {
				t.Errorf("原因为 %s，期望 forbidden", got.Reason)
			}
		})
	}
}

func TestDecide_CreatingACredential_IsHighRiskNotForbidden(t *testing.T) {
	// PRD §12.3 把「创建 API Token」列为高风险而不是禁止：它要人确认，但可以发生。
	// 只按名词匹配的话它会被误拦，那等于把一个 PRD 允许的操作永久关掉。
	for _, operation := range []string{
		"api_token.create", "token.rotate", "deploy_key.add", "secret.update",
	} {
		t.Run(operation+" 不进禁止列表", func(t *testing.T) {
			input := mediumWrite()
			input.Assessment.Level = risk.LevelHigh
			input.Scope.Scope.Operation = operation

			outcome := decision.Decide(input)
			assertOutcome(t, outcome, decision.VerdictRequireApproval, decision.ReasonHighRisk)
		})
	}
}

func TestDecide_ExternalServiceRoleOperation_IsNotSelfEscalation(t *testing.T) {
	// 在 GitHub 上建一个 role 是外部的高风险操作，不是「扩大自身权限」。
	// 后三类禁止操作以服务为准，正是为了区分这两件事。
	input := mediumWrite()
	input.Assessment.Level = risk.LevelHigh
	input.Scope.Scope.Service = "github"
	input.Scope.Scope.Operation = "org.role.create"

	assertOutcome(t, decision.Decide(input),
		decision.VerdictRequireApproval, decision.ReasonHighRisk)
}

// ——— 自动放行的三条抑制 ———

func TestDecide_AutoAllowIsSuppressed_ByEachGuard(t *testing.T) {
	// 三者都不改变风险等级，改变的是「能不能不问人」。
	// 结论落到默认分支上，而不是新增第八个分支。
	cases := []struct {
		name  string
		apply func(*decision.Input)
	}{
		{"身份被标记需要检查", func(i *decision.Input) { i.Match.NeedsReview = true }},
		{"资源指向不止一个目标", func(i *decision.Input) { i.Scope.Ambiguous = true }},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"时不自动放行", func(t *testing.T) {
			input := lowRiskRead()
			testCase.apply(&input)

			assertOutcome(t, decision.Decide(input),
				decision.VerdictRequireApproval, decision.ReasonRequiresConfirmation)
		})
	}
}

func TestDecide_UnverifiedAgentWrite_IsNeverAutoAllowed(t *testing.T) {
	// REQ-AGENT-002 AC2：unverified Agent 的任何写操作不得为 auto_allow，
	// 即使它完全落在一条已学习的授权之内。
	input := mediumWrite()
	input.AgentTrust = agentauth.TrustUnverified
	input.Learned = []decision.Grant{grantCovering(baseScope())}

	assertOutcome(t, decision.Decide(input),
		decision.VerdictRequireApproval, decision.ReasonRequiresConfirmation)
}

func TestDecide_UnverifiedAgentRead_IsStillAutoAllowed(t *testing.T) {
	// 反向对照：AC2 说的是写操作。读操作不受影响，否则上一条用例恒成立。
	input := lowRiskRead()
	input.AgentTrust = agentauth.TrustUnverified

	assertOutcome(t, decision.Decide(input), decision.VerdictAutoAllow, decision.ReasonLowRisk)
}

// ——— 穷举出来的不变量 ———

// enumerate 穷举会影响结论的十个开关，共 1024 组输入。
func enumerate(t *testing.T, visit func(decision.Input)) {
	t.Helper()

	switches := []func(*decision.Input){
		func(i *decision.Input) { i.Write = true },
		func(i *decision.Input) { i.AgentTrust = agentauth.TrustUnverified },
		func(i *decision.Input) { i.AgentTrust = agentauth.TrustTrusted },
		func(i *decision.Input) { i.Match.NeedsReview = true },
		func(i *decision.Input) { i.Match = ambiguousMatch() },
		func(i *decision.Input) { i.Scope.Ambiguous = true },
		func(i *decision.Input) { i.Scope.Scope = baseScope() },
		func(i *decision.Input) { i.Learned = []decision.Grant{grantCovering(baseScope())} },
		func(i *decision.Input) { i.Learned = append(i.Learned, grantForAnotherRecord()) },
		func(i *decision.Input) { i.Assessment.Level = risk.LevelHigh },
	}
	levels := []risk.Level{risk.LevelLow, risk.LevelMedium, risk.LevelHigh}

	for mask := 0; mask < 1<<len(switches); mask++ {
		for _, level := range levels {
			input := lowRiskRead()
			input.Assessment.Level = level
			for index, apply := range switches {
				if mask&(1<<index) != 0 {
					apply(&input)
				}
			}
			visit(input)
		}
	}
}

func TestDecide_HighRisk_IsNeverAutoAllowed(t *testing.T) {
	// REQ-DECIDE-003 AC3 在平衡模式下的一半：没有任何输入组合能让高风险自动执行。
	// 另外两种模式在 `mode_test.go` 里。
	enumerate(t, func(input decision.Input) {
		outcome := decision.Decide(input)
		if input.Assessment.Level == risk.LevelHigh && outcome.Verdict == decision.VerdictAutoAllow {
			t.Fatalf("高风险被自动放行：%+v", input)
		}
	})
}

func TestDecide_AutoAllow_OnlyEverComesFromTwoBranches(t *testing.T) {
	// 放行出口唯一：结论为 auto_allow 时，原因只可能是这两个之一。
	allowed := map[decision.Reason]bool{
		decision.ReasonLowRisk: true, decision.ReasonTrustMemoryMatch: true,
	}

	enumerate(t, func(input decision.Input) {
		outcome := decision.Decide(input)
		if outcome.Verdict != decision.VerdictAutoAllow {
			return
		}
		if !allowed[outcome.Reason] {
			t.Fatalf("auto_allow 来自 %s：%+v", outcome.Reason, input)
		}
		if outcome.ApprovalRequirement != decision.ApprovalNone {
			t.Fatalf("auto_allow 却要求确认 %s：%+v", outcome.ApprovalRequirement, input)
		}
	})
}

func TestDecide_EveryOutcome_IsWellFormed(t *testing.T) {
	// AC3：每个结论都给得出原因。另外结论只能是三种之一，
	// 且拒绝与自动放行都不产生审批项。
	reasons := map[decision.Reason]bool{
		decision.ReasonFailClosed: true, decision.ReasonForbidden: true,
		decision.ReasonHighRisk: true, decision.ReasonBeyondLearnedScope: true,
		decision.ReasonIdentityAmbiguous: true, decision.ReasonLowRisk: true,
		decision.ReasonTrustMemoryMatch: true, decision.ReasonRequiresConfirmation: true,
	}

	enumerate(t, func(input decision.Input) {
		outcome := decision.Decide(input)

		if !reasons[outcome.Reason] {
			t.Fatalf("原因 %q 不在码表里：%+v", outcome.Reason, input)
		}
		switch outcome.Verdict {
		case decision.VerdictAutoAllow, decision.VerdictDeny:
			if outcome.ApprovalRequirement != decision.ApprovalNone {
				t.Fatalf("%s 却要求确认 %s：%+v", outcome.Verdict, outcome.ApprovalRequirement, input)
			}
		case decision.VerdictRequireApproval:
			if outcome.ApprovalRequirement == decision.ApprovalNone {
				t.Fatalf("要人确认却没有确认强度：%+v", input)
			}
		default:
			t.Fatalf("结论 %q 不是三种之一：%+v", outcome.Verdict, input)
		}
	})
}

func TestDecide_HighRiskApproval_AlwaysRequiresStrongAuth(t *testing.T) {
	// REQ-APPROVAL-005：高风险的确认要走强认证。
	enumerate(t, func(input decision.Input) {
		outcome := decision.Decide(input)
		if outcome.Verdict != decision.VerdictRequireApproval {
			return
		}
		wanted := decision.ApprovalStandard
		if input.Assessment.Level == risk.LevelHigh {
			wanted = decision.ApprovalStrongAuth
		}
		if outcome.ApprovalRequirement != wanted {
			t.Fatalf("确认强度为 %s，期望 %s：%+v", outcome.ApprovalRequirement, wanted, input)
		}
	})
}

func TestDecide_SameInput_ProducesTheSameOutcome(t *testing.T) {
	input := mediumWrite()
	input.Learned = []decision.Grant{grantForAnotherRecord(), grantCovering(baseScope())}

	first := decision.Decide(input)
	for round := 0; round < 256; round++ {
		if again := decision.Decide(input); !reflect.DeepEqual(again, first) {
			t.Fatalf("第 %d 轮结论为 %+v，首轮为 %+v", round, again, first)
		}
	}
}

// ——— 「始终要求确认」的记忆（REQ-APPROVAL-002 AC5）———

func TestDecide_AlwaysAskMemory_LowRiskRead_StillRequiresApproval_Regression(t *testing.T) {
	// 回归：Grant 原本只带范围不带行为，于是一条用户特意收紧成「始终问我」的
	// 记忆与一条「今后自动允许」的记忆在决策引擎里长得一模一样 ——
	// 低风险读取照样落到 low_risk 那一支被自动放行，与用户的选择正好相反。
	// 这条用例永不删除。
	input := lowRiskRead()
	input.Learned = []decision.Grant{alwaysAskGrant(readScope())}

	outcome := decision.Decide(input)
	if outcome.Verdict != decision.VerdictRequireApproval {
		t.Fatalf("结论为 %s（%s），期望 require_approval", outcome.Verdict, outcome.Reason)
	}
}

func TestDecide_AlwaysAskMemory_MediumWrite_StillRequiresApproval_Regression(t *testing.T) {
	// 同一个缺口在中风险那一支上的形状：命中记忆本来就是 trust_memory_match
	// 自动放行的条件，而「始终要求确认」恰恰是要取消这个资格。
	input := mediumWrite()
	input.AgentTrust = agentauth.TrustKnown
	input.Learned = []decision.Grant{alwaysAskGrant(baseScope())}

	outcome := decision.Decide(input)
	if outcome.Verdict != decision.VerdictRequireApproval {
		t.Fatalf("结论为 %s（%s），期望 require_approval", outcome.Verdict, outcome.Reason)
	}
	// 命中的仍然是那条记忆：结论变了，但「为什么」还查得出来。
	if outcome.MatchedMemoryID != memoryID {
		t.Errorf("命中的记忆为 %q，期望 %q", outcome.MatchedMemoryID, memoryID)
	}
}

func TestDecide_AlwaysAskAlongsideAutoAllow_TakesTheStricterOne(t *testing.T) {
	// 同一个组合下先学了「今后自动允许」，后来又收紧成「始终要求确认」。
	// 两句话都在时取严的那一句 —— 顺序换过来也是同一个答案。
	strict := alwaysAskGrant(baseScope())
	loose := grantCovering(baseScope())

	for name, learned := range map[string][]decision.Grant{
		"收紧的那条在前": {strict, loose},
		"收紧的那条在后": {loose, strict},
	} {
		t.Run(name, func(t *testing.T) {
			input := mediumWrite()
			input.AgentTrust = agentauth.TrustKnown
			input.Learned = learned

			if outcome := decision.Decide(input); outcome.Verdict != decision.VerdictRequireApproval {
				t.Fatalf("结论为 %s（%s），期望 require_approval", outcome.Verdict, outcome.Reason)
			}
		})
	}
}

func TestDecide_AlwaysAskMemoryForAnotherResource_DoesNotBlockThisOne(t *testing.T) {
	// 收紧只对它覆盖的那个范围生效。否则一条针对别的记录的「始终问我」
	// 会把整个服务都变回逐次询问，而那不是用户说过的话。
	input := mediumWrite()
	input.AgentTrust = agentauth.TrustKnown
	elsewhere := grantForAnotherRecord()
	elsewhere.AlwaysAsk = true
	input.Learned = []decision.Grant{elsewhere, grantCovering(baseScope())}

	outcome := decision.Decide(input)
	if outcome.Verdict != decision.VerdictAutoAllow {
		t.Fatalf("结论为 %s（%s），期望 auto_allow", outcome.Verdict, outcome.Reason)
	}
}
