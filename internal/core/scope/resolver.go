package scope

import (
	"sort"
	"strings"
	"time"

	adapters "github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/core/intent"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/risk"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/audit"
	"github.com/Runcoor/opendelo/internal/platform/clock"
)

/*
 * Scope Resolver：Intent + 发起方 + 已匹配的身份 → 十个维度的最小 Scope。
 *
 * 输入里没有任何一项来自请求方对范围的表述。Agent 能影响的只有「要对哪个资源做哪个
 * 操作」，而那两项已经被能力声明限死（REQ-SCOPE-002）。请求里出现的 scope 类字段
 * 在这里被点名报回调用方，由它记 security.scope_injection_ignored。
 */

// DefaultDuration 是授权的默认时长（REQ-LEASE-001 AC2）。
const DefaultDuration = 15 * time.Minute

// DefaultRequestLimit 是授权的默认请求次数。
//
// 取 1 而不是「不限」：次数是十个维度之一，Scope 层面不存在无上限的写法。
// 需要更多次由审批显式放大（REQ-APPROVAL-002）。
const DefaultRequestLimit = 1

// TaskRequestLimit 是「允许到任务结束」放大到的请求次数。
//
// 不能是「不限」：次数是十个维度之一，Scope 层面没有无上限的写法（scope.go）。
// 也不能沿用 1 —— 那样「仅允许这一次」与「允许到任务结束」在效果上完全一样，
// 用户点哪个都只换来一次调用（R-49）。
//
// 取 20 是因为一次任务里同一件事重复几遍是常态（重试、翻页、逐条处理），
// 而 20 之外仍然有两道更硬的边界在管着它：授权绑定本次会话，会话结束当场收回；
// 到期时刻照旧是 15 分钟。次数在这里是第三道，不是唯一一道。
const TaskRequestLimit = 20

// InjectionEvent 是 Result.Injection 非空时调用方必须记的审计事件（REQ-SCOPE-002 AC2）。
const InjectionEvent = audit.EventScopeInjectionIgnored

// Input 是一次 Scope 收敛的全部输入。
//
// 它不接受任何形式的「期望范围」：Duration 与 RequestLimit 是网关侧的默认值或
// 审批结果，不是请求方给的。
type Input struct {
	Intent intent.Intent

	AgentID     string
	WorkspaceID string
	// Identity 是 Credential Matcher 给出的唯一身份。歧义未解决时不该走到这里。
	Identity matcher.Identity

	// Duration 为零时用 DefaultDuration。负值被拒绝。
	Duration time.Duration
	// RequestLimit 为零时用 DefaultRequestLimit。负值被拒绝。
	RequestLimit int
}

// Result 是一次收敛的结论。
//
// Ambiguous 与 EnvironmentAssumed 不在 Scope 里：Scope 就是十个维度本身，
// 会原样落进 decisions 与 leases；这两项是「这次收敛有多确定」的说明，
// 供决策引擎判断能不能自动放行、供审批页面显示原因。
type Result struct {
	Scope Scope

	// Injection 是请求里出现的、试图影响授权范围的字段名，已排序。
	// 非空时调用方记一条 InjectionEvent（REQ-SCOPE-002 AC2）。
	Injection []string
	// IgnoredFields 是全部被忽略的字段名，含 Injection。审计元数据用它，
	// 因为「Agent 多送了哪些字段」本身是有价值的记录。
	IgnoredFields []string

	// Ambiguous 为真表示资源标识指向不止一个目标，不得自动放行
	// （REQ-INTENT-002 AC1）。
	Ambiguous bool
	// EnvironmentAssumed 为真表示环境无从判定、已按生产处理，
	// 审批页面要显示这一点（REQ-INTENT-003 AC2）。
	EnvironmentAssumed bool
}

// Resolver 收敛 Scope。无状态，可并发使用。
type Resolver struct {
	clock clock.Clock
}

// NewResolver 构造收敛器。时钟必填：时间是十个维度之一，
// 拿不到当前时刻就算不出有效期。
func NewResolver(source clock.Clock) (*Resolver, error) {
	if source == nil {
		return nil, apperr.New(apperr.CodeInternal).WithDetail("Scope Resolver 缺少时钟")
	}
	return &Resolver{clock: source}, nil
}

// Resolve 把一次请求收敛成最小 Scope。
//
// 十个维度里任一确定不下来即返回错误（REQ-SCOPE-001 AC2）。Adapter 未声明的操作
// 走不到这里 —— Intent Resolver 已经把它拦在 capability_not_offered 上（AC3）。
func (r *Resolver) Resolve(input Input) (Result, error) {
	if err := r.checkInput(input); err != nil {
		return Result{}, err
	}

	duration := input.Duration
	if duration == 0 {
		duration = DefaultDuration
	}
	limit := input.RequestLimit
	if limit == 0 {
		limit = DefaultRequestLimit
	}

	now := r.clock.Now()
	resolved := Scope{
		AgentID:      input.AgentID,
		WorkspaceID:  input.WorkspaceID,
		Service:      input.Intent.Service,
		IdentityID:   input.Identity.ID,
		Account:      input.Identity.AccountLabel,
		Resource:     copyResource(input.Intent.Resource),
		ResourceKey:  input.Intent.ResourceKey,
		Operation:    input.Intent.Operation,
		NotBefore:    now,
		ExpiresAt:    now.Add(duration),
		RequestLimit: limit,
		Environment:  input.Intent.Environment,
		RiskCeiling:  ceilingOf(input.Intent.RiskLabel),
	}
	if err := resolved.Validate(); err != nil {
		return Result{}, err
	}

	ignored := append([]string(nil), input.Intent.IgnoredFields...)
	sort.Strings(ignored)

	return Result{
		Scope:              resolved,
		Injection:          injectionFields(ignored),
		IgnoredFields:      ignored,
		Ambiguous:          input.Intent.ResourceAmbiguous,
		EnvironmentAssumed: input.Intent.EnvironmentAssumed,
	}, nil
}

// checkInput 校验调用方交来的数据自洽。
//
// 这里的不自洽都是编排错误而不是请求错误，所以是 internal：Agent 无从制造
// 「身份属于另一个服务」这种输入，出现了就说明上游装配错了数据，
// 而带着错配的身份继续收敛会算出一个看起来完整、实际指向别人账户的 Scope。
func (r *Resolver) checkInput(input Input) error {
	if input.Identity.ID != "" && input.Intent.Service != "" &&
		input.Identity.Service != input.Intent.Service {
		return apperr.New(apperr.CodeInternal).WithDetail(
			"身份属于服务 " + input.Identity.Service + "，意图属于服务 " + input.Intent.Service)
	}
	if input.Identity.Environment != "" &&
		input.Identity.Environment != input.Intent.Environment {
		return apperr.New(apperr.CodeInternal).WithDetail(
			"身份标记为 " + string(input.Identity.Environment) +
				" 环境，意图判定为 " + string(input.Intent.Environment))
	}
	if input.Duration < 0 {
		return apperr.New(apperr.CodeInvalidRequest).WithDetail("授权时长不能为负")
	}
	if input.RequestLimit < 0 {
		return apperr.New(apperr.CodeInvalidRequest).WithDetail("授权次数不能为负")
	}
	return nil
}

// ceilingOf 把 Adapter 声明的标签换成风险上限。认不出的标签落到零值，
// 由 validate 拒绝（Fail Closed：风险等级未知）。
func ceilingOf(label adapters.RiskLabel) risk.Level {
	switch label {
	case adapters.RiskLabelLow:
		return risk.LevelLow
	case adapters.RiskLabelMedium:
		return risk.LevelMedium
	case adapters.RiskLabelHigh:
		return risk.LevelHigh
	default:
		return ""
	}
}

func copyResource(resource map[string]string) map[string]string {
	copied := make(map[string]string, len(resource))
	for key, value := range resource {
		copied[key] = value
	}
	return copied
}

/*
 * 越权字段词表（REQ-SCOPE-002）。
 *
 * 收的是两类词：直接命名 Scope 十个维度或授权本身的（scope、permission、lease、
 * expires…），以及直接命名决策结果的（approval、allow、trust…）。
 * Agent 送来这些字段时，它想影响的不是「做什么」而是「能做多少」。
 *
 * 词表之外被忽略的字段只进 IgnoredFields，不算注入 —— 那多半是 Adapter 声明
 * 没有列出的请求参数，不是扩权尝试。
 */
var injectionWords = []string{
	// Scope 的十个维度与授权本身。
	"scope",
	"permission",
	"privilege",
	"grant",
	"role",
	"policy",
	"risklevel",
	"lease",
	"expires",
	"ttl",
	"requestlimit",
	"environment",
	// 决策结果。
	"approval",
	"approve",
	"autoallow",
	"allow",
	"deny",
	"trust",
	"bypass",
	"override",
	"elevate",
	"escalate",
}

// InjectionWords 返回词表的副本，供调用方在审计说明中引用。
func InjectionWords() []string {
	return append([]string(nil), injectionWords...)
}

// injectionFields 挑出被忽略字段里命中词表的那些。
//
// 匹配在归一化后做子串比较，与 platform/audit 的脱敏词表同一套路：
// 只比全等的话，`requested_scope`、`x-permissions`、`allowList` 全都漏网。
func injectionFields(ignored []string) []string {
	var matched []string
	for _, field := range ignored {
		if isInjection(field) {
			matched = append(matched, field)
		}
	}
	return matched
}

func isInjection(field string) bool {
	normalized := normalizeField(field)
	for _, word := range injectionWords {
		if strings.Contains(normalized, word) {
			return true
		}
	}
	return false
}

func normalizeField(field string) string {
	replacer := strings.NewReplacer("-", "", "_", "", ".", "", " ", "")
	return replacer.Replace(strings.ToLower(field))
}
