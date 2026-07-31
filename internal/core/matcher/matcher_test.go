package matcher_test

import (
	"testing"

	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * core/matcher 的行为用例（REQ-IDENT-002）。
 *
 * 本包是纯函数：候选身份、绑定与命中的记忆都由调用方加载后传入（ADR-003），
 * 所以用例直接给数据，不需要数据库。
 */

const (
	workIdentityID     = "01K1IDENTITY000000000000WRK"
	personalIdentityID = "01K1IDENTITY000000000000PSN"
	thirdIdentityID    = "01K1IDENTITY000000000000TRD"
	workspaceID        = "01K1WORKSPACE00000000000TEL"
	resourceKey        = "repo=Runcoor/opendelo"
)

func workIdentity(options ...fixtures.IdentityOption) matcher.Identity {
	return fixtures.Identity(append([]fixtures.IdentityOption{
		fixtures.WithIdentityID(workIdentityID),
		fixtures.WithIdentityAccountLabel("work"),
	}, options...)...)
}

func personalIdentity(options ...fixtures.IdentityOption) matcher.Identity {
	return fixtures.Identity(append([]fixtures.IdentityOption{
		fixtures.WithIdentityID(personalIdentityID),
		fixtures.WithIdentityAccountLabel("personal"),
	}, options...)...)
}

func thirdIdentity() matcher.Identity {
	return fixtures.Identity(
		fixtures.WithIdentityID(thirdIdentityID),
		fixtures.WithIdentityAccountLabel("open-source"),
	)
}

func request() matcher.Request {
	return matcher.Request{
		Service:     fixtures.DefaultServiceLabel,
		WorkspaceID: workspaceID,
		ResourceKey: resourceKey,
	}
}

// everyLevelSpeaks 让四级依据同时指向不同身份，用来验证顺序。
func everyLevelSpeaks() matcher.Inputs {
	return matcher.Inputs{
		WorkspaceBindings: []matcher.Binding{{
			ID:          "01K1BINDING0000000000000WSP",
			Kind:        matcher.BindingWorkspace,
			Service:     fixtures.DefaultServiceLabel,
			WorkspaceID: workspaceID,
			IdentityID:  workIdentityID,
			CreatedAt:   fixtures.Instant,
		}},
		ResourceBindings: []matcher.Binding{{
			ID:          "01K1BINDING0000000000000RES",
			Kind:        matcher.BindingResource,
			Service:     fixtures.DefaultServiceLabel,
			ResourceKey: resourceKey,
			IdentityID:  personalIdentityID,
			CreatedAt:   fixtures.Instant,
		}},
		MemoryIdentityIDs: []string{thirdIdentityID},
		Identities:        []matcher.Identity{workIdentity(), personalIdentity(), thirdIdentity()},
	}
}

func mustMatch(t *testing.T, inputs matcher.Inputs) matcher.Result {
	t.Helper()

	result, err := matcher.Match(request(), inputs)
	if err != nil {
		t.Fatalf("匹配失败：%v", err)
	}
	return result
}

func assertHit(t *testing.T, result matcher.Result, identityID string, level matcher.MatchLevel) {
	t.Helper()

	if result.Ambiguous {
		t.Fatalf("匹配返回歧义，期望命中 %s", identityID)
	}
	if result.Identity.ID != identityID {
		t.Errorf("匹配到 %s，期望 %s", result.Identity.ID, identityID)
	}
	if result.Level != level {
		t.Errorf("命中层级为 %q，期望 %q", result.Level, level)
	}
}

// ——— 五级顺序（REQ-IDENT-002）———

func TestMatch_WorkspaceBinding_IsTheFirstLevel(t *testing.T) {
	// AC1：项目已绑定 GitHub Work 时，该项目的请求自动匹配它，无需询问。
	assertHit(t, mustMatch(t, everyLevelSpeaks()), workIdentityID, matcher.MatchWorkspaceBinding)
}

func TestMatch_ResourceBinding_IsTheSecondLevel(t *testing.T) {
	inputs := everyLevelSpeaks()
	inputs.WorkspaceBindings = nil

	assertHit(t, mustMatch(t, inputs), personalIdentityID, matcher.MatchResourceBinding)
}

func TestMatch_TrustMemory_IsTheThirdLevel(t *testing.T) {
	inputs := everyLevelSpeaks()
	inputs.WorkspaceBindings = nil
	inputs.ResourceBindings = nil

	assertHit(t, mustMatch(t, inputs), thirdIdentityID, matcher.MatchTrustMemory)
}

func TestMatch_SoleIdentity_IsTheFourthLevel(t *testing.T) {
	inputs := matcher.Inputs{Identities: []matcher.Identity{workIdentity()}}

	assertHit(t, mustMatch(t, inputs), workIdentityID, matcher.MatchSoleIdentity)
}

func TestMatch_ManualSelection_IsTheFifthLevel(t *testing.T) {
	// 用户在审批里选定后，同一次请求重新匹配就有了答案。
	inputs := matcher.Inputs{
		Identities:      []matcher.Identity{workIdentity(), personalIdentity()},
		ManualSelection: personalIdentityID,
	}

	assertHit(t, mustMatch(t, inputs), personalIdentityID, matcher.MatchManualSelection)
}

func TestMatch_LevelsAreConsultedInOrder(t *testing.T) {
	// 顺序写在一张表里：调换任意两级，这条用例里对应的两行就会互换结果。
	cases := []struct {
		name     string
		strip    func(*matcher.Inputs)
		identity string
		level    matcher.MatchLevel
	}{
		{"四级都说话时听项目绑定", func(*matcher.Inputs) {}, workIdentityID, matcher.MatchWorkspaceBinding},
		{"没有项目绑定时听资源绑定", func(i *matcher.Inputs) {
			i.WorkspaceBindings = nil
		}, personalIdentityID, matcher.MatchResourceBinding},
		{"两种绑定都没有时听历史选择", func(i *matcher.Inputs) {
			i.WorkspaceBindings = nil
			i.ResourceBindings = nil
		}, thirdIdentityID, matcher.MatchTrustMemory},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			inputs := everyLevelSpeaks()
			testCase.strip(&inputs)

			assertHit(t, mustMatch(t, inputs), testCase.identity, testCase.level)
		})
	}
}

func TestMatch_BindingForAnotherWorkspaceOrResource_DoesNotCount(t *testing.T) {
	inputs := everyLevelSpeaks()
	inputs.WorkspaceBindings[0].WorkspaceID = "01K1WORKSPACE00000000000OTH"

	// 项目绑定不再适用，落到资源绑定这一级。
	assertHit(t, mustMatch(t, inputs), personalIdentityID, matcher.MatchResourceBinding)

	inputs.ResourceBindings[0].ResourceKey = "repo=someone/else"
	assertHit(t, mustMatch(t, inputs), thirdIdentityID, matcher.MatchTrustMemory)
}

func TestMatch_BindingOfTheWrongKind_DoesNotCount(t *testing.T) {
	// 两级的依据不同，放错列表的绑定不该被当成那一级的答案。两个方向都要验：
	// 只挡一个方向，另一边的种类判断删掉了也没人发现。
	t.Run("资源绑定混进项目绑定列表", func(t *testing.T) {
		inputs := everyLevelSpeaks()
		inputs.WorkspaceBindings[0].Kind = matcher.BindingResource

		assertHit(t, mustMatch(t, inputs), personalIdentityID, matcher.MatchResourceBinding)
	})

	t.Run("项目绑定混进资源绑定列表", func(t *testing.T) {
		inputs := everyLevelSpeaks()
		inputs.WorkspaceBindings = nil
		inputs.ResourceBindings[0].Kind = matcher.BindingWorkspace
		inputs.ResourceBindings[0].WorkspaceID = workspaceID

		assertHit(t, mustMatch(t, inputs), thirdIdentityID, matcher.MatchTrustMemory)
	})
}

// ——— 歧义（REQ-IDENT-002 AC2）———

func TestMatch_TwoUnboundIdentities_MustBeAsked(t *testing.T) {
	// AC2：同一 service 存在两个未绑定的 Identity 时必须询问，审批页面列出两个候选。
	inputs := matcher.Inputs{Identities: []matcher.Identity{workIdentity(), personalIdentity()}}

	result := mustMatch(t, inputs)

	if !result.Ambiguous {
		t.Fatalf("两个未绑定的身份却匹配出了 %s", result.Identity.ID)
	}
	if result.Identity.ID != "" || result.Level != "" {
		t.Errorf("歧义结果里带着 %+v，调用方可能会当成命中直接用", result)
	}
	if len(result.Candidates) != 2 {
		t.Fatalf("候选有 %d 个，期望 2 个", len(result.Candidates))
	}
	if result.Candidates[0].ID == result.Candidates[1].ID {
		t.Error("两个候选是同一个身份")
	}
}

func TestMatch_DefaultIdentity_IsNotPickedAutomatically(t *testing.T) {
	// is_default 是展示上的默认，不是匹配依据：拿它当答案就是替用户做主。
	inputs := matcher.Inputs{Identities: []matcher.Identity{
		workIdentity(func(identity *matcher.Identity) { identity.IsDefault = true }),
		personalIdentity(func(identity *matcher.Identity) { identity.IsDefault = false }),
	}}

	if result := mustMatch(t, inputs); !result.Ambiguous {
		t.Fatalf("有默认身份就直接选了 %s，用户没被问过", result.Identity.ID)
	}
}

func TestMatch_AmbiguityAtOneLevel_DoesNotFallThrough(t *testing.T) {
	// 某一级出现多个候选时停下来问，不去下一级找答案 ——
	// 往下找等于用一条更弱的依据覆盖用户已经表达过的意思。
	inputs := everyLevelSpeaks()
	inputs.WorkspaceBindings = append(inputs.WorkspaceBindings, matcher.Binding{
		ID:          "01K1BINDING0000000000000WS2",
		Kind:        matcher.BindingWorkspace,
		Service:     fixtures.DefaultServiceLabel,
		WorkspaceID: workspaceID,
		IdentityID:  personalIdentityID,
		CreatedAt:   fixtures.Instant,
	})

	result := mustMatch(t, inputs)

	if !result.Ambiguous {
		t.Fatalf("项目绑定有歧义却仍然匹配出了 %s（层级 %s）", result.Identity.ID, result.Level)
	}
	if len(result.Candidates) != 2 {
		t.Errorf("候选有 %d 个，期望只有那两条项目绑定指向的身份", len(result.Candidates))
	}
	for _, candidate := range result.Candidates {
		if candidate.ID == thirdIdentityID {
			t.Error("历史选择指向的身份混进了项目绑定这一级的候选里")
		}
	}
}

func TestMatch_ManualSelection_OnlyCountsWithinTheCandidates(t *testing.T) {
	// 选一个不在候选里的身份等于绕过匹配，必须仍然是歧义。
	inputs := matcher.Inputs{
		Identities:      []matcher.Identity{workIdentity(), personalIdentity()},
		ManualSelection: "01K1IDENTITY00000000000XXXX",
	}

	if result := mustMatch(t, inputs); !result.Ambiguous {
		t.Fatalf("候选之外的选择被采纳成了 %s", result.Identity.ID)
	}
}

func TestMatch_ManualSelection_DoesNotOverrideAnExplicitBinding(t *testing.T) {
	// 前四级已经给出唯一答案时不该再看手动选择：那是第五级。
	inputs := everyLevelSpeaks()
	inputs.ManualSelection = personalIdentityID

	assertHit(t, mustMatch(t, inputs), workIdentityID, matcher.MatchWorkspaceBinding)
}

// ——— 引用与数据完整性 ———

func TestMatch_CandidatePointingAtAnUnknownIdentity_IsSkipped(t *testing.T) {
	// 指向未知身份的记忆要么已失效要么属于别的服务，两种情况都不该参与匹配。
	// 这一级因此变成「什么也没说」，而不是「有一个候选」。
	inputs := matcher.Inputs{
		MemoryIdentityIDs: []string{"01K1IDENTITY00000000000GONE"},
		Identities:        []matcher.Identity{workIdentity()},
	}

	assertHit(t, mustMatch(t, inputs), workIdentityID, matcher.MatchSoleIdentity)
}

func TestMatch_SameIdentityNamedTwice_IsNotAmbiguity(t *testing.T) {
	// 两条记忆指向同一个身份仍然只有一个答案，不该因此去问用户。
	inputs := matcher.Inputs{
		MemoryIdentityIDs: []string{workIdentityID, workIdentityID},
		Identities:        []matcher.Identity{workIdentity(), personalIdentity()},
	}

	assertHit(t, mustMatch(t, inputs), workIdentityID, matcher.MatchTrustMemory)
}

func TestMatch_WithoutAnyIdentity_IsRefused(t *testing.T) {
	// 匹配不到身份的请求执行不了，带着零值往下走只会把失败推迟到取凭据那一步。
	_, err := matcher.Match(request(), matcher.Inputs{})
	if !apperr.Is(err, apperr.CodeCredentialNotAuthorized) {
		t.Fatalf("错误码为 %s，期望 credential_not_authorized（%v）", apperr.CodeOf(err), err)
	}
}

func TestMatch_IdentityFromAnotherService_IsRefused(t *testing.T) {
	// 服务不符说明调用方加载错了数据。悄悄过滤掉会让「为什么没匹配上」查不出来。
	inputs := matcher.Inputs{Identities: []matcher.Identity{
		workIdentity(),
		personalIdentity(fixtures.WithIdentityService("cloudflare")),
	}}

	_, err := matcher.Match(request(), inputs)
	if !apperr.Is(err, apperr.CodeInternal) {
		t.Fatalf("错误码为 %s，期望 internal（%v）", apperr.CodeOf(err), err)
	}
}

func TestMatch_WithoutAService_IsRejected(t *testing.T) {
	_, err := matcher.Match(matcher.Request{}, matcher.Inputs{
		Identities: []matcher.Identity{workIdentity()},
	})
	if !apperr.Is(err, apperr.CodeInvalidRequest) {
		t.Fatalf("错误码为 %s，期望 invalid_request（%v）", apperr.CodeOf(err), err)
	}
}

// ——— 结果的可解释性 ———

func TestMatch_EveryHit_CarriesItsLevel(t *testing.T) {
	// AC3：命中层级要与结果一并写入 Decision 与审计事件。
	// 只知道匹配到了哪个身份，无法解释为什么是它。
	known := map[matcher.MatchLevel]bool{
		matcher.MatchWorkspaceBinding: true,
		matcher.MatchResourceBinding:  true,
		matcher.MatchTrustMemory:      true,
		matcher.MatchSoleIdentity:     true,
		matcher.MatchManualSelection:  true,
	}

	sole := matcher.Inputs{Identities: []matcher.Identity{workIdentity()}}
	manual := matcher.Inputs{
		Identities:      []matcher.Identity{workIdentity(), personalIdentity()},
		ManualSelection: workIdentityID,
	}
	memory := everyLevelSpeaks()
	memory.WorkspaceBindings = nil
	memory.ResourceBindings = nil
	resource := everyLevelSpeaks()
	resource.WorkspaceBindings = nil

	seen := make(map[matcher.MatchLevel]bool)
	for _, inputs := range []matcher.Inputs{everyLevelSpeaks(), resource, memory, sole, manual} {
		result := mustMatch(t, inputs)
		if !known[result.Level] {
			t.Fatalf("命中层级 %q 不在 REQ-IDENT-002 的五级里", result.Level)
		}
		seen[result.Level] = true
	}
	if len(seen) != len(known) {
		t.Errorf("只覆盖了 %d 个层级，期望五级各出现一次", len(seen))
	}
}

func TestMatch_IdentityNeedingReview_IsFlagged(t *testing.T) {
	// REQ-IDENT-004：外部 Scope 变化后自动授权暂停。匹配仍然成立，
	// 但结果要带上这个标记，由决策链路转人工。
	inputs := matcher.Inputs{Identities: []matcher.Identity{
		workIdentity(func(identity *matcher.Identity) { identity.Status = matcher.StatusNeedsReview }),
	}}

	result := mustMatch(t, inputs)

	if !result.NeedsReview {
		t.Error("需要检查的身份没有被标记，决策链路会把它当成正常身份自动放行")
	}

	healthy := mustMatch(t, matcher.Inputs{Identities: []matcher.Identity{workIdentity()}})
	if healthy.NeedsReview {
		t.Error("正常身份被标成了需要检查")
	}
}

func TestMatch_CandidateOrder_DoesNotDependOnInputOrder(t *testing.T) {
	// 候选顺序直接显示在审批页面上。仓储查询的返回顺序、记忆的命中顺序都可能变，
	// 候选的排列不该跟着变 —— 否则同一次请求每次刷新都换一个次序。
	cases := []struct {
		name  string
		one   matcher.Inputs
		other matcher.Inputs
	}{
		{
			name:  "候选身份的加载顺序",
			one:   matcher.Inputs{Identities: []matcher.Identity{thirdIdentity(), personalIdentity(), workIdentity()}},
			other: matcher.Inputs{Identities: []matcher.Identity{workIdentity(), thirdIdentity(), personalIdentity()}},
		},
		{
			name: "历史选择的命中顺序",
			one: matcher.Inputs{
				MemoryIdentityIDs: []string{thirdIdentityID, workIdentityID},
				Identities:        []matcher.Identity{workIdentity(), personalIdentity(), thirdIdentity()},
			},
			other: matcher.Inputs{
				MemoryIdentityIDs: []string{workIdentityID, thirdIdentityID},
				Identities:        []matcher.Identity{workIdentity(), personalIdentity(), thirdIdentity()},
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			one := mustMatch(t, testCase.one)
			other := mustMatch(t, testCase.other)

			if len(one.Candidates) != len(other.Candidates) {
				t.Fatalf("两种输入顺序给出 %d 与 %d 个候选", len(one.Candidates), len(other.Candidates))
			}
			for index := range one.Candidates {
				if one.Candidates[index].ID != other.Candidates[index].ID {
					t.Fatalf("第 %d 个候选是 %s 与 %s，排列随输入顺序漂移了",
						index, one.Candidates[index].ID, other.Candidates[index].ID)
				}
			}
		})
	}
}

func TestMatch_IdentityWithoutAPrimaryKey_IsRefused(t *testing.T) {
	// 没有主键的身份写不进 Decision，也无从在账本里指认。
	inputs := matcher.Inputs{Identities: []matcher.Identity{
		workIdentity(func(identity *matcher.Identity) { identity.ID = "" }),
	}}

	_, err := matcher.Match(request(), inputs)
	if !apperr.Is(err, apperr.CodeInternal) {
		t.Fatalf("错误码为 %s，期望 internal（%v）", apperr.CodeOf(err), err)
	}
}

func TestMatch_ResultFitsTheDecisionRecord(t *testing.T) {
	// AC3：匹配结果与命中层级一并写入 Decision。decisions 表上有一条约束要求
	// identity_id 与 match_level 同时有值或同时为空，所以这两者必须成对产出。
	hit := mustMatch(t, everyLevelSpeaks())
	record := decision.Decision{IdentityID: hit.Identity.ID, MatchLevel: hit.Level}
	if (record.IdentityID == "") != (record.MatchLevel == "") {
		t.Errorf("命中结果写成 %+v，与 decisions 表的成对约束冲突", record)
	}
	if record.IdentityID == "" {
		t.Error("命中却没有身份可写")
	}

	asked := mustMatch(t, matcher.Inputs{
		Identities: []matcher.Identity{workIdentity(), personalIdentity()},
	})
	ambiguousRecord := decision.Decision{IdentityID: asked.Identity.ID, MatchLevel: asked.Level}
	if (ambiguousRecord.IdentityID == "") != (ambiguousRecord.MatchLevel == "") {
		t.Errorf("歧义结果写成 %+v，与 decisions 表的成对约束冲突", ambiguousRecord)
	}
	if ambiguousRecord.IdentityID != "" {
		t.Error("还没问用户就先把一个身份写进了决策")
	}
}
