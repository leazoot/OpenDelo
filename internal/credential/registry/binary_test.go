package registry_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/credential/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * ResolveBinary 的行为用例。
 *
 * 守的是一条真实存在的路径：Agent 有文件系统访问权，能往 PATH 里放一个同名文件
 * 顶替掉真的 op —— 顶替成功就等于把明文凭据交到 Agent 手里。
 */

// writeExecutable 写一个可执行文件并把权限定死。
//
// 先写再 Chmod：os.WriteFile 的权限会被 umask 削掉，而这些用例断言的正是
// 「组可写 / 他人可写」这两个位。
func writeExecutable(t *testing.T, directory, name string, mode os.FileMode) string {
	t.Helper()

	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("写可执行文件失败：%v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("设置权限失败：%v", err)
	}
	return path
}

func TestResolveBinary_AbsolutePathOwnerOnly_IsAccepted(t *testing.T) {
	path := writeExecutable(t, t.TempDir(), "op", 0o700)

	resolved, err := registry.ResolveBinary(path)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if resolved != path {
		t.Errorf("解析结果为 %q，期望 %q", resolved, path)
	}
}

func TestResolveBinary_BareName_IsResolvedToAnAbsolutePath(t *testing.T) {
	// 默认值 "op" 是从 PATH 里找的。解析成绝对路径之后，后续的执行不再
	// 每次重新在 PATH 里碰运气。
	directory := t.TempDir()
	path := writeExecutable(t, directory, "op", 0o700)
	t.Setenv("PATH", directory)

	resolved, err := registry.ResolveBinary("op")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if !filepath.IsAbs(resolved) {
		t.Errorf("解析结果 %q 不是绝对路径", resolved)
	}
	if resolved != path {
		t.Errorf("解析结果为 %q，期望 %q", resolved, path)
	}
}

func TestResolveBinary_RelativeName_IsRefused(t *testing.T) {
	// 带斜杠的相对名字（配置里写 "bin/op" 这种）LookPath 会原样返回。
	// 那种路径指向哪个文件取决于进程当时的工作目录 —— 不是一个确定的可执行文件。
	directory := t.TempDir()
	nested := filepath.Join(directory, "bin")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatalf("建目录失败：%v", err)
	}
	writeExecutable(t, nested, "op", 0o700)
	t.Chdir(directory)

	_, err := registry.ResolveBinary(filepath.Join("bin", "op"))

	if !apperr.Is(err, apperr.CodeProviderUnavailable) {
		t.Fatalf("错误码为 %s，期望 provider_unavailable（%v）", apperr.CodeOf(err), err)
	}
	if !strings.Contains(err.Error(), "必须写绝对路径") {
		t.Errorf("拒绝理由为 %q，期望是绝对路径检查给出的那一条", err)
	}
}

func TestResolveBinary_WritableByOtherUsers_IsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 用 ACL 而不是 Unix 权限位，本用例的前提不成立")
	}

	cases := []struct {
		name string
		mode os.FileMode
	}{
		{"组可写", 0o770},
		{"他人可写", 0o707},
		{"人人可写", 0o777},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"的可执行文件被拒绝", func(t *testing.T) {
			path := writeExecutable(t, t.TempDir(), "op", testCase.mode)

			_, err := registry.ResolveBinary(path)
			if !apperr.Is(err, apperr.CodeProviderUnavailable) {
				t.Fatalf("错误码为 %s，期望 provider_unavailable（%v）", apperr.CodeOf(err), err)
			}
			if err != nil && !strings.Contains(err.Error(), "可被其他用户改写") {
				t.Errorf("拒绝理由为 %q，期望是权限检查给出的那一条", err)
			}
		})
	}
}

func TestResolveBinary_Directory_IsRefused(t *testing.T) {
	// 一个名叫 op 的目录不该被当成 op。这条由 LookPath 保证，用例把它钉住。
	directory := t.TempDir()
	nested := filepath.Join(directory, "op")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatalf("建目录失败：%v", err)
	}

	_, err := registry.ResolveBinary(nested)
	if !apperr.Is(err, apperr.CodeProviderUnavailable) {
		t.Fatalf("错误码为 %s，期望 provider_unavailable（%v）", apperr.CodeOf(err), err)
	}
}

func TestResolveBinary_Missing_IsUnavailable(t *testing.T) {
	// 两种写法都要落在同一个结论上：取不到凭据，请求必须被拒。
	directory := t.TempDir()
	t.Setenv("PATH", directory)

	for _, name := range []string{"definitely-not-op", filepath.Join(directory, "definitely-not-op")} {
		_, err := registry.ResolveBinary(name)

		if !apperr.Is(err, apperr.CodeProviderUnavailable) {
			t.Fatalf("错误码为 %s，期望 provider_unavailable（%v）", apperr.CodeOf(err), err)
		}
		if !strings.Contains(err.Error(), "找不到可执行文件") {
			t.Errorf("拒绝理由为 %q，期望是查找失败给出的那一条", err)
		}
	}
}

func TestResolveBinary_NotExecutable_IsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不用执行位判断可执行性，本用例的前提不成立")
	}

	path := writeExecutable(t, t.TempDir(), "op", 0o600)

	_, err := registry.ResolveBinary(path)
	if !apperr.Is(err, apperr.CodeProviderUnavailable) {
		t.Fatalf("错误码为 %s，期望 provider_unavailable（%v）", apperr.CodeOf(err), err)
	}
}
