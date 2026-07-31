package trust_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/risk"
	"github.com/Runcoor/opendelo/internal/core/scope"
	"github.com/Runcoor/opendelo/internal/core/trust"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * core/trust 的行为用例（REQ-TRUST-001~005）。
 *
 * 用真实的 SQLite 仓储：记忆的七个维度非空、risk_ceiling 里没有 high、
 * 「一次审批只学一条」都是 schema 层面的约束，换成替身就测不到它们。
 */

type harness struct {
	manager  *trust.Manager
	memories *repo.TrustMemories
	db       *store.DB
	clock    *clock.Fixed
}

func newHarness(t *testing.T) harness {
	t.Helper()

	db := fixtures.SeededChain(t)
	fixed := clock.NewFixed(fixtures.Instant)
	memories := repo.NewTrustMemories(db)

	manager, err := trust.NewManager(trust.Options{
		Memories: memories,
		Clock:    fixed,
		IDs:      ulid.New(fixed),
	})
	if err != nil {
		t.Fatalf("构造 Trust Manager 失败：%v", err)
	}
	return harness{manager: manager, memories: memories, db: db, clock: fixed}
}

// approvedScope 是 PRD §14.2 的那次审批：修改 api.tele-call.cn 的 A 记录。
func approvedScope() scope.Scope {
	return scope.Scope{
		AgentID:     fixtures.DefaultAgentID,
		WorkspaceID: fixtures.DefaultWorkspaceID,
		Service:     fixtures.DefaultServiceLabel,
		IdentityID:  fixtures.DefaultIdentityID,
		Account:     "work",
		Resource: map[string]string{
			"zone": "tele-call.cn", "record": "api.tele-call.cn", "record_type": "A",
		},
		ResourceKey:  "record=api.tele-call.cn;record_type=A;zone=tele-call.cn",
		Operation:    "dns.record.update",
		NotBefore:    fixtures.Instant,
		ExpiresAt:    fixtures.Instant.Add(scope.DefaultDuration),
		RequestLimit: 1,
		Environment:  matcher.EnvironmentProduction,
		RiskCeiling:  risk.LevelMedium,
	}
}

func generateRequest() trust.GenerateRequest {
	return trust.GenerateRequest{
		Approved:   approvedScope(),
		Learned:    approvedScope(),
		ApprovalID: fixtures.DefaultApprovalID,
		RiskLevel:  risk.LevelMedium,
		Behavior:   trust.BehaviorAutoAllow,
	}
}

func generate(t *testing.T, all harness, request trust.GenerateRequest) trust.Memory {
	t.Helper()

	memory, err := all.manager.Generate(t.Context(), request)
	if err != nil {
		t.Fatalf("生成记忆失败：%v", err)
	}
	return memory
}

// beyondApproval 是收敛校验的拒绝理由片段。
const beyondApproval = "超出了那次审批放行的范围"

func assertCode(t *testing.T, err error, expected apperr.Code) {
	t.Helper()

	if err == nil {
		t.Fatalf("期望失败并返回 %s，实际成功", expected)
	}
	if !apperr.Is(err, expected) {
		t.Fatalf("错误码为 %s，期望 %s（%v）", apperr.CodeOf(err), expected, err)
	}
}

// assertNotLearned 断言这次学习被**本包**在写库之前拒绝。
//
// 只断言错误码不够：记忆表上的 CHECK 与外键会把同样的输入也拦下来，
// 而仓储把约束冲突同样翻译成 invalid_request。两者分不开的话，
// 「收敛校验被去掉」这类变异会因为数据库替它挡了一下而漏网。
// because 是拒绝理由里必须出现的片段。多条检查会对同一个坏输入都说「不」，
// 只断言「有错」的话，去掉其中一条的变异会因为另一条替它挡了一下而漏网。
func assertNotLearned(t *testing.T, all harness, request trust.GenerateRequest, because string) {
	t.Helper()

	_, err := all.manager.Generate(t.Context(), request)
	assertCode(t, err, apperr.CodeInvalidRequest)
	if !strings.Contains(err.Error(), because) {
		t.Errorf("拒绝理由为 %v，期望其中包含 %q", err, because)
	}

	stored, listErr := all.memories.MemoriesByStatus(t.Context(), trust.StatusActive, 10)
	if listErr != nil {
		t.Fatalf("列出记忆失败：%v", listErr)
	}
	if len(stored) != 0 {
		t.Errorf("被拒绝的学习仍然留下了 %d 条记忆", len(stored))
	}
}

// ——— 自动生成（REQ-TRUST-001）———

func TestGenerate_RecordsWhereItCameFrom(t *testing.T) {
	// AC2：created_from 指向产生它的 approval 记录，Automation 页面据此显示来源。
	all := newHarness(t)
	memory := generate(t, all, generateRequest())

	if memory.CreatedFrom != fixtures.DefaultApprovalID {
		t.Errorf("来源为 %q，期望 %q", memory.CreatedFrom, fixtures.DefaultApprovalID)
	}
	if memory.Status != trust.StatusActive || memory.InvalidationReason != "" {
		t.Errorf("新记忆的状态为 %s、原因为 %q", memory.Status, memory.InvalidationReason)
	}
	if !memory.LastUsedAt.IsZero() {
		t.Errorf("新记忆的 last_used_at 为 %v，期望零值（从未使用）", memory.LastUsedAt)
	}
}

func TestGenerate_WithoutAnApproval_IsRefused(t *testing.T) {
	all := newHarness(t)
	request := generateRequest()
	request.ApprovalID = ""

	assertNotLearned(t, all, request, "必须指向产生它的那次审批")
}

func TestGenerate_SameApprovalTwice_IsRefused(t *testing.T) {
	// 一次审批只学出一条记忆：学两遍会让同一个范围有两处来源，
	// 用户在 Automation 页面删掉一条也止不住另一条。
	all := newHarness(t)
	generate(t, all, generateRequest())

	_, err := all.manager.Generate(t.Context(), generateRequest())
	assertCode(t, err, apperr.CodeConflict)
}

// ——— 不得扩大（REQ-TRUST-002）———

func TestGenerate_ThePRDCounterExample_IsRefused(t *testing.T) {
	// PRD §14.2：批准「修改 api.tele-call.cn A 记录」不得学成
	// 「修改 tele-call.cn 任意 DNS」。
	all := newHarness(t)
	request := generateRequest()
	request.Learned.Resource = map[string]string{"zone": "tele-call.cn"}
	request.Learned.ResourceKey = "zone=tele-call.cn"

	assertNotLearned(t, all, request, "超出了那次审批放行的范围")
}

func TestGenerate_TheResourceIsRememberedExactly(t *testing.T) {
	// AC1 的正向一侧：记下的资源精确等于那次审批的那一条记录。
	all := newHarness(t)
	memory := generate(t, all, generateRequest())

	learned, err := trust.ScopeOf(memory)
	if err != nil {
		t.Fatalf("读回记忆的范围失败：%v", err)
	}
	if !reflect.DeepEqual(learned.Resource, approvedScope().Resource) {
		t.Errorf("记下的资源为 %v，期望 %v", learned.Resource, approvedScope().Resource)
	}

	var capabilities []string
	if err := json.Unmarshal([]byte(memory.CapabilityScope), &capabilities); err != nil {
		t.Fatalf("能力清单不是合法 JSON：%v", err)
	}
	if !reflect.DeepEqual(capabilities, []string{approvedScope().Operation}) {
		t.Errorf("能力清单为 %v，期望只有那一个操作", capabilities)
	}
}

func TestGenerate_TryingToWidenAnyDimension_IsRefused(t *testing.T) {
	// AC2：七个维度各有一个「尝试扩大」的用例，结果均为不扩大。
	// 时间维由 TTL 上限单独限定 —— 记忆本来就要活得比一次授权久，
	// 拿 15 分钟的窗口去比它没有意义。
	cases := []struct {
		dimension string
		widen     func(*trust.GenerateRequest)
		because   string
	}{
		{"资源", func(r *trust.GenerateRequest) {
			r.Learned.Resource = map[string]string{
				"zone": "tele-call.cn", "record": "www.tele-call.cn", "record_type": "A",
			}
			r.Learned.ResourceKey = "record=www.tele-call.cn;record_type=A;zone=tele-call.cn"
		}, beyondApproval},
		{"操作", func(r *trust.GenerateRequest) {
			r.Learned.Operation = "dns.record.delete"
		}, beyondApproval},
		{"时间", func(r *trust.GenerateRequest) {
			r.TTL = trust.MaxTTL + time.Hour
		}, "有效期超过上限"},
		{"Agent", func(r *trust.GenerateRequest) {
			r.Learned.AgentID = "01K1AGENT0000000000000OTHER"
		}, beyondApproval},
		{"项目", func(r *trust.GenerateRequest) {
			r.Learned.WorkspaceID = "01K1WORKSPACE000000000OTHER"
		}, beyondApproval},
		{"身份", func(r *trust.GenerateRequest) {
			r.Learned.IdentityID = "01K1IDENTITY0000000000OTHER"
		}, beyondApproval},
		{"环境", func(r *trust.GenerateRequest) {
			r.Learned.Environment = matcher.EnvironmentNonProduction
		}, beyondApproval},
		{"次数", func(r *trust.GenerateRequest) {
			r.Learned.RequestLimit = 100
		}, beyondApproval},
		{"风险上限", func(r *trust.GenerateRequest) {
			r.Learned.RiskCeiling = risk.LevelHigh
		}, beyondApproval},
	}

	for _, testCase := range cases {
		t.Run(testCase.dimension+"维尝试扩大时不生成", func(t *testing.T) {
			request := generateRequest()
			testCase.widen(&request)

			assertNotLearned(t, newHarness(t), request, testCase.because)
		})
	}
}

func TestGenerate_NarrowerLearnedScope_IsAccepted(t *testing.T) {
	// 反向对照：记住的范围比审批更窄是允许的，否则上一组用例
	// 换成「什么都不生成」也照样通过。
	all := newHarness(t)
	request := generateRequest()
	request.Learned.RiskCeiling = risk.LevelLow

	memory, err := all.manager.Generate(t.Context(), request)
	if err != nil {
		t.Fatalf("更窄的范围被拒绝：%v", err)
	}

	// 记下的是「要记住的」那一份，不是审批那一份：两者在风险上限上不同。
	if memory.RiskCeiling != risk.LevelLow {
		t.Errorf("记忆的风险上限为 %s，期望取更严的 low", memory.RiskCeiling)
	}
	learned, err := trust.ScopeOf(memory)
	if err != nil {
		t.Fatalf("读回记忆的范围失败：%v", err)
	}
	if learned.RiskCeiling != risk.LevelLow {
		t.Errorf("入库的范围里风险上限为 %s，期望 low", learned.RiskCeiling)
	}
}

func TestGenerate_RiskCeilingNeverExceedsTheApproval(t *testing.T) {
	// AC3：risk_ceiling 不得高于产生它的那次审批的风险等级。
	// 每一级一份独立的数据库 —— 一次审批只学得出一条记忆。
	for _, level := range []risk.Level{risk.LevelLow, risk.LevelMedium} {
		t.Run(string(level)+" 的审批学出同一级的上限", func(t *testing.T) {
			request := generateRequest()
			request.RiskLevel = level
			request.Learned.RiskCeiling = level
			request.Approved.RiskCeiling = level

			memory := generate(t, newHarness(t), request)
			if memory.RiskCeiling != level {
				t.Errorf("风险上限为 %s，期望 %s", memory.RiskCeiling, level)
			}
		})
	}
}

// ——— 高风险不得学习（REQ-TRUST-003）———

func TestGenerate_HighRiskApproval_ProducesNoMemoryAtAll(t *testing.T) {
	// AC1：high 风险审批不产生任何 approval_behavior=auto_allow 的记忆。
	// 这里更进一步：它不产生任何记忆 —— 一条 always_ask 的高风险记忆也没有用武之地，
	// 存在本身就是一个可能被误用的东西。
	all := newHarness(t)

	for _, behavior := range []trust.Behavior{trust.BehaviorAutoAllow, trust.BehaviorAlwaysAsk} {
		request := generateRequest()
		request.RiskLevel = risk.LevelHigh
		request.Behavior = behavior

		assertNotLearned(t, all, request, "高风险审批不产生授权记忆")
	}
}

func TestGenerate_UnknownRiskLevelOrBehavior_IsRefused(t *testing.T) {
	all := newHarness(t)

	cases := []struct {
		name    string
		apply   func(*trust.GenerateRequest)
		because string
	}{
		{"风险等级为空", func(r *trust.GenerateRequest) { r.RiskLevel = "" }, "风险等级认不出来"},
		{"风险等级认不出来", func(r *trust.GenerateRequest) { r.RiskLevel = "critical" }, "风险等级认不出来"},
		{"行为为空", func(r *trust.GenerateRequest) { r.Behavior = "" }, "行为认不出来"},
		{"行为认不出来", func(r *trust.GenerateRequest) { r.Behavior = "always_allow" }, "行为认不出来"},
		{"有效期为负", func(r *trust.GenerateRequest) { r.TTL = -time.Hour }, "有效期不能为负"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"时不生成", func(t *testing.T) {
			request := generateRequest()
			testCase.apply(&request)

			assertNotLearned(t, all, request, testCase.because)
		})
	}
}

func TestGenerate_IncompleteScope_IsRefused(t *testing.T) {
	all := newHarness(t)

	for _, name := range []string{"审批范围", "要记住的范围"} {
		t.Run(name+"不完整时不生成", func(t *testing.T) {
			request := generateRequest()
			if name == "审批范围" {
				request.Approved.Operation = ""
			} else {
				request.Learned.Operation = ""
			}

			assertNotLearned(t, all, request, "维度无法确定")
		})
	}
}

// ——— 有效期（时间维度）———

func TestGenerate_TTLDefaultsToThirtyDaysAndIsBounded(t *testing.T) {
	// 「永久记忆」在这里不可表达：有效期有默认值，也有上限。
	all := newHarness(t)
	memory := generate(t, all, generateRequest())

	if got := memory.ExpiresAt.Sub(fixtures.Instant); got != trust.DefaultTTL {
		t.Errorf("默认有效期为 %v，期望 %v", got, trust.DefaultTTL)
	}
	if trust.DefaultTTL != 30*24*time.Hour {
		t.Errorf("默认有效期为 %v，期望 30 天", trust.DefaultTTL)
	}
	if trust.MaxTTL <= trust.DefaultTTL {
		t.Errorf("上限 %v 不大于默认值 %v", trust.MaxTTL, trust.DefaultTTL)
	}
}

// ——— 匹配与使用 ———

func TestMatch_ReturnsOnlyUsableMemories(t *testing.T) {
	// 失效的记忆读得到但匹配不到（REQ-TRUST-004 AC3）；已到期的同样匹配不到 ——
	// 到期与失效是两件事，前者不需要有人来把它标记成失效才成立。
	all := newHarness(t)
	memory := generate(t, all, generateRequest())

	usable, err := all.manager.Match(t.Context(),
		fixtures.DefaultAgentID, fixtures.DefaultWorkspaceID, fixtures.DefaultServiceLabel, 10)
	if err != nil {
		t.Fatalf("匹配失败：%v", err)
	}
	if len(usable) != 1 || usable[0].ID != memory.ID {
		t.Fatalf("匹配到 %d 条：%+v", len(usable), usable)
	}

	all.clock.Set(memory.ExpiresAt)
	expired, err := all.manager.Match(t.Context(),
		fixtures.DefaultAgentID, fixtures.DefaultWorkspaceID, fixtures.DefaultServiceLabel, 10)
	if err != nil {
		t.Fatalf("匹配失败：%v", err)
	}
	if len(expired) != 0 {
		t.Errorf("已到期的记忆仍被匹配到：%+v", expired)
	}
}

func TestMatch_InvalidatedMemory_IsNotReturned(t *testing.T) {
	all := newHarness(t)
	memory := generate(t, all, generateRequest())

	if _, err := all.manager.Invalidate(
		t.Context(), memory.ID, trust.ReasonDeviceUntrusted); err != nil {
		t.Fatalf("失效失败：%v", err)
	}

	usable, err := all.manager.Match(t.Context(),
		fixtures.DefaultAgentID, fixtures.DefaultWorkspaceID, fixtures.DefaultServiceLabel, 10)
	if err != nil {
		t.Fatalf("匹配失败：%v", err)
	}
	if len(usable) != 0 {
		t.Errorf("失效的记忆仍被匹配到：%+v", usable)
	}
}

func TestUse_RefreshesLastUsedAt(t *testing.T) {
	all := newHarness(t)
	memory := generate(t, all, generateRequest())

	all.clock.Advance(time.Hour)
	used, err := all.manager.Use(t.Context(), memory.ID)
	if err != nil {
		t.Fatalf("记一次使用失败：%v", err)
	}
	if !used.LastUsedAt.Equal(all.clock.Now()) {
		t.Errorf("last_used_at 为 %v，期望 %v", used.LastUsedAt, all.clock.Now())
	}
}

func TestScopeOf_UnreadableMemory_IsRefused(t *testing.T) {
	// 一个空范围什么都不覆盖，看起来「安全」，但它会让这条记忆
	// 静静地失去作用而没有人知道。
	cases := []struct {
		stored   string
		expected apperr.Code
	}{
		{`[]`, apperr.CodeInternal},
		{`{"agent_id":"01K1AGENT00000000000000000"}`, apperr.CodeInvalidRequest},
	}

	for _, testCase := range cases {
		_, err := trust.ScopeOf(fixtures.Memory(fixtures.WithMemoryResourceScope(testCase.stored)))
		assertCode(t, err, testCase.expected)
	}
}

// ——— 自动失效（REQ-TRUST-004）———

func TestReasons_AreExactlyTheEightFromThePRD(t *testing.T) {
	expected := []trust.InvalidationReason{
		"provider_disconnected", "identity_scope_changed", "agent_executable_changed",
		"project_fingerprint_changed", "device_untrusted", "unused_too_long",
		"cautious_mode_selected", "adapter_risk_upgraded",
	}

	if got := trust.Reasons(); !reflect.DeepEqual(got, expected) {
		t.Fatalf("失效条件为 %v，期望 %v", got, expected)
	}
}

func TestInvalidate_EveryConditionKeepsItsReason(t *testing.T) {
	// AC1：八个条件各一个用例，触发后状态变为 invalidated。
	// AC2：失效的记忆保留原因，不是直接消失。
	for _, reason := range trust.Reasons() {
		t.Run(string(reason), func(t *testing.T) {
			all := newHarness(t)
			memory := generate(t, all, generateRequest())

			invalidated, err := all.manager.Invalidate(t.Context(), memory.ID, reason)
			if err != nil {
				t.Fatalf("失效失败：%v", err)
			}
			if invalidated.Status != trust.StatusInvalidated {
				t.Errorf("状态为 %s，期望 invalidated", invalidated.Status)
			}
			if invalidated.InvalidationReason != reason {
				t.Errorf("原因为 %s，期望 %s", invalidated.InvalidationReason, reason)
			}

			stored, err := all.memories.MemoryByID(t.Context(), memory.ID)
			if err != nil {
				t.Fatalf("失效后读不到这条记忆：%v", err)
			}
			if stored.InvalidationReason != reason {
				t.Errorf("库里的原因为 %s，期望 %s", stored.InvalidationReason, reason)
			}
		})
	}
}

func TestInvalidate_UnknownReason_IsRefused(t *testing.T) {
	all := newHarness(t)
	memory := generate(t, all, generateRequest())

	for _, reason := range []trust.InvalidationReason{"", "because", "expired"} {
		_, err := all.manager.Invalidate(t.Context(), memory.ID, reason)
		assertCode(t, err, apperr.CodeInvalidRequest)
		// 说明必须来自本包：数据库的 CHECK 也会拦下同样的取值，
		// 只看错误码分不出是谁拒的。
		if !strings.Contains(err.Error(), "失效原因认不出来") {
			t.Errorf("拒绝理由为 %v，期望来自本包的校验", err)
		}
	}
}

func TestInvalidateAll_ClearsEverythingForACautiousModeSwitch(t *testing.T) {
	// REQ-DECIDE-003：切换到谨慎模式会使既有 Trust Memory 失效。
	all := newHarness(t)
	memory := generate(t, all, generateRequest())

	cleared, err := all.manager.InvalidateAll(
		t.Context(), trust.ReasonCautiousModeSelected, 100)
	if err != nil {
		t.Fatalf("批量失效失败：%v", err)
	}
	if len(cleared) != 1 || cleared[0].ID != memory.ID {
		t.Fatalf("失效了 %d 条：%+v", len(cleared), cleared)
	}
	if cleared[0].InvalidationReason != trust.ReasonCautiousModeSelected {
		t.Errorf("原因为 %s，期望 cautious_mode_selected", cleared[0].InvalidationReason)
	}

	again, err := all.manager.InvalidateAll(t.Context(), trust.ReasonCautiousModeSelected, 100)
	if err != nil {
		t.Fatalf("第二次批量失效失败：%v", err)
	}
	if len(again) != 0 {
		t.Errorf("第二次又失效了 %d 条", len(again))
	}
}

func TestInvalidateAll_UnknownReason_IsRefused(t *testing.T) {
	all := newHarness(t)
	generate(t, all, generateRequest())

	_, err := all.manager.InvalidateAll(t.Context(), "spring_cleaning", 100)
	assertCode(t, err, apperr.CodeInvalidRequest)
	if !strings.Contains(err.Error(), "失效原因认不出来") {
		t.Errorf("拒绝理由为 %v，期望来自本包的校验", err)
	}
}

func TestInvalidateUnused_CountsFromCreationWhenNeverUsed(t *testing.T) {
	// 从未使用过的记忆以创建时刻起算：否则一条创建后一直没被用过的记忆
	// 永远不会因为「长期未使用」而失效，那一维就形同虚设。
	all := newHarness(t)
	memory := generate(t, all, generateRequest())

	all.clock.Set(fixtures.Instant.Add(trust.UnusedTTL - time.Minute))
	early, err := all.manager.InvalidateUnused(t.Context(), 100)
	if err != nil {
		t.Fatalf("清理失败：%v", err)
	}
	if len(early) != 0 {
		t.Fatalf("还没到阈值就失效了 %d 条", len(early))
	}

	all.clock.Set(fixtures.Instant.Add(trust.UnusedTTL))
	stale, err := all.manager.InvalidateUnused(t.Context(), 100)
	if err != nil {
		t.Fatalf("清理失败：%v", err)
	}
	if len(stale) != 1 || stale[0].ID != memory.ID {
		t.Fatalf("失效了 %d 条：%+v", len(stale), stale)
	}
	if stale[0].InvalidationReason != trust.ReasonUnusedTooLong {
		t.Errorf("原因为 %s，期望 unused_too_long", stale[0].InvalidationReason)
	}
}

func TestInvalidateUnused_CountsFromTheLastUse(t *testing.T) {
	// 用过之后重新计时：一条一直在用的记忆不该因为「创建得早」而失效。
	all := newHarness(t)
	memory := generate(t, all, generateRequest())

	all.clock.Set(fixtures.Instant.Add(trust.UnusedTTL - time.Hour))
	if _, err := all.manager.Use(t.Context(), memory.ID); err != nil {
		t.Fatalf("记一次使用失败：%v", err)
	}

	all.clock.Set(fixtures.Instant.Add(trust.UnusedTTL + time.Hour))
	stale, err := all.manager.InvalidateUnused(t.Context(), 100)
	if err != nil {
		t.Fatalf("清理失败：%v", err)
	}
	if len(stale) != 0 {
		t.Errorf("最近用过的记忆被失效了：%+v", stale)
	}
}

// ——— 构造 ———

func TestNewManager_MissingAnyDependency_IsRefused(t *testing.T) {
	db := fixtures.MigratedDB(t)
	fixed := clock.NewFixed(fixtures.Instant)
	complete := trust.Options{
		Memories: repo.NewTrustMemories(db),
		Clock:    fixed,
		IDs:      ulid.New(fixed),
	}

	if _, err := trust.NewManager(complete); err != nil {
		t.Fatalf("完整依赖仍被拒绝：%v", err)
	}

	cases := map[string]trust.Options{
		"缺仓储":      {Clock: complete.Clock, IDs: complete.IDs},
		"缺时钟":      {Memories: complete.Memories, IDs: complete.IDs},
		"缺 ID 生成器": {Memories: complete.Memories, Clock: complete.Clock},
	}
	for name, options := range cases {
		t.Run(name+"时拒绝构造", func(t *testing.T) {
			if _, err := trust.NewManager(options); err == nil {
				t.Error("依赖不全仍构造成功")
			}
		})
	}
}

func TestRepository_HasNoMethodThatChangesAScope(t *testing.T) {
	// 改范围就是重新学习，只能由一次新的审批产生新记忆。
	methods := reflect.TypeOf((*trust.Repository)(nil)).Elem()
	// TightenBehavior 是唯一的修改入口，且只朝收紧的方向（auto_allow → always_ask），
	// 方向由 SQL 的 WHERE 钉住；其余全是读或删。
	expected := []string{
		"ActiveMemoriesByIdentity", "CreateMemory", "DeleteMemory", "Invalidate",
		"MatchMemories", "MemoriesByStatus", "MemoryByID", "TightenBehavior", "Touch",
	}

	names := make([]string, 0, methods.NumMethod())
	for index := 0; index < methods.NumMethod(); index++ {
		names = append(names, methods.Method(index).Name)
	}
	if !reflect.DeepEqual(names, expected) {
		t.Fatalf("Repository 的方法为 %v，期望 %v", names, expected)
	}
}

// failingMemories 在 Invalidate 上返回哨兵错误，其余转交真实仓储。
type failingMemories struct {
	*repo.TrustMemories
	err error
}

var errRepositoryDown = apperr.New(apperr.CodeInternal).WithDetail("SENTINEL_REPO_DOWN")

func (f failingMemories) Invalidate(
	_ context.Context, _ string, _ trust.InvalidationReason, _ time.Time,
) (trust.Memory, error) {
	return trust.Memory{}, f.err
}

func TestInvalidateAll_RepositoryFailure_IsPassedUpUnchanged(t *testing.T) {
	// 批量失效途中出错即返回：吞掉它会让「切换到谨慎模式」看起来成功，
	// 而实际上还有记忆活着（REQ-DECIDE-003）。
	all := newHarness(t)
	generate(t, all, generateRequest())

	fixed := clock.NewFixed(fixtures.Instant)
	manager, err := trust.NewManager(trust.Options{
		Memories: failingMemories{TrustMemories: all.memories, err: errRepositoryDown},
		Clock:    fixed,
		IDs:      ulid.New(fixed),
	})
	if err != nil {
		t.Fatalf("构造 Manager 失败：%v", err)
	}

	cleared, err := manager.InvalidateAll(t.Context(), trust.ReasonCautiousModeSelected, 100)
	if !errors.Is(err, errRepositoryDown) {
		t.Errorf("返回的错误为 %v，期望原样传出哨兵错误", err)
	}
	if len(cleared) != 0 {
		t.Errorf("一条都没成功却报出失效了 %d 条", len(cleared))
	}
}

// ——— 管理与查看（REQ-TRUST-005）———

func TestByStatus_ShowsActiveAndInvalidatedSeparately(t *testing.T) {
	// 失效的记忆读得到而不是消失：页面要显示它为什么失效（REQ-TRUST-004 AC2）。
	all := newHarness(t)
	memory := generate(t, all, generateRequest())

	active, err := all.manager.ByStatus(t.Context(), trust.StatusActive, 10)
	if err != nil {
		t.Fatalf("列出生效中的记忆失败：%v", err)
	}
	if len(active) != 1 {
		t.Fatalf("生效中的记忆有 %d 条，期望 1 条", len(active))
	}

	if _, err = all.manager.Invalidate(
		t.Context(), memory.ID, trust.ReasonDeviceUntrusted); err != nil {
		t.Fatalf("失效失败：%v", err)
	}

	active, err = all.manager.ByStatus(t.Context(), trust.StatusActive, 10)
	if err != nil {
		t.Fatalf("列出生效中的记忆失败：%v", err)
	}
	if len(active) != 0 {
		t.Errorf("失效之后还列出了 %d 条生效中的记忆", len(active))
	}

	invalidated, err := all.manager.ByStatus(t.Context(), trust.StatusInvalidated, 10)
	if err != nil {
		t.Fatalf("列出失效记忆失败：%v", err)
	}
	if len(invalidated) != 1 {
		t.Fatalf("失效记忆有 %d 条，期望 1 条", len(invalidated))
	}
	if invalidated[0].InvalidationReason != trust.ReasonDeviceUntrusted {
		t.Errorf("失效原因为 %s", invalidated[0].InvalidationReason)
	}
}

func TestByStatus_UnknownStatusIsRefused(t *testing.T) {
	// 认不出的状态返回空列表会被界面读成「一条都没有」，那是一句假话。
	all := newHarness(t)

	_, err := all.manager.ByStatus(t.Context(), trust.Status("whatever"), 10)
	assertCode(t, err, apperr.CodeInvalidRequest)
}

func TestByID_ReadsBackWhatWasGenerated(t *testing.T) {
	all := newHarness(t)
	memory := generate(t, all, generateRequest())

	read, err := all.manager.ByID(t.Context(), memory.ID)
	if err != nil {
		t.Fatalf("读取记忆失败：%v", err)
	}
	if read.CreatedFrom != fixtures.DefaultApprovalID {
		t.Errorf("记忆的来源是 %q，期望指向产生它的审批", read.CreatedFrom)
	}

	if _, err = all.manager.ByID(t.Context(), "01J000000000000000NOPE"); !apperr.Is(
		err, apperr.CodeNotFound) {
		t.Errorf("不存在的 id 返回 %v，期望 not_found", err)
	}
}

func TestTightenBehavior_OnlyGoesFromAutoAllowToAlwaysAsk(t *testing.T) {
	// 这是本包唯一的修改入口，且只朝收紧的方向：放宽只能由一次新的审批产生。
	all := newHarness(t)
	memory := generate(t, all, generateRequest())

	tightened, err := all.manager.TightenBehavior(t.Context(), memory.ID)
	if err != nil {
		t.Fatalf("收紧失败：%v", err)
	}
	if tightened.Behavior != trust.BehaviorAlwaysAsk {
		t.Errorf("行为为 %s，期望 always_ask", tightened.Behavior)
	}

	// 已经是 always_ask 的记忆再收紧一次不成立：方向由 SQL 的 WHERE 钉住。
	_, err = all.manager.TightenBehavior(t.Context(), memory.ID)
	assertCode(t, err, apperr.CodeConflict)
}

func TestTightenBehavior_OnAnInvalidatedMemoryIsRefused(t *testing.T) {
	// 收紧一条已失效的记忆等于在复活它。
	all := newHarness(t)
	memory := generate(t, all, generateRequest())

	if _, err := all.manager.Invalidate(
		t.Context(), memory.ID, trust.ReasonDeviceUntrusted); err != nil {
		t.Fatalf("失效失败：%v", err)
	}

	_, err := all.manager.TightenBehavior(t.Context(), memory.ID)
	assertCode(t, err, apperr.CodeConflict)

	still, err := all.memories.MemoryByID(t.Context(), memory.ID)
	if err != nil {
		t.Fatalf("读取记忆失败：%v", err)
	}
	if still.Status != trust.StatusInvalidated {
		t.Errorf("状态被改成了 %s", still.Status)
	}
}

func TestDelete_RemovesTheMemorySoItStopsMatching(t *testing.T) {
	// REQ-TRUST-005 AC1：删掉之后对应请求下次进入审批。
	all := newHarness(t)
	memory := generate(t, all, generateRequest())

	if err := all.manager.Delete(t.Context(), memory.ID); err != nil {
		t.Fatalf("删除失败：%v", err)
	}

	matched, err := all.manager.Match(
		t.Context(), memory.AgentID, memory.WorkspaceID, memory.Service, 10)
	if err != nil {
		t.Fatalf("匹配失败：%v", err)
	}
	if len(matched) != 0 {
		t.Errorf("删掉之后还能匹配到 %d 条", len(matched))
	}
}

func TestInvalidateForIdentity_TouchesOnlyThatIdentitysMemories(t *testing.T) {
	// 断开一个身份不该殃及别的身份名下的记忆。
	all := newHarness(t)
	mine := generate(t, all, generateRequest())

	second, approvalID := secondIdentity(t, all)
	other := generateRequest()
	other.Approved.IdentityID = second
	other.Learned.IdentityID = second
	other.ApprovalID = approvalID
	if _, err := all.manager.Generate(t.Context(), other); err != nil {
		t.Fatalf("生成第二条记忆失败：%v", err)
	}

	invalidated, err := all.manager.InvalidateForIdentity(
		t.Context(), mine.IdentityID, trust.ReasonIdentityScopeChanged, 100)
	if err != nil {
		t.Fatalf("按身份失效失败：%v", err)
	}
	if len(invalidated) != 1 {
		t.Fatalf("失效了 %d 条，期望只失效那个身份名下的 1 条", len(invalidated))
	}
	if invalidated[0].ID != mine.ID {
		t.Errorf("失效的是 %s，期望 %s", invalidated[0].ID, mine.ID)
	}

	remaining, err := all.manager.ByStatus(t.Context(), trust.StatusActive, 10)
	if err != nil {
		t.Fatalf("列出生效中的记忆失败：%v", err)
	}
	if len(remaining) != 1 {
		t.Errorf("别的身份名下的记忆也被动了，剩下 %d 条", len(remaining))
	}
}

func TestInvalidateForIdentity_UnknownReasonIsRefused(t *testing.T) {
	all := newHarness(t)
	generate(t, all, generateRequest())

	_, err := all.manager.InvalidateForIdentity(
		t.Context(), fixtures.DefaultIdentityID, trust.InvalidationReason("因为我说了算"), 100)
	assertCode(t, err, apperr.CodeInvalidRequest)
}

// secondIdentity 建一个同服务下的第二个身份，外加它自己的请求 / 决策 / 审批。
//
// 一条记忆只能来自一个审批（created_from 上有唯一索引），所以第二条记忆
// 必须有自己的那一整条前置链，不能复用第一条的审批。
func secondIdentity(t *testing.T, all harness) (identityID, approvalID string) {
	t.Helper()

	identity := fixtures.Identity()
	identity.ID = "01K1IDENTITYSECOND000000000"
	identity.AccountLabel = "personal"
	created, err := repo.NewIdentities(all.db).CreateIdentity(t.Context(), identity)
	if err != nil {
		t.Fatalf("写入第二个身份失败：%v", err)
	}

	const (
		requestID  = "01K1REQUESTSECOND0000000000"
		decisionID = "01K1DECISIONSECOND000000000"
		itemID     = "01K1APPROVALSECOND000000000"
	)
	if _, err = repo.NewCapabilityRequests(all.db).CreateRequest(
		t.Context(), fixtures.Request(fixtures.WithRequestID(requestID))); err != nil {
		t.Fatalf("写入第二个能力请求失败：%v", err)
	}
	if _, err = repo.NewDecisions(all.db).CreateDecision(t.Context(), fixtures.Decision(
		fixtures.WithDecisionID(decisionID),
		fixtures.WithDecisionRequestID(requestID),
		fixtures.WithDecisionIdentityID(created.ID),
	)); err != nil {
		t.Fatalf("写入第二个决策失败：%v", err)
	}

	item := fixtures.Approval()
	item.ID = itemID
	item.DecisionID = decisionID
	if _, err = repo.NewApprovals(all.db).CreateApproval(t.Context(), item); err != nil {
		t.Fatalf("写入第二个审批项失败：%v", err)
	}
	return created.ID, itemID
}
