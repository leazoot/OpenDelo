package arch_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

/*
 * SQL 只能来自 .sql 文件（`.claude/rules/database.md` §7.3、
 * `.claude/rules/security.md` §7）。
 *
 * 参数化查询由 sqlc 生成，仓储只调用生成出来的方法。这条检查守的是
 * 「没有人在 Go 字符串里现拼一句 SQL」—— 一处拼接就是一处注入面，
 * 而它看起来和别的字符串没有区别。
 *
 * 扫的是仓储与 store 自己写的代码，不含 sqlc 产物（那些本来就全是常量字符串）。
 */

// sqlKeywords 是拼接出来的 SQL 最可能带上的关键字。
//
// 只挑动词开头的：`WHERE` 这类会出现在注释里，而 `SELECT ` / `INSERT INTO `
// 这样的组合在非 SQL 的字符串里几乎不会出现。
var sqlKeywords = []string{
	"select ", "insert into", "update ", "delete from", "create table", "drop table",
}

// hand-written SQL 允许存在的位置：PRAGMA 与迁移由 store 自己给出，
// 它们不含任何来自外部的值。
var sqlAllowedFiles = map[string]bool{
	"internal/store/pragma.go": true,
	"internal/store/dsn.go":    true,
}

func TestStore_BuildsNoSQLFromStrings(t *testing.T) {
	root := repoRoot(t)

	scanned := 0
	for _, subtree := range []string{"internal/store"} {
		walkErr := filepath.WalkDir(filepath.Join(root, subtree),
			func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() || !strings.HasSuffix(path, ".go") {
					return nil
				}
				relative, relErr := filepath.Rel(root, path)
				if relErr != nil {
					return relErr
				}
				// sqlc 的产物是常量字符串，不参与拼接；用例本身也不受这条约束。
				if strings.HasSuffix(path, ".sql.go") ||
					strings.HasSuffix(path, "_test.go") ||
					sqlAllowedFiles[filepath.ToSlash(relative)] {
					return nil
				}
				scanned++
				return checkNoConcatenatedSQL(t, path, filepath.ToSlash(relative))
			})
		if walkErr != nil {
			t.Fatalf("扫描 %s 失败：%v", subtree, walkErr)
		}
	}

	if scanned == 0 {
		t.Fatal("一个文件都没扫到 —— 这条检查是空跑的")
	}
}

// checkNoConcatenatedSQL 在一个文件里找「被加号连起来的 SQL 字符串」。
func checkNoConcatenatedSQL(t *testing.T, path, relative string) error {
	t.Helper()

	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return err
	}

	ast.Inspect(parsed, func(node ast.Node) bool {
		binary, isBinary := node.(*ast.BinaryExpr)
		if !isBinary || binary.Op != token.ADD {
			return true
		}
		if !looksLikeSQL(binary.X) && !looksLikeSQL(binary.Y) {
			return true
		}
		t.Errorf("%s:%d 用字符串拼出了 SQL —— 查询只能来自 .sql 文件",
			relative, fset.Position(binary.Pos()).Line)
		return true
	})
	return nil
}

func looksLikeSQL(expr ast.Expr) bool {
	literal, isLiteral := expr.(*ast.BasicLit)
	if !isLiteral || literal.Kind != token.STRING {
		return false
	}

	lowered := strings.ToLower(literal.Value)
	for _, keyword := range sqlKeywords {
		if strings.Contains(lowered, keyword) {
			return true
		}
	}
	return false
}
