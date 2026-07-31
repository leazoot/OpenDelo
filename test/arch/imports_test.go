package arch_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

/*
 * 导入关系扫描器，供本包的各条依赖方向检查复用。
 *
 * 只扫描会被编译进二进制的生产代码：_test.go 被排除，因为证明某条边界生效的用例
 * 本身往往必须跨过那条边界。
 */

// productionRoots 是会进入二进制的两棵子树。
var productionRoots = []string{"cmd", "internal"}

// importSite 是一处被报告的导入。
type importSite struct {
	dir  string
	file string
	line int
}

// importRule 描述一条「哪些包不许导入什么」的规则。
type importRule struct {
	// matches 判定一个导入路径是否受本规则管辖。
	matches func(importPath string) bool
	// allowedDirs 是允许导入的目录（含子目录），仓库根的相对路径。
	allowedDirs []string
}

func (r importRule) allows(dir string) bool {
	for _, allowed := range r.allowedDirs {
		if dir == allowed || strings.HasPrefix(dir, allowed+"/") {
			return true
		}
	}
	return false
}

// scanImports 返回 roots 下违反 rule 的导入位置，以及实际扫描过的文件数
// （用于识别「什么都没扫到」的空跑）。
func scanImports(root string, roots []string, rule importRule) (found []importSite, scanned int, err error) {
	fset := token.NewFileSet()

	for _, sub := range roots {
		subRoot := filepath.Join(root, sub)
		if _, statErr := os.Stat(subRoot); os.IsNotExist(statErr) {
			continue
		}

		walkErr := filepath.WalkDir(subRoot, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			scanned++

			relative, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			relative = filepath.ToSlash(relative)
			dir := filepath.ToSlash(filepath.Dir(relative))
			if rule.allows(dir) {
				return nil
			}

			parsed, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if parseErr != nil {
				return parseErr
			}
			for _, spec := range parsed.Imports {
				imported, unquoteErr := strconv.Unquote(spec.Path.Value)
				if unquoteErr != nil {
					return unquoteErr
				}
				if rule.matches(imported) {
					found = append(found, importSite{
						dir:  dir,
						file: relative,
						line: fset.Position(spec.Pos()).Line,
					})
				}
			}
			return nil
		})
		if walkErr != nil {
			return nil, scanned, walkErr
		}
	}

	return found, scanned, nil
}
