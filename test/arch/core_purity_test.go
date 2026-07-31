package arch_test

import (
	"strings"
	"testing"
)

/*
 * core 的纯度检查。
 *
 * `core` 是纯逻辑：不发起网络请求、不读文件、不直接访问数据库，所需数据由调用方
 * 加载后传入（ADR-003）。对 Intent Resolver 这一条尤其要紧 —— 它一旦能发起网络请求
 * 或调外部命令，解析结果就可能被外部内容左右，而本产品从根上排除的正是这条路
 * （提示注入防护）。
 *
 * 用导入关系而不是调用点来判定：一个包只要导入了 net/http，它就已经有能力发请求了，
 * 而导入无法被别名或间接调用绕过。
 */

const coreDir = "internal/core"

// impureImports 是 core 不得导入的包。
//
// net/url 不在其中：它只做字符串解析，不打开任何连接。
var impureImports = map[string]string{
	"net":          "发起网络连接",
	"net/http":     "发起网络请求",
	"os":           "读写文件与环境变量",
	"os/exec":      "调用外部命令",
	"database/sql": "直接访问数据库",
}

func impureImportRule() importRule {
	return importRule{
		matches: func(importPath string) bool {
			_, banned := impureImports[importPath]
			return banned
		},
		// 没有豁免目录：core 里没有任何一处该做这些事。
		allowedDirs: nil,
	}
}

func TestCore_DoesNotReachOutsideItself(t *testing.T) {
	root := repoRoot(t)

	found, scanned, err := scanImports(root, []string{coreDir}, impureImportRule())
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if scanned == 0 {
		t.Fatalf("%s 下没有扫描到任何 Go 文件，检查等于没做", coreDir)
	}

	for _, imported := range found {
		t.Errorf("%s:%d 让 core 具备了 I/O 能力；所需数据应由调用方加载后传入", imported.file, imported.line)
	}
	t.Logf("已扫描 %s 下 %d 个生产代码文件，越界导入 %d 处", coreDir, scanned, len(found))
}

func TestScanImports_ImpureImportInCore_IsReported(t *testing.T) {
	// 仓库扫描恒为零违规，无法证明扫描器真的会报。用临时目录树做正向对照：
	// 五个被禁的包各一个文件，另加一个允许的、一个测试文件。
	root := t.TempDir()

	writeGoFile(t, root, "internal/core/intent/resolver.go", "net/http")
	writeGoFile(t, root, "internal/core/decision/decide.go", "os")
	writeGoFile(t, root, "internal/core/scope/resolve.go", "os/exec")
	writeGoFile(t, root, "internal/core/lease/manager.go", "database/sql")
	writeGoFile(t, root, "internal/core/risk/level.go", "net")
	writeGoFile(t, root, "internal/core/trust/memory.go", "net/url")
	writeGoFile(t, root, "internal/core/intent/resolver_test.go", "net/http")

	found, scanned, err := scanImports(root, []string{coreDir}, impureImportRule())
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if scanned != 6 {
		t.Errorf("扫描到 %d 个生产代码文件，期望 6（_test.go 不计入）", scanned)
	}
	if len(found) != len(impureImports) {
		t.Fatalf("报出 %d 处越界，期望 %d 处（每个被禁的包各一处）", len(found), len(impureImports))
	}

	reported := strings.Join(fileNames(found), " ")
	if strings.Contains(reported, "trust/memory.go") {
		t.Error("net/url 被误报了；它只做字符串解析，不打开任何连接")
	}
	if strings.Contains(reported, "_test.go") {
		t.Error("测试文件被计入了越界")
	}
}

func fileNames(sites []importSite) []string {
	names := make([]string, 0, len(sites))
	for _, site := range sites {
		names = append(names, site.file)
	}
	return names
}
