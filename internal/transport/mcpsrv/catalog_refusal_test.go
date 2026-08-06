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
func (f fakeAdapter) BaseURL() string                     { return "https://example.invalid" }
func (f fakeAdapter) AuthScheme() registry.AuthScheme     { return registry.AuthBearer }

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

/*
 * 合并到 core 之后的对账（R-46）。
 *
 * 本文件此前有一份自己的关键词表：`read_` 前缀 + 六个名词的**子串**匹配。
 * 判定改问 `core/decision` 之后，两者在四个名字上不同，逐条记在这里 ——
 * 「合并了两份实现」这句话不写用例就无从验证，而验证的对象恰恰是差异本身。
 */
func TestNewCatalog_AfterMergingIntoCore_TheHolesInTheOldRuleAreClosed(t *testing.T) {
	// 旧表按**子串**匹配六个名词，于是这四个名字它一个都拦不住：
	// 拆开写的 api_key / private_key 拼不出旧表里的 apikey，
	// passphrase 与 keychain 则根本不在旧表里。四个都是取用现成的凭据。
	//
	// 用例只取以 `read_` 开头的名字：动词表（`core/intent`）是封闭的，
	// list_ / export_ 这类名字在取到凭据判定之前就已经因为动词不合法被拒，
	// 拿它们当例子的话，这条用例验的是动词表而不是凭据判定。
	for _, operation := range []string{
		"read_api_key", "read_private_key", "read_passphrase", "read_keychain_item",
	} {
		t.Run(operation, func(t *testing.T) {
			if err := catalogOf(t, "svc", operation); err == nil {
				t.Errorf("取用凭据的操作 %q 仍然生成了工具", operation)
			}
		})
	}
}

func TestNewCatalog_AfterMergingIntoCore_SubstringCollisionsAreNoLongerRefused(t *testing.T) {
	// 旧表用子串匹配，`tokenizer` 里凑出一个 `token` 就被判成读取凭据 ——
	// 与 R-38 里 `manage_token` 拼成 `managetoken` 长出 `get` 是同一个坑。
	// 分段匹配之后它们不再命中：`tokenizer` 不是 `token`。
	//
	// 这是本次合并**唯一放松**的一处，因此单列一条用例盯着它：放松的边界
	// 必须是「名字里恰好含有那几个字母」，不能顺带把真正的取用也放出去。
	for _, operation := range []string{"read_tokenizer", "read_tokenized_input"} {
		t.Run(operation, func(t *testing.T) {
			if err := catalogOf(t, "svc", operation); err != nil {
				t.Errorf("操作 %q 只是名字里含有 token 几个字母，不该被拒绝：%v", operation, err)
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
