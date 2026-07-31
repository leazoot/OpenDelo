package crypto_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/crypto"
)

// testParams 与生产默认值一致。用例不降低代价参数：
// 降下来跑得快，但测的就不是真正会被使用的那条路径了。
var testParams = crypto.Default

var (
	password  = []byte("correct horse battery staple")
	plaintext = []byte("SENTINEL_TOKEN_d3adb33f_DO_NOT_LEAK")
)

func assertCode(t *testing.T, err error, want apperr.Code) {
	t.Helper()

	if err == nil {
		t.Fatalf("期望错误码 %s，但没有出错", want)
	}
	var appError *apperr.Error
	if !errors.As(err, &appError) {
		t.Fatalf("错误不是 *apperr.Error：%v", err)
	}
	if appError.Code() != want {
		t.Errorf("错误码是 %s，期望 %s（%v）", appError.Code(), want, err)
	}
}

func TestSealOpen_RoundTripsThePlaintext(t *testing.T) {
	sealed, err := crypto.Seal(password, plaintext, testParams)
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}
	if bytes.Contains(sealed, plaintext) {
		t.Error("密文里出现了明文")
	}

	opened, err := crypto.Open(password, sealed)
	if err != nil {
		t.Fatalf("解密失败：%v", err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Errorf("解出 %q，期望 %q", opened, plaintext)
	}
}

func TestSealOpen_HandlesEmptyAndLargePlaintext(t *testing.T) {
	cases := map[string][]byte{
		"空明文":  {},
		"单字节":  {0x00},
		"含零字节": {0x00, 0x01, 0x00, 0xff},
		"较大明文": bytes.Repeat([]byte("A"), 64*1024),
	}

	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			sealed, err := crypto.Seal(password, source, testParams)
			if err != nil {
				t.Fatalf("加密失败：%v", err)
			}
			opened, err := crypto.Open(password, sealed)
			if err != nil {
				t.Fatalf("解密失败：%v", err)
			}
			if !bytes.Equal(opened, source) {
				t.Errorf("往返后内容变了（长度 %d → %d）", len(source), len(opened))
			}
		})
	}
}

func TestOpen_WrongPassword_Fails(t *testing.T) {
	// AC2。REQ-CRED-004 AC3 的存储层前提：没有主密码就解不开。
	sealed, err := crypto.Seal(password, plaintext, testParams)
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}

	for _, wrong := range [][]byte{
		[]byte("correct horse battery stapl"),
		[]byte("Correct horse battery staple"),
		[]byte(""),
		append(append([]byte{}, password...), 0x00),
	} {
		opened, err := crypto.Open(wrong, sealed)
		if err == nil {
			t.Errorf("错误口令 %q 竟然解开了", wrong)
		}
		if opened != nil {
			t.Error("解密失败时仍返回了内容")
		}
	}
}

func TestOpen_EveryFailureLooksTheSame(t *testing.T) {
	// 把「口令错」与「数据被改过」分开，等于告诉攻击者他离对了还有多远。
	sealed, err := crypto.Seal(password, plaintext, testParams)
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}

	tamperedSalt := append([]byte{}, sealed...)
	tamperedSalt[14] ^= 0xff
	tamperedNonce := append([]byte{}, sealed...)
	tamperedNonce[len(tamperedNonce)-40] ^= 0xff
	tamperedCiphertext := append([]byte{}, sealed...)
	tamperedCiphertext[len(tamperedCiphertext)-1] ^= 0xff
	tamperedTag := append([]byte{}, sealed...)
	tamperedTag[len(tamperedTag)-8] ^= 0xff

	cases := map[string]struct {
		password []byte
		sealed   []byte
	}{
		"口令错":      {password: []byte("wrong"), sealed: sealed},
		"口令为空":     {password: nil, sealed: sealed},
		"口令超长":     {password: bytes.Repeat([]byte("x"), 4096), sealed: sealed},
		"盐被改":      {password: password, sealed: tamperedSalt},
		"nonce 被改": {password: password, sealed: tamperedNonce},
		"密文被改":     {password: password, sealed: tamperedCiphertext},
		"标签被改":     {password: password, sealed: tamperedTag},
	}

	var messages []string
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := crypto.Open(testCase.password, testCase.sealed)
			assertCode(t, err, apperr.CodeUnauthenticated)
			messages = append(messages, err.Error())
		})
	}

	for index := 1; index < len(messages); index++ {
		if messages[index] != messages[0] {
			t.Errorf("失败信息不一致，可以据此区分失败原因：\n%s\n%s", messages[0], messages[index])
		}
	}
}

func TestSeal_TamperedHeaderIsRejected(t *testing.T) {
	// 头部作为附加数据参与认证：改掉参数不会让解密悄悄用另一组参数进行。
	sealed, err := crypto.Seal(password, plaintext, testParams)
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}

	// 正向对照：没被动过的信封必须解得开。
	// 少了这一条，「所有信封都解不开」的实现也能让下面的断言成立。
	if _, openErr := crypto.Open(password, sealed); openErr != nil {
		t.Fatalf("未经改动的信封解不开：%v", openErr)
	}

	// 把 iterations 从 3 改成 4（仍然合法，但与加密时不符）。
	tampered := append([]byte{}, sealed...)
	tampered[10] = 4
	if _, openErr := crypto.Open(password, tampered); openErr == nil {
		t.Error("改过参数的密文竟然解开了")
	}

	// 把 memory 改到下限以下：在验证参数这一步就被拒。
	weakened := append([]byte{}, sealed...)
	weakened[3], weakened[4], weakened[5], weakened[6] = 0, 0, 0, 1
	_, err = crypto.Open(password, weakened)
	assertCode(t, err, apperr.CodeInvalidConfiguration)
}

func TestParseHeader_ExposesTheParametersWithoutThePassword(t *testing.T) {
	// AC3：参数可从密文头解析，这是将来判断「哪些密文需要重新加密」的依据。
	sealed, err := crypto.Seal(password, plaintext, testParams)
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}

	header, err := crypto.ParseHeader(sealed)
	if err != nil {
		t.Fatalf("解析头部失败：%v", err)
	}

	if header.FormatVersion != crypto.FormatVersion {
		t.Errorf("信封版本是 %d，期望 %d", header.FormatVersion, crypto.FormatVersion)
	}
	if header.KDF != crypto.KDFArgon2id {
		t.Errorf("KDF 标识是 %d，期望 Argon2id", header.KDF)
	}
	if header.AEAD != crypto.AEADXChaCha20Poly1305 {
		t.Errorf("AEAD 标识是 %d，期望 XChaCha20-Poly1305", header.AEAD)
	}
	if header.Params != testParams {
		t.Errorf("参数是 %+v，期望 %+v", header.Params, testParams)
	}
	if header.SaltLength == 0 {
		t.Error("头部里的盐长度为零")
	}
}

func TestParseHeader_CarriesTheActualParamsNotTheDefaults(t *testing.T) {
	// 默认参数恰好等于下限，用它加密再读头部，分不出「如实写下」与
	// 「写死成下限」。这里刻意用一组强于下限的参数。
	stronger := crypto.Params{
		MemoryKiB:   crypto.MinMemoryKiB * 2,
		Iterations:  crypto.MinIterations + 2,
		Parallelism: crypto.MinParallelism + 3,
	}

	sealed, err := crypto.Seal(password, plaintext, stronger)
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}

	header, err := crypto.ParseHeader(sealed)
	if err != nil {
		t.Fatalf("解析头部失败：%v", err)
	}
	if header.Params != stronger {
		t.Errorf("头部里的参数是 %+v，期望 %+v", header.Params, stronger)
	}

	// 而且必须真的用这组参数解密：写下一组、用另一组加密的话解不开。
	opened, err := crypto.Open(password, sealed)
	if err != nil {
		t.Fatalf("用头部里的参数解密失败：%v", err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Error("解出的内容不对")
	}
}

func TestParseHeader_RejectsMalformedEnvelopes(t *testing.T) {
	sealed, err := crypto.Seal(password, plaintext, testParams)
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}

	cases := map[string][]byte{
		"空输入":     {},
		"只有半个头部":  sealed[:6],
		"未知信封版本":  patched(sealed, 0, 9),
		"未知 KDF":  patched(sealed, 1, 9),
		"未知 AEAD": patched(sealed, 2, 9),
		"盐长度为零":   patched(sealed, 12, 0),
	}

	for name, broken := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := crypto.ParseHeader(broken); err == nil {
				t.Error("不合法的信封被接受了")
			}
			if _, err := crypto.Open(password, broken); err == nil {
				t.Error("不合法的信封被解开了")
			}
		})
	}
}

func TestOpen_TruncatedCiphertextIsRejected(t *testing.T) {
	// 头部说盐有 16 字节，实际数据却不够 —— 不能按越界的切片去读。
	sealed, err := crypto.Seal(password, plaintext, testParams)
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}

	for _, cut := range []int{len(sealed) - 1, 20, 14} {
		if _, err := crypto.Open(password, sealed[:cut]); err == nil {
			t.Errorf("截断到 %d 字节的密文被解开了", cut)
		}
	}
}

func TestSeal_NonceAndSaltNeverRepeat(t *testing.T) {
	// AC4 的属性测试：同一口令、同一明文，反复加密。
	// nonce 复用会让 XChaCha20 的密钥流重复，两段密文异或即可还原明文关系。
	//
	// 轮数取 128 而不是更多：被测的性质来自 crypto/rand，与 KDF 代价无关，
	// 而每一轮都要跑一次 64MiB 的 Argon2id（约 30ms）。128 个互不相同的值
	// 足以打掉「固定 nonce」「计数器 nonce」「按口令派生 nonce」这几种实现，
	// 再多只是让每次 make check 多等几十秒。
	const rounds = 128

	nonces := make(map[string]bool, rounds)
	salts := make(map[string]bool, rounds)
	ciphertexts := make(map[string]bool, rounds)

	for round := range rounds {
		sealed, err := crypto.Seal(password, plaintext, testParams)
		if err != nil {
			t.Fatalf("第 %d 次加密失败：%v", round, err)
		}

		header, err := crypto.ParseHeader(sealed)
		if err != nil {
			t.Fatalf("第 %d 次解析头部失败：%v", round, err)
		}

		saltStart := 13
		saltEnd := saltStart + header.SaltLength
		nonceEnd := saltEnd + 24

		salt := string(sealed[saltStart:saltEnd])
		nonce := string(sealed[saltEnd:nonceEnd])

		if salts[salt] {
			t.Fatalf("第 %d 次出现了重复的盐", round)
		}
		if nonces[nonce] {
			t.Fatalf("第 %d 次出现了重复的 nonce", round)
		}
		if ciphertexts[string(sealed)] {
			t.Fatalf("第 %d 次产出了完全相同的密文", round)
		}
		salts[salt] = true
		nonces[nonce] = true
		ciphertexts[string(sealed)] = true
	}

	if len(nonces) != rounds || len(salts) != rounds {
		t.Errorf("%d 次加密产出了 %d 个 nonce、%d 个盐", rounds, len(nonces), len(salts))
	}
}

func TestParams_BelowTheFloor_AreRejected(t *testing.T) {
	// AC4 的参数下限。调低它们需要 Decision Required，因此这里只允许上调。
	cases := map[string]crypto.Params{
		"内存不足":  {MemoryKiB: crypto.MinMemoryKiB - 1, Iterations: 3, Parallelism: 4},
		"轮数不足":  {MemoryKiB: crypto.MinMemoryKiB, Iterations: 2, Parallelism: 4},
		"并行度不足": {MemoryKiB: crypto.MinMemoryKiB, Iterations: 3, Parallelism: 3},
		"全部为零":  {},
	}

	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			assertCode(t, params.Validate(), apperr.CodeInvalidConfiguration)
			_, err := crypto.Seal(password, plaintext, params)
			assertCode(t, err, apperr.CodeInvalidConfiguration)
		})
	}

	stronger := crypto.Params{
		MemoryKiB:   crypto.MinMemoryKiB * 2,
		Iterations:  crypto.MinIterations + 1,
		Parallelism: crypto.MinParallelism + 1,
	}
	if err := stronger.Validate(); err != nil {
		t.Errorf("更强的参数被拒绝了：%v", err)
	}
}

func TestDefault_MatchesTheRequiredFloor(t *testing.T) {
	// REQ-CRED-004 AC4 点名的三个数字，逐个钉住。
	if crypto.Default.MemoryKiB != 64*1024 {
		t.Errorf("默认内存代价是 %d KiB，期望 64MiB", crypto.Default.MemoryKiB)
	}
	if crypto.Default.Iterations != 3 {
		t.Errorf("默认轮数是 %d，期望 3", crypto.Default.Iterations)
	}
	if crypto.Default.Parallelism != 4 {
		t.Errorf("默认并行度是 %d，期望 4", crypto.Default.Parallelism)
	}
	if err := crypto.Default.Validate(); err != nil {
		t.Errorf("默认参数没有通过自己的校验：%v", err)
	}
}

func TestSeal_EmptyPassword_IsRejected(t *testing.T) {
	// 空口令加出来的密文任何人都能解开，那不是加密。
	_, err := crypto.Seal(nil, plaintext, testParams)
	assertCode(t, err, apperr.CodeInvalidRequest)
}

func TestSeal_SameKeyMaterialAcrossCalls_IsNotReused(t *testing.T) {
	// 两次加密用不同的盐，因此派生出的密钥不同 ——
	// 同一口令下的多份密文之间没有共享密钥。
	first, err := crypto.Seal(password, plaintext, testParams)
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}
	second, err := crypto.Seal(password, plaintext, testParams)
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}

	firstHeader, err := crypto.ParseHeader(first)
	if err != nil {
		t.Fatalf("解析头部失败：%v", err)
	}
	saltEnd := 13 + firstHeader.SaltLength
	if bytes.Equal(first[13:saltEnd], second[13:saltEnd]) {
		t.Error("两次加密用了同一个盐")
	}

	// 交叉解密必须失败：盐不同，密钥就不同。
	crossed := append(append([]byte{}, first[:saltEnd]...), second[saltEnd:]...)
	if _, err := crypto.Open(password, crossed); err == nil {
		t.Error("拼接两份密文后仍能解开")
	}
}

func TestZero_ClearsTheBuffer(t *testing.T) {
	buffer := []byte("SENTINEL_PASSWORD_8c1f04_DO_NOT_LEAK")
	crypto.Zero(buffer)

	for index, value := range buffer {
		if value != 0 {
			t.Fatalf("第 %d 字节仍是 %#x", index, value)
		}
	}

	// 空切片与 nil 不该 panic。
	crypto.Zero(nil)
	crypto.Zero([]byte{})
}

func patched(source []byte, index int, value byte) []byte {
	copied := append([]byte{}, source...)
	copied[index] = value
	return copied
}
