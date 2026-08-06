package registry_test

import (
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * 能力声明与注册表的行为用例（REQ-ADAPTER-001）。
 *
 * 九项声明缺一即启动失败，未声明的操作调不出来 —— 两条都要能被单独证伪。
 */

// readCapability 是一个合法的读取类声明，用例只改自己关心的那一项。
func readCapability() registry.Capability {
	return registry.Capability{
		Operation:      "read_repository",
		InputSchema:    `{"type":"object"}`,
		MinimumScope:   registry.MinimumScope{ResourceKeys: []string{"owner", "repo"}},
		RiskLabel:      registry.RiskLabelLow,
		Method:         "GET",
		Path:           "/repos/{owner}/{repo}",
		RedactionRules: []string{},
		ResponseFields: []string{"id", "name"},
		Rollback:       registry.RollbackNone,
		Idempotency:    registry.Idempotent,
	}
}

// writeCapability 是一个合法的写入类声明。
func writeCapability() registry.Capability {
	capability := readCapability()
	capability.Operation = "create_issue"
	capability.RiskLabel = registry.RiskLabelMedium
	capability.Method = "POST"
	capability.Path = "/repos/{owner}/{repo}/issues"
	capability.Rollback = registry.RollbackManual
	capability.Idempotency = registry.NonIdempotent
	return capability
}

func assertConfigError(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("期望声明被拒绝，实际通过了")
	}
	if !apperr.Is(err, apperr.CodeInvalidConfiguration) {
		t.Fatalf("错误码为 %s，期望 invalid_configuration（%v）", apperr.CodeOf(err), err)
	}
}

func TestCapability_ValidDeclaration_IsAccepted(t *testing.T) {
	for _, capability := range []registry.Capability{readCapability(), writeCapability()} {
		if err := capability.Validate(); err != nil {
			t.Errorf("%s 被拒绝：%v", capability.Operation, err)
		}
	}
}

func TestCapability_MissingAnyOfTheNineDeclarations_IsRefused(t *testing.T) {
	// REQ-ADAPTER-001 AC1：九项缺一即启动失败。
	cases := []struct {
		name   string
		mutate func(*registry.Capability)
	}{
		{"没有操作名", func(c *registry.Capability) { c.Operation = "  " }},
		{"没有输入 Schema", func(c *registry.Capability) { c.InputSchema = "" }},
		{"输入 Schema 不是 JSON", func(c *registry.Capability) { c.InputSchema = "{not json" }},
		{"没有最小 Scope", func(c *registry.Capability) {
			// 路径不带占位符，好让「占位符必须在最小 Scope 里」那条规则
			// 不抢在前面 —— 否则这里测的就不是「有没有声明最小 Scope」了。
			c.Path = "/user/repos"
			c.MinimumScope.ResourceKeys = nil
		}},
		{"没有风险标签", func(c *registry.Capability) { c.RiskLabel = "" }},
		{"风险标签认不出来", func(c *registry.Capability) { c.RiskLabel = "critical" }},
		{"没有请求方法", func(c *registry.Capability) { c.Method = "" }},
		{"请求方法大小写不对", func(c *registry.Capability) { c.Method = "get" }},
		{"没有请求路径", func(c *registry.Capability) { c.Path = "repos" }},
		{"没有声明脱敏规则", func(c *registry.Capability) { c.RedactionRules = nil }},
		{"没有响应过滤白名单", func(c *registry.Capability) { c.ResponseFields = nil }},
		{"没有回滚能力", func(c *registry.Capability) { c.Rollback = "" }},
		{"回滚能力认不出来", func(c *registry.Capability) { c.Rollback = "maybe" }},
		{"没有幂等性", func(c *registry.Capability) { c.Idempotency = "" }},
		{"幂等性认不出来", func(c *registry.Capability) { c.Idempotency = "sometimes" }},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"时拒绝注册", func(t *testing.T) {
			capability := readCapability()
			testCase.mutate(&capability)
			assertConfigError(t, capability.Validate())
		})
	}
}

func TestCapability_WriteOperationMissingIdempotency_IsRefused(t *testing.T) {
	// 读取操作还有一条「只能是 idempotent」的一致性规则会先一步拒绝，
	// 因此「缺幂等性」必须在写操作上单独验证 ——
	// 否则把那项检查整个删掉，用例也不会红。
	for _, value := range []registry.Idempotency{"", "sometimes"} {
		capability := writeCapability()
		capability.Idempotency = value

		err := capability.Validate()
		assertConfigError(t, err)
		if !strings.Contains(err.Error(), "幂等性") {
			t.Errorf("拒绝理由为 %q，期望来自幂等性检查", err)
		}
	}
}

func TestCapability_EmptyRedactionRules_IsADeclarationNotAnOmission(t *testing.T) {
	// nil 与空切片必须区别对待，否则「忘了声明」与「本操作没有额外规则」
	// 在类型上无法分开，AC1 就落不了地。
	capability := readCapability()
	capability.RedactionRules = []string{}
	if err := capability.Validate(); err != nil {
		t.Errorf("空的脱敏规则被当成漏声明：%v", err)
	}

	capability.RedactionRules = nil
	assertConfigError(t, capability.Validate())
}

func TestCapability_NatureContradictingTheRiskLabel_IsRefused(t *testing.T) {
	// 对照 core/risk 的规则表：前四种性质命中即封顶 high。声明成 low 的话
	// 风险引擎照样会算成 high，但那要等到运行时才发现，而声明本身已经错了。
	cases := []struct {
		name   string
		mutate func(*registry.Capability)
	}{
		{"删除类操作标成 medium", func(c *registry.Capability) {
			c.Nature.Destructive = true
			c.RiskLabel = registry.RiskLabelMedium
		}},
		{"权限变更标成 medium", func(c *registry.Capability) {
			c.Nature.PermissionChange = true
			c.RiskLabel = registry.RiskLabelMedium
		}},
		{"读取 Secret 标成 medium", func(c *registry.Capability) {
			c.Nature.SecretAccess = true
			c.RiskLabel = registry.RiskLabelMedium
		}},
		{"账单操作标成 medium", func(c *registry.Capability) {
			c.Nature.Billing = true
			c.RiskLabel = registry.RiskLabelMedium
		}},
		{"对外通信的写操作标成 low", func(c *registry.Capability) {
			c.Nature.ExternalCommunication = true
			c.RiskLabel = registry.RiskLabelLow
		}},
		{"不可逆的写操作标成 medium", func(c *registry.Capability) {
			c.Rollback = registry.RollbackNone
			c.RiskLabel = registry.RiskLabelMedium
		}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"时拒绝注册", func(t *testing.T) {
			capability := writeCapability()
			testCase.mutate(&capability)
			assertConfigError(t, capability.Validate())
		})
	}
}

func TestCapability_HighRiskNature_IsAcceptedWhenLabelledHigh(t *testing.T) {
	// 反向对照：性质与标签一致时必须能注册，否则上面那组用例可能只是
	// 因为「写操作一律拒绝」而通过。
	capability := writeCapability()
	capability.Nature = registry.Nature{Destructive: true, PermissionChange: true}
	capability.Method = "DELETE"
	capability.RiskLabel = registry.RiskLabelHigh
	capability.Rollback = registry.RollbackNone

	if err := capability.Validate(); err != nil {
		t.Errorf("性质与标签一致的高风险声明被拒绝：%v", err)
	}
}

func TestCapability_ReadOperationContradictions_AreRefused(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*registry.Capability)
	}{
		{"读取操作声明为非幂等", func(c *registry.Capability) { c.Idempotency = registry.NonIdempotent }},
		{"读取操作声明为删除类", func(c *registry.Capability) { c.Nature.Destructive = true }},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"时拒绝注册", func(t *testing.T) {
			capability := readCapability()
			testCase.mutate(&capability)
			assertConfigError(t, capability.Validate())
		})
	}
}

func TestCapability_PathPlaceholderNotInTheMinimumScope_IsRefused(t *testing.T) {
	// 少声明一个维度，就会出现一个没有被授权覆盖、却能改变请求目标的取值。
	capability := readCapability()
	capability.MinimumScope.ResourceKeys = []string{"repo"}

	err := capability.Validate()
	assertConfigError(t, err)
	if !strings.Contains(err.Error(), "owner") {
		t.Errorf("拒绝理由为 %q，期望点出缺的是 owner", err)
	}
}

func TestCapability_Write_FollowsTheMethod(t *testing.T) {
	cases := map[string]bool{
		"GET": false, "HEAD": false,
		"POST": true, "PUT": true, "PATCH": true, "DELETE": true,
	}

	for method, expected := range cases {
		capability := registry.Capability{Method: method}
		if got := capability.Write(); got != expected {
			t.Errorf("%s 的写判定为 %v，期望 %v", method, got, expected)
		}
	}
}

// ——— 注册表 ———

// fakeAdapter 只回答注册契约要问的三件事。
type fakeAdapter struct {
	service      string
	kind         registry.Kind
	capabilities []registry.Capability
}

func (a fakeAdapter) Service() string                     { return a.service }
func (a fakeAdapter) Kind() registry.Kind                 { return a.kind }
func (a fakeAdapter) Capabilities() []registry.Capability { return a.capabilities }
func (a fakeAdapter) BaseURL() string                     { return "https://example.invalid" }
func (a fakeAdapter) AuthScheme() registry.AuthScheme     { return registry.AuthBearer }

func githubAdapter() fakeAdapter {
	return fakeAdapter{
		service:      "github",
		kind:         registry.KindGitHub,
		capabilities: []registry.Capability{readCapability(), writeCapability()},
	}
}

func TestNew_ValidAdapters_AreRegistered(t *testing.T) {
	cloudflare := fakeAdapter{
		service:      "cloudflare",
		kind:         registry.KindCloudflare,
		capabilities: []registry.Capability{readCapability()},
	}

	registered, err := registry.New(githubAdapter(), cloudflare)
	if err != nil {
		t.Fatalf("注册失败：%v", err)
	}

	services := registered.Services()
	if len(services) != 2 || services[0] != "cloudflare" || services[1] != "github" {
		t.Fatalf("已注册的服务为 %v，期望按字典序的 [cloudflare github]", services)
	}
}

func TestNew_AnyBadDeclaration_FailsStartup(t *testing.T) {
	// Fail Fast：不提供「跳过有问题的 Adapter 继续启动」的选项。
	broken := readCapability()
	broken.RiskLabel = ""

	cases := []struct {
		name     string
		adapters []registry.Adapter
	}{
		{"空的 Adapter", []registry.Adapter{nil}},
		{"没有服务名", []registry.Adapter{fakeAdapter{
			kind: registry.KindGitHub, capabilities: []registry.Capability{readCapability()},
		}}},
		{"种类认不出来", []registry.Adapter{fakeAdapter{
			service: "github", kind: "gitlab", capabilities: []registry.Capability{readCapability()},
		}}},
		{"没有声明任何操作", []registry.Adapter{fakeAdapter{
			service: "github", kind: registry.KindGitHub,
		}}},
		{"某项声明不合法", []registry.Adapter{fakeAdapter{
			service: "github", kind: registry.KindGitHub,
			capabilities: []registry.Capability{readCapability(), broken},
		}}},
		{"同一个操作声明了两次", []registry.Adapter{fakeAdapter{
			service: "github", kind: registry.KindGitHub,
			capabilities: []registry.Capability{readCapability(), readCapability()},
		}}},
		{"同一个服务注册了两次", []registry.Adapter{githubAdapter(), githubAdapter()}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"时启动失败", func(t *testing.T) {
			registered, err := registry.New(testCase.adapters...)
			assertConfigError(t, err)
			if registered != nil {
				t.Error("注册失败时仍然返回了一个注册表")
			}
		})
	}
}

func TestCapability_UndeclaredOperation_CannotBeCalled(t *testing.T) {
	// REQ-ADAPTER-001 AC2：拿不到声明就没有方法、没有路径、没有风险标签，
	// 请求根本构造不出来。
	registered, err := registry.New(githubAdapter())
	if err != nil {
		t.Fatalf("注册失败：%v", err)
	}

	cases := []struct {
		name      string
		service   string
		operation string
	}{
		{"服务没有 Adapter", "gitlab", "read_repository"},
		{"操作没有被声明", "github", "delete_repository"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"时返回未提供该能力", func(t *testing.T) {
			_, lookupErr := registered.Capability(testCase.service, testCase.operation)
			if !apperr.Is(lookupErr, apperr.CodeCapabilityNotOffered) {
				t.Fatalf("错误码为 %s，期望 capability_not_offered（%v）",
					apperr.CodeOf(lookupErr), lookupErr)
			}
		})
	}

	found, err := registered.Capability("github", "read_repository")
	if err != nil {
		t.Fatalf("已声明的操作查不到：%v", err)
	}
	if found.Path != "/repos/{owner}/{repo}" {
		t.Errorf("查到的路径为 %q，期望声明里的那条", found.Path)
	}
}

func TestAdapter_UnknownService_IsCapabilityNotOffered(t *testing.T) {
	registered, err := registry.New(githubAdapter())
	if err != nil {
		t.Fatalf("注册失败：%v", err)
	}

	if _, lookupErr := registered.Adapter("gitlab"); !apperr.Is(lookupErr, apperr.CodeCapabilityNotOffered) {
		t.Fatalf("错误码为 %s，期望 capability_not_offered", apperr.CodeOf(lookupErr))
	}

	adapter, err := registered.Adapter("github")
	if err != nil {
		t.Fatalf("已注册的服务查不到：%v", err)
	}
	if adapter.Service() != "github" {
		t.Errorf("查到的是 %q，期望 github", adapter.Service())
	}
}
