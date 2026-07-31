package apperr

import (
	"encoding/json"
	"sort"
)

// Code 是对外错误码，与 REQ-API-003 的 `error.code` 字段一一对应。
//
// 用带非导出字段的结构体而不是字符串常量，使枚举在编译期封闭：包外无法构造出
// 本表之外的 Code，也就无法让一个前端不认识的码出现在 API 响应里
// （REQ-API-003 AC2 要求前后端共享同一份定义）。
//
// 零值表示「未指定」，Valid 为 false。New 与 Wrap 会把它规整为 CodeInternal。
type Code struct {
	name string
}

// String 返回上线格式的码名（snake_case），也是 JSON 中的取值。
func (c Code) String() string { return c.name }

// Valid 报告该 Code 是否来自本表。
func (c Code) Valid() bool {
	_, registered := messages[c]
	return registered
}

// MarshalJSON 使 Code 直接序列化为码名字符串。
func (c Code) MarshalJSON() ([]byte, error) { return json.Marshal(c.name) }

// messages 是码 → 对外可展示文本的唯一映射。
//
// 文本是编译期常量，不含任何请求内容、路径、主机名或 cause 链信息 —— 对外 message
// 只能取自这里，脱敏因此由构造方式保证，而不依赖调用方自觉（见 Error.Public）。
//
// 文本为英文（用户决定 D-09）。这条通道的读者是 MCP 与 Proxy 两个面的消费方 ——
// 大模型与外部工具；Console 侧按 code 查自己的 i18n，不读这里的 message。
// 因此这里不做多语言：多一份翻译就多一处「两个面说法不一致」的可能。
var messages = map[Code]string{}

func register(name, message string) Code {
	code := Code{name: name}
	if _, duplicated := messages[code]; duplicated {
		panic("apperr: 错误码重复注册 " + name)
	}
	messages[code] = message
	return code
}

// 通用码。对应需求文档中以 HTTP 状态而非码名描述的失败。
var (
	// CodeInvalidRequest 对应 400：结构、字段或 JSON Schema 校验未通过。
	CodeInvalidRequest = register("invalid_request", "The request is not valid.")
	// CodeUnauthenticated 对应 401：缺少或无效的会话令牌 / Session Key。
	CodeUnauthenticated = register("unauthenticated", "Missing or invalid credentials.")
	// CodeForbidden 对应 403：身份有效但不允许执行该操作，含 Origin 校验未通过。
	CodeForbidden = register("forbidden", "This identity may not perform the operation.")
	// CodeNotFound 对应 404。跨 Agent 查询也返回它，避免泄漏资源存在性。
	CodeNotFound = register("not_found", "The requested resource does not exist.")
	// CodeConflict 对应 409：状态机已推进，重复或迟到的操作未执行。
	CodeConflict = register("conflict", "The resource changed state; this operation was not applied.")
	// CodeInternal 对应 500，也是一切无法归类错误的落点（Fail Closed）。
	CodeInternal = register("internal", "Gateway internal error. Look up the operation_id in the ledger.")
	// CodeNotImplemented 用于显式未实现的能力，禁止用占位实现假装完成。
	CodeNotImplemented = register("not_implemented", "That capability is not implemented.")
	// CodeInvalidConfiguration 用于配置不合法导致的拒绝启动与偏好校验失败（REQ-PREF-001）。
	CodeInvalidConfiguration = register("invalid_configuration", "The configuration is not valid.")
)

// Agent 身份（REQ-AGENT-001）。
var (
	CodeAgentIdentityUnverifiable = register(
		"agent_identity_unverifiable", "The caller's identity could not be established; the request was denied.")
	CodeSessionExpired = register(
		"session_expired", "The session has expired; authenticate again.")
)

// 能力与审批（REQ-CAP-001、REQ-CAP-002、REQ-APPROVAL-004）。
var (
	CodeCapabilityNotOffered = register(
		"capability_not_offered", "The gateway does not offer that capability.")
	CodeApprovalTimeout = register(
		"approval_timeout", "Timed out waiting for human confirmation; the request was denied.")
)

// 凭据源（REQ-CRED-002、REQ-CRED-003、REQ-CRED-004、REQ-GATEWAY-004）。
var (
	CodeCredentialNotAuthorized = register(
		"credential_not_authorized", "That credential is not authorized for this request.")
	CodeProviderUnavailable = register(
		"provider_unavailable", "The credential source is unavailable; the request was denied.")
	CodeProviderNotSupportedOnPlatform = register(
		"provider_not_supported_on_platform", "That credential source is unavailable on this platform.")
	CodeProviderLockedTimeout = register(
		"provider_locked_timeout", "Timed out waiting for the credential source to unlock; the request was denied.")
	CodeVaultLocked = register(
		"vault_locked", "The local vault is locked.")
)

// 执行与网关（REQ-ADAPTER-005、REQ-ADAPTER-008、REQ-GATEWAY-003）。
var (
	CodePathNotAllowed = register(
		"path_not_allowed", "The request path is outside what that service allows.")
	CodeAdapterTimeout = register(
		"adapter_timeout", "The external service timed out.")
	CodeGatewayUnavailable = register(
		"gateway_unavailable", "The gateway is unavailable; the request never reached the external service.")
)

// All 按码名升序返回全部错误码，供逐项核对码表是否完整（REQ-API-003 AC2）。
func All() []Code {
	all := make([]Code, 0, len(messages))
	for code := range messages {
		all = append(all, code)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].name < all[j].name })
	return all
}
