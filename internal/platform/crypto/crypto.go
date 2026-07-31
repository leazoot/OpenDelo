package crypto

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * 本包不接触 platform/secret：明文只允许出现在 credential 与 adapter 两个包的
 * 签名中（ADR-002，由 test/arch 强制）。这里收发的是裸字节，调用方负责在
 * secret.Value 与字节之间转换，并在用完后清零。
 */

// 信封的格式与算法标识。写进每一份密文的头部，使密文自描述 ——
// 换算法或调参之后，旧密文仍然解得开（REQ-CRED-004 AC4 的「支持后续升级」）。
const (
	// FormatVersion 是信封布局的版本。布局本身改变时才递增。
	FormatVersion byte = 1
	// KDFArgon2id 是当前使用的密钥派生函数。
	KDFArgon2id byte = 1
	// AEADXChaCha20Poly1305 是当前使用的认证加密算法。
	AEADXChaCha20Poly1305 byte = 1
)

// keyLength 是 XChaCha20-Poly1305 的密钥长度。
const keyLength = chacha20poly1305.KeySize

// saltLength 是 Argon2id 的盐长度。16 字节是 RFC 9106 的建议下限。
const saltLength = 16

// headerLength 是定长头部的字节数：
// 版本 + KDF + AEAD + memory(4) + iterations(4) + parallelism + saltLen。
const headerLength = 1 + 1 + 1 + 4 + 4 + 1 + 1

// Params 是 Argon2id 的代价参数。
//
// 下限来自 REQ-CRED-004 AC4，
// 且**只允许上调**：Validate 拒绝任何低于下限的组合，因此不存在
// 「配置成弱参数」这条路径。
type Params struct {
	// MemoryKiB 是 Argon2id 的内存代价，单位 KiB。
	MemoryKiB uint32
	// Iterations 是时间代价。
	Iterations uint32
	// Parallelism 是并行度。
	Parallelism uint8
}

// 参数下限。调低它们需要 `Decision Required`。
const (
	MinMemoryKiB   uint32 = 64 * 1024
	MinIterations  uint32 = 3
	MinParallelism uint8  = 4
)

// Default 是本产品使用的参数，恰好等于下限。
var Default = Params{MemoryKiB: MinMemoryKiB, Iterations: MinIterations, Parallelism: MinParallelism}

// Validate 检查参数不低于下限。
func (p Params) Validate() error {
	switch {
	case p.MemoryKiB < MinMemoryKiB:
		return weakParams("memory 不得低于 64MiB")
	case p.Iterations < MinIterations:
		return weakParams("iterations 不得低于 3")
	case p.Parallelism < MinParallelism:
		return weakParams("parallelism 不得低于 4")
	default:
		return nil
	}
}

func weakParams(detail string) error {
	return apperr.New(apperr.CodeInvalidConfiguration).WithDetail("Argon2id 参数过弱：" + detail)
}

// Header 是密文头部解析出来的元信息。
//
// 它让「这份密文是用什么参数加的」成为可读的事实，而不是散落在代码里的常量。
// 升级参数时可以据此判断哪些密文需要重新加密。
type Header struct {
	FormatVersion byte
	KDF           byte
	AEAD          byte
	Params        Params
	SaltLength    int
}

// ParseHeader 从密文中读出元信息，不需要密码（REQ-CRED-004 AC4）。
//
// 头部本身不是秘密：它只说明「用什么算法、什么代价参数加的」。
// 头部同时作为 AEAD 的附加数据参与认证，改动它会让解密失败。
func ParseHeader(sealed []byte) (Header, error) {
	if len(sealed) < headerLength {
		return Header{}, malformed("密文过短，读不出头部")
	}

	header := Header{
		FormatVersion: sealed[0],
		KDF:           sealed[1],
		AEAD:          sealed[2],
		Params: Params{
			MemoryKiB:   binary.BigEndian.Uint32(sealed[3:7]),
			Iterations:  binary.BigEndian.Uint32(sealed[7:11]),
			Parallelism: sealed[11],
		},
		SaltLength: int(sealed[12]),
	}

	if header.FormatVersion != FormatVersion {
		return Header{}, malformed("不认识的信封版本")
	}
	if header.KDF != KDFArgon2id {
		return Header{}, malformed("不认识的密钥派生函数")
	}
	if header.AEAD != AEADXChaCha20Poly1305 {
		return Header{}, malformed("不认识的认证加密算法")
	}
	if header.SaltLength == 0 {
		return Header{}, malformed("盐长度为零")
	}
	return header, nil
}

func malformed(detail string) error {
	return apperr.New(apperr.CodeInvalidRequest).WithDetail("密文格式不合法：" + detail)
}

// Seal 用口令加密 plaintext，返回自描述的密文信封。
//
// 每次调用都生成新的盐与 nonce。XChaCha20 的 nonce 有 192 位，
// 随机生成时的碰撞概率可以忽略 —— 这正是选它而不是 AES-GCM 的原因
// （REQ-CRED-004 §1 的假设）。
//
// 调用方给出的 password 与 plaintext 不会被本函数修改，也不会被清零：
// 它们的生命周期归调用方管。派生出的密钥在返回前清零。
func Seal(password, plaintext []byte, params Params) ([]byte, error) {
	if err := params.Validate(); err != nil {
		return nil, err
	}
	if len(password) == 0 {
		return nil, apperr.New(apperr.CodeInvalidRequest).WithDetail("加密口令不能为空")
	}

	salt := make([]byte, saltLength)
	if _, readErr := rand.Read(salt); readErr != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, readErr).WithDetail("生成盐失败")
	}

	header, err := encodeHeader(params, len(salt))
	if err != nil {
		return nil, err
	}

	key := deriveKey(password, salt, params)
	defer Zero(key)

	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, err).WithDetail("初始化认证加密失败")
	}

	nonce := make([]byte, aead.NonceSize())
	if _, readErr := rand.Read(nonce); readErr != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, readErr).WithDetail("生成 nonce 失败")
	}

	// 头部与盐作为附加数据参与认证：改掉参数或盐会让解密失败，
	// 而不是悄悄用另一组参数解出别的东西。
	associated := append(append([]byte{}, header...), salt...)

	sealed := make([]byte, 0, len(header)+len(salt)+len(nonce)+len(plaintext)+aead.Overhead())
	sealed = append(sealed, header...)
	sealed = append(sealed, salt...)
	sealed = append(sealed, nonce...)
	sealed = aead.Seal(sealed, nonce, plaintext, associated)
	return sealed, nil
}

// Open 用口令解密信封。
//
// 口令错误、密文被改动、盐被替换，返回的都是**同一个错误**：
// 区分开来会告诉攻击者「口令对了但数据坏了」这类信息。
func Open(password, sealed []byte) ([]byte, error) {
	header, err := ParseHeader(sealed)
	if err != nil {
		return nil, err
	}
	if validateErr := header.Params.Validate(); validateErr != nil {
		return nil, validateErr
	}

	nonceLength := chacha20poly1305.NonceSizeX
	saltEnd := headerLength + header.SaltLength
	nonceEnd := saltEnd + nonceLength
	if len(sealed) < nonceEnd+chacha20poly1305.Overhead {
		return nil, malformed("密文长度与头部声明不符")
	}

	salt := sealed[headerLength:saltEnd]
	nonce := sealed[saltEnd:nonceEnd]
	ciphertext := sealed[nonceEnd:]

	key := deriveKey(password, salt, header.Params)
	defer Zero(key)

	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, err).WithDetail("初始化认证加密失败")
	}

	associated := append(append([]byte{}, sealed[:headerLength]...), salt...)

	plaintext, err := aead.Open(nil, nonce, ciphertext, associated)
	if err != nil {
		return nil, decryptFailed()
	}
	return plaintext, nil
}

// decryptFailed 是解密路径上唯一的失败结果。
//
// 不区分原因是有意的：把「口令错」与「数据被改过」分开，等于告诉攻击者
// 他离对了还有多远（REQ-CRED-004 AC1 的同一条原则）。
func decryptFailed() error {
	return apperr.New(apperr.CodeUnauthenticated).WithDetail("解密失败")
}

// encodeHeader 把参数写进定长头部。盐长度只占一个字节，
// 超过 255 的盐无法被如实记下 —— 那种密文将来解不开，所以在这里就拒绝。
func encodeHeader(params Params, saltLength int) ([]byte, error) {
	if saltLength <= 0 || saltLength > 255 {
		return nil, apperr.New(apperr.CodeInternal).
			WithDetail("盐长度必须在 1 到 255 字节之间")
	}

	header := make([]byte, headerLength)
	header[0] = FormatVersion
	header[1] = KDFArgon2id
	header[2] = AEADXChaCha20Poly1305
	binary.BigEndian.PutUint32(header[3:7], params.MemoryKiB)
	binary.BigEndian.PutUint32(header[7:11], params.Iterations)
	header[11] = params.Parallelism
	header[12] = byte(saltLength)
	return header, nil
}

func deriveKey(password, salt []byte, params Params) []byte {
	return argon2.IDKey(password, salt, params.Iterations, params.MemoryKiB, params.Parallelism, keyLength)
}

// Zero 清零一段字节。用 subtle 的常量时间写入，避免被编译器判定为无效存储而优化掉。
//
// 这是调用方在用完明文与密钥后应当做的事；本包对自己派生的密钥一律这样处理。
func Zero(buffer []byte) {
	if len(buffer) == 0 {
		return
	}
	subtle.ConstantTimeCopy(1, buffer, make([]byte, len(buffer)))
}
