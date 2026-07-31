package onepassword_test

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

	"github.com/Runcoor/opendelo/internal/credential/onepassword"
	"github.com/Runcoor/opendelo/internal/credential/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * 1Password Provider 的行为用例（REQ-CRED-002）。
 *
 * **用例绝不调用真实的 op，也不碰用户的保险库**。
 * 行为用真实实现 + 可控的 Runner 验证；「参数数组、不经 shell」这条
 * 由一个写在临时目录里的假 op 脚本端到端验证。
 */

// recordingRunner 记下被调用的参数并返回预设结果。
type recordingRunner struct {
	binary string
	args   []string
	calls  int
	output []byte
	err    error
}

func (r *recordingRunner) Run(_ context.Context, binary string, args []string) ([]byte, error) {
	r.calls++
	r.binary = binary
	r.args = append([]string(nil), args...)
	if r.err != nil {
		return nil, r.err
	}
	return append([]byte(nil), r.output...), nil
}

func reference() registry.Reference {
	return registry.Reference{
		ItemRef: "op://Personal/GitHub Work",
		Field:   "token",
	}
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

// ——— 只读用户显式选择的那一个字段（AC1）———

func TestFetch_ReadsExactlyTheSelectedField(t *testing.T) {
	runner := &recordingRunner{output: []byte(sentinel.SentinelToken + "\n")}
	source := onepassword.New(onepassword.Options{Runner: runner})

	value, err := source.Fetch(t.Context(), reference())
	if err != nil {
		t.Fatalf("取用失败：%v", err)
	}
	defer value.Zero()

	if string(value.Reveal()) != sentinel.SentinelToken {
		t.Errorf("取到的值不对")
	}

	expected := []string{"read", "--no-newline", "op://Personal/GitHub Work/token"}
	if !reflect.DeepEqual(runner.args, expected) {
		t.Fatalf("调用参数为 %v，期望 %v", runner.args, expected)
	}
	// 一次调用只读一个字段：没有 `op item get`，也没有任何列举子命令。
	for _, banned := range []string{"item", "list", "get", "export", "vault", "--format"} {
		for _, arg := range runner.args {
			if arg == banned {
				t.Errorf("调用参数里出现了 %q，那会读回比一个字段更多的东西", banned)
			}
		}
	}
}

func TestFetch_ReferenceThatWouldReadMoreThanOneField_IsRefused(t *testing.T) {
	// 校验的目的不是「拼得对」，而是拼不出一个比用户选的更宽的地址。
	cases := []struct {
		name      string
		itemRef   string
		field     string
		expectRun bool
	}{
		{"不是 op:// 地址", "Personal/GitHub Work", "token", false},
		{"少一段（指向整个保险库）", "op://Personal", "token", false},
		{"多一段", "op://Personal/GitHub Work/extra", "token", false},
		{"保险库名带通配符", "op://*/GitHub Work", "token", false},
		{"条目名带通配符", "op://Personal/GitHub?", "token", false},
		{"字段名带通配符", "op://Personal/GitHub Work", "*", false},
		{"字段名带斜杠", "op://Personal/GitHub Work", "token/extra", false},
		{"保险库名为空", "op:///GitHub Work", "token", false},
		{"字段名为空", "op://Personal/GitHub Work", "", false},
		{"条目名是上级目录", "op://Personal/..", "token", false},
		{"字段名是上级目录", "op://Personal/GitHub Work", "..", false},
		{"带换行", "op://Personal/GitHub Work", "token\nfield", false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"时拒绝", func(t *testing.T) {
			runner := &recordingRunner{output: []byte(sentinel.SentinelToken)}
			source := onepassword.New(onepassword.Options{Runner: runner})

			_, err := source.Fetch(t.Context(), registry.Reference{
				ItemRef: testCase.itemRef, Field: testCase.field,
			})
			assertCode(t, err, apperr.CodeCredentialNotAuthorized)
			if runner.calls != 0 {
				t.Errorf("被拒绝的引用仍然调用了 op %d 次", runner.calls)
			}
		})
	}
}

func TestFetch_EmptyOutput_IsRefused(t *testing.T) {
	// 空值不是凭据。返回一个空的 secret.Value 会让调用方以为
	// 「这个字段本来就是空的」，然后拿着它去请求外部服务。
	for _, output := range [][]byte{nil, []byte(""), []byte("\n"), []byte("\r\n")} {
		runner := &recordingRunner{output: output}
		source := onepassword.New(onepassword.Options{Runner: runner})

		_, err := source.Fetch(t.Context(), reference())
		assertCode(t, err, apperr.CodeCredentialNotAuthorized)
	}
}

// ——— CLI 不可用（AC3）———

func TestAvailable_ProbesWithoutTouchingAnyItem(t *testing.T) {
	// 一次健康探测不该在用户屏幕上弹出授权窗口，所以只问版本。
	runner := &recordingRunner{output: []byte("2.30.0")}
	source := onepassword.New(onepassword.Options{Runner: runner})

	if err := source.Available(t.Context()); err != nil {
		t.Fatalf("探测失败：%v", err)
	}
	if !reflect.DeepEqual(runner.args, []string{"--version"}) {
		t.Errorf("探测参数为 %v，期望 [--version]", runner.args)
	}
}

func TestFetch_WhenTheCLIIsMissing_IsUnavailable(t *testing.T) {
	// AC3：op 未安装时返回 provider_unavailable，请求被拒而不是放行。
	runner := &recordingRunner{err: exec.ErrNotFound}
	source := onepassword.New(onepassword.Options{Runner: runner})

	_, err := source.Fetch(t.Context(), reference())
	assertCode(t, err, apperr.CodeProviderUnavailable)

	assertCode(t, source.Available(t.Context()), apperr.CodeProviderUnavailable)
}

func TestFetch_WhenTheCLIFails_IsUnavailable(t *testing.T) {
	// 未登录、Service Account Token 失效、条目不存在都落在这里：
	// 对调用方来说后果一样 —— 取不到凭据，请求必须被拒。
	runner := &recordingRunner{err: errors.New("[ERROR] you are not currently signed in")}
	source := onepassword.New(onepassword.Options{Runner: runner})

	_, err := source.Fetch(t.Context(), reference())
	assertCode(t, err, apperr.CodeProviderUnavailable)
}

func TestFetch_WhenTheUserNeverConfirms_TimesOut(t *testing.T) {
	// 桌面授权模式下 op 会等用户点确认。等得有上限 ——
	// 一次永远等下去的取用会把整条请求挂住。
	source := onepassword.New(onepassword.Options{
		Runner:  blockingRunner{},
		Timeout: 20 * time.Millisecond,
	})

	_, err := source.Fetch(t.Context(), reference())
	assertCode(t, err, apperr.CodeProviderLockedTimeout)
}

// blockingRunner 一直等到 context 被取消。
type blockingRunner struct{}

func (blockingRunner) Run(ctx context.Context, _ string, _ []string) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestDefaults_AreTheDocumentedOnes(t *testing.T) {
	// 一次永远等下去的取用会把整条请求挂住，所以上限不能是「很久」。
	if onepassword.DefaultTimeout != 30*time.Second {
		t.Errorf("默认超时为 %v，期望 30 秒", onepassword.DefaultTimeout)
	}
	if onepassword.DefaultBinary != "op" {
		t.Errorf("默认可执行文件为 %q，期望 op", onepassword.DefaultBinary)
	}
}

func TestSource_Kind_IsOnePassword(t *testing.T) {
	if got := onepassword.New(onepassword.Options{}).Kind(); got != registry.KindOnePassword {
		t.Errorf("种类为 %s，期望 1password", got)
	}
}

// ——— 参数数组、不经 shell———

func TestExecRunner_PassesArgumentsAsAnArrayNotThroughAShell(t *testing.T) {
	// 用一个写在临时目录里的假 op 端到端验证真实的 exec 路径。
	// 假 op 把收到的参数逐行打印出来 —— 若中间经过了 shell，
	// 带分号与反引号的条目名会被拆开或求值，打印出来的就不是原样。
	if runtime.GOOS == "windows" {
		t.Skip("假 op 用的是 POSIX shell 脚本，Windows 上不适用")
	}

	directory := t.TempDir()
	script := filepath.Join(directory, "op")
	body := "#!/bin/sh\nfor arg in \"$@\"; do printf '%s\\n' \"$arg\"; done\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("写假 op 失败：%v", err)
	}

	// 条目名里塞进 shell 元字符：经过 shell 的话它们会被解释。
	hostile := registry.Reference{
		ItemRef: "op://Personal/GitHub; rm -rf $HOME `id`",
		Field:   "token",
	}
	source := onepassword.New(onepassword.Options{Binary: script})

	value, err := source.Fetch(t.Context(), hostile)
	if err != nil {
		t.Fatalf("调用假 op 失败：%v", err)
	}
	defer value.Zero()

	lines := strings.Split(strings.TrimRight(string(value.Reveal()), "\n"), "\n")
	expected := []string{
		"read", "--no-newline",
		"op://Personal/GitHub; rm -rf $HOME `id`/token",
	}
	if !reflect.DeepEqual(lines, expected) {
		t.Fatalf("假 op 收到的参数为 %q，期望 %q", lines, expected)
	}
}

func TestExecRunner_MissingBinary_IsUnavailable(t *testing.T) {
	// 真实 exec 路径下找不到可执行文件同样落在 provider_unavailable。
	source := onepassword.New(onepassword.Options{
		Binary: filepath.Join(t.TempDir(), "definitely-not-op"),
	})

	_, err := source.Fetch(t.Context(), reference())
	assertCode(t, err, apperr.CodeProviderUnavailable)
}

func TestExecRunner_DoesNotInheritTheProcessEnvironment(t *testing.T) {
	// 本进程的环境里可能有别的服务的令牌，没有理由传给 op。
	if runtime.GOOS == "windows" {
		t.Skip("假 op 用的是 POSIX shell 脚本，Windows 上不适用")
	}

	directory := t.TempDir()
	script := filepath.Join(directory, "op")
	body := "#!/bin/sh\nprintf '%s' \"${OPENDELO_PROBE:-absent}\"\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("写假 op 失败：%v", err)
	}
	t.Setenv("OPENDELO_PROBE", sentinel.SentinelToken)

	source := onepassword.New(onepassword.Options{Binary: script})
	value, err := source.Fetch(t.Context(), reference())
	if err != nil {
		t.Fatalf("调用假 op 失败：%v", err)
	}
	defer value.Zero()

	if got := string(value.Reveal()); got != "absent" {
		t.Errorf("假 op 看到的环境变量为 %q，期望它看不到本进程的环境", got)
	}
}

func TestExecRunner_BinaryWritableByOtherUsers_IsRefusedWithoutRunningIt(t *testing.T) {
	// Agent 有文件系统访问权，能往 PATH 里放一个同名的 op 顶替掉真的 op。
	// 顶替成功就等于把明文凭据交到 Agent 手里，所以拒绝必须发生在执行之前。
	if runtime.GOOS == "windows" {
		t.Skip("Windows 用 ACL 而不是 Unix 权限位，本用例的前提不成立")
	}

	directory := t.TempDir()
	marker := filepath.Join(directory, "it-ran")
	script := filepath.Join(directory, "op")
	body := "#!/bin/sh\ntouch " + marker + "\nprintf '%s' 'stolen'\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("写假 op 失败：%v", err)
	}
	// 先写再 Chmod：os.WriteFile 的权限会被 umask 削掉。
	if err := os.Chmod(script, 0o777); err != nil {
		t.Fatalf("设置权限失败：%v", err)
	}

	source := onepassword.New(onepassword.Options{Binary: script})

	_, err := source.Fetch(t.Context(), reference())
	assertCode(t, err, apperr.CodeProviderUnavailable)
	// 拒绝理由要保留下来：一句通用的「调用失败」会让用户去查 1Password 的登录状态，
	// 而真正要改的是那个文件的权限。
	if !strings.Contains(err.Error(), "可被其他用户改写") {
		t.Errorf("拒绝理由为 %q，期望是权限检查给出的那一条", err)
	}

	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("可被其他用户改写的 op 仍然被执行了")
	}
}

func TestExecRunner_ResolvesTheBareDefaultNameFromPATH(t *testing.T) {
	// 默认值 "op" 走 PATH 查找，这条路径必须仍然能跑通 ——
	// 校验的目的是定死一个绝对路径，不是让默认安装用不了。
	if runtime.GOOS == "windows" {
		t.Skip("假 op 用的是 POSIX shell 脚本，Windows 上不适用")
	}

	directory := t.TempDir()
	script := filepath.Join(directory, "op")
	body := "#!/bin/sh\nprintf '%s' \"$0\"\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("写假 op 失败：%v", err)
	}
	t.Setenv("PATH", directory)

	source := onepassword.New(onepassword.Options{})
	value, err := source.Fetch(t.Context(), reference())
	if err != nil {
		t.Fatalf("调用假 op 失败：%v", err)
	}
	defer value.Zero()

	if got := string(value.Reveal()); got != script {
		t.Errorf("实际执行的是 %q，期望 PATH 解析出的绝对路径 %q", got, script)
	}
}
