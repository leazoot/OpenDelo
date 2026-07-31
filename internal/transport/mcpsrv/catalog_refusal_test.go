package mcpsrv_test

import (
	"testing"

	"github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/transport/mcpsrv"
)

/*
 * 清单生成会拒绝哪些声明。
 *
 * 这些用例必须用合成声明：真实的四个 Adapter 里一条违规声明都没有，
 * 拿它们测「违规会被拒绝」，检查被删掉用例照样通过 —— 那是在测一个
 * 永远不会走到的分支。
 */

// fakeAdapter 是一个只有声明、不会被执行的 Adapter。
type fakeAdapter struct {
	service      string
	capabilities []registry.Capability
}

func (f fakeAdapter) Service() string                     { return f.service }
func (f fakeAdapter) Kind() registry.Kind                 { return registry.KindGenericHTTP }
func (f fakeAdapter) Capabilities() []registry.Capability { return f.capabilities }

// declare 造一条各项齐备的只读声明，只有操作名按用例变化。
//
// 其余字段取合法值：这些用例要测的是工具名那一层，
// 让声明本身先过 Validate，失败才说明是工具名的问题。
func declare(operation string) registry.Capability {
	return registry.Capability{
		Operation:      operation,
		InputSchema:    `{"type":"object","properties":{"owner":{"type":"string"}}}`,
		MinimumScope:   registry.MinimumScope{ResourceKeys: []string{"owner"}},
		RiskLabel:      registry.RiskLabelLow,
		Method:         "GET",
		Path:           "/things/{owner}",
		RedactionRules: []string{},
		ResponseFields: []string{"name"},
		Rollback:       registry.RollbackNone,
		Idempotency:    registry.Idempotent,
	}
}

func catalogOf(t *testing.T, service string, operations ...string) error {
	t.Helper()

	declarations := make([]registry.Capability, 0, len(operations))
	for _, operation := range operations {
		declarations = append(declarations, declare(operation))
	}
	adapters, err := registry.New(fakeAdapter{service: service, capabilities: declarations})
	if err != nil {
		return err
	}

	_, err = mcpsrv.NewCatalog(adapters)
	return err
}

func TestNewCatalog_OperationWithoutAKnownVerb_FailsAtStartup(t *testing.T) {
	// 工具名要拆成 <resource>.<action>，而拆点不能猜 —— 取第一个下划线之前
	// 的部分会把 bulk_update_dns 切成 bulk / update_dns。动词表因此是封闭的：
	// 不在表里的操作名让进程起不来，而不是生成一个名字古怪的工具。
	for _, operation := range []string{
		"fetch_repository", "repository_read", "list_zones", "readrepository", "read",
	} {
		t.Run(operation, func(t *testing.T) {
			if err := catalogOf(t, "svc", operation); err == nil {
				t.Errorf("操作名 %q 生成了工具，而它不以任何已知动词开头", operation)
			}
		})
	}
}

func TestNewCatalog_KnownVerbs_AllProduceAToolName(t *testing.T) {
	// 反向断言：动词表里的每一个都真的能走通，否则上一条用例可以靠
	// 「什么都拒绝」通过。
	for _, operation := range []string{
		"read_zone", "create_issue", "update_secret", "delete_record",
		"merge_branch", "manage_member", "purge_cache", "bulk_update_dns",
	} {
		t.Run(operation, func(t *testing.T) {
			if err := catalogOf(t, "svc", operation); err != nil {
				t.Errorf("操作名 %q 被拒绝了：%v", operation, err)
			}
		})
	}
}

func TestNewCatalog_BulkUpdate_KeepsTheWholeVerbNotJustTheFirstWord(t *testing.T) {
	// bulk_update_dns 必须变成 svc.dns.bulk_update，不是 svc.update_dns.bulk。
	adapters, err := registry.New(fakeAdapter{
		service: "svc", capabilities: []registry.Capability{declare("bulk_update_dns")},
	})
	if err != nil {
		t.Fatalf("注册失败：%v", err)
	}
	catalog, err := mcpsrv.NewCatalog(adapters)
	if err != nil {
		t.Fatalf("生成清单失败：%v", err)
	}

	tools := catalog.Tools()
	if len(tools) != 1 {
		t.Fatalf("生成了 %d 个工具", len(tools))
	}
	if tools[0].Name != "svc.dns.bulk_update" {
		t.Errorf("工具名为 %q，期望 svc.dns.bulk_update", tools[0].Name)
	}
}

func TestNewCatalog_NamesThatCollideOnlyAfterReplacingDots_FailAtStartup(t *testing.T) {
	// 实测：客户端把点换成下划线之后才交给模型。
	// read_pull_request 与 read_pull.request 在线路上不同，在模型眼里都是
	// svc_pull_request_read —— 模型无法区分，因此不允许同时存在。
	err := catalogOf(t, "svc", "read_pull_request", "read_pull.request")
	if err == nil {
		t.Error("两个规范化之后同名的工具被同时生成了")
	}
}

func TestNewCatalog_TwoCapabilitiesWithTheSameName_FailAtStartup(t *testing.T) {
	// 同名声明在 registry 那一层就该被挡住；这里确认清单不会替它兜底。
	if err := catalogOf(t, "svc", "read_zone", "read_zone"); err == nil {
		t.Error("同一个操作声明两次仍然生成了清单")
	}
}

func TestNewCatalog_ReadingACredential_FailsAtStartup(t *testing.T) {
	// REQ-MCP-002 AC1。一个名字叫 read_credential 的工具即便永远失败，
	// 也会让模型反复尝试 —— 它不该出现在清单里。
	for _, operation := range []string{
		"read_credential", "read_secret", "read_token", "read_vault_item", "read_api_password",
	} {
		t.Run(operation, func(t *testing.T) {
			if err := catalogOf(t, "svc", operation); err == nil {
				t.Errorf("读取凭据的操作 %q 生成了工具", operation)
			}
		})
	}
}

func TestNewCatalog_RotatingACredential_IsAllowed(t *testing.T) {
	// 区别在动词：读出来的是现成的凭据，造出来的不是。PRD 明确允许
	// 经审批的创建与轮换，把它们一起关掉是把需求做窄了。
	for _, operation := range []string{"update_secret", "manage_token", "create_token"} {
		t.Run(operation, func(t *testing.T) {
			if err := catalogOf(t, "svc", operation); err != nil {
				t.Errorf("轮换类操作 %q 被拒绝了：%v", operation, err)
			}
		})
	}
}

func TestNewCatalog_MalformedInputSchema_FailsAtStartup(t *testing.T) {
	broken := declare("read_zone")
	broken.InputSchema = "{not json"

	adapters, err := registry.New(fakeAdapter{
		service: "svc", capabilities: []registry.Capability{broken},
	})
	if err != nil {
		// registry 自己先挡下也算达到目的，但要确认确实被挡住了。
		return
	}
	if _, err := mcpsrv.NewCatalog(adapters); err == nil {
		t.Error("输入 Schema 不是合法 JSON 仍然生成了工具")
	}
}

func TestNewCatalog_ToolsAreSortedByName(t *testing.T) {
	// 清单按名字排序：客户端把它原样展示给用户，顺序随注册次序漂移会让
	// 同一台网关每次看起来都不一样。
	previous := ""
	for _, tool := range realCatalog(t).Tools() {
		if previous != "" && tool.Name < previous {
			t.Errorf("%q 排在 %q 之后", tool.Name, previous)
		}
		previous = tool.Name
	}
}

func TestNewCatalog_WithoutARegistry_IsRefused(t *testing.T) {
	if _, err := mcpsrv.NewCatalog(nil); err == nil {
		t.Error("没有注册表也生成出了清单")
	}
}
