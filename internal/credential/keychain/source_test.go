package keychain_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/credential/keychain"
	"github.com/Runcoor/opendelo/internal/credential/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * macOS Keychain Provider 的行为用例（REQ-CRED-003）。
 *
 * **用例绝不读用户真实的钥匙串**：
 * 行为用可控的 Runner 验证，真实 exec 路径用一个写在临时目录里的假 security 验证。
 *
 * 平台判断走可注入的 GOOS 而不是编译标签，因此「在 Linux 上会怎样」
 * 在 macOS 上也测得到，反之亦然 —— 唯一真正需要 skip 的只有假脚本用的 POSIX shell。
 */

type recordingRunner struct {
	args   []string
	calls  int
	output []byte
	err    error
}

func (r *recordingRunner) Run(_ context.Context, _ string, args []string) ([]byte, error) {
	r.calls++
	r.args = append([]string(nil), args...)
	if r.err != nil {
		return nil, r.err
	}
	return append([]byte(nil), r.output...), nil
}

func onMac(runner registry.CommandRunner) *keychain.Source {
	return keychain.New(keychain.Options{Runner: runner, GOOS: "darwin"})
}

func reference(itemRef, account string) registry.Reference {
	return registry.Reference{ItemRef: itemRef, Field: account}
}

func assertCode(t *testing.T, err error, expected apperr.Code) {
	t.Helper()

	if err == nil {
		t.Fatalf("期望失败并返回 %s，实际成功", expected)
	}
	if !apperr.Is(err, expected) {
		t.Fatalf("错误码为 %s，期望 %s（%v）", apperr.CodeOf(err), expected, err)
	}
}

// ——— 平台边界（AC2）———

func TestFetch_OnEveryPlatformOtherThanMacOS_IsNotSupported(t *testing.T) {
	// AC2：非 macOS 上返回 provider_not_supported_on_platform，
	// 而不是「找不到 security」—— 后者会把「这个平台不支持」说成「装一下就好了」。
	for _, goos := range []string{"linux", "windows", "freebsd"} {
		t.Run(goos+" 上不支持", func(t *testing.T) {
			runner := &recordingRunner{output: []byte(sentinel.SentinelToken)}
			source := keychain.New(keychain.Options{Runner: runner, GOOS: goos})

			_, err := source.Fetch(t.Context(), reference("keychain://generic/github.com", "work"))
			assertCode(t, err, apperr.CodeProviderNotSupportedOnPlatform)
			assertCode(t, source.Available(t.Context()), apperr.CodeProviderNotSupportedOnPlatform)

			if runner.calls != 0 {
				t.Errorf("不支持的平台上仍然调用了 security %d 次", runner.calls)
			}
		})
	}
}

func TestNew_DefaultsToTheRunningPlatform(t *testing.T) {
	// 不传 GOOS 时取 runtime.GOOS：生产路径上没有人会去设置它。
	runner := &recordingRunner{output: []byte(sentinel.SentinelToken)}
	source := keychain.New(keychain.Options{Runner: runner})

	_, err := source.Fetch(t.Context(), reference("keychain://generic/github.com", "work"))
	if runtime.GOOS == "darwin" {
		if err != nil {
			t.Fatalf("macOS 上取用失败：%v", err)
		}
		return
	}
	assertCode(t, err, apperr.CodeProviderNotSupportedOnPlatform)
}

// ——— 读一条条目（AC1）———

func TestFetch_GenericAndInternetPasswords_UseTheirOwnSubcommand(t *testing.T) {
	cases := []struct {
		itemRef string
		command string
	}{
		{"keychain://generic/github.com", "find-generic-password"},
		{"keychain://internet/github.com", "find-internet-password"},
	}

	for _, testCase := range cases {
		t.Run(testCase.command, func(t *testing.T) {
			runner := &recordingRunner{output: []byte(sentinel.SentinelToken + "\n")}

			value, err := onMac(runner).Fetch(t.Context(), reference(testCase.itemRef, "work"))
			if err != nil {
				t.Fatalf("取用失败：%v", err)
			}
			defer value.Zero()

			if string(value.Reveal()) != sentinel.SentinelToken {
				t.Errorf("取到的值不对")
			}
			expected := []string{testCase.command, "-s", "github.com", "-a", "work", "-w"}
			if !reflect.DeepEqual(runner.args, expected) {
				t.Fatalf("调用参数为 %v，期望 %v", runner.args, expected)
			}
		})
	}
}

func TestFetch_WithoutTheWFlag_WouldLeakItemAttributes(t *testing.T) {
	// -w 让 security 只打印密码本身。没有它，输出里会带上创建时间、
	// 访问组等元信息，而那些不该进本进程的内存。
	runner := &recordingRunner{output: []byte(sentinel.SentinelToken)}

	value, err := onMac(runner).Fetch(t.Context(), reference("keychain://generic/github.com", "work"))
	if err != nil {
		t.Fatalf("取用失败：%v", err)
	}
	defer value.Zero()

	if runner.args[len(runner.args)-1] != "-w" {
		t.Errorf("调用参数为 %v，最后一项期望是 -w", runner.args)
	}
	for _, banned := range []string{"-g", "dump-keychain", "list-keychains"} {
		for _, arg := range runner.args {
			if arg == banned {
				t.Errorf("调用参数里出现了 %q，那会读回比一条密码更多的东西", banned)
			}
		}
	}
}

func TestFetch_ReferenceThatWouldWidenTheQuery_IsRefused(t *testing.T) {
	// security 的 -s 支持模糊匹配，一个带通配符的服务名会命中一批条目。
	cases := []struct {
		name    string
		itemRef string
		account string
	}{
		{"不是 keychain:// 地址", "generic/github.com", "work"},
		{"少一段", "keychain://generic", "work"},
		{"种类认不出来", "keychain://password/github.com", "work"},
		{"服务名带通配符", "keychain://generic/*.com", "work"},
		{"账户名带通配符", "keychain://generic/github.com", "wo?k"},
		{"服务名为空", "keychain://generic/", "work"},
		{"账户名为空", "keychain://generic/github.com", ""},
		{"服务名带换行", "keychain://generic/github.com\ndump", "work"},
		{"服务名以短横开头", "keychain://generic/-g", "work"},
		{"账户名以短横开头", "keychain://generic/github.com", "-g"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"时拒绝", func(t *testing.T) {
			runner := &recordingRunner{output: []byte(sentinel.SentinelToken)}

			_, err := onMac(runner).Fetch(
				t.Context(), reference(testCase.itemRef, testCase.account))
			assertCode(t, err, apperr.CodeCredentialNotAuthorized)
			if runner.calls != 0 {
				t.Errorf("被拒绝的引用仍然调用了 security %d 次", runner.calls)
			}
		})
	}
}

func TestFetch_EmptyPassword_IsRefused(t *testing.T) {
	for _, output := range [][]byte{nil, []byte(""), []byte("\n")} {
		runner := &recordingRunner{output: output}

		_, err := onMac(runner).Fetch(t.Context(), reference("keychain://generic/github.com", "work"))
		assertCode(t, err, apperr.CodeCredentialNotAuthorized)
	}
}

// ——— 不可用与超时 ———

func TestFetch_WhenSecurityIsMissing_IsUnavailable(t *testing.T) {
	runner := &recordingRunner{err: exec.ErrNotFound}

	_, err := onMac(runner).Fetch(t.Context(), reference("keychain://generic/github.com", "work"))
	assertCode(t, err, apperr.CodeProviderUnavailable)
}

func TestFetch_WhenTheItemIsMissingOrAccessDenied_IsUnavailable(t *testing.T) {
	// 条目不存在、用户拒绝授权、钥匙串已锁都落在这里：
	// 对调用方来说后果一样 —— 取不到凭据，请求必须被拒。
	runner := &recordingRunner{err: errors.New("The specified item could not be found")}

	_, err := onMac(runner).Fetch(t.Context(), reference("keychain://generic/github.com", "work"))
	assertCode(t, err, apperr.CodeProviderUnavailable)
}

func TestFetch_WhenTouchIDIsNeverConfirmed_TimesOut(t *testing.T) {
	source := keychain.New(keychain.Options{
		Runner: blockingRunner{}, GOOS: "darwin", Timeout: 20 * time.Millisecond,
	})

	_, err := source.Fetch(t.Context(), reference("keychain://generic/github.com", "work"))
	assertCode(t, err, apperr.CodeProviderLockedTimeout)
}

type blockingRunner struct{}

func (blockingRunner) Run(ctx context.Context, _ string, _ []string) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestAvailable_OnMacOSProbesWithoutReadingAnyItem(t *testing.T) {
	runner := &recordingRunner{output: []byte("Usage: security ...")}

	if err := onMac(runner).Available(t.Context()); err != nil {
		t.Fatalf("探测失败：%v", err)
	}
	if !reflect.DeepEqual(runner.args, []string{"help"}) {
		t.Errorf("探测参数为 %v，期望 [help]", runner.args)
	}
}

func TestDefaults_AreTheDocumentedOnes(t *testing.T) {
	// 绝对路径而不是名字：从 PATH 里找会让一个同名的、放在 PATH 前面的
	// 文件顶替掉系统自带的 security。
	if keychain.DefaultBinary != "/usr/bin/security" {
		t.Errorf("默认可执行文件为 %q，期望 /usr/bin/security", keychain.DefaultBinary)
	}
	if keychain.DefaultTimeout != 30*time.Second {
		t.Errorf("默认超时为 %v，期望 30 秒", keychain.DefaultTimeout)
	}
}

func TestSource_Kind_IsMacOSKeychain(t *testing.T) {
	if got := keychain.New(keychain.Options{}).Kind(); got != registry.KindMacOSKeychain {
		t.Errorf("种类为 %s，期望 macos-keychain", got)
	}
}

// ——— 参数数组、不经 shell ———

func TestExecRunner_PassesArgumentsAsAnArrayNotThroughAShell(t *testing.T) {
	// 假 security 把收到的参数逐行打印出来。经过 shell 的话，
	// 带分号与反引号的服务名会被拆开或求值。
	if runtime.GOOS == "windows" {
		t.Skip("假 security 用的是 POSIX shell 脚本，Windows 上不适用")
	}

	script := filepath.Join(t.TempDir(), "security")
	body := "#!/bin/sh\nfor arg in \"$@\"; do printf '%s\\n' \"$arg\"; done\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("写假 security 失败：%v", err)
	}

	source := keychain.New(keychain.Options{Binary: script, GOOS: "darwin"})
	value, err := source.Fetch(t.Context(),
		reference("keychain://generic/github.com; rm -rf $HOME `id`", "work"))
	if err != nil {
		t.Fatalf("调用假 security 失败：%v", err)
	}
	defer value.Zero()

	lines := strings.Split(strings.TrimRight(string(value.Reveal()), "\n"), "\n")
	expected := []string{
		"find-generic-password", "-s", "github.com; rm -rf $HOME `id`", "-a", "work", "-w",
	}
	if !reflect.DeepEqual(lines, expected) {
		t.Fatalf("假 security 收到的参数为 %q，期望 %q", lines, expected)
	}
}

func TestExecRunner_DoesNotInheritTheProcessEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("假 security 用的是 POSIX shell 脚本，Windows 上不适用")
	}

	script := filepath.Join(t.TempDir(), "security")
	body := "#!/bin/sh\nprintf '%s' \"${OPENDELO_PROBE:-absent}\"\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("写假 security 失败：%v", err)
	}
	t.Setenv("OPENDELO_PROBE", sentinel.SentinelToken)

	source := keychain.New(keychain.Options{Binary: script, GOOS: "darwin"})
	value, err := source.Fetch(t.Context(), reference("keychain://generic/github.com", "work"))
	if err != nil {
		t.Fatalf("调用假 security 失败：%v", err)
	}
	defer value.Zero()

	if got := string(value.Reveal()); got != "absent" {
		t.Errorf("假 security 看到的环境变量为 %q，期望它看不到本进程的环境", got)
	}
}

func TestExecRunner_MissingBinary_IsUnavailable(t *testing.T) {
	source := keychain.New(keychain.Options{
		Binary: filepath.Join(t.TempDir(), "definitely-not-security"), GOOS: "darwin",
	})

	_, err := source.Fetch(t.Context(), reference("keychain://generic/github.com", "work"))
	assertCode(t, err, apperr.CodeProviderUnavailable)
}

func TestExecRunner_BinaryWritableByOtherUsers_IsRefusedWithoutRunningIt(t *testing.T) {
	// 默认值是系统自带的绝对路径，但 Binary 可被配置覆盖。覆盖进来的值同样要过这一关，
	// 而拒绝必须发生在执行之前 —— 跑起来就已经晚了。
	if runtime.GOOS == "windows" {
		t.Skip("Windows 用 ACL 而不是 Unix 权限位，本用例的前提不成立")
	}

	directory := t.TempDir()
	marker := filepath.Join(directory, "it-ran")
	script := filepath.Join(directory, "security")
	body := "#!/bin/sh\ntouch " + marker + "\nprintf '%s' 'stolen'\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("写假 security 失败：%v", err)
	}
	// 先写再 Chmod：os.WriteFile 的权限会被 umask 削掉。
	if err := os.Chmod(script, 0o777); err != nil {
		t.Fatalf("设置权限失败：%v", err)
	}

	source := keychain.New(keychain.Options{Binary: script, GOOS: "darwin"})

	_, err := source.Fetch(t.Context(), reference("keychain://generic/github.com", "work"))
	assertCode(t, err, apperr.CodeProviderUnavailable)
	// 拒绝理由要保留下来：一句通用的「调用失败」会把用户引去查钥匙串是否上锁，
	// 而真正要改的是那个文件的权限。
	if !strings.Contains(err.Error(), "可被其他用户改写") {
		t.Errorf("拒绝理由为 %q，期望是权限检查给出的那一条", err)
	}

	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("可被其他用户改写的 security 仍然被执行了")
	}
}
