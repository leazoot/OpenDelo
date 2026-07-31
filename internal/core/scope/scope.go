package scope

import (
	"strings"
	"time"

	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/risk"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * Scope 是一次授权的十个维度（PRD §3.1 目标四、REQ-SCOPE-001）。
 *
 * 十个维度在这里各占一个字段，并由 dimensionChecks 逐个检查。任一维度确定不下来
 * 就拒绝，不填默认值 —— 一个「维度留空」的 Scope 等于在那一维上不设限，
 * 而不设限正是最小权限要排除的情况。
 */

// 十个维度的名字。它们出现在错误说明与审计元数据里，也是 Dimensions 的取值。
const (
	DimensionAgent        = "agent"
	DimensionWorkspace    = "workspace"
	DimensionService      = "service"
	DimensionAccount      = "account"
	DimensionResource     = "resource"
	DimensionOperation    = "operation"
	DimensionTime         = "time"
	DimensionRequestCount = "request_count"
	DimensionEnvironment  = "environment"
	DimensionRisk         = "risk_level"
)

// Scope 是收敛后的最小授权范围，直接落进 decisions.resolved_scope 与
// leases.resource_scope。
//
// 字段的 JSON 名固定：这两列的内容会被账本导出、被 Console 展示，
// 改名等于让历史记录与新记录长得不一样。
type Scope struct {
	AgentID     string `json:"agent_id"`
	WorkspaceID string `json:"workspace_id"`
	Service     string `json:"service"`

	// IdentityID 与 Account 共同构成「账户」这一维：前者是本机主键，
	// 后者是外部服务里的账户名，账本上要看得懂的是后者。
	IdentityID string `json:"identity_id"`
	Account    string `json:"account"`

	// Resource 是能力声明列出的资源字段，已由 Intent Resolver 过滤过。
	Resource map[string]string `json:"resource"`
	// ResourceKey 是 Resource 的规范化文本，供比较与展示使用。
	ResourceKey string `json:"resource_key"`

	// Operation 恒为单个操作。最小权限意味着一次授权只覆盖这一个操作，
	// 需要第二个操作就重新走一遍决策。
	Operation string `json:"operation"`

	NotBefore time.Time `json:"not_before"`
	ExpiresAt time.Time `json:"expires_at"`

	// RequestLimit 恒为正数。Scope 层面不存在「不限次数」——
	// 次数是十个维度之一，留空就等于在这一维上不设限。
	RequestLimit int `json:"request_limit"`

	Environment matcher.Environment `json:"environment"`

	// RiskCeiling 是 Adapter 为该操作声明的风险上限，不是 Risk Engine 算出的等级。
	// 计算结果高于它意味着这次请求超出了声明覆盖的范围，决策链路据此上调
	// （声明标签与计算等级是两件事）。
	RiskCeiling risk.Level `json:"risk_ceiling"`
}

// Dimensions 按 PRD §3.1 的顺序返回十个维度的名字。
//
// 它与 dimensionChecks 同源：少检查一个维度，这里就少一个名字，
// 「维度恰好十个」的用例会失败。
func Dimensions() []string {
	checks := Scope{}.dimensionChecks()
	names := make([]string, 0, len(checks))
	for _, check := range checks {
		names = append(names, check.dimension)
	}
	return names
}

type dimensionCheck struct {
	dimension string
	filled    bool
}

func (s Scope) dimensionChecks() []dimensionCheck {
	return []dimensionCheck{
		{DimensionAgent, s.AgentID != ""},
		{DimensionWorkspace, s.WorkspaceID != ""},
		{DimensionService, s.Service != ""},
		{DimensionAccount, s.IdentityID != "" && s.Account != ""},
		{DimensionResource, len(s.Resource) > 0 && s.ResourceKey != ""},
		{DimensionOperation, s.Operation != ""},
		{DimensionTime, !s.NotBefore.IsZero() && s.ExpiresAt.After(s.NotBefore)},
		{DimensionRequestCount, s.RequestLimit > 0},
		{DimensionEnvironment, validEnvironment(s.Environment)},
		{DimensionRisk, validLevel(s.RiskCeiling)},
	}
}

// Validate 是 REQ-SCOPE-001 AC2：十个维度全部有值，任一为空即拒绝。
//
// 导出是因为决策引擎要独立确认这一点：一个维度不全的 Scope 意味着「Scope 无法确定」，
// 而那是 Fail Closed 的十种情况之一，不能只靠收敛那一步记得报错。
func (s Scope) Validate() error {
	for _, check := range s.dimensionChecks() {
		if !check.filled {
			return apperr.New(apperr.CodeInvalidRequest).
				WithDetail("Scope 的 " + check.dimension + " 维度无法确定")
		}
	}
	return nil
}

// Covers 报告 other 是否完全落在 s 之内。
//
// 用途是 REQ-RISK-003 的范围扩大检测与 REQ-TRUST-002 的收敛校验：新请求的 Scope
// 不被已授权的 Scope 覆盖，就说明范围扩大了，必须重新审批。
//
// 判定对七个维度要求逐字相等（Agent、项目、服务、账户、资源、操作、环境），
// 对时间、次数与风险上限要求 other 不超过 s。任何一维不满足即为不覆盖 ——
// 没有「差不多」的中间态。
//
// 资源值里带通配符时一律返回 false：那样的标识指向不止一个目标，
// 既不能覆盖别人，也不能被别人覆盖（REQ-INTENT-002：不得猜测高影响资源）。
func (s Scope) Covers(other Scope) bool {
	if s.Validate() != nil || other.Validate() != nil {
		return false
	}
	if hasWildcard(s.Resource) || hasWildcard(other.Resource) {
		return false
	}

	switch {
	case s.AgentID != other.AgentID,
		s.WorkspaceID != other.WorkspaceID,
		s.Service != other.Service,
		s.IdentityID != other.IdentityID,
		s.Account != other.Account,
		s.Operation != other.Operation,
		s.Environment != other.Environment:
		return false
	}

	if !sameResource(s.Resource, other.Resource) {
		return false
	}
	if other.NotBefore.Before(s.NotBefore) || other.ExpiresAt.After(s.ExpiresAt) {
		return false
	}
	if other.RequestLimit > s.RequestLimit {
		return false
	}
	return levelRank(other.RiskCeiling) <= levelRank(s.RiskCeiling)
}

// CoversIgnoringWindow 与 Covers 相同，但不比较时间窗口。
//
// 一条已签发的授权（Lease 或 Trust Memory）面对一次新请求时，双方的时间窗口
// 本来就对不上：新请求的 Scope 总是从「现在」起算，而授权是过去某一刻签发的。
// 让窗口参与比较，任何授权都会立刻显得「不够宽」，复用就永远不成立。
//
// 授权到没到期由它自己的到期时刻说了算（Lease 在计量那条语句里判、
// 记忆在匹配时判），不需要范围比较再管一次。其余九个维度照常逐一比较。
func (s Scope) CoversIgnoringWindow(other Scope) bool {
	aligned := other
	aligned.NotBefore = s.NotBefore
	aligned.ExpiresAt = s.ExpiresAt
	return s.Covers(aligned)
}

func sameResource(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

// hasWildcard 与 intent 的歧义判定用同一组字符（`*` 与 `?`）。
func hasWildcard(resource map[string]string) bool {
	for _, value := range resource {
		if strings.ContainsAny(value, "*?") {
			return true
		}
	}
	return false
}

func validEnvironment(environment matcher.Environment) bool {
	switch environment {
	case matcher.EnvironmentProduction, matcher.EnvironmentNonProduction:
		return true
	default:
		return false
	}
}

func validLevel(level risk.Level) bool {
	return levelRank(level) > 0
}

// levelRank 把风险等级排成可比较的序。未知等级为 0，因此既不合法也覆盖不了任何东西
// （Fail Closed：风险等级未知一律拒绝）。
func levelRank(level risk.Level) int {
	switch level {
	case risk.LevelLow:
		return 1
	case risk.LevelMedium:
		return 2
	case risk.LevelHigh:
		return 3
	default:
		return 0
	}
}
