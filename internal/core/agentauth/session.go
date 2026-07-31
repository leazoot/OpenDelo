package agentauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * Session Key 是 Agent 面的持有型凭证：拿着它就等于是那个 Agent。
 *
 * 它不是外部服务的凭据，所以不走 platform/secret —— 那个类型按
 * 只允许出现在 credential 与 adapter 两个包里，
 * core 不得导入。但「误打印不该泄漏」这条要求一模一样，所以这里用同样的手法：
 * 明文只能经 Reveal 取出，其余全部输出路径产出 [redacted]。
 */

// sessionKeyBytes 是 Session Key 的熵。256 位随机值不可能被穷举，因此存库前
// 用一次 SHA-256 就够，不需要 Argon2id 那样的慢哈希 —— 慢哈希防的是低熵口令被
// 离线爆破，这里没有低熵口令。
const sessionKeyBytes = 32

// SessionKeyHashPrefix 标明哈希算法，使日后换算法时旧值仍可辨认。
const SessionKeyHashPrefix = "sha256:"

// redacted 是 SessionKey 在每一条输出路径上的替代文本。
const redacted = "[redacted]"

// keyBox 使明文只能经一层指针到达。
//
// fmt 在深度大于 0 时走反射，而经非导出字段取到的 reflect.Value 无法 Interface()，
// Format 与 String 都不会被调用。把明文放在指针后面，反射打印到的就只是地址。
type keyBox struct {
	value string
}

// SessionKey 是签发给 Agent 的会话凭证。
//
// 后置条件：全部输出路径 —— fmt 动词、JSON 编码、slog 记录 —— 都不产出明文。
// 取得明文只有 Reveal 一条路径，因此每个接触明文的调用点在 review 中都可见。
//
// 零值可用，表示「没有会话」。
type SessionKey struct {
	box *keyBox
}

// NewSessionKey 由已有的明文构造，供接入面把收到的凭证交回本包校验。
func NewSessionKey(value string) SessionKey {
	if value == "" {
		return SessionKey{}
	}
	return SessionKey{box: &keyBox{value: value}}
}

// Reveal 返回明文。这是取得明文的唯一路径。
func (k SessionKey) Reveal() string {
	if k.box == nil {
		return ""
	}
	return k.box.value
}

// IsEmpty 报告是否不含凭证。校验入口据此走 Fail Closed，
// 而不是拿空字符串去查库。
func (k SessionKey) IsEmpty() bool { return k.Reveal() == "" }

// String 实现 fmt.Stringer，兜住不经 fmt 动词的字符串拼接。
func (SessionKey) String() string { return redacted }

// Format 实现 fmt.Formatter。fmt 对它的检查先于 Stringer 与 GoStringer，
// 因此这一个方法封死全部 fmt 路径，包括 %#v 与 %q。不按动词分支是有意为之：
// 将来出现的任何动词默认脱敏。
func (SessionKey) Format(state fmt.State, _ rune) {
	fmt.Fprint(state, redacted)
}

// MarshalJSON 实现 json.Marshaler，使会话凭证不会随任何响应体被序列化出去。
// 接入面要把它交给 Agent 时必须显式调用 Reveal，那是一次有意的动作。
func (SessionKey) MarshalJSON() ([]byte, error) { return json.Marshal(redacted) }

// LogValue 实现 slog.LogValuer，使任意 handler 下都记录为 [redacted]。
func (SessionKey) LogValue() slog.Value { return slog.StringValue(redacted) }

var (
	_ fmt.Stringer   = SessionKey{}
	_ fmt.Formatter  = SessionKey{}
	_ json.Marshaler = SessionKey{}
	_ slog.LogValuer = SessionKey{}
)

// generateSessionKey 从 entropy 取 256 位随机值并编码为 URL 安全的 base64。
//
// 熵源失败必须向上传递：签发一个弱密钥比拒绝注册糟糕得多。
func generateSessionKey(entropy io.Reader) (SessionKey, error) {
	raw := make([]byte, sessionKeyBytes)
	if _, err := io.ReadFull(entropy, raw); err != nil {
		return SessionKey{}, apperr.Wrap(apperr.CodeInternal, err).WithDetail("生成会话密钥失败")
	}
	return NewSessionKey(base64.RawURLEncoding.EncodeToString(raw)), nil
}

// HashSessionKey 返回入库与查找用的哈希。
//
// 库里只有哈希：明文一旦落库，读到库的人就能直接冒充该 Agent，而校验只需要
// 比对，不需要还原。
func HashSessionKey(key SessionKey) string {
	sum := sha256.Sum256([]byte(key.Reveal()))
	return SessionKeyHashPrefix + hex.EncodeToString(sum[:])
}

// sameHash 以常量时间比较两个哈希。
//
// 查找走的是数据库唯一索引，这一步防的是查到记录之后的二次确认变成计时旁路。
func sameHash(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

// defaultEntropy 是进程运行时的熵源。
var defaultEntropy io.Reader = rand.Reader
