package registry

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * 结果脱敏（REQ-ADAPTER-007、PRD §10.8）。
 *
 * 返回给 Agent 的只有五样东西：成功与否、脱敏后的数据、资源变化、操作 ID、
 * 可解释的错误。Header、Cookie、Token、原始 Credential、调试全文一律不返回。
 *
 * 做法是**先白名单再黑名单**：白名单决定哪些顶层字段能出去（外部服务将来新增
 * 的字段默认出不去），黑名单再把嵌套结构里命中词表的值抹掉。只做黑名单的话，
 * 每次外部服务加一个字段就要有人记得来加一条规则。
 */

// RedactedMarker 是被抹掉的值的占位，与 platform/secret 的输出保持一致。
const RedactedMarker = "[redacted]"

// redactionWords 是全局脱敏词表。
//
// 比较前会把字段名转小写并去掉 - 与 _，因此 Set-Cookie、api_key、APIKey
// 都能命中同一条。用包含而不是相等：access_token、refresh_token、
// client_secret 这些都该被抹掉。
var redactionWords = []string{
	"authorization",
	"cookie",
	"token",
	"apikey",
	"password",
	"secret",
	"privatekey",
	"credential",
}

// Result 是返回给 Agent 的执行结果。
//
// 这个结构里**没有 headers 字段**，将来也不能有（REQ-ADAPTER-007 AC2）：
// 让它在类型上不存在，比每次返回前记得删掉它可靠。
type Result struct {
	OK          bool             `json:"ok"`
	OperationID string           `json:"operation_id"`
	Data        json.RawMessage  `json:"data,omitempty"`
	Changes     []ResourceChange `json:"changes,omitempty"`
	Error       *ResultError     `json:"error,omitempty"`

	// UpstreamStatus 是外部服务真正答的状态码，0 表示没有拿到过响应。
	//
	// `json:"-"`：**不给 Agent**。给 Agent 的状态码只有 200 与 502
	// （见 ExchangeReply.StatusCode），原始状态码本身就是外部服务的内部信息。
	// 它存在只为账本与服务端日志 —— 排查一次失败时，「GitHub 答了 422」
	// 与「网关不可用」是两条完全不同的线索，而后者曾经是账本上唯一看得见的。
	UpstreamStatus int `json:"-"`
}

// FromUpstream 记下外部服务答的状态码。
//
// 单独一个方法而不是加进 Success/Failure 的参数：那两个构造函数也用在
// 「请求根本没发出去」的路径上，多一个参数就多一处可以顺手填个 0 敷衍过去。
func (r Result) FromUpstream(status int) Result {
	r.UpstreamStatus = status
	return r
}

// ResourceChange 是一次操作造成的资源变化，供审批与账本展示。
type ResourceChange struct {
	Resource string `json:"resource"`
	Field    string `json:"field"`
	Before   string `json:"before,omitempty"`
	After    string `json:"after,omitempty"`
}

// ResultError 是可解释的错误（REQ-ADAPTER-007 AC3）。
type ResultError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Redact 把外部服务的响应体裁成允许返回给 Agent 的样子。
//
// 顶层是对象时按 ResponseFields 过滤；顶层是数组时对每个元素分别过滤。
// 其余形状（数字、字符串、布尔）没有可过滤的字段，一律不返回 ——
// 那种响应说明 Adapter 的声明与实际端点对不上。
func Redact(body []byte, capability Capability) (json.RawMessage, error) {
	if len(body) == 0 {
		return nil, nil
	}

	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, err).
			WithDetail("外部服务的响应不是合法 JSON，已放弃返回")
	}

	allowed := make(map[string]struct{}, len(capability.ResponseFields))
	for _, field := range capability.ResponseFields {
		allowed[field] = struct{}{}
	}
	extra := normalizeAll(capability.RedactionRules)

	filtered, ok := filterTop(decoded, allowed, extra)
	if !ok {
		return nil, apperr.New(apperr.CodeInternal).
			WithDetail("外部服务的响应既不是对象也不是数组，已放弃返回")
	}

	encoded, err := json.Marshal(filtered)
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, err).
			WithDetail("脱敏后的响应无法序列化")
	}
	return encoded, nil
}

func filterTop(decoded any, allowed map[string]struct{}, extra []string) (any, bool) {
	switch shaped := decoded.(type) {
	case map[string]any:
		return pickAllowed(shaped, allowed, extra), true
	case []any:
		items := make([]any, 0, len(shaped))
		for _, item := range shaped {
			object, isObject := item.(map[string]any)
			if !isObject {
				return nil, false
			}
			items = append(items, pickAllowed(object, allowed, extra))
		}
		return items, true
	default:
		return nil, false
	}
}

// pickAllowed 保留白名单里的顶层字段，并对留下的值继续做黑名单脱敏。
func pickAllowed(object map[string]any, allowed map[string]struct{}, extra []string) map[string]any {
	kept := make(map[string]any, len(allowed))
	for key, value := range object {
		if _, permitted := allowed[key]; !permitted {
			continue
		}
		if sensitiveKey(key, extra) {
			kept[key] = RedactedMarker
			continue
		}
		kept[key] = scrub(value, extra)
	}
	return kept
}

// scrub 递归抹掉命中词表的字段。
//
// 白名单只管顶层，嵌套结构里仍然可能出现 token —— 例如 GitHub 的
// repository 对象里带着一串 *_url，而 Generic HTTP 的响应形状完全由用户定义。
func scrub(value any, extra []string) any {
	switch shaped := value.(type) {
	case map[string]any:
		cleaned := make(map[string]any, len(shaped))
		for key, nested := range shaped {
			if sensitiveKey(key, extra) {
				cleaned[key] = RedactedMarker
				continue
			}
			cleaned[key] = scrub(nested, extra)
		}
		return cleaned
	case []any:
		cleaned := make([]any, len(shaped))
		for index, nested := range shaped {
			cleaned[index] = scrub(nested, extra)
		}
		return cleaned
	case string:
		return scrubURL(shaped)
	default:
		return value
	}
}

// scrubURL 去掉 URL 里可能带凭据的查询串。
//
// 只在查询串命中词表时丢弃整段 query：外部服务返回的 URL 绝大多数是可用的资源
// 地址，无差别砍掉 query 会让它们变得不可用。
func scrubURL(value string) string {
	if !strings.Contains(value, "?") {
		return value
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.RawQuery == "" {
		return value
	}

	for key := range parsed.Query() {
		if sensitiveKey(key, nil) {
			parsed.RawQuery = ""
			return parsed.String()
		}
	}
	return value
}

// sensitiveKey 判断一个字段名是否命中词表。
func sensitiveKey(key string, extra []string) bool {
	normalized := normalize(key)
	for _, word := range redactionWords {
		if strings.Contains(normalized, word) {
			return true
		}
	}
	for _, word := range extra {
		if word != "" && strings.Contains(normalized, word) {
			return true
		}
	}
	return false
}

func normalize(key string) string {
	lowered := strings.ToLower(key)
	lowered = strings.ReplaceAll(lowered, "-", "")
	lowered = strings.ReplaceAll(lowered, "_", "")
	return strings.ReplaceAll(lowered, " ", "")
}

func normalizeAll(keys []string) []string {
	normalized := make([]string, 0, len(keys))
	for _, key := range keys {
		normalized = append(normalized, normalize(key))
	}
	return normalized
}

// 两种赋值写法各用一条规则，因为「值到哪里为止」不一样：
//
//   - 冒号是请求头与日志行的写法，值一直到行尾（"Authorization: Bearer xxx"
//     里的 Bearer 与令牌是一个整体，只抹掉 Bearer 等于没抹）。
//   - 等号是查询串与环境变量的写法，值到下一个空白或 & 为止。
var (
	colonAssignment  = regexp.MustCompile(`([A-Za-z0-9_\-]+)\s*:\s*([^\r\n]+)`)
	equalsAssignment = regexp.MustCompile(`([A-Za-z0-9_\-]+)\s*=\s*([^\s&]+)`)
	// urlToken 用来在文本里就地找出 URL，不破坏原有的空白与换行。
	urlToken = regexp.MustCompile(`https?://[^\s"'<>]+`)
)

// RedactText 抹掉无结构文本里命中词表的赋值。
//
// 用于日志这类没有结构的内容（REQ-ADAPTER-002 AC3 的本地脱敏规则）：
// 外部服务已经做过掩码，但它只认得自己知道的 Secret。
//
// 保留字段名，只换掉值：一行 "Authorization: [redacted]" 仍然告诉读者
// 这里原本有个请求头，而整行删掉会让日志读起来像是缺了一段。
//
// 这是第二层防线而不是唯一防线：一个没有字段名的裸令牌，从文本上无从识别。
func RedactText(text string, extra []string) string {
	normalizedExtra := normalizeAll(extra)

	// 先处理 URL：等号规则会把整个 https://x?token=y 当成 url 的值，
	// 而 url 本身不命中词表，藏在查询串里的令牌就漏过去了。
	text = urlToken.ReplaceAllStringFunc(text, scrubURL)

	for _, pattern := range []*regexp.Regexp{equalsAssignment, colonAssignment} {
		text = maskAssignments(text, pattern, normalizedExtra)
	}
	return text
}

func maskAssignments(text string, pattern *regexp.Regexp, extra []string) string {
	return pattern.ReplaceAllStringFunc(text, func(match string) string {
		groups := pattern.FindStringSubmatch(match)
		if len(groups) != 3 || !sensitiveKey(groups[1], extra) {
			return match
		}
		return strings.TrimSuffix(match, groups[2]) + RedactedMarker
	})
}

// Success 构造一个成功的结果。
func Success(operationID string, data json.RawMessage, changes []ResourceChange) Result {
	return Result{OK: true, OperationID: operationID, Data: data, Changes: changes}
}

// Failure 构造一个失败的结果。
//
// 保留操作 ID 与可读原因，不带外部服务的原始报文 ——
// 那里面可能有请求回显（REQ-ADAPTER-007 AC3）。
func Failure(operationID string, err error) Result {
	// 走 PublicOf：对外文本只能取自 apperr 的编译期常量表，
	// 因此 cause 链里的路径、主机名、外部报文不可能顺着错误漏出去。
	public := apperr.PublicOf(err, operationID)
	return Result{
		OK:          false,
		OperationID: public.OperationID,
		Error:       &ResultError{Code: public.Code.String(), Message: public.Message},
	}
}
