package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Redacted 是脱敏后的替代文本，与 platform/secret 的取值一致。
//
// 这里重复定义而不是引用 secret 包：明文类型只允许出现在 credential 与 adapter
// 两个包中（ADR-002），logging 引用它会破坏该边界。两个常量的一致性由
// logging 的用例断言。
const Redacted = "[redacted]"

// OperationIDKey 是每条日志中操作 ID 的字段名。
// 每条日志都必须带它，它是把日志、审计与
// 账本中同一次请求串起来的唯一线索。
const OperationIDKey = "operation_id"

// sensitiveKeyWords 是脱敏词表，逐条取自安全规则。
// 修改本表需要同步修改该规则文件。
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

// normalizedSensitiveKeyWords 是去掉分隔符后的词表，使 api_key、api-key、apiKey
// 命中同一条规则。
var normalizedSensitiveKeyWords = normalizeSensitiveKeyWords()

func normalizeSensitiveKeyWords() []string {
	normalized := make([]string, 0, len(sensitiveKeyWords))
	seen := make(map[string]bool, len(sensitiveKeyWords))
	for _, word := range sensitiveKeyWords {
		key := normalizeKey(word)
		if seen[key] {
			continue
		}
		seen[key] = true
		normalized = append(normalized, key)
	}
	return normalized
}

// SensitiveKeyWords 返回脱敏词表的副本，供测试逐条核对。
func SensitiveKeyWords() []string {
	return append([]string(nil), sensitiveKeyWords...)
}

// Options 是日志的全部可配置项。
//
// 这里没有、也不会有关闭脱敏的开关：debug 级别同样脱敏，不存在「调试模式输出
// 完整信息」。
type Options struct {
	// Level 是最低输出级别，零值为 slog.LevelInfo。
	Level slog.Level
	// Writer 是输出目标，nil 时为 os.Stderr。
	Writer io.Writer
}

// New 构造本产品唯一的 logger：JSON 输出、按 key 脱敏、每条带 operation_id。
func New(options Options) *slog.Logger {
	writer := options.Writer
	if writer == nil {
		writer = os.Stderr
	}

	encoder := slog.NewJSONHandler(writer, &slog.HandlerOptions{
		Level:       options.Level,
		ReplaceAttr: redactSensitiveAttr,
	})
	return slog.New(&operationIDHandler{inner: encoder})
}

// redactSensitiveAttr 把命中词表的字段值换成 Redacted。
//
// 判断只看 key，不看 value —— 一个「像凭据」的值无法被可靠识别。value 侧的防线是
// secret.Value：它的每条输出路径都产出 [redacted]。两者互补，都不可省。
func redactSensitiveAttr(_ []string, attr slog.Attr) slog.Attr {
	if isSensitiveKey(attr.Key) {
		return slog.String(attr.Key, Redacted)
	}
	return attr
}

// isSensitiveKey 用子串匹配而非全等。
//
// 宁可多脱敏也不能漏：access_token、x-api-key、db_password 都必须命中。代价是
// credential_reference_id 这类本身无害的字段也会被脱敏，可接受 —— 它们的准确记录
// 归审计，日志只是运维辅助。
func isSensitiveKey(key string) bool {
	normalized := normalizeKey(key)
	for _, word := range normalizedSensitiveKeyWords {
		if strings.Contains(normalized, word) {
			return true
		}
	}
	return false
}

func normalizeKey(key string) string {
	return strings.NewReplacer("-", "", "_", "", ".", "").Replace(strings.ToLower(key))
}

type operationIDContextKey struct{}

// WithOperationID 把操作 ID 放进 context，供后续每条日志取用。
func WithOperationID(ctx context.Context, operationID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, operationIDContextKey{}, operationID)
}

// OperationIDFrom 从 context 取操作 ID。context 为 nil 或未携带时返回空字符串，
// 不 panic —— 日志不该成为请求失败的原因。
func OperationIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	operationID, carried := ctx.Value(operationIDContextKey{}).(string)
	if !carried {
		return ""
	}
	return operationID
}

// operationIDHandler 给每条记录补上 operation_id 字段。
type operationIDHandler struct {
	inner slog.Handler
}

func (h *operationIDHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *operationIDHandler) Handle(ctx context.Context, record slog.Record) error {
	// Clone 是必须的：Handle 收到的记录可能与其他 handler 共享底层数组。
	enriched := record.Clone()
	enriched.AddAttrs(slog.String(OperationIDKey, OperationIDFrom(ctx)))
	return h.inner.Handle(ctx, enriched)
}

func (h *operationIDHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &operationIDHandler{inner: h.inner.WithAttrs(attrs)}
}

// WithGroup 契约提示：分组打开后 operation_id 会落在该组内，而不是顶层。
// slog 的 Handler 接口无法在组已打开时再往顶层写字段。本项目的日志不使用分组；
// 若将来需要，注入位置要重新设计。
func (h *operationIDHandler) WithGroup(name string) slog.Handler {
	return &operationIDHandler{inner: h.inner.WithGroup(name)}
}
