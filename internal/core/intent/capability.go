package intent

import (
	"encoding/json"
	"sort"
	"strings"

	adapters "github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * 能力映射表：Adapter 声明里的 Capabilities 文本在这里被解析成一张查找表。
 *
 * 这张表是 REQ-INTENT-002 AC3 的实现方式 —— Resolver 只能输出表里已有的 operation，
 * 因为它没有别的地方可取。表以外的工具名一律 capability_not_offered，
 * 且**不做任何模糊匹配**：拼错一个字母就该被拒，而不是被猜成最接近的那个操作。
 */

// Capability 是一个操作的完整声明（REQ-ADAPTER-001）。
//
// Reversible、SensitiveData 与 Idempotent 用指针：REQ-INTENT-002 要求
// 「无法确定是否可逆」时不得猜测，所以「没声明」必须和「声明为 false」区分开。
// 缺任何一项都视为该 Adapter 未声明这个能力。
type Capability struct {
	// Tool 是 MCP 工具名，例如 cloudflare.dns.update。
	Tool string `json:"tool"`
	// Operation 是标准化后的操作名，例如 dns.record.update。它与 Tool 不同：
	// 工具名是接入面的叫法，操作名是决策链路与审计里的叫法。
	Operation string `json:"operation"`
	// Method 与 Path 供 Proxy 侧按「方法 + 路径」反查同一个能力。
	// Path 中的 {name} 匹配任意一个非空路径段。
	Method string `json:"method"`
	Path   string `json:"path"`

	Risk adapters.RiskLabel `json:"risk"`

	Idempotent    *bool `json:"idempotent"`
	Reversible    *bool `json:"reversible"`
	SensitiveData *bool `json:"sensitive_data"`

	// ResourceKeys 是构成资源标识的字段名，例如 ["zone", "record"]。
	// 请求里这些字段之外的内容不进入意图（REQ-SCOPE-002 的「忽略越权字段」）。
	ResourceKeys []string `json:"resource_keys"`
}

// Catalog 是全部已启用 Adapter 的能力映射表。
//
// core 不读数据库（ADR-003），声明由调用方加载后传入。
type Catalog struct {
	byTool map[string]entry
	// byOperation 服务「这个服务的这个操作叫什么工具名」。
	//
	// 决策链路按工具名解析，而 Web API 上到达的是服务名 + 操作名（REQ-API-001
	// 的请求体形状）。没有这张表，8787 面就得自己按 REQ-MCP-001 的命名规则
	// 拼一个工具名出来 —— 那条规则就有了第二处实现。
	byOperation map[string]entry
	byRoute     []entry
}

// entry 把一个能力和它所属的服务绑在一起。
type entry struct {
	service    string
	capability Capability
	segments   []string
}

// NewCatalog 解析一组 Adapter 声明。
//
// 任何一条声明不合法都返回错误而不是跳过它：跳过等于让一个「声明得不完整」的
// Adapter 悄悄退化成「没有这个能力」，而两者在账本上应该是不同的事。
// 只收 enabled 的声明 —— 停用的 Adapter 不参与决策。
func NewCatalog(declarations []adapters.Declaration) (*Catalog, error) {
	catalog := &Catalog{
		byTool:      make(map[string]entry),
		byOperation: make(map[string]entry),
	}

	for _, declaration := range declarations {
		if declaration.Status != adapters.StatusEnabled {
			continue
		}

		capabilities, err := parseCapabilities(declaration)
		if err != nil {
			return nil, err
		}
		for _, capability := range capabilities {
			added := entry{
				service:    declaration.Service,
				capability: capability,
				segments:   splitPath(capability.Path),
			}
			if _, duplicated := catalog.byTool[capability.Tool]; duplicated {
				return nil, declarationError(declaration.Service, "工具名 "+capability.Tool+" 被声明了两次")
			}
			// 同一个服务下操作名也不得重复。工具名不撞不等于操作名不撞，
			// 而账本、Scope 与 Lease 认的是操作名 —— 撞了就没法回答
			// 「这条授权覆盖的是哪一个操作」。
			operationKey := operationKeyOf(declaration.Service, capability.Operation)
			if previous, duplicated := catalog.byOperation[operationKey]; duplicated {
				return nil, declarationError(declaration.Service, "操作名 "+capability.Operation+
					" 同时属于工具 "+previous.capability.Tool+" 与 "+capability.Tool)
			}
			catalog.byTool[capability.Tool] = added
			catalog.byOperation[operationKey] = added
			catalog.byRoute = append(catalog.byRoute, added)
		}
	}
	return catalog, nil
}

// Size 返回表中的能力条数，供调用方确认加载结果非空。
func (c *Catalog) Size() int { return len(c.byTool) }

// ToolFor 回答「这个服务的这个操作对应哪个工具名」。
//
// 查不到即该能力未被声明，调用方按 Fail Closed 一律拒绝。同样不做任何模糊匹配：
// 拼错一个字母该被拒，而不是被猜成最接近的那个操作。
func (c *Catalog) ToolFor(service, operation string) (string, bool) {
	found, present := c.byOperation[operationKeyOf(service, operation)]
	if !present {
		return "", false
	}
	return found.capability.Tool, true
}

// operationKeyOf 拼出 byOperation 的键。
//
// 用 NUL 分隔而不是斜杠或点：这两个字符都可能出现在服务名或操作名里，
// 用它们分隔会让 a/b + c 与 a + b/c 撞成同一个键。
func operationKeyOf(service, operation string) string { return service + "\x00" + operation }

// lookupTool 按工具名精确查找。不做大小写折叠、前缀匹配或最近邻 —— 见本文件开头。
func (c *Catalog) lookupTool(tool string) (entry, bool) {
	found, present := c.byTool[tool]
	return found, present
}

// lookupRoute 按 HTTP 方法与路径查找。
//
// 命中多条模板时返回歧义错误：两条模板都能解释这个请求，说明声明本身有重叠，
// 此时挑一条等于替用户做主（REQ-INTENT-002）。
func (c *Catalog) lookupRoute(method, path string) (entry, bool, error) {
	wanted := splitPath(path)
	normalizedMethod := strings.ToUpper(method)

	var matched []entry
	for _, candidate := range c.byRoute {
		if candidate.capability.Method != normalizedMethod {
			continue
		}
		if matchSegments(candidate.segments, wanted) {
			matched = append(matched, candidate)
		}
	}

	switch len(matched) {
	case 0:
		return entry{}, false, nil
	case 1:
		return matched[0], true, nil
	default:
		return entry{}, false, apperr.New(apperr.CodeCapabilityNotOffered).
			WithDetail("路径 " + path + " 同时命中 " + strings.Join(matchedServices(matched), "、") + " 的多条声明")
	}
}

func matchedServices(entries []entry) []string {
	names := make([]string, 0, len(entries))
	for _, matched := range entries {
		names = append(names, matched.service+"/"+matched.capability.Operation)
	}
	sort.Strings(names)
	return names
}

func parseCapabilities(declaration adapters.Declaration) ([]Capability, error) {
	var capabilities []Capability
	if err := json.Unmarshal([]byte(declaration.Capabilities), &capabilities); err != nil {
		return nil, apperr.Wrap(apperr.CodeInvalidConfiguration, err).
			WithDetail("服务 " + declaration.Service + " 的能力声明不是合法 JSON")
	}
	if len(capabilities) == 0 {
		return nil, declarationError(declaration.Service, "没有声明任何能力")
	}

	for _, capability := range capabilities {
		if err := capability.validate(declaration.Service); err != nil {
			return nil, err
		}
	}
	return capabilities, nil
}

// validate 逐项检查一条能力声明。
//
// 缺任何一项都拒绝整份声明：一条「只说了操作名」的能力在决策链路里无法计算风险、
// 无法判断可逆、无法收敛 Scope，让它进表等于给后面每一步都留一个未知数。
func (c Capability) validate(service string) error {
	switch {
	case c.Tool == "":
		return declarationError(service, "有一条能力没有工具名")
	case c.Operation == "":
		return declarationError(service, "能力 "+c.Tool+" 没有操作名")
	case c.Method == "":
		return declarationError(service, "能力 "+c.Tool+" 没有 HTTP 方法")
	case c.Path == "":
		return declarationError(service, "能力 "+c.Tool+" 没有路径")
	case !validRiskLabel(c.Risk):
		return declarationError(service, "能力 "+c.Tool+" 的风险标签不合法")
	case c.Idempotent == nil:
		return declarationError(service, "能力 "+c.Tool+" 没有声明是否幂等")
	case c.Reversible == nil:
		return declarationError(service, "能力 "+c.Tool+" 没有声明是否可逆")
	case c.SensitiveData == nil:
		return declarationError(service, "能力 "+c.Tool+" 没有声明是否涉及敏感数据")
	case len(c.ResourceKeys) == 0:
		return declarationError(service, "能力 "+c.Tool+" 没有声明构成资源标识的字段")
	}

	for _, key := range c.ResourceKeys {
		if key == "" {
			return declarationError(service, "能力 "+c.Tool+" 的资源字段名为空")
		}
	}
	return nil
}

func validRiskLabel(label adapters.RiskLabel) bool {
	switch label {
	case adapters.RiskLabelLow, adapters.RiskLabelMedium, adapters.RiskLabelHigh:
		return true
	default:
		return false
	}
}

func declarationError(service, detail string) error {
	return apperr.New(apperr.CodeInvalidConfiguration).
		WithDetail("服务 " + service + " 的能力声明不可用：" + detail)
}

// splitPath 把路径切成非空段，忽略首尾斜杠与重复斜杠。
func splitPath(path string) []string {
	var segments []string
	for _, segment := range strings.Split(path, "/") {
		if segment != "" {
			segments = append(segments, segment)
		}
	}
	return segments
}

// matchSegments 逐段比较，{name} 匹配任意一个非空段。
//
// 不支持尾部通配：一条能声明「这条路径以下全部」的模板会让端点白名单形同虚设
func matchSegments(template, actual []string) bool {
	if len(template) != len(actual) {
		return false
	}
	for index, segment := range template {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			continue
		}
		if segment != actual[index] {
			return false
		}
	}
	return true
}
