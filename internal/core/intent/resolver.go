package intent

import (
	"encoding/json"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * Intent Resolver：工具名 / 方法 + 路径 → 能力映射表 → 校验 → 结构化意图。
 *
 * 只走确定性路径。不调用大模型（假设 A-06），因此工具描述与响应内容都无法
 * 影响解析结果 —— 提示注入在这条链路上没有落点。
 */

// Call 是接入面收到的一次调用。
//
// MCP 侧给出 Tool；Proxy 侧给出 Method 与 Path。两者都给时以 Tool 为准 ——
// 工具名是精确的，路径要靠模板匹配。
type Call struct {
	Tool   string
	Method string
	Path   string

	// Resource 是请求携带的资源字段，JSON 对象文本。
	Resource string
	// DesiredChange 为空表示读操作。
	DesiredChange string

	// IdentityEnvironment 是已匹配到的身份上的显式环境标记，是环境判定的第一优先级
	// （REQ-INTENT-003）。为空表示尚未匹配到身份，此时按命名规则与默认值判定。
	IdentityEnvironment matcher.Environment
}

// Options 是环境判定的命名规则（REQ-INTENT-003）。
//
// 两个列表都用 glob（`*` 匹配任意字符，`?` 匹配一个字符），与资源字段的值逐个比较。
// 默认两个都为空：那时一切资源都判为生产并标记「未确认」，这是就高不就低的一侧。
type Options struct {
	ProductionPatterns    []string
	NonProductionPatterns []string
}

// Resolver 把调用解析为意图。无状态，可并发使用。
type Resolver struct {
	production    []string
	nonProduction []string
}

// NewResolver 校验命名规则并构造解析器。非法的 glob 在这里就被拒绝，
// 而不是等到某次解析时静默匹配失败。
func NewResolver(options Options) (*Resolver, error) {
	if err := validatePatterns(options.ProductionPatterns, "生产"); err != nil {
		return nil, err
	}
	if err := validatePatterns(options.NonProductionPatterns, "非生产"); err != nil {
		return nil, err
	}
	return &Resolver{
		production:    append([]string(nil), options.ProductionPatterns...),
		nonProduction: append([]string(nil), options.NonProductionPatterns...),
	}, nil
}

func validatePatterns(patterns []string, label string) error {
	for _, pattern := range patterns {
		if pattern == "" {
			return apperr.New(apperr.CodeInvalidConfiguration).WithDetail(label + "环境命名规则中有空模式")
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return apperr.Wrap(apperr.CodeInvalidConfiguration, err).
				WithDetail(label + "环境命名规则 " + strconv.Quote(pattern) + " 不是合法的模式")
		}
	}
	return nil
}

// Resolve 把一次调用解析为结构化意图。
//
// 表里没有的工具名返回 capability_not_offered 且不做模糊匹配（REQ-INTENT-001 AC3）；
// 资源字段缺失返回 invalid_request；解析结果不完整同样拒绝，不猜测（AC2）。
func (r *Resolver) Resolve(catalog *Catalog, call Call) (Intent, error) {
	if catalog == nil {
		return Intent{}, apperr.New(apperr.CodeCapabilityNotOffered).WithDetail("能力映射表为空")
	}

	found, err := r.lookup(catalog, call)
	if err != nil {
		return Intent{}, err
	}

	resource, ignored, ambiguous, err := extractResource(call.Resource, found.capability.ResourceKeys)
	if err != nil {
		return Intent{}, err
	}

	environment, assumed := r.resolveEnvironment(call.IdentityEnvironment, resource)

	resolved := Intent{
		Service:            found.service,
		Operation:          found.capability.Operation,
		Resource:           resource,
		ResourceKey:        ResourceKeyOf(resource),
		IgnoredFields:      ignored,
		Environment:        environment,
		EnvironmentAssumed: assumed,
		DesiredChange:      call.DesiredChange,
		Reversible:         *found.capability.Reversible,
		SensitiveData:      *found.capability.SensitiveData,
		Idempotent:         *found.capability.Idempotent,
		RiskLabel:          found.capability.Risk,
		ResourceAmbiguous:  ambiguous,
	}
	if err := resolved.validate(); err != nil {
		return Intent{}, err
	}
	return resolved, nil
}

func (r *Resolver) lookup(catalog *Catalog, call Call) (entry, error) {
	if call.Tool != "" {
		found, present := catalog.lookupTool(call.Tool)
		if !present {
			return entry{}, notOffered("网关未声明工具 " + call.Tool)
		}
		return found, nil
	}

	if call.Method == "" || call.Path == "" {
		return entry{}, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("调用既没有工具名，也没有方法与路径")
	}

	found, present, err := catalog.lookupRoute(call.Method, call.Path)
	if err != nil {
		return entry{}, err
	}
	if !present {
		return entry{}, notOffered("网关未声明 " + strings.ToUpper(call.Method) + " " + call.Path)
	}
	return found, nil
}

func notOffered(detail string) error {
	return apperr.New(apperr.CodeCapabilityNotOffered).WithDetail(detail)
}

// extractResource 取出能力声明要求的资源字段。
//
// 声明之外的字段被忽略并以字段名的形式报回调用方（REQ-SCOPE-002）；
// 值里带通配符意味着这个标识指向不止一个目标，标记为歧义而不是照着执行
// （REQ-INTENT-002：不得猜测高影响资源）。
func extractResource(raw string, keys []string) (map[string]string, []string, bool, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil, false, apperr.New(apperr.CodeInvalidRequest).WithDetail("请求没有携带资源字段")
	}

	var fields map[string]any
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, nil, false, apperr.Wrap(apperr.CodeInvalidRequest, err).
			WithDetail("资源字段不是合法的 JSON 对象")
	}

	required := make(map[string]bool, len(keys))
	for _, key := range keys {
		required[key] = true
	}

	resource := make(map[string]string, len(keys))
	var ambiguous bool
	for _, key := range keys {
		value, present := fields[key]
		if !present {
			return nil, nil, false, apperr.New(apperr.CodeInvalidRequest).
				WithDetail("请求缺少资源字段 " + key)
		}
		text, ok := value.(string)
		if !ok || text == "" {
			return nil, nil, false, apperr.New(apperr.CodeInvalidRequest).
				WithDetail("资源字段 " + key + " 不是非空字符串")
		}
		if strings.ContainsAny(text, "*?") {
			ambiguous = true
		}
		resource[key] = text
	}

	var ignored []string
	for key := range fields {
		if !required[key] {
			ignored = append(ignored, key)
		}
	}
	sort.Strings(ignored)

	return resource, ignored, ambiguous, nil
}

// resolveEnvironment 按 REQ-INTENT-003 的三级优先级判定环境，
// 并报告结果是否属于「无从判定，按生产处理」。
func (r *Resolver) resolveEnvironment(
	declared matcher.Environment, resource map[string]string,
) (matcher.Environment, bool) {
	switch declared {
	case matcher.EnvironmentProduction, matcher.EnvironmentNonProduction:
		return declared, false
	}

	// 生产模式优先于非生产模式：两边都命中时就高不就低。
	if matchesAny(r.production, resource) {
		return matcher.EnvironmentProduction, false
	}
	if matchesAny(r.nonProduction, resource) {
		return matcher.EnvironmentNonProduction, false
	}
	return matcher.EnvironmentProduction, true
}

func matchesAny(patterns []string, resource map[string]string) bool {
	for _, pattern := range patterns {
		for _, value := range resource {
			matched, err := path.Match(pattern, value)
			// 模式在 NewResolver 就校验过，这里不可能返回 ErrBadPattern；
			// 真出现了也按「不匹配」处理，那会让环境落到默认的生产一侧。
			if err == nil && matched {
				return true
			}
		}
	}
	return false
}
