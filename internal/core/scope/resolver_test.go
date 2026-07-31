package scope_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	adapters "github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/core/intent"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/risk"
	"github.com/Runcoor/opendelo/internal/core/scope"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/audit"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * core/scope 的行为用例（REQ-SCOPE-001/002）。
 *
 * 本包是纯函数：意图、发起方与身份都由调用方加载后传入（ADR-003），
 * 所以用例直接给数据，不需要数据库。
 */

const (
	dnsZone      = "tele-call.cn"
	dnsRecord    = "api.tele-call.cn"
	otherRecord  = "www.tele-call.cn"
	dnsOperation = "dns.record.update"
	dnsService   = "cloudflare"
)

func newResolver(t *testing.T) *scope.Resolver {
	t.Helper()

	resolver, err := scope.NewResolver(clock.NewFixed(fixtures.Instant))
	if err != nil {
		t.Fatalf("构造 Scope Resolver 失败：%v", err)
	}
	return resolver
}

// dnsIntent 走真实的 Intent Resolver 而不是手搓一个 Intent：
// 「Adapter 未声明的操作推导不出 Scope」（AC3）只有在这条链路上才成立。
func dnsIntent(t *testing.T, resourceJSON string) intent.Intent {
	t.Helper()

	resolver, err := intent.NewResolver(intent.Options{})
	if err != nil {
		t.Fatalf("构造 Intent Resolver 失败：%v", err)
	}
	catalog, err := intent.NewCatalog([]adapters.Declaration{
		fixtures.CloudflareDeclaration(
			fixtures.WithDeclarationCapabilities(fixtures.CloudflareRecordCapabilities),
		),
	})
	if err != nil {
		t.Fatalf("构造能力映射表失败：%v", err)
	}

	resolved, err := resolver.Resolve(catalog, intent.Call{
		Tool:                "cloudflare.dns.update",
		Resource:            resourceJSON,
		DesiredChange:       `{"content":"203.0.113.10"}`,
		IdentityEnvironment: matcher.EnvironmentProduction,
	})
	if err != nil {
		t.Fatalf("解析意图失败：%v", err)
	}
	return resolved
}

func dnsResource(record string) string {
	return `{"zone":"` + dnsZone + `","record":"` + record + `","record_type":"A"}`
}

func dnsIdentity() matcher.Identity {
	return fixtures.Identity(
		fixtures.WithIdentityService(dnsService),
		fixtures.WithIdentityAccountLabel("cloudflare-production"),
		fixtures.WithIdentityEnvironment(matcher.EnvironmentProduction),
	)
}

func dnsInput(t *testing.T, resourceJSON string) scope.Input {
	t.Helper()

	return scope.Input{
		Intent:      dnsIntent(t, resourceJSON),
		AgentID:     fixtures.DefaultAgentID,
		WorkspaceID: fixtures.DefaultWorkspaceID,
		Identity:    dnsIdentity(),
	}
}

func resolve(t *testing.T, input scope.Input) scope.Result {
	t.Helper()

	resolved, err := newResolver(t).Resolve(input)
	if err != nil {
		t.Fatalf("收敛 Scope 失败：%v", err)
	}
	return resolved
}

func assertCode(t *testing.T, err error, expected apperr.Code) {
	t.Helper()

	if err == nil {
		t.Fatalf("期望失败并返回 %s，实际成功", expected)
	}
	if !apperr.Is(err, expected) {
		t.Fatalf("错误码为 %s，期望 %s（%v）", apperr.CodeOf(err), expected, err)
	}
}

// ——— 十个维度（REQ-SCOPE-001）———

func TestDimensions_AreExactlyTheTenFromThePRD(t *testing.T) {
	// PRD §3.1 目标四逐条列出的十项，顺序照抄。少一项就意味着那一维没人检查。
	expected := []string{
		"agent", "workspace", "service", "account", "resource",
		"operation", "time", "request_count", "environment", "risk_level",
	}

	if got := scope.Dimensions(); !reflect.DeepEqual(got, expected) {
		t.Fatalf("维度清单为 %v，期望 %v", got, expected)
	}
}

func TestResolve_DNSUpdate_IsNarrowedToASingleRecord(t *testing.T) {
	// AC1：修改 api.tele-call.cn A 记录的请求，Scope 精确到
	// zone + record + record_type + operation。
	resolved := resolve(t, dnsInput(t, dnsResource(dnsRecord)))

	expectedResource := map[string]string{
		"zone":        dnsZone,
		"record":      dnsRecord,
		"record_type": "A",
	}
	if !reflect.DeepEqual(resolved.Scope.Resource, expectedResource) {
		t.Errorf("资源维度为 %v，期望 %v", resolved.Scope.Resource, expectedResource)
	}
	if resolved.Scope.Operation != dnsOperation {
		t.Errorf("操作维度为 %q，期望 %q", resolved.Scope.Operation, dnsOperation)
	}
	if resolved.Scope.Service != dnsService {
		t.Errorf("服务维度为 %q，期望 %q", resolved.Scope.Service, dnsService)
	}
}

func TestResolve_DNSUpdate_DoesNotCoverAnotherRecordInTheSameZone(t *testing.T) {
	// AC1 的另一半：同一个 zone 下的另一条记录不在这个 Scope 里。
	approved := resolve(t, dnsInput(t, dnsResource(dnsRecord))).Scope
	another := resolve(t, dnsInput(t, dnsResource(otherRecord))).Scope

	if !approved.Covers(approved) {
		t.Fatal("Scope 覆盖不了它自己，判定本身有问题")
	}
	if approved.Covers(another) {
		t.Errorf("对 %s 的授权覆盖到了 %s", dnsRecord, otherRecord)
	}
	if another.Covers(approved) {
		t.Errorf("对 %s 的授权覆盖到了 %s", otherRecord, dnsRecord)
	}
}

func TestResolve_EveryDimension_IsFilled(t *testing.T) {
	// AC2 的正向一侧：十个维度全部有值。
	resolved := resolve(t, dnsInput(t, dnsResource(dnsRecord))).Scope

	filled := map[string]bool{
		"agent":         resolved.AgentID != "",
		"workspace":     resolved.WorkspaceID != "",
		"service":       resolved.Service != "",
		"account":       resolved.IdentityID != "" && resolved.Account != "",
		"resource":      len(resolved.Resource) > 0 && resolved.ResourceKey != "",
		"operation":     resolved.Operation != "",
		"time":          !resolved.NotBefore.IsZero() && resolved.ExpiresAt.After(resolved.NotBefore),
		"request_count": resolved.RequestLimit > 0,
		"environment":   resolved.Environment == matcher.EnvironmentProduction,
		"risk_level":    resolved.RiskCeiling == risk.LevelMedium,
	}
	for _, dimension := range scope.Dimensions() {
		if !filled[dimension] {
			t.Errorf("%s 维度没有取到值", dimension)
		}
	}
}

func TestResolve_AnyEmptyDimension_IsRefused(t *testing.T) {
	// AC2：十个维度任一为空即拒绝（Fail Closed）。
	// 每个用例只抹掉一个维度，其余保持合法。
	cases := []struct {
		dimension string
		break_    func(*scope.Input)
	}{
		{"agent", func(input *scope.Input) { input.AgentID = "" }},
		{"workspace", func(input *scope.Input) { input.WorkspaceID = "" }},
		{"service", func(input *scope.Input) {
			input.Intent.Service = ""
			input.Identity.Service = ""
		}},
		{"account", func(input *scope.Input) { input.Identity.ID = "" }},
		{"account", func(input *scope.Input) { input.Identity.AccountLabel = "" }},
		{"resource", func(input *scope.Input) { input.Intent.Resource = nil }},
		{"resource", func(input *scope.Input) { input.Intent.ResourceKey = "" }},
		{"operation", func(input *scope.Input) { input.Intent.Operation = "" }},
		{"environment", func(input *scope.Input) {
			input.Intent.Environment = ""
			input.Identity.Environment = ""
		}},
		{"environment", func(input *scope.Input) {
			// 「非空」不等于「合法」：staging 不是本产品的两个取值之一。
			input.Intent.Environment = "staging"
			input.Identity.Environment = "staging"
		}},
		{"risk_level", func(input *scope.Input) { input.Intent.RiskLabel = "" }},
	}

	for _, testCase := range cases {
		t.Run(testCase.dimension+" 维度为空时拒绝", func(t *testing.T) {
			input := dnsInput(t, dnsResource(dnsRecord))
			testCase.break_(&input)

			_, err := newResolver(t).Resolve(input)
			assertCode(t, err, apperr.CodeInvalidRequest)
			if !strings.Contains(err.Error(), testCase.dimension) {
				t.Errorf("错误说明没有点名 %s 维度：%v", testCase.dimension, err)
			}
		})
	}
}

func TestResolve_TimeDimension_IsAlwaysAWindowThatEnds(t *testing.T) {
	// 时间维度不是「有个时刻」而是「有个会结束的窗口」：
	// 起点、终点都要有，且终点必须晚于起点。
	resolved := resolve(t, dnsInput(t, dnsResource(dnsRecord))).Scope

	if !resolved.NotBefore.Equal(fixtures.Instant) {
		t.Errorf("窗口起点为 %v，期望 %v", resolved.NotBefore, fixtures.Instant)
	}
	if want := fixtures.Instant.Add(scope.DefaultDuration); !resolved.ExpiresAt.Equal(want) {
		t.Errorf("窗口终点为 %v，期望 %v", resolved.ExpiresAt, want)
	}
	if scope.DefaultDuration != 15*time.Minute {
		t.Errorf("默认时长为 %v，REQ-LEASE-001 AC2 要求 15 分钟", scope.DefaultDuration)
	}
}

func TestResolve_RequestCount_HasNoUnlimitedForm(t *testing.T) {
	// 次数是十个维度之一，所以 Scope 层面不存在「不限次数」。
	resolved := resolve(t, dnsInput(t, dnsResource(dnsRecord))).Scope
	if resolved.RequestLimit != 1 {
		t.Errorf("默认次数为 %d，期望 1", resolved.RequestLimit)
	}

	input := dnsInput(t, dnsResource(dnsRecord))
	input.RequestLimit = 5
	if got := resolve(t, input).Scope.RequestLimit; got != 5 {
		t.Errorf("显式给出的次数为 %d，期望 5", got)
	}
}

func TestResolve_NegativeDurationOrRequestLimit_IsRejected(t *testing.T) {
	cases := []struct {
		name  string
		apply func(*scope.Input)
	}{
		{"负的时长", func(input *scope.Input) { input.Duration = -time.Second }},
		{"负的次数", func(input *scope.Input) { input.RequestLimit = -1 }},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"被拒绝", func(t *testing.T) {
			input := dnsInput(t, dnsResource(dnsRecord))
			testCase.apply(&input)

			_, err := newResolver(t).Resolve(input)
			assertCode(t, err, apperr.CodeInvalidRequest)
			// 说明必须点出「为负」：负值同样会让时间或次数维度校验不过，
			// 只看错误码的话分不出「被拦下了」与「碰巧撞上另一条检查」。
			if !strings.Contains(err.Error(), "不能为负") {
				t.Errorf("错误说明没有指出负值：%v", err)
			}
		})
	}
}

func TestResolve_UnknownRiskLabel_IsRefused(t *testing.T) {
	// Fail Closed 的十种情况之一：风险等级未知一律拒绝（PRD §6.3）。
	for _, label := range []adapters.RiskLabel{"", "unknown", "critical", "LOW"} {
		input := dnsInput(t, dnsResource(dnsRecord))
		input.Intent.RiskLabel = label

		_, err := newResolver(t).Resolve(input)
		assertCode(t, err, apperr.CodeInvalidRequest)
	}
}

func TestResolve_RiskCeiling_ComesFromTheDeclaredLabel(t *testing.T) {
	cases := map[adapters.RiskLabel]risk.Level{
		adapters.RiskLabelLow:    risk.LevelLow,
		adapters.RiskLabelMedium: risk.LevelMedium,
		adapters.RiskLabelHigh:   risk.LevelHigh,
	}

	for label, expected := range cases {
		input := dnsInput(t, dnsResource(dnsRecord))
		input.Intent.RiskLabel = label

		if got := resolve(t, input).Scope.RiskCeiling; got != expected {
			t.Errorf("标签 %s 收敛出的风险上限为 %s，期望 %s", label, got, expected)
		}
	}
}

// ——— Scope 不得被请求方指定（REQ-SCOPE-002）———

func TestResolve_ScopeInjectionFields_DoNotChangeTheResult(t *testing.T) {
	// AC1：请求体带 scope / permissions 时结果与不带时完全一致。
	clean := resolve(t, dnsInput(t, dnsResource(dnsRecord))).Scope

	injected := `{"zone":"` + dnsZone + `","record":"` + dnsRecord + `",` +
		`"record_type":"A","scope":"*","permissions":["admin"],` +
		`"expires_at":"2099-01-01T00:00:00Z","request_limit":9999}`
	dirty := resolve(t, dnsInput(t, injected))

	if !reflect.DeepEqual(dirty.Scope, clean) {
		t.Fatalf("携带越权字段后 Scope 变了：\n带 = %+v\n不带 = %+v", dirty.Scope, clean)
	}

	expected := []string{"expires_at", "permissions", "request_limit", "scope"}
	if !reflect.DeepEqual(dirty.Injection, expected) {
		t.Errorf("报出的越权字段为 %v，期望 %v", dirty.Injection, expected)
	}
}

func TestResolve_InjectionEvent_IsTheSecurityAuditEvent(t *testing.T) {
	// AC2：调用方据此记 security.scope_injection_ignored。
	if scope.InjectionEvent != audit.EventScopeInjectionIgnored {
		t.Fatalf("注入事件为 %s，期望 %s", scope.InjectionEvent, audit.EventScopeInjectionIgnored)
	}
	if string(scope.InjectionEvent) != "security.scope_injection_ignored" {
		t.Fatalf("事件类型的字面值为 %s，与 REQ-SCOPE-002 AC2 不符", scope.InjectionEvent)
	}
}

func TestResolve_WithoutInjection_ReportsNothing(t *testing.T) {
	// 反向对照：不带越权字段时不能报出任何注入，否则上一条用例恒成立。
	resolved := resolve(t, dnsInput(t, dnsResource(dnsRecord)))

	if len(resolved.Injection) != 0 {
		t.Errorf("没有越权字段却报出了 %v", resolved.Injection)
	}
	if len(resolved.IgnoredFields) != 0 {
		t.Errorf("没有多余字段却报出了 %v", resolved.IgnoredFields)
	}
}

func TestResolve_HarmlessExtraField_IsIgnoredButNotCalledInjection(t *testing.T) {
	// 多送一个无关字段不是扩权尝试：它进 IgnoredFields，但不产生 security 事件。
	extra := `{"zone":"` + dnsZone + `","record":"` + dnsRecord + `",` +
		`"record_type":"A","comment":"临时改一下"}`
	resolved := resolve(t, dnsInput(t, extra))

	if len(resolved.Injection) != 0 {
		t.Errorf("无关字段被当成越权字段：%v", resolved.Injection)
	}
	if !reflect.DeepEqual(resolved.IgnoredFields, []string{"comment"}) {
		t.Errorf("被忽略的字段为 %v，期望 [comment]", resolved.IgnoredFields)
	}
}

func TestInjectionWords_MatchTheDeclaredList(t *testing.T) {
	// 词表逐字钉死：删掉一个词就意味着那类扩权尝试不再留痕。
	expected := []string{
		"scope", "permission", "privilege", "grant", "role", "policy",
		"risklevel", "lease", "expires", "ttl", "requestlimit", "environment",
		"approval", "approve", "autoallow", "allow", "deny", "trust",
		"bypass", "override", "elevate", "escalate",
	}

	if got := scope.InjectionWords(); !reflect.DeepEqual(got, expected) {
		t.Fatalf("越权字段词表为 %v，期望 %v", got, expected)
	}
}

func TestResolve_InjectionWord_IsMatchedInEveryWriting(t *testing.T) {
	// 只比全等的话，requested_scope / X-Permissions / allowList 全都漏网。
	writings := []string{
		"scope", "SCOPE", "requested_scope", "scopes",
		"X-Permissions", "permission_set", "autoAllow",
		"risk_level", "request-limit", "Trust", "bypass.check",
	}

	for _, field := range writings {
		t.Run(field+" 被认出", func(t *testing.T) {
			resource := `{"zone":"` + dnsZone + `","record":"` + dnsRecord + `",` +
				`"record_type":"A","` + field + `":"x"}`
			resolved := resolve(t, dnsInput(t, resource))

			if !reflect.DeepEqual(resolved.Injection, []string{field}) {
				t.Errorf("报出的越权字段为 %v，期望 [%s]", resolved.Injection, field)
			}
		})
	}
}

// ——— 未声明的能力推导不出 Scope（REQ-SCOPE-001 AC3）———

func TestResolve_UndeclaredOperation_NeverProducesAScope(t *testing.T) {
	// AC3：能力表外的工具名在 Intent Resolver 就被拦下，走不到收敛这一步。
	resolver, err := intent.NewResolver(intent.Options{})
	if err != nil {
		t.Fatalf("构造 Intent Resolver 失败：%v", err)
	}
	catalog, err := intent.NewCatalog([]adapters.Declaration{
		fixtures.CloudflareDeclaration(
			fixtures.WithDeclarationCapabilities(fixtures.CloudflareRecordCapabilities),
		),
	})
	if err != nil {
		t.Fatalf("构造能力映射表失败：%v", err)
	}

	_, err = resolver.Resolve(catalog, intent.Call{
		Tool:     "cloudflare.dns.delete",
		Resource: dnsResource(dnsRecord),
	})
	assertCode(t, err, apperr.CodeCapabilityNotOffered)
}

// ——— 输入自洽（编排错误一律拒绝）———

func TestResolve_IdentityFromAnotherService_IsRefused(t *testing.T) {
	input := dnsInput(t, dnsResource(dnsRecord))
	input.Identity = fixtures.Identity(
		fixtures.WithIdentityService("github"),
		fixtures.WithIdentityEnvironment(matcher.EnvironmentProduction),
	)

	_, err := newResolver(t).Resolve(input)
	assertCode(t, err, apperr.CodeInternal)
}

func TestResolve_IdentityEnvironmentContradiction_IsRefused(t *testing.T) {
	// 身份标记为非生产、意图判定为生产：两者对不上时不能挑一个用。
	input := dnsInput(t, dnsResource(dnsRecord))
	input.Identity = dnsIdentity()
	input.Identity.Environment = matcher.EnvironmentNonProduction

	_, err := newResolver(t).Resolve(input)
	assertCode(t, err, apperr.CodeInternal)
}

func TestNewResolver_WithoutAClock_IsRefused(t *testing.T) {
	if _, err := scope.NewResolver(nil); err == nil {
		t.Fatal("没有时钟也构造出了 Scope Resolver")
	}
}

// ——— 结果的性质 ———

func TestResolve_AmbiguousResource_IsFlaggedButStillResolved(t *testing.T) {
	// REQ-INTENT-002 AC1：资源指向不止一个目标时转人工，不是拒绝。
	resolved := resolve(t, dnsInput(t, dnsResource("*.tele-call.cn")))

	if !resolved.Ambiguous {
		t.Error("带通配符的资源没有被标记为歧义")
	}
	if resolved.Scope.Covers(resolved.Scope) {
		t.Error("带通配符的 Scope 覆盖了自己，它会被当成一条可复用的授权")
	}
}

func TestResolve_EnvironmentAssumed_IsCarriedToTheCaller(t *testing.T) {
	// REQ-INTENT-003 AC2：审批页面要显示「环境未确认，按生产处理」。
	resolver, err := intent.NewResolver(intent.Options{})
	if err != nil {
		t.Fatalf("构造 Intent Resolver 失败：%v", err)
	}
	catalog, err := intent.NewCatalog([]adapters.Declaration{
		fixtures.CloudflareDeclaration(
			fixtures.WithDeclarationCapabilities(fixtures.CloudflareRecordCapabilities),
		),
	})
	if err != nil {
		t.Fatalf("构造能力映射表失败：%v", err)
	}
	assumed, err := resolver.Resolve(catalog, intent.Call{
		Tool:     "cloudflare.dns.update",
		Resource: dnsResource(dnsRecord),
	})
	if err != nil {
		t.Fatalf("解析意图失败：%v", err)
	}

	input := dnsInput(t, dnsResource(dnsRecord))
	input.Intent = assumed
	input.Identity.Environment = ""

	resolved := resolve(t, input)
	if !resolved.EnvironmentAssumed {
		t.Error("环境是猜出来的，结果里却没有说明")
	}
	if resolved.Scope.Environment != matcher.EnvironmentProduction {
		t.Errorf("环境维度为 %s，期望就高不就低的 production", resolved.Scope.Environment)
	}
}

func TestResolve_Resource_IsCopiedNotShared(t *testing.T) {
	// 收敛后改动输入不能改动 Scope：Scope 会被写进 Lease，与调用方共享一张 map
	// 意味着授权范围可以在签发后被改。
	input := dnsInput(t, dnsResource(dnsRecord))
	resolved := resolve(t, input)

	input.Intent.Resource["record"] = otherRecord

	if resolved.Scope.Resource["record"] != dnsRecord {
		t.Errorf("改动输入后 Scope 的资源变成了 %q", resolved.Scope.Resource["record"])
	}
}

func TestResolve_SameInput_ProducesTheSameScope(t *testing.T) {
	input := dnsInput(t, dnsResource(dnsRecord))
	first := resolve(t, input).Scope

	for round := 0; round < 64; round++ {
		if again := resolve(t, input).Scope; !reflect.DeepEqual(again, first) {
			t.Fatalf("第 %d 轮收敛出的 Scope 与首轮不同", round)
		}
	}
}

// ——— 覆盖关系（REQ-RISK-003 与 REQ-TRUST-002 的判定基础）———

func TestCovers_AnyWiderDimension_IsNotCovered(t *testing.T) {
	base := resolve(t, dnsInput(t, dnsResource(dnsRecord))).Scope

	cases := []struct {
		dimension string
		widen     func(*scope.Scope)
	}{
		{"agent", func(s *scope.Scope) { s.AgentID = "01K1AGENT00000000000000OTH" }},
		{"workspace", func(s *scope.Scope) { s.WorkspaceID = "01K1WORKSPACE0000000000OTH" }},
		{"service", func(s *scope.Scope) { s.Service = "github" }},
		{"identity", func(s *scope.Scope) { s.IdentityID = "01K1IDENTITY000000000000OTH" }},
		{"account", func(s *scope.Scope) { s.Account = "cloudflare-staging" }},
		{"operation", func(s *scope.Scope) { s.Operation = "dns.record.delete" }},
		{"environment", func(s *scope.Scope) { s.Environment = matcher.EnvironmentNonProduction }},
		{"resource 的值", func(s *scope.Scope) {
			s.Resource = map[string]string{
				"zone": dnsZone, "record": otherRecord, "record_type": "A",
			}
		}},
		{"resource 少一个字段", func(s *scope.Scope) {
			s.Resource = map[string]string{
				"zone": dnsZone, "record": dnsRecord,
			}
		}},
		{"resource 多一个字段", func(s *scope.Scope) {
			s.Resource = map[string]string{
				"zone": dnsZone, "record": dnsRecord, "record_type": "A", "proxied": "true",
			}
		}},
		{"时间窗口", func(s *scope.Scope) { s.ExpiresAt = s.ExpiresAt.Add(time.Minute) }},
		{"窗口起点", func(s *scope.Scope) { s.NotBefore = s.NotBefore.Add(-time.Minute) }},
		{"次数", func(s *scope.Scope) { s.RequestLimit = base.RequestLimit + 1 }},
		{"风险上限", func(s *scope.Scope) { s.RiskCeiling = risk.LevelHigh }},
	}

	for _, testCase := range cases {
		t.Run(testCase.dimension+"变大后不再被覆盖", func(t *testing.T) {
			wider := base
			wider.Resource = map[string]string{
				"zone": dnsZone, "record": dnsRecord, "record_type": "A",
			}
			testCase.widen(&wider)

			if base.Covers(wider) {
				t.Errorf("%s 变大后仍被原授权覆盖", testCase.dimension)
			}
		})
	}
}

func TestCovers_NarrowerScope_IsCovered(t *testing.T) {
	base := resolve(t, dnsInput(t, dnsResource(dnsRecord))).Scope
	base.RiskCeiling = risk.LevelHigh
	base.RequestLimit = 10

	narrower := base
	narrower.RiskCeiling = risk.LevelLow
	narrower.RequestLimit = 1
	narrower.ExpiresAt = base.ExpiresAt.Add(-time.Minute)

	if !base.Covers(narrower) {
		t.Error("更窄的 Scope 没有被覆盖，收敛判定会把正常的复用当成扩大")
	}
}

func TestCovers_AScopeMissingAnyDimension_CoversNothing(t *testing.T) {
	// 十个维度逐个抹掉。时间与次数两维在 Resolve 里有默认值，抹不掉，
	// 但调用方可以手搓一个 Scope 交给 Covers —— 那正是收敛校验要挡住的输入。
	base := resolve(t, dnsInput(t, dnsResource(dnsRecord))).Scope

	cases := []struct {
		dimension string
		break_    func(*scope.Scope)
	}{
		{"agent", func(s *scope.Scope) { s.AgentID = "" }},
		{"workspace", func(s *scope.Scope) { s.WorkspaceID = "" }},
		{"service", func(s *scope.Scope) { s.Service = "" }},
		{"account", func(s *scope.Scope) { s.IdentityID = "" }},
		{"resource", func(s *scope.Scope) { s.Resource = nil }},
		{"operation", func(s *scope.Scope) { s.Operation = "" }},
		{"time", func(s *scope.Scope) { s.ExpiresAt = s.NotBefore }},
		{"request_count", func(s *scope.Scope) { s.RequestLimit = 0 }},
		{"environment", func(s *scope.Scope) { s.Environment = "" }},
		{"environment 取值不合法", func(s *scope.Scope) { s.Environment = "staging" }},
		{"risk_level", func(s *scope.Scope) { s.RiskCeiling = "" }},
		{"risk_level 取值不合法", func(s *scope.Scope) { s.RiskCeiling = "critical" }},
	}

	for _, testCase := range cases {
		t.Run(testCase.dimension+" 维度为空时覆盖不成立", func(t *testing.T) {
			broken := base
			broken.Resource = map[string]string{
				"zone": dnsZone, "record": dnsRecord, "record_type": "A",
			}
			testCase.break_(&broken)

			if base.Covers(broken) {
				t.Errorf("缺少 %s 维度的 Scope 被判为落在授权之内", testCase.dimension)
			}
			if broken.Covers(base) {
				t.Errorf("缺少 %s 维度的 Scope 覆盖了一个完整的 Scope", testCase.dimension)
			}
		})
	}
}

func TestCovers_WildcardResource_CoversNothingAndIsCoveredByNothing(t *testing.T) {
	// 与 Intent Resolver 的歧义判定用同一组字符：`*` 与 `?` 都指向不止一个目标。
	base := resolve(t, dnsInput(t, dnsResource(dnsRecord))).Scope

	for _, pattern := range []string{"*.tele-call.cn", "api?.tele-call.cn"} {
		t.Run(pattern+" 覆盖不了任何东西", func(t *testing.T) {
			wild := resolve(t, dnsInput(t, dnsResource(pattern))).Scope

			if base.Covers(wild) {
				t.Error("通配符 Scope 被判为落在一条具体授权之内")
			}
			if wild.Covers(base) {
				t.Error("通配符 Scope 覆盖了一条具体授权")
			}
			if wild.Covers(wild) {
				t.Error("通配符 Scope 覆盖了它自己")
			}
		})
	}
}

func TestCoversIgnoringWindow_IgnoresTheWindowAndNothingElse(t *testing.T) {
	// 已签发的授权与新请求的时间窗口本来就对不上：授权是过去某一刻记下的，
	// 请求的窗口从「现在」起算。让窗口参与比较，任何授权都会立刻显得「不够宽」。
	granted := resolve(t, dnsInput(t, dnsResource(dnsRecord))).Scope

	shifted := granted
	shifted.NotBefore = granted.NotBefore.Add(time.Hour)
	shifted.ExpiresAt = granted.ExpiresAt.Add(time.Hour)

	if granted.Covers(shifted) {
		t.Error("窗口整体后移后仍被 Covers 判为落在授权之内")
	}
	if !granted.CoversIgnoringWindow(shifted) {
		t.Error("忽略窗口后仍不成立，复用就永远不可能")
	}

	// 其余九个维度照常比较：忽略的只有窗口。
	widened := shifted
	widened.RequestLimit = granted.RequestLimit + 1
	if granted.CoversIgnoringWindow(widened) {
		t.Error("次数变多后仍被判为落在授权之内")
	}

	elsewhere := shifted
	elsewhere.Resource = map[string]string{
		"zone": dnsZone, "record": otherRecord, "record_type": "A",
	}
	if granted.CoversIgnoringWindow(elsewhere) {
		t.Error("换了资源后仍被判为落在授权之内")
	}
}
