package intent_test

import (
	"strings"
	"testing"

	adapters "github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/core/intent"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * core/intent 的行为用例（REQ-INTENT-001/002/003）。
 *
 * 本包是纯函数，不需要数据库：能力声明由调用方加载后传入（ADR-003）。
 */

const (
	dnsTool  = "cloudflare.dns.update"
	dnsZone  = "tele-call.cn"
	dnsRecrd = "api.tele-call.cn"
)

func dnsResource() string {
	return `{"zone":"` + dnsZone + `","record":"` + dnsRecrd + `"}`
}

func newCatalog(t *testing.T, declarations ...adapters.Declaration) *intent.Catalog {
	t.Helper()

	catalog, err := intent.NewCatalog(declarations)
	if err != nil {
		t.Fatalf("构造能力映射表失败：%v", err)
	}
	return catalog
}

func newResolver(t *testing.T, options intent.Options) *intent.Resolver {
	t.Helper()

	resolver, err := intent.NewResolver(options)
	if err != nil {
		t.Fatalf("构造解析器失败：%v", err)
	}
	return resolver
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

// ——— 标准化（REQ-INTENT-001）———

func TestResolve_DNSUpdate_MatchesTheDocumentedExample(t *testing.T) {
	// AC1：cloudflare.dns.update + zone=tele-call.cn 解析出 service=cloudflare、
	// operation=dns.record.update、environment=production、reversible=true。
	// 一并给出 record：更新一条 DNS 记录本来就要指到记录（PRD §10.4 的 Scope 例子）。
	resolver := newResolver(t, intent.Options{})
	catalog := newCatalog(t, fixtures.CloudflareDeclaration())

	resolved, err := resolver.Resolve(catalog, intent.Call{
		Tool:          dnsTool,
		Resource:      dnsResource(),
		DesiredChange: `{"content":"203.0.113.9"}`,
	})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}

	if resolved.Service != "cloudflare" {
		t.Errorf("service 为 %q，期望 cloudflare", resolved.Service)
	}
	if resolved.Operation != "dns.record.update" {
		t.Errorf("operation 为 %q，期望 dns.record.update", resolved.Operation)
	}
	if resolved.Environment != matcher.EnvironmentProduction {
		t.Errorf("environment 为 %q，期望 production", resolved.Environment)
	}
	if !resolved.Reversible {
		t.Error("reversible 为 false，期望 true")
	}
	if resolved.Resource["zone"] != dnsZone {
		t.Errorf("zone 为 %q，期望 %q", resolved.Resource["zone"], dnsZone)
	}
	if resolved.ResourceKey != "record="+dnsRecrd+";zone="+dnsZone {
		t.Errorf("资源标识文本为 %q，字段顺序不稳定就无法与记忆比较", resolved.ResourceKey)
	}
}

func TestResolve_ToolOutsideTheCatalog_IsNeverGuessed(t *testing.T) {
	// AC3：映射表之外的工具名一律 capability_not_offered，且不做模糊匹配。
	resolver := newResolver(t, intent.Options{})
	catalog := newCatalog(t, fixtures.CloudflareDeclaration())

	cases := []struct {
		name string
		tool string
	}{
		{"少一个字母", "cloudflare.dns.updat"},
		{"多一个字母", "cloudflare.dns.updates"},
		{"大小写不同", "Cloudflare.DNS.Update"},
		{"只有前缀", "cloudflare.dns"},
		{"只有后缀", "dns.update"},
		{"换了分隔符", "cloudflare_dns_update"},
		{"两侧有空格", " cloudflare.dns.update "},
		{"用操作名当工具名", "dns.record.update"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := resolver.Resolve(catalog, intent.Call{
				Tool:     testCase.tool,
				Resource: dnsResource(),
			})
			assertCode(t, err, apperr.CodeCapabilityNotOffered)
		})
	}
}

func TestResolve_NeverOutputsAnUndeclaredOperation(t *testing.T) {
	// REQ-INTENT-002 AC3：任何情况下都不得输出一个未在 Adapter 中声明的 operation。
	// 解析器没有别的取值来源 —— 这条用例守的是「将来也不许有」。
	resolver := newResolver(t, intent.Options{})
	catalog := newCatalog(t, fixtures.Declaration(), fixtures.CloudflareDeclaration())
	declared := map[string]bool{"pull_request.create": true, "dns.record.update": true}

	calls := []intent.Call{
		{Tool: dnsTool, Resource: dnsResource()},
		{Tool: "github.pull_request.create", Resource: `{"repo":"Runcoor/opendelo"}`},
		{Method: "PUT", Path: "/zones/abc/dns_records/def", Resource: dnsResource()},
		{Method: "POST", Path: "/repos/Runcoor/opendelo/pulls", Resource: `{"repo":"Runcoor/opendelo"}`},
		{Tool: "cloudflare.dns.delete", Resource: dnsResource()},
		{Method: "DELETE", Path: "/zones/abc/dns_records/def", Resource: dnsResource()},
	}

	for _, call := range calls {
		resolved, err := resolver.Resolve(catalog, call)
		if err != nil {
			continue // 拒绝也是合规的结果，这里只关心「放行时输出了什么」。
		}
		if !declared[resolved.Operation] {
			t.Errorf("输出了未声明的 operation %q", resolved.Operation)
		}
	}
}

func TestResolve_ByMethodAndPath_FindsTheSameCapability(t *testing.T) {
	resolver := newResolver(t, intent.Options{})
	catalog := newCatalog(t, fixtures.CloudflareDeclaration())

	byTool, err := resolver.Resolve(catalog, intent.Call{Tool: dnsTool, Resource: dnsResource()})
	if err != nil {
		t.Fatalf("按工具名解析失败：%v", err)
	}
	byRoute, err := resolver.Resolve(catalog, intent.Call{
		Method:   "put",
		Path:     "/zones/9f1/dns_records/3ab",
		Resource: dnsResource(),
	})
	if err != nil {
		t.Fatalf("按路径解析失败：%v", err)
	}

	if byTool.Operation != byRoute.Operation || byTool.Service != byRoute.Service {
		t.Errorf("两个入口解析出不同的能力：%+v 与 %+v", byTool, byRoute)
	}
}

func TestResolve_PathThatDoesNotFitTheTemplate_IsNotOffered(t *testing.T) {
	// 模板逐段比较，不支持尾部通配 —— 否则端点白名单形同虚设。
	resolver := newResolver(t, intent.Options{})
	catalog := newCatalog(t, fixtures.CloudflareDeclaration())

	cases := []string{
		"/zones/9f1/dns_records",
		"/zones/9f1/dns_records/3ab/extra",
		"/zones/9f1/records/3ab",
		"/zones//dns_records/3ab",
	}

	for _, requestPath := range cases {
		t.Run(requestPath, func(t *testing.T) {
			_, err := resolver.Resolve(catalog, intent.Call{
				Method:   "PUT",
				Path:     requestPath,
				Resource: dnsResource(),
			})
			assertCode(t, err, apperr.CodeCapabilityNotOffered)
		})
	}
}

func TestResolve_OnePathMatchingTwoDeclarations_IsRefused(t *testing.T) {
	// 两条声明都能解释这个请求时挑一条等于替用户做主（REQ-INTENT-002）。
	resolver := newResolver(t, intent.Options{})
	overlapping := fixtures.CloudflareDeclaration(
		fixtures.WithDeclarationID("01K1ADAPTER000000000000003"),
		fixtures.WithDeclarationService("cloudflare-mirror"),
		fixtures.WithDeclarationCapabilities(strings.Replace(
			fixtures.CloudflareCapabilities,
			`"tool":"cloudflare.dns.update"`,
			`"tool":"mirror.dns.update"`, 1)),
	)
	catalog := newCatalog(t, fixtures.CloudflareDeclaration(), overlapping)

	_, err := resolver.Resolve(catalog, intent.Call{
		Method:   "PUT",
		Path:     "/zones/9f1/dns_records/3ab",
		Resource: dnsResource(),
	})
	assertCode(t, err, apperr.CodeCapabilityNotOffered)

	// 同一份表按工具名仍然是确定的：歧义只发生在路径这一侧。
	if _, toolErr := resolver.Resolve(catalog, intent.Call{
		Tool: dnsTool, Resource: dnsResource(),
	}); toolErr != nil {
		t.Fatalf("按工具名解析也失败了：%v", toolErr)
	}
}

// ——— 资源字段 ———

func TestResolve_ResourceFields_MustBeCompleteAndTyped(t *testing.T) {
	resolver := newResolver(t, intent.Options{})
	catalog := newCatalog(t, fixtures.CloudflareDeclaration())

	cases := []struct {
		name     string
		resource string
	}{
		{"没有资源字段", ""},
		{"不是 JSON", "not-json"},
		{"不是对象", `["zone"]`},
		{"缺一个字段", `{"zone":"tele-call.cn"}`},
		{"字段是空串", `{"zone":"","record":"api.tele-call.cn"}`},
		{"字段不是字符串", `{"zone":123,"record":"api.tele-call.cn"}`},
		{"字段是 null", `{"zone":null,"record":"api.tele-call.cn"}`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := resolver.Resolve(catalog, intent.Call{Tool: dnsTool, Resource: testCase.resource})
			assertCode(t, err, apperr.CodeInvalidRequest)
		})
	}
}

func TestResolve_UndeclaredFields_AreIgnoredAndReported(t *testing.T) {
	// REQ-SCOPE-002：越权字段忽略并记录。本包负责「忽略」并把字段名交出去，
	// 记审计由调用方完成。
	resolver := newResolver(t, intent.Options{})
	catalog := newCatalog(t, fixtures.CloudflareDeclaration())

	resolved, err := resolver.Resolve(catalog, intent.Call{
		Tool: dnsTool,
		Resource: `{"zone":"tele-call.cn","record":"api.tele-call.cn",` +
			`"scope":"*","permissions":["admin"]}`,
	})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}

	if len(resolved.Resource) != 2 {
		t.Errorf("资源字段有 %d 个，越权字段没有被挡在意图之外：%v", len(resolved.Resource), resolved.Resource)
	}
	if strings.Join(resolved.IgnoredFields, ",") != "permissions,scope" {
		t.Errorf("被忽略的字段为 %v，调用方无从记 security.scope_injection_ignored", resolved.IgnoredFields)
	}
	if resolved.ResourceAmbiguous {
		t.Error("越权字段里的通配符影响了资源歧义判定")
	}
}

func TestResolve_WildcardResource_IsMarkedAmbiguous(t *testing.T) {
	// REQ-INTENT-002：资源标识指向不止一个目标时不得猜测高影响资源。
	resolver := newResolver(t, intent.Options{})
	catalog := newCatalog(t, fixtures.CloudflareDeclaration())

	cases := []struct {
		name     string
		resource string
	}{
		{"星号", `{"zone":"tele-call.cn","record":"*.tele-call.cn"}`},
		{"问号", `{"zone":"tele-call.cn","record":"ap?.tele-call.cn"}`},
		{"整个 zone 是星号", `{"zone":"*","record":"api.tele-call.cn"}`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			resolved, err := resolver.Resolve(catalog, intent.Call{Tool: dnsTool, Resource: testCase.resource})
			if err != nil {
				t.Fatalf("解析失败：%v", err)
			}
			if !resolved.ResourceAmbiguous {
				t.Error("通配的资源标识没有被标记为歧义，决策链路会把它当成一个确定目标执行")
			}
		})
	}

	precise, err := resolver.Resolve(catalog, intent.Call{Tool: dnsTool, Resource: dnsResource()})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if precise.ResourceAmbiguous {
		t.Error("精确的资源标识被误判为歧义")
	}
}

// ——— 环境判定（REQ-INTENT-003）———

func TestResolve_UnknownEnvironment_IsTreatedAsProduction(t *testing.T) {
	// AC2：未标记环境的资源默认判为 production，并且要能告诉审批页面「环境未确认」。
	resolver := newResolver(t, intent.Options{})
	catalog := newCatalog(t, fixtures.CloudflareDeclaration())

	resolved, err := resolver.Resolve(catalog, intent.Call{Tool: dnsTool, Resource: dnsResource()})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}

	if resolved.Environment != matcher.EnvironmentProduction {
		t.Errorf("environment 为 %q，期望 production", resolved.Environment)
	}
	if !resolved.EnvironmentAssumed {
		t.Error("环境是猜出来的却没有标记，审批页面就说不出「环境未确认，按生产处理」")
	}
}

func TestResolve_IdentityMarker_OutranksNamingRules(t *testing.T) {
	// 优先级：Identity 的显式标记 → 命名规则 → 默认生产。
	resolver := newResolver(t, intent.Options{ProductionPatterns: []string{"*.tele-call.cn"}})
	catalog := newCatalog(t, fixtures.CloudflareDeclaration())

	resolved, err := resolver.Resolve(catalog, intent.Call{
		Tool:                dnsTool,
		Resource:            dnsResource(),
		IdentityEnvironment: matcher.EnvironmentNonProduction,
	})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}

	if resolved.Environment != matcher.EnvironmentNonProduction {
		t.Errorf("environment 为 %q，期望身份上的显式标记 non-production", resolved.Environment)
	}
	if resolved.EnvironmentAssumed {
		t.Error("有显式标记却被标成了「未确认」")
	}
}

func TestResolve_NamingRules_DecideTheEnvironment(t *testing.T) {
	catalog := newCatalog(t, fixtures.CloudflareDeclaration())

	cases := []struct {
		name        string
		options     intent.Options
		resource    string
		environment matcher.Environment
		assumed     bool
	}{
		{
			name:        "命中生产模式",
			options:     intent.Options{ProductionPatterns: []string{"*.tele-call.cn"}},
			resource:    dnsResource(),
			environment: matcher.EnvironmentProduction,
		},
		{
			name:        "命中非生产模式",
			options:     intent.Options{NonProductionPatterns: []string{"*.dev.tele-call.cn"}},
			resource:    `{"zone":"tele-call.cn","record":"api.dev.tele-call.cn"}`,
			environment: matcher.EnvironmentNonProduction,
		},
		{
			name: "两边都命中时就高不就低",
			options: intent.Options{
				ProductionPatterns:    []string{"*.tele-call.cn"},
				NonProductionPatterns: []string{"*.tele-call.cn"},
			},
			resource:    dnsResource(),
			environment: matcher.EnvironmentProduction,
		},
		{
			name:        "两边都不命中时按生产处理并标记未确认",
			options:     intent.Options{NonProductionPatterns: []string{"*.example.test"}},
			resource:    dnsResource(),
			environment: matcher.EnvironmentProduction,
			assumed:     true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			resolver := newResolver(t, testCase.options)

			resolved, err := resolver.Resolve(catalog, intent.Call{Tool: dnsTool, Resource: testCase.resource})
			if err != nil {
				t.Fatalf("解析失败：%v", err)
			}
			if resolved.Environment != testCase.environment {
				t.Errorf("environment 为 %q，期望 %q", resolved.Environment, testCase.environment)
			}
			if resolved.EnvironmentAssumed != testCase.assumed {
				t.Errorf("「未确认」标记为 %v，期望 %v", resolved.EnvironmentAssumed, testCase.assumed)
			}
		})
	}
}

func TestNewResolver_MalformedPattern_IsRefused(t *testing.T) {
	cases := []intent.Options{
		{ProductionPatterns: []string{"["}},
		{NonProductionPatterns: []string{"["}},
		{ProductionPatterns: []string{""}},
	}

	for _, options := range cases {
		if _, err := intent.NewResolver(options); !apperr.Is(err, apperr.CodeInvalidConfiguration) {
			t.Errorf("模式 %+v 的错误码为 %s，期望 invalid_configuration", options, apperr.CodeOf(err))
		}
	}
}

// ——— 能力映射表 ———

func TestNewCatalog_IncompleteDeclaration_IsRefused(t *testing.T) {
	// 一条说不清自己是否可逆、涉不涉及敏感数据的能力，会让后面每一步都留一个未知数。
	cases := []struct {
		name    string
		removed string
	}{
		{"没有工具名", `"tool":"cloudflare.dns.update",`},
		{"没有操作名", `"operation":"dns.record.update",`},
		{"没有方法", `"method":"PUT",`},
		{"没有路径", `"path":"/zones/{zone_id}/dns_records/{record_id}",`},
		{"没有风险标签", `"risk":"medium",`},
		{"没声明是否幂等", `"idempotent":true,`},
		{"没声明是否可逆", `"reversible":true,`},
		{"没声明是否涉及敏感数据", `"sensitive_data":false,`},
		{"没声明资源字段", `"resource_keys":["zone","record"]`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			capabilities := strings.Replace(fixtures.CloudflareCapabilities, testCase.removed, "", 1)
			if capabilities == fixtures.CloudflareCapabilities {
				t.Fatalf("片段 %q 不在声明里，这条用例什么也没测", testCase.removed)
			}
			// 去掉末段时会多出一个逗号，补回合法 JSON 以确保被拒的原因是缺字段而不是语法。
			capabilities = strings.Replace(capabilities, `,}`, `}`, 1)

			_, err := intent.NewCatalog([]adapters.Declaration{
				fixtures.CloudflareDeclaration(fixtures.WithDeclarationCapabilities(capabilities)),
			})
			if !apperr.Is(err, apperr.CodeInvalidConfiguration) {
				t.Fatalf("错误码为 %s，期望 invalid_configuration（%v）", apperr.CodeOf(err), err)
			}
			// 语法坏掉的 JSON 也返回同一个码。断言拒绝的理由是「缺字段」而不是
			// 「解析不了」，否则这条用例证明的只是我删坏了一个字符串。
			if !strings.Contains(err.Error(), "能力声明不可用") {
				t.Fatalf("拒绝的理由是 %q，不是缺字段", err)
			}
		})
	}
}

func TestNewCatalog_UnusableDeclarations_AreRefusedNotSkipped(t *testing.T) {
	// 跳过一条坏声明等于让「声明得不完整」悄悄退化成「没有这个能力」，
	// 而两者在账本上应该是不同的事。
	cases := []struct {
		name         string
		capabilities string
	}{
		{"不是 JSON", `{`},
		{"不是数组", `{"tool":"x"}`},
		{"空数组", `[]`},
		{"风险标签不在取值里", strings.Replace(
			fixtures.CloudflareCapabilities, `"risk":"medium"`, `"risk":"critical"`, 1)},
		{"资源字段名为空", strings.Replace(
			fixtures.CloudflareCapabilities, `["zone","record"]`, `[""]`, 1)},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := intent.NewCatalog([]adapters.Declaration{
				fixtures.CloudflareDeclaration(fixtures.WithDeclarationCapabilities(testCase.capabilities)),
			})
			if !apperr.Is(err, apperr.CodeInvalidConfiguration) {
				t.Fatalf("错误码为 %s，期望 invalid_configuration（%v）", apperr.CodeOf(err), err)
			}
		})
	}
}

func TestNewCatalog_DuplicateTool_IsRefused(t *testing.T) {
	// 同一个工具名映射到两个操作时，任何一次解析都在两条之间二选一。
	_, err := intent.NewCatalog([]adapters.Declaration{
		fixtures.CloudflareDeclaration(),
		fixtures.CloudflareDeclaration(
			fixtures.WithDeclarationID("01K1ADAPTER000000000000004"),
			fixtures.WithDeclarationService("cloudflare-copy"),
		),
	})
	if !apperr.Is(err, apperr.CodeInvalidConfiguration) {
		t.Fatalf("错误码为 %s，期望 invalid_configuration（%v）", apperr.CodeOf(err), err)
	}
}

func TestNewCatalog_DisabledDeclaration_DoesNotParticipate(t *testing.T) {
	resolver := newResolver(t, intent.Options{})
	catalog := newCatalog(t, fixtures.CloudflareDeclaration(func(declaration *adapters.Declaration) {
		declaration.Status = adapters.StatusDisabled
	}))

	if catalog.Size() != 0 {
		t.Errorf("停用的 Adapter 贡献了 %d 条能力", catalog.Size())
	}
	_, err := resolver.Resolve(catalog, intent.Call{Tool: dnsTool, Resource: dnsResource()})
	assertCode(t, err, apperr.CodeCapabilityNotOffered)
}

func TestResolve_WithoutACatalogOrARoute_IsRefused(t *testing.T) {
	resolver := newResolver(t, intent.Options{})

	_, nilErr := resolver.Resolve(nil, intent.Call{Tool: dnsTool, Resource: dnsResource()})
	assertCode(t, nilErr, apperr.CodeCapabilityNotOffered)

	catalog := newCatalog(t, fixtures.CloudflareDeclaration())
	_, emptyErr := resolver.Resolve(catalog, intent.Call{Resource: dnsResource()})
	assertCode(t, emptyErr, apperr.CodeInvalidRequest)

	_, pathOnlyErr := resolver.Resolve(catalog, intent.Call{Path: "/zones/9f1/dns_records/3ab"})
	assertCode(t, pathOnlyErr, apperr.CodeInvalidRequest)
}

func TestResolve_ReadOperation_CarriesNoDesiredChange(t *testing.T) {
	resolver := newResolver(t, intent.Options{})
	catalog := newCatalog(t, fixtures.CloudflareDeclaration())

	resolved, err := resolver.Resolve(catalog, intent.Call{Tool: dnsTool, Resource: dnsResource()})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if resolved.DesiredChange != "" {
		t.Errorf("没有提交变更却解析出 %q", resolved.DesiredChange)
	}
}

func TestResolve_SameCall_AlwaysProducesTheSameIntent(t *testing.T) {
	// 确定性是本包的立身之本：同样的输入必须得到同样的输出，
	// 否则同一次操作在账本里会有两种解释。map 的遍历顺序是最容易破坏它的地方。
	resolver := newResolver(t, intent.Options{ProductionPatterns: []string{"*.tele-call.cn"}})
	catalog := newCatalog(t, fixtures.CloudflareDeclaration())
	call := intent.Call{
		Tool: dnsTool,
		Resource: `{"zone":"tele-call.cn","record":"api.tele-call.cn",` +
			`"scope":"*","permissions":["admin"],"extra":"x"}`,
		DesiredChange: `{"content":"203.0.113.9"}`,
	}

	first, err := resolver.Resolve(catalog, call)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	for round := range 64 {
		again, resolveErr := resolver.Resolve(catalog, call)
		if resolveErr != nil {
			t.Fatalf("第 %d 轮解析失败：%v", round, resolveErr)
		}
		if again.ResourceKey != first.ResourceKey {
			t.Fatalf("第 %d 轮的资源标识为 %q，首轮为 %q", round, again.ResourceKey, first.ResourceKey)
		}
		if strings.Join(again.IgnoredFields, ",") != strings.Join(first.IgnoredFields, ",") {
			t.Fatalf("第 %d 轮忽略的字段为 %v，首轮为 %v", round, again.IgnoredFields, first.IgnoredFields)
		}
	}
}

func TestResolve_DeclarationWithoutAService_ProducesNoIntent(t *testing.T) {
	// 输出校验是最后一道防线（REQ-INTENT-001 AC2）：映射表能建起来，不代表解析
	// 出来的意图是完整的。少了服务名的声明恰好能穿过建表那一层，到这里必须被拦下 ——
	// 一个没有服务的意图会让后面的身份匹配、Scope 收敛与账本全部失去落点。
	resolver := newResolver(t, intent.Options{})
	catalog := newCatalog(t, fixtures.CloudflareDeclaration(
		func(declaration *adapters.Declaration) { declaration.Service = "" },
	))

	_, err := resolver.Resolve(catalog, intent.Call{Tool: dnsTool, Resource: dnsResource()})
	assertCode(t, err, apperr.CodeCapabilityNotOffered)
	if !strings.Contains(err.Error(), "没有服务") {
		t.Errorf("拒绝的理由是 %q，不是「解析结果不完整」", err)
	}
}

func TestCatalogToolFor_AnsweredOnlyForDeclaredOperations(t *testing.T) {
	// 8787 面收到的是服务名 + 操作名，而解析按工具名进行。这张表是两者之间
	// 唯一的换算，没有它那一面就得自己按 REQ-MCP-001 的规则拼一个名字出来。
	catalog := newCatalog(t, fixtures.CloudflareDeclaration())

	tool, declared := catalog.ToolFor("cloudflare", "dns.record.update")
	if !declared {
		t.Fatal("已声明的操作查不到工具名")
	}
	if tool != dnsTool {
		t.Errorf("工具名为 %q，期望 %q", tool, dnsTool)
	}

	// 服务与操作各错一次，两次都必须查不到 —— 只按操作名匹配的话，
	// 另一个服务上的同名操作会被当成这一个。
	if _, declared := catalog.ToolFor("cloudflare", "dns.record.delete"); declared {
		t.Error("未声明的操作查到了工具名")
	}
	if _, declared := catalog.ToolFor("github", "dns.record.update"); declared {
		t.Error("另一个服务上的同名操作被当成了本服务的")
	}
}

func TestNewCatalog_DuplicateOperationWithinAService_IsRefused(t *testing.T) {
	// 工具名不撞不等于操作名不撞。账本、Scope 与 Lease 认的是操作名，
	// 撞了就答不出「这条授权覆盖的是哪一个操作」。
	const twoToolsOneOperation = `[` +
		`{"tool":"cloudflare.dns.update","operation":"dns.record.update","method":"PUT",` +
		`"path":"/zones/{zone}/dns_records/{record}","risk":"high","idempotent":true,` +
		`"reversible":true,"sensitive_data":false,"resource_keys":["zone","record"]},` +
		`{"tool":"cloudflare.record.update","operation":"dns.record.update","method":"PATCH",` +
		`"path":"/zones/{zone}/dns_records/{record}/patch","risk":"high","idempotent":true,` +
		`"reversible":true,"sensitive_data":false,"resource_keys":["zone","record"]}` +
		`]`

	_, err := intent.NewCatalog([]adapters.Declaration{
		fixtures.CloudflareDeclaration(fixtures.WithDeclarationCapabilities(twoToolsOneOperation)),
	})
	if !apperr.Is(err, apperr.CodeInvalidConfiguration) {
		t.Fatalf("错误码为 %s，期望 invalid_configuration（%v）", apperr.CodeOf(err), err)
	}
}
