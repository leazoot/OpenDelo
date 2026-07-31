package intent

import (
	"sort"
	"strings"

	adapters "github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

// Intent 是一次请求被标准化后的结构化意图（PRD §10.2、REQ-INTENT-001）。
//
// PRD 列出的七项在这里各占一个字段：服务、资源、操作、环境、预期变化、是否可逆、
// 是否涉及敏感数据。它们全部来自能力声明与请求本身，没有一项是推测出来的 ——
// 推不出来的情况由 Resolver 拒绝，而不是在这里填一个默认值。
type Intent struct {
	Service   string
	Operation string
	// Resource 只保留能力声明中列出的资源字段，其余一律不进入意图。
	Resource map[string]string
	// ResourceKey 是 Resource 的规范化文本（字段名升序、以分号连接），
	// 供 Scope 收敛与授权记忆做稳定比较。
	ResourceKey string
	// IgnoredFields 是请求里带了、但能力声明没有要求的字段名，已被忽略。
	// 调用方据此记 security.scope_injection_ignored（REQ-SCOPE-002）。
	IgnoredFields []string

	Environment matcher.Environment
	// EnvironmentAssumed 为真表示环境无从判定，已按生产处理。审批页面要显示
	// 「环境未确认，按生产处理」（REQ-INTENT-003 AC2）。
	EnvironmentAssumed bool

	// DesiredChange 为空表示读操作。
	DesiredChange string

	Reversible    bool
	SensitiveData bool
	Idempotent    bool
	// RiskLabel 是 Adapter 声明的标签，不是最终风险等级 —— 等级由 core/risk 计算，
	// 可能高于标签。
	RiskLabel adapters.RiskLabel

	// ResourceAmbiguous 为真表示资源标识指向不止一个目标。此时不得自动放行，
	// 由决策链路转为人工确认并列出候选（REQ-INTENT-002 AC1）。
	ResourceAmbiguous bool
}

// validate 是 REQ-INTENT-001 AC2 的输出校验：意图必须完整才允许离开本包。
//
// 校验不过就是拒绝，不是补一个默认值 —— 后面每一步（Scope、风险、审批展示）
// 都以这些字段为准，缺一项就等于让后面自己去猜。
func (i Intent) validate() error {
	switch {
	case i.Service == "":
		return incomplete("解析结果没有服务")
	case i.Operation == "":
		return incomplete("解析结果没有操作")
	case len(i.Resource) == 0 || i.ResourceKey == "":
		return incomplete("解析结果没有资源标识")
	case i.Environment != matcher.EnvironmentProduction && i.Environment != matcher.EnvironmentNonProduction:
		return incomplete("解析结果的环境取值不合法")
	case !validRiskLabel(i.RiskLabel):
		return incomplete("解析结果的风险标签不合法")
	}
	return nil
}

func incomplete(detail string) error {
	return apperr.New(apperr.CodeCapabilityNotOffered).WithDetail(detail)
}

// ResourceKeyOf 把资源字段拼成稳定文本：字段名升序，避免 map 遍历顺序影响结果。
//
// 导出是因为 Proxy 侧要拼出与签发时**逐字相同**的一份用于比较。
// 两处各写一份规范化迟早会不同，而不同的那一次会让一条本该匹配的 Lease 匹配不上，
// 或者更糟 —— 让一条不该匹配的匹配上。
func ResourceKeyOf(resource map[string]string) string {
	keys := make([]string, 0, len(resource))
	for key := range resource {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+resource[key])
	}
	return strings.Join(parts, ";")
}
