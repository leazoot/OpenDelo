package mcpsrv

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * 工具清单的生成（REQ-MCP-001、REQ-MCP-002）。
 *
 * 清单**只**来自 Adapter 的能力声明，本文件不含任何一处手写的工具定义。
 * 每一条不变式都在启动时校验：不合规的声明让进程起不来，而不是在运行时
 * 变成一个行为古怪的工具（与 registry.New 的 Fail Fast 同向）。
 */

// verbs 是操作名允许的动词。
//
// Adapter 的操作名形如 <verb>_<resource>（read_repository、bulk_update_dns），
// 而 REQ-MCP-001 要求工具名形如 <service>.<resource>.<action>。两者之间的换算
// 需要知道从哪里断开，而这不能靠「取第一个下划线之前的部分」猜 ——
// bulk_update_dns 会被切成 bulk / update_dns。
//
// 因此这里是一张封闭的表，匹配的是 <verb>_ 这个整体：操作名不以其中任何一个
// 开头，注册就失败。新增动词是一次显式的改动，而不是一个悄悄生效的推断。
var verbs = []string{
	"bulk_update",
	"create",
	"delete",
	"manage",
	"merge",
	"purge",
	"read",
	"update",
}

// credentialResources 是凭据类资源名里会出现的词。
var credentialResources = []string{"credential", "secret", "token", "vault", "password", "apikey"}

// readingVerbs 是「把现成的东西取出来」的动词。
//
// 凭据类工具的判定按**动词**而不是按名词：读出来的是现成的凭据，
// 造出来的不是。只按名词匹配会把 PRD 明确允许（经审批）的创建与轮换类操作
// 一起永久关掉 —— cloudflare 的 manage_token 与 github 的 update_secret
// 都是轮换，它们的返回值由 Adapter 的 Redact() 负责（REQ-ADAPTER-007）。
//
// 这与 REQ-CAP-001 AC2 在 httpapi 侧的拦截是同一条规则的两个落点。
var readingVerbs = []string{"read"}

// Tool 是一条对外可见的工具定义。
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Catalog 是全部工具，按名字排序。
//
// 按名字而不是按注册次序：客户端把这份清单原样展示给用户，顺序跟着
// Adapter 的注册次序漂移会让同一台网关每次看起来都不一样。
type Catalog struct {
	tools []Tool
	// byName 用于 tools/call 的查找，键是线路上的工具名。
	byName map[string]toolTarget
}

// toolTarget 是一个工具背后的服务与操作。
type toolTarget struct {
	Service   string
	Operation string
}

// NewCatalog 从注册表生成工具清单。
//
// 返回错误即启动失败（REQ-MCP-001 AC1 的必然结果：清单既然由声明生成，
// 声明不合规就没有一份可用的清单）。不提供「跳过有问题的工具继续启动」——
// 那会让一个本该暴露的能力静默消失，而调用方无从察觉。
func NewCatalog(adapters *registry.Registry) (*Catalog, error) {
	if adapters == nil {
		return nil, apperr.New(apperr.CodeInternal).WithDetail("工具清单缺少 Adapter 注册表")
	}

	catalog := &Catalog{byName: make(map[string]toolTarget)}
	// normalized 记录「点换成下划线之后」的名字。实测：
	// 客户端把工具名里的点换成下划线之后才交给模型，因此
	// github.pull_request.create 与 github.pull.request.create 在模型眼里是同一个。
	// 线路上不撞名不等于模型那边不撞名。
	normalized := make(map[string]string)

	for _, service := range adapters.Services() {
		adapter, err := adapters.Adapter(service)
		if err != nil {
			return nil, err
		}
		for _, capability := range adapter.Capabilities() {
			tool, err := newTool(service, capability)
			if err != nil {
				return nil, err
			}
			if previous, taken := catalog.byName[tool.Name]; taken {
				return nil, declarationError("工具名 " + tool.Name + " 与 " +
					previous.Service + "/" + previous.Operation + " 重复")
			}
			key := normalize(tool.Name)
			if previous, taken := normalized[key]; taken {
				return nil, declarationError("工具名 " + tool.Name + " 与 " + previous +
					" 在替换点号之后重复，模型侧无法区分")
			}

			normalized[key] = tool.Name
			catalog.byName[tool.Name] = toolTarget{Service: service, Operation: capability.Operation}
			catalog.tools = append(catalog.tools, tool)
		}
	}

	sort.Slice(catalog.tools, func(i, j int) bool {
		return catalog.tools[i].Name < catalog.tools[j].Name
	})
	return catalog, nil
}

// Tools 返回清单的副本。
func (c *Catalog) Tools() []Tool {
	copied := make([]Tool, len(c.tools))
	copy(copied, c.tools)
	return copied
}

// Target 查一个工具背后的服务与操作。查不到即该能力未被声明，
// 按 Fail Closed 一律拒绝。
func (c *Catalog) Target(name string) (toolTarget, bool) {
	target, found := c.byName[name]
	return target, found
}

// newTool 把一条能力声明翻成一个工具。
func newTool(service string, capability registry.Capability) (Tool, error) {
	if err := capability.Validate(); err != nil {
		return Tool{}, err
	}

	name, err := toolName(service, capability.Operation)
	if err != nil {
		return Tool{}, err
	}
	if word := credentialReadIn(capability.Operation); word != "" {
		return Tool{}, declarationError("能力 " + service + "/" + capability.Operation +
			" 是读取 " + word + "，索取凭据的能力不得出现在工具清单里")
	}

	var schema json.RawMessage
	if err := json.Unmarshal([]byte(capability.InputSchema), &schema); err != nil {
		return Tool{}, declarationError("能力 " + service + "/" + capability.Operation +
			" 的输入 Schema 不是合法 JSON")
	}

	return Tool{Name: name, Description: describe(capability), InputSchema: schema}, nil
}

// toolName 把 <service> 与 <verb>_<resource> 拼成 <service>.<resource>.<action>。
func toolName(service, operation string) (string, error) {
	for _, verb := range verbs {
		if operation == verb {
			return "", declarationError("操作名 " + operation + " 只有动词没有资源")
		}
		if !strings.HasPrefix(operation, verb+"_") {
			continue
		}
		resource := strings.TrimPrefix(operation, verb+"_")
		if resource == "" {
			return "", declarationError("操作名 " + operation + " 只有动词没有资源")
		}
		return service + "." + resource + "." + verb, nil
	}
	return "", declarationError("操作名 " + operation +
		" 不以任何一个已知动词开头，无法生成符合 REQ-MCP-001 的工具名")
}

// describe 生成工具描述。
//
// 描述里写风险等级与是否可回滚，是为了让模型在调用之前就知道这一次可能
// 需要人工确认 —— 一个被拒绝的调用如果毫无预兆，模型只会换个工具再试。
// 描述**不含** BaseURL、路径与任何声明之外的信息。
func describe(capability registry.Capability) string {
	var text strings.Builder
	text.WriteString("Risk: ")
	text.WriteString(string(capability.RiskLabel))
	if capability.Write() {
		text.WriteString(". Changes external state")
	} else {
		text.WriteString(". Read-only")
	}
	text.WriteString(". Rollback: ")
	text.WriteString(string(capability.Rollback))
	text.WriteString(". Every call is authorized individually and may require human approval.")
	return text.String()
}

// normalize 复现客户端交给模型之前做的改写：点换成下划线。
func normalize(name string) string { return strings.ReplaceAll(name, ".", "_") }

// credentialReadIn 报告一个操作是不是「读取凭据」，是则返回命中的资源词。
//
// 两个条件必须同时成立：动词是取用类，资源是凭据类。
// 只满足其一的（update_secret 是轮换，read_repository 读的不是凭据）不在此列。
func credentialReadIn(operation string) string {
	lowered := strings.ToLower(operation)
	reading := false
	for _, verb := range readingVerbs {
		if strings.HasPrefix(lowered, verb+"_") {
			reading = true
			break
		}
	}
	if !reading {
		return ""
	}

	for _, word := range credentialResources {
		if strings.Contains(lowered, word) {
			return word
		}
	}
	return ""
}

func declarationError(detail string) error {
	return apperr.New(apperr.CodeInvalidConfiguration).WithDetail(detail)
}
