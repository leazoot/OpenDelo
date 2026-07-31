package audit

import (
	"encoding/json"
	"strings"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

// Redacted 是脱敏后的替代文本，与 platform/logging 和 platform/secret 一致。
const Redacted = "[redacted]"

// maxMetadataValueLength 是元数据里单个字符串值的长度上限。
//
// PRD §22.1 的「默认不记录完整请求正文、完整响应正文、文件内容」在这里是
// 结构性的：超过这个长度的东西不是键值明细，是正文。截断而不是拒绝 ——
// 一条审计事件不该因为附带信息太长就写不进去（ADR-004：写不进去等于请求失败）。
const maxMetadataValueLength = 512

// Truncated 标记一个被截断的值。它出现在账本里就是在说
// 「这里原本更长，被审计层截掉了」，而不是伪装成完整内容。
const Truncated = "…[truncated]"

// sensitiveKeyWords 是脱敏词表，逐条取自安全规则
// 与 PRD §22.2。与 platform/logging 的词表逐字相同，由用例断言两者一致。
//
// 这里重复一份而不是引用 logging：审计与日志是两条独立的输出路径，
// 一条出问题不该悄悄带走另一条的防线。
var sensitiveKeyWords = []string{
	"authorization",
	"cookie",
	"set-cookie",
	"token",
	"api_key",
	"apikey",
	"password",
	"secret",
	"private_key",
	"credential",
}

var normalizedSensitiveKeyWords = normalizeWords(sensitiveKeyWords)

// SensitiveKeyWords 返回脱敏词表的副本，供测试逐条核对。
func SensitiveKeyWords() []string {
	return append([]string(nil), sensitiveKeyWords...)
}

func normalizeWords(words []string) []string {
	normalized := make([]string, 0, len(words))
	for _, word := range words {
		normalized = append(normalized, normalizeKey(word))
	}
	return normalized
}

func normalizeKey(key string) string {
	return strings.NewReplacer("-", "", "_", "", ".", "").Replace(strings.ToLower(key))
}

// isSensitiveKey 用归一化后的子串匹配，与日志侧同一套判断：
// access_token、X-API-Key、db_password 都命中。宁可过度脱敏也不能漏。
func isSensitiveKey(key string) bool {
	normalized := normalizeKey(key)
	for _, word := range normalizedSensitiveKeyWords {
		if strings.Contains(normalized, word) {
			return true
		}
	}
	return false
}

// redactJSONObject 解析一段 JSON 对象，把命中词表的键的值换成 Redacted，
// 递归处理嵌套对象与数组，然后重新序列化。
//
// 判断只看键不看值：一个「像凭据」的值无法被可靠识别。值那一侧的防线在上游 ——
// Adapter 的 Redact() 先过一遍，凭据明文只以 secret.Value 流转，
// 而 secret.Value 根本进不到这一层的签名里。三者互补，都不可省。
func redactJSONObject(field, source string) (string, error) {
	var decoded any
	if err := json.Unmarshal([]byte(source), &decoded); err != nil {
		return "", apperr.Wrap(apperr.CodeInvalidRequest, err).
			WithDetail("审计事件的 " + field + " 不是合法的 JSON")
	}
	if _, isObject := decoded.(map[string]any); !isObject {
		return "", apperr.New(apperr.CodeInvalidRequest).
			WithDetail("审计事件的 " + field + " 必须是 JSON 对象")
	}

	encoded, err := json.Marshal(redactValue(decoded))
	if err != nil {
		return "", apperr.Wrap(apperr.CodeInternal, err).
			WithDetail("审计事件的 " + field + " 无法重新序列化")
	}
	return string(encoded), nil
}

func redactValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(typed))
		for key, nested := range typed {
			if isSensitiveKey(key) {
				redacted[key] = Redacted
				continue
			}
			redacted[key] = redactValue(nested)
		}
		return redacted
	case []any:
		redacted := make([]any, 0, len(typed))
		for _, nested := range typed {
			redacted = append(redacted, redactValue(nested))
		}
		return redacted
	default:
		return value
	}
}

// redactMetadata 在脱敏之外还要求元数据是**扁平的键值明细**。
//
// 嵌套结构与超长字符串正是「完整响应正文」混进账本的形状。Ledger 的条目详情
// 展示的就是一组键值对（设计稿 §07），扁平因此不是限制而是它本来的样子。
func redactMetadata(source string) (string, error) {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(source), &decoded); err != nil {
		return "", apperr.Wrap(apperr.CodeInvalidRequest, err).
			WithDetail("审计事件的 metadata 不是合法的 JSON 对象")
	}

	redacted := make(map[string]any, len(decoded))
	for key, value := range decoded {
		if isSensitiveKey(key) {
			redacted[key] = Redacted
			continue
		}
		scalar, err := flatMetadataValue(key, value)
		if err != nil {
			return "", err
		}
		redacted[key] = scalar
	}

	encoded, err := json.Marshal(redacted)
	if err != nil {
		return "", apperr.Wrap(apperr.CodeInternal, err).
			WithDetail("审计事件的 metadata 无法重新序列化")
	}
	return string(encoded), nil
}

func flatMetadataValue(key string, value any) (any, error) {
	switch typed := value.(type) {
	case string:
		if len(typed) > maxMetadataValueLength {
			return typed[:maxMetadataValueLength] + Truncated, nil
		}
		return typed, nil
	case float64, bool, nil:
		return typed, nil
	default:
		return nil, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("审计事件的 metadata." + key + " 不是标量；账本记键值明细，不记正文")
	}
}
