package arch_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

/*
 * secret.Value 的可见范围检查（ADR-002）。
 *
 * 规则原文是「只允许出现在 credential 与 adapter 两个包的签名中」。这里检查的是
 * 导入关系而非签名：一个包哪怕只在内部持有明文而不出现在签名里，也已经把明文
 * 带出了允许范围。导入级检查是规则的严格超集，且无法被别名或类型转换绕过。
 *
 * 只扫描会被编译进二进制的生产代码：_test.go 被排除，因为证明脱敏生效的用例
 * 本身必须能构造出含明文的 Value。
 */

const secretPackageDir = "internal/platform/secret"

// 允许持有凭据明文的目录。凭据取用在 credential，注入在 adapter，
// 定义在 secret 自身，此外没有第四处。
var secretAllowedDirs = []string{
	secretPackageDir,
	"internal/credential",
	"internal/adapter",
}

func TestSecretValue_OutsideCredentialAndAdapter_NotImported(t *testing.T) {
	root := repoRoot(t)
	module := modulePath(t, root)

	found, scanned, err := scanSecretImports(root, module, productionRoots)
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if scanned == 0 {
		t.Fatal("没有扫描到任何 Go 文件，检查等于没做")
	}

	for _, imported := range found {
		t.Errorf(
			"%s:%d 导入了 %s；明文只允许出现在 %s",
			imported.file, imported.line, secretPackageDir, strings.Join(secretAllowedDirs, "、"),
		)
	}
	t.Logf("已扫描 %d 个生产代码文件，越界导入 %d 处", scanned, len(found))
}

func TestScanSecretImports_ImportOutsideAllowedDirs_IsReported(t *testing.T) {
	// 上面的用例在仓库里恒为零违规，无法证明扫描器真的会报。这里用一棵
	// 临时目录树做正向对照：允许目录、越界目录、测试文件各一个。
	const module = "example.com/probe"

	root := t.TempDir()
	writeGoFile(t, root, "internal/adapter/github/client.go", module+"/"+secretPackageDir)
	writeGoFile(t, root, "internal/credential/onepassword/provider.go", module+"/"+secretPackageDir)
	writeGoFile(t, root, "internal/core/decision/decide_test.go", module+"/"+secretPackageDir)
	writeGoFile(t, root, "internal/transport/httpapi/handler.go", module+"/"+secretPackageDir)
	writeGoFile(t, root, "cmd/opendelo/main.go", "fmt")

	found, scanned, err := scanSecretImports(root, module, productionRoots)
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if scanned != 4 {
		t.Errorf("扫描到 %d 个生产代码文件，期望 4（_test.go 不计入）", scanned)
	}
	if len(found) != 1 {
		t.Fatalf("报告了 %d 处越界导入，期望 1：%+v", len(found), found)
	}
	if want := "internal/transport/httpapi"; found[0].dir != want {
		t.Errorf("越界导入位于 %s，期望 %s", found[0].dir, want)
	}
}

// scanSecretImports 返回 roots 下所有导入了 secret 包却不在允许目录内的生产代码文件，
// 以及实际扫描过的文件数（用于识别「什么都没扫到」的空跑）。
func scanSecretImports(root, module string, roots []string) ([]importSite, int, error) {
	secretPath := module + "/" + secretPackageDir

	return scanImports(root, roots, importRule{
		matches:     func(importPath string) bool { return importPath == secretPath },
		allowedDirs: secretAllowedDirs,
	})
}

func repoRoot(t *testing.T) string {
	t.Helper()

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("解析仓库根目录失败：%v", err)
	}
	return root
}

func modulePath(t *testing.T, root string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("读取 go.mod 失败：%v", err)
	}
	for _, line := range strings.Split(string(content), "\n") {
		if after, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(after)
		}
	}

	t.Fatal("go.mod 中没有 module 声明")
	return ""
}

func writeGoFile(t *testing.T, root, relative, importPath string) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("创建目录失败：%v", err)
	}

	source := "package " + filepath.Base(filepath.Dir(relative)) + "\n\nimport " + strconv.Quote(importPath) + "\n"
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatalf("写入 %s 失败：%v", relative, err)
	}
}
