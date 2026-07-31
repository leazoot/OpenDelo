package arch_test

import (
	"strings"
	"testing"
)

/*
 * 数据库驱动的可见范围检查。
 *
 * 规则原文是「只有 internal/store 可以 import 数据库驱动」。这条边界的作用是让
 * 「所有 SQL 都经过 store」成为可核查的事实：驱动一旦能在别处被导入，就有人能
 * 绕开 repository 接口自己开一个连接，参数化查询、PRAGMA、事务纪律随之全部失守。
 *
 * 匹配的是驱动的模块路径而不是某个具体包，换驱动或驱动内部拆包都不会让检查失效。
 */

const storePackageDir = "internal/store"

// databaseDriverModules 是被视为「数据库驱动」的模块前缀。
// 技术栈只允许 modernc.org/sqlite（ADR-006：纯 Go，无 cgo）；另外两个是明确禁止的
// 替代品，列出来是为了它们一旦出现就立刻被报出。
var databaseDriverModules = []string{
	"modernc.org/sqlite",
	"github.com/mattn/go-sqlite3",
	"crawshaw.io/sqlite",
}

// databaseSQLPackage 是标准库的数据库接口。它本身不是驱动，但同样只应出现在 store：
// 别处拿到 *sql.DB 就等于绕过了 repository 接口。
const databaseSQLPackage = "database/sql"

func TestDatabaseDriver_OutsideStore_NotImported(t *testing.T) {
	root := repoRoot(t)

	found, scanned, err := scanImports(root, productionRoots, driverImportRule())
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if scanned == 0 {
		t.Fatal("没有扫描到任何 Go 文件，检查等于没做")
	}

	for _, imported := range found {
		t.Errorf("%s:%d 导入了数据库驱动；持久化只允许经 %s", imported.file, imported.line, storePackageDir)
	}
	t.Logf("已扫描 %d 个生产代码文件，越界导入 %d 处", scanned, len(found))
}

func TestStore_ImportsTheDriver(t *testing.T) {
	// 上面的用例在越界为零时通过，驱动被彻底删掉时同样通过。这里确认驱动确实
	// 还在 store 里被导入着，否则那条检查就成了永远为真的空断言。
	root := repoRoot(t)

	found, _, err := scanImports(root, []string{storePackageDir}, importRule{matches: isDatabaseDriver})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if len(found) == 0 {
		t.Errorf("%s 下没有任何文件导入数据库驱动", storePackageDir)
	}
}

func TestScanImports_DriverImportOutsideStore_IsReported(t *testing.T) {
	// 仓库扫描恒为零违规，无法证明扫描器真的会报。用一棵临时目录树做正向对照：
	// 允许目录、越界目录、被禁驱动、database/sql、测试文件各一个。
	root := t.TempDir()
	writeGoFile(t, root, "internal/store/db.go", "modernc.org/sqlite")
	writeGoFile(t, root, "internal/store/repo/agents.go", databaseSQLPackage)
	writeGoFile(t, root, "internal/core/decision/decide_test.go", "modernc.org/sqlite")
	writeGoFile(t, root, "internal/transport/httpapi/handler.go", databaseSQLPackage)
	writeGoFile(t, root, "internal/credential/onepassword/provider.go", "github.com/mattn/go-sqlite3")
	writeGoFile(t, root, "cmd/opendelo/main.go", "fmt")

	found, scanned, err := scanImports(root, productionRoots, driverImportRule())
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if scanned != 5 {
		t.Errorf("扫描到 %d 个生产代码文件，期望 5（_test.go 不计入）", scanned)
	}
	if len(found) != 2 {
		t.Fatalf("报告了 %d 处越界导入，期望 2：%+v", len(found), found)
	}

	reported := make(map[string]bool, len(found))
	for _, imported := range found {
		reported[imported.dir] = true
	}
	for _, want := range []string{"internal/transport/httpapi", "internal/credential/onepassword"} {
		if !reported[want] {
			t.Errorf("%s 的越界导入未被报告：%+v", want, found)
		}
	}
}

func driverImportRule() importRule {
	return importRule{
		matches: func(importPath string) bool {
			return importPath == databaseSQLPackage || isDatabaseDriver(importPath)
		},
		allowedDirs: []string{storePackageDir},
	}
}

func isDatabaseDriver(importPath string) bool {
	for _, module := range databaseDriverModules {
		if importPath == module || strings.HasPrefix(importPath, module+"/") {
			return true
		}
	}
	return false
}
