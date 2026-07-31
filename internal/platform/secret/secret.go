package secret

import (
	"encoding"
	"encoding/json"
	"fmt"
	"log/slog"
)

// Redacted 是 Value 在每一条输出路径上的替代文本。
const Redacted = "[redacted]"

// plaintextBox 使明文只能经一层指针到达。
//
// fmt 在深度大于 0 时才会走反射，而通过非导出字段取到的 reflect.Value 无法
// Interface()，Format 与 String 都不会被调用。此时字节切片会被逐字节打印成
// [83 69 78 ...]，指针则只打印地址。把明文放在指针后面，就把这条唯一剩下的
// 反射泄漏路径也堵上了。
type plaintextBox struct {
	bytes []byte
}

// Value 承载凭据明文，是本产品中允许明文存在的唯一类型（ADR-002）。
//
// 后置条件：Value 的全部输出路径 —— fmt 动词、JSON 编码、slog 记录、以及
// 嵌在其他结构体里被反射打印 —— 都不产出明文。取得明文只有 Reveal 一条路径，
// 因此每个接触明文的调用点在 review 中都可见。
//
// Value 只允许出现在 internal/credential 与 internal/adapter 的签名中，
// 由 test/arch 强制。
//
// 零值可用，表示不含明文。
type Value struct {
	box *plaintextBox
}

// New 接管 plaintext 的所有权：调用方此后不得再读写该切片，因为 Zero 会
// 就地清零它。不复制是有意为之 —— 复制会在堆上留下第二份无法清零的明文。
func New(plaintext []byte) Value {
	return Value{box: &plaintextBox{bytes: plaintext}}
}

// NewString 从字符串构造 Value。
//
// 字符串不可变且无法清零，其内容会留在内存中直到被 GC 回收，Zero 对它无能为力。
// 只在明文本来就是字符串常量（如测试哨兵）时使用；从 Provider 读到的字节流一律走 New。
func NewString(plaintext string) Value {
	return New([]byte(plaintext))
}

// Reveal 返回明文，是取得明文的唯一路径。
//
// 返回的是内部切片本身而非副本 —— 副本无法被 Zero 清零。调用方不得保留该切片，
// 不得将其写入日志、审计、API 响应、临时文件、环境变量、命令行参数或错误信息。
func (v Value) Reveal() []byte {
	if v.box == nil {
		return nil
	}
	return v.box.bytes
}

// IsEmpty 报告是否不含任何明文字节。凭据源返回空值时据此走 Fail Closed，
// 而不是把空凭据当成有效凭据发出请求。
func (v Value) IsEmpty() bool {
	return len(v.Reveal()) == 0
}

// Zero 就地清零明文字节并丢弃引用。取用后应立即 defer 调用。
//
// 同一 Value 的所有副本共享同一份明文，因此任一副本上的 Zero 对全部副本生效。
func (v *Value) Zero() {
	if v.box == nil {
		return
	}
	for i := range v.box.bytes {
		v.box.bytes[i] = 0
	}
	v.box.bytes = nil
	v.box = nil
}

// String 实现 fmt.Stringer，兜住不经 fmt 动词的字符串拼接。
func (Value) String() string { return Redacted }

// Format 实现 fmt.Formatter。
//
// fmt 对 Formatter 的检查先于 GoStringer 与 Stringer，因此这一个方法封死了
// 全部 fmt 路径，包括 %#v。不按动词分支是有意为之：将来出现的任何动词默认脱敏。
//
// 写入错误无处上报（Format 没有 error 返回）也无从补救：state 是 fmt 自己的
// 缓冲区，写失败意味着调用方的 Writer 已经坏了。
func (Value) Format(state fmt.State, _ rune) {
	fmt.Fprint(state, Redacted)
}

// MarshalJSON 实现 json.Marshaler。
func (Value) MarshalJSON() ([]byte, error) {
	return json.Marshal(Redacted)
}

// LogValue 实现 slog.LogValuer，使 Value 在任意 handler 下都记录为 Redacted，
// 而不依赖该 handler 恰好走 JSON 或 fmt 路径。
func (Value) LogValue() slog.Value { return slog.StringValue(Redacted) }

var (
	_ fmt.Stringer   = Value{}
	_ fmt.Formatter  = Value{}
	_ json.Marshaler = Value{}
	_ slog.LogValuer = Value{}
)

// 以下三个声明让「Value 未实现 encoding.TextMarshaler」成为编译期约束。
//
// textMarshalerConflict 在同一深度嵌入 Value 与一个自带 MarshalText 的类型。
// 若 Value 日后也获得 MarshalText，两个方法同深度冲突，选择器变为不明确，
// textMarshalerConflict 不再实现 encoding.TextMarshaler，末尾的断言无法编译。
//
// 这条约束是必要的：encoding/json 与 slog 都优先使用 TextMarshaler，一旦
// Value 实现它并返回明文，MarshalJSON 与 Format 两道防线就被整体绕过。
type textMarshalerProbe struct{}

func (textMarshalerProbe) MarshalText() ([]byte, error) { return nil, nil }

type textMarshalerConflict struct {
	Value
	textMarshalerProbe
}

var _ encoding.TextMarshaler = textMarshalerConflict{}
