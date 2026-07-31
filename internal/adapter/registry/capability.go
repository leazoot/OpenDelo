package registry

import (
	"encoding/json"
	"strings"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * Adapter 的能力声明（REQ-ADAPTER-001）。
 *
 * PRD §10.7 要求每个 Adapter 声明九项：支持的操作、输入 Schema、最小 Scope、
 * 风险标签、请求方法、脱敏规则、响应过滤、回滚能力、幂等性。九项在这里各占一个
 * 字段，注册时逐项校验，缺一即启动失败 —— 一个「注册得上但声明不全」的 Adapter
 * 只会在第一次真实请求时才暴露，而那时凭据已经取出来了。
 *
 * 与 Declaration 的关系：Declaration 是落库的那一行（用户可见的服务配置），
 * Capability 是编译期写死的契约（ADR-009：Adapter 不动态加载）。前者能改，
 * 后者跟着二进制走。
 */

// Rollback 是一个操作的回滚能力声明。
type Rollback string

const (
	// RollbackNone 表示做完就回不去了。写操作声明成它就必须是高风险。
	RollbackNone Rollback = "none"
	// RollbackManual 表示用户可以手动还原，前提是审批时记下了旧值。
	RollbackManual Rollback = "manual"
	// RollbackAutomatic 表示 Adapter 自己能还原。
	RollbackAutomatic Rollback = "automatic"
)

// Idempotency 是幂等性声明。
//
// 做成枚举而不是 bool：bool 的零值是「非幂等」，看起来安全，但那样
// 「忘了声明」与「声明为非幂等」在类型上无法区分，AC1 的「缺失则启动失败」
// 也就落不了地。
type Idempotency string

const (
	// Idempotent 表示重复执行与执行一次的结果相同，失败后允许重试。
	Idempotent Idempotency = "idempotent"
	// NonIdempotent 表示重复执行会产生第二个副作用，失败后一律不重试。
	NonIdempotent Idempotency = "non_idempotent"
)

// Nature 是一个操作的性质，喂给 core/risk 的五个因子（PRD §10.5）。
//
// 这五项只有 Adapter 知道：「删除一条 DNS 记录」是删除类操作、
// 「修改仓库成员」是权限变更，请求本身看不出来。声明错了风险等级就算错了，
// 因此 validate 会拒绝与风险标签自相矛盾的声明。
type Nature struct {
	// Destructive 表示删除类操作。
	Destructive bool
	// PermissionChange 表示修改权限、成员或认证方式。
	PermissionChange bool
	// SecretAccess 表示读取 Secret 或签发凭证。
	SecretAccess bool
	// Billing 表示涉及账单、支付或转账。
	Billing bool
	// ExternalCommunication 表示会向外部发出可见的通信，例如发邮件。
	ExternalCommunication bool
}

// MinimumScope 是执行该操作所需的最小 Scope（REQ-ADAPTER-001、REQ-SCOPE-001）。
//
// 只描述「哪些维度必须被确定下来」，不描述取值 —— 取值由 core/scope 在解析
// 请求时推导。写在这里是为了让「资源维度定不下来」在进入决策之前就被拒绝。
type MinimumScope struct {
	// ResourceKeys 是必须有确定取值的资源维度，例如 owner、repo。
	// 为空表示这个操作不针对任何具体资源 —— 那样的操作无法被授权，一律拒绝。
	ResourceKeys []string
	// RequiresAccount 为真表示必须先确定用哪个账号来做这件事。
	RequiresAccount bool
}

// Capability 是一个操作的完整声明。
type Capability struct {
	// Operation 是操作名，在同一个 Adapter 内唯一。
	Operation string
	// InputSchema 是输入的 JSON Schema 文本（REQ-CAP-001 AC1）。
	InputSchema string
	// MinimumScope 是最小 Scope。
	MinimumScope MinimumScope
	// RiskLabel 是风险标签，作为 core/risk 计算的基线。
	RiskLabel RiskLabel
	// Method 是 HTTP 方法。
	Method string
	// Path 是端点白名单里的路径模板，形如 /repos/{owner}/{repo}/issues。
	Path string
	// RedactionRules 是在全局脱敏词表之外还要抹掉的字段名。
	//
	// nil 与空切片有区别：空切片是「本操作没有额外规则」，nil 是「忘了声明」，
	// 后者拒绝注册。
	RedactionRules []string
	// ResponseFields 是允许返回给 Agent 的顶层字段白名单（REQ-ADAPTER-007）。
	// 白名单之外的一切不返回，包括将来外部服务新增的字段。
	ResponseFields []string
	// Rollback 是回滚能力。
	Rollback Rollback
	// Idempotency 是幂等性。
	Idempotency Idempotency
	// Nature 是操作性质。
	Nature Nature
}

// Write 报告这个操作会不会改变外部状态。
func (c Capability) Write() bool {
	switch strings.ToUpper(c.Method) {
	case "GET", "HEAD":
		return false
	default:
		return true
	}
}

// Validate 是 REQ-ADAPTER-001 AC1：九项声明缺一即拒绝。
//
// 除了「有没有」，还查「彼此矛不矛盾」：一个声明为删除类却标成 low 的操作，
// 风险引擎照样会把它算成 high，但那要等到运行时才发现，而声明本身已经错了。
func (c Capability) Validate() error {
	if err := c.validatePresence(); err != nil {
		return err
	}
	return c.validateConsistency()
}

func (c Capability) validatePresence() error {
	switch {
	case strings.TrimSpace(c.Operation) == "":
		return declarationError("没有声明操作名")
	case strings.TrimSpace(c.InputSchema) == "":
		return declarationError(c.Operation + " 没有声明输入 Schema")
	case !json.Valid([]byte(c.InputSchema)):
		return declarationError(c.Operation + " 的输入 Schema 不是合法 JSON")
	case len(c.MinimumScope.ResourceKeys) == 0:
		return declarationError(c.Operation + " 没有声明最小 Scope 的资源维度")
	case !validRiskLabel(c.RiskLabel):
		return declarationError(c.Operation + " 没有声明风险标签")
	case !validMethod(c.Method):
		return declarationError(c.Operation + " 没有声明合法的请求方法：" + c.Method)
	case !strings.HasPrefix(c.Path, "/"):
		return declarationError(c.Operation + " 没有声明以 / 开头的请求路径")
	case c.RedactionRules == nil:
		return declarationError(c.Operation + " 没有声明脱敏规则")
	case len(c.ResponseFields) == 0:
		return declarationError(c.Operation + " 没有声明响应过滤白名单")
	case !validRollback(c.Rollback):
		return declarationError(c.Operation + " 没有声明回滚能力")
	case !validIdempotency(c.Idempotency):
		return declarationError(c.Operation + " 没有声明幂等性")
	}
	return nil
}

// validateConsistency 拦下与风险引擎必然冲突的声明。
//
// 对照的是 core/risk 的规则表：前四种性质命中即封顶 high，
// 对外通信在写操作上抬到至少 medium，写操作不可逆同样封顶 high。
func (c Capability) validateConsistency() error {
	switch {
	case c.Nature.Destructive && c.RiskLabel != RiskLabelHigh:
		return declarationError(c.Operation + " 是删除类操作，风险标签必须是 high")
	case c.Nature.PermissionChange && c.RiskLabel != RiskLabelHigh:
		return declarationError(c.Operation + " 会改变权限，风险标签必须是 high")
	case c.Nature.SecretAccess && c.RiskLabel != RiskLabelHigh:
		return declarationError(c.Operation + " 会读取 Secret，风险标签必须是 high")
	case c.Nature.Billing && c.RiskLabel != RiskLabelHigh:
		return declarationError(c.Operation + " 涉及账单，风险标签必须是 high")
	case c.Write() && c.Rollback == RollbackNone && c.RiskLabel != RiskLabelHigh:
		return declarationError(c.Operation + " 是不可逆的写操作，风险标签必须是 high")
	case c.Write() && c.Nature.ExternalCommunication && c.RiskLabel == RiskLabelLow:
		return declarationError(c.Operation + " 会向外部发出通信，风险标签至少是 medium")
	case !c.Write() && c.Idempotency != Idempotent:
		return declarationError(c.Operation + " 是读取操作，幂等性只能是 idempotent")
	case !c.Write() && c.Nature.Destructive:
		return declarationError(c.Operation + " 用 " + c.Method + " 却声明为删除类操作")
	}
	return c.validatePlaceholders()
}

// validatePlaceholders 要求路径里的每个占位符都在最小 Scope 里声明过。
//
// 两者必须对上：占位符是「这次请求打向哪个资源」，最小 Scope 是「授权时要把哪些
// 维度定下来」。少声明一个，就会出现一个没有被授权覆盖、却能改变请求目标的取值。
func (c Capability) validatePlaceholders() error {
	declared := make(map[string]struct{}, len(c.MinimumScope.ResourceKeys))
	for _, key := range c.MinimumScope.ResourceKeys {
		declared[key] = struct{}{}
	}

	for _, segment := range strings.Split(strings.TrimPrefix(c.Path, "/"), "/") {
		if !strings.HasPrefix(segment, "{") || !strings.HasSuffix(segment, "}") {
			continue
		}
		key := strings.TrimSuffix(strings.TrimPrefix(segment, "{"), "}")
		if _, found := declared[key]; !found {
			return declarationError(c.Operation + " 的路径用到了 " + key +
				"，但最小 Scope 里没有声明它")
		}
	}
	return nil
}

func declarationError(detail string) error {
	return apperr.New(apperr.CodeInvalidConfiguration).
		WithDetail("Adapter 能力声明不完整：" + detail)
}

func validRiskLabel(label RiskLabel) bool {
	switch label {
	case RiskLabelLow, RiskLabelMedium, RiskLabelHigh:
		return true
	default:
		return false
	}
}

func validRollback(rollback Rollback) bool {
	switch rollback {
	case RollbackNone, RollbackManual, RollbackAutomatic:
		return true
	default:
		return false
	}
}

func validIdempotency(idempotency Idempotency) bool {
	switch idempotency {
	case Idempotent, NonIdempotent:
		return true
	default:
		return false
	}
}

// validMethod 只认这六个方法。大小写敏感：声明里写 "get" 说明写声明的人
// 没有照着规范抄，而这种随手写法迟早会变成路径拼错。
func validMethod(method string) bool {
	switch method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE":
		return true
	default:
		return false
	}
}
