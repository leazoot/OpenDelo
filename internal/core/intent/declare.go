package intent

import (
	"encoding/json"
	"sort"
	"strings"

	adapters "github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * 把编译期的 Adapter 声明编码成落库的那份 JSON。
 *
 * 编码与解码放在同一个包里：`parseCapabilities` 读的正是这里写的东西，
 * 两边分家之后，改一个字段名只会让某一次决策悄悄少一项输入 ——
 * JSON 不会因为多一个或少一个字段而报错。
 */

// verbs 是操作名允许的动词，工具名按 REQ-MCP-001 的规则由它切分。
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

// ToolName 把 <service> 与 <verb>_<resource> 拼成 <service>.<resource>.<action>。
//
// 规则只此一处：MCP 的工具清单与落库声明里的工具名必须是同一个字符串，
// 各写一份的话，Agent 看得见的工具名与决策链路认得的工具名会在某次改动后错开，
// 而那时每一次调用都会以「这个能力没被声明过」被拒。
func ToolName(service, operation string) (string, error) {
	for _, verb := range verbs {
		if operation == verb {
			return "", declarationError(service, "操作名 "+operation+" 只有动词没有资源")
		}
		if !strings.HasPrefix(operation, verb+"_") {
			continue
		}
		resource := strings.TrimPrefix(operation, verb+"_")
		if resource == "" {
			return "", declarationError(service, "操作名 "+operation+" 只有动词没有资源")
		}
		return service + "." + resource + "." + verb, nil
	}
	return "", declarationError(service, "操作名 "+operation+
		" 不以任何一个已知动词开头，无法生成符合 REQ-MCP-001 的工具名")
}

// EncodeCapabilities 把一个 Adapter 的能力声明编码成 `service_adapters.capabilities`。
//
// 九项声明缺一即整份拒绝（REQ-ADAPTER-001 AC1）：一条「只说了操作名」的能力
// 在决策链路里无法计算风险、无法判断可逆、无法收敛 Scope，让它进表等于
// 给后面每一步都留一个未知数。
func EncodeCapabilities(service string, declared []adapters.Capability) (string, error) {
	if len(declared) == 0 {
		return "", declarationError(service, "没有声明任何能力")
	}

	encoded := make([]Capability, 0, len(declared))
	for _, capability := range declared {
		if err := capability.Validate(); err != nil {
			return "", err
		}
		tool, err := ToolName(service, capability.Operation)
		if err != nil {
			return "", err
		}

		idempotent := capability.Idempotency == adapters.Idempotent
		// 可逆与否看回滚能力：只有「做完回不去」才是不可逆。
		reversible := capability.Rollback != adapters.RollbackNone
		sensitive := capability.Nature.SecretAccess

		encoded = append(encoded, Capability{
			Tool:          tool,
			Operation:     capability.Operation,
			Method:        capability.Method,
			Path:          capability.Path,
			Risk:          capability.RiskLabel,
			Idempotent:    &idempotent,
			Reversible:    &reversible,
			SensitiveData: &sensitive,
			ResourceKeys:  capability.MinimumScope.ResourceKeys,
		})
	}

	text, err := json.Marshal(encoded)
	if err != nil {
		return "", apperr.Wrap(apperr.CodeInternal, err).
			WithDetail("服务 " + service + " 的能力声明无法编码")
	}
	return string(text), nil
}

// EncodePaths 与 EncodeMethods 是端点白名单（REQ-ADAPTER-005 AC1）。
//
// 从能力声明里取而不是让 Adapter 另外声明一份：两份清单迟早会出现
// 「能力里有、白名单里没有」的组合，那时这个操作会在执行的最后一步才被拒。
func EncodePaths(declared []adapters.Capability) (string, error) {
	return encodeSorted(declared, func(c adapters.Capability) string { return c.Path })
}

func EncodeMethods(declared []adapters.Capability) (string, error) {
	return encodeSorted(declared, func(c adapters.Capability) string {
		return strings.ToUpper(c.Method)
	})
}

// EncodeRedactionRules 汇总各操作声明的额外脱敏字段。
func EncodeRedactionRules(declared []adapters.Capability) (string, error) {
	seen := map[string]bool{}
	for _, capability := range declared {
		for _, rule := range capability.RedactionRules {
			seen[rule] = true
		}
	}
	return encodeSet(seen)
}

// DefaultRiskOf 是兜底风险等级：取全部操作里最高的那一档。
//
// 取最高而不是最低：这一项是「没有更精确的信息时按什么算」，
// 往低了取会让一个未被逐条声明的操作被当成读操作放行。
func DefaultRiskOf(declared []adapters.Capability) adapters.RiskLabel {
	highest := adapters.RiskLabelLow
	for _, capability := range declared {
		switch capability.RiskLabel {
		case adapters.RiskLabelHigh:
			return adapters.RiskLabelHigh
		case adapters.RiskLabelMedium:
			highest = adapters.RiskLabelMedium
		case adapters.RiskLabelLow:
		}
	}
	return highest
}

func encodeSorted(declared []adapters.Capability, pick func(adapters.Capability) string) (string, error) {
	seen := map[string]bool{}
	for _, capability := range declared {
		seen[pick(capability)] = true
	}
	return encodeSet(seen)
}

func encodeSet(seen map[string]bool) (string, error) {
	values := make([]string, 0, len(seen))
	for value := range seen {
		values = append(values, value)
	}
	// 排序而不是按遍历顺序：map 的顺序每次都不同，落库的内容会跟着变，
	// 让「这份声明改过没有」变得答不出来。
	sort.Strings(values)

	text, err := json.Marshal(values)
	if err != nil {
		return "", apperr.Wrap(apperr.CodeInternal, err).WithDetail("声明的清单无法编码")
	}
	return string(text), nil
}
