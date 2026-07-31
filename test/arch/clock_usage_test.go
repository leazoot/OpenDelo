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
 * 时间来源检查。
 *
 * 决策链路里的时间就是安全边界：Lease 何时过期、审批何时超时、Trust Memory 何时失效。
 * 直接调用 time.Now 会让这些分支只能靠 sleep 来测，既慢又不稳。时间一律从
 * platform/clock.Clock 注入。
 *
 * Since 与 Until 一并禁止 —— 它们内部就是 time.Now。
 */

// clockScannedRoots 是强制注入时钟的范围。
// AC3 要求的是 internal/core；等其余包也接入时钟后可以逐步扩大。
var clockScannedRoots = []string{"internal/core"}

// clockExemptDirs 是允许直接读真实时间的目录 —— 只有 System 实现本身。
var clockExemptDirs = []string{"internal/platform/clock"}

var bannedTimeFunctions = map[string]bool{"Now": true, "Since": true, "Until": true}

type timeCall struct {
	file     string
	line     int
	function string
}

func TestCore_DoesNotReadWallClockDirectly(t *testing.T) {
	root := repoRoot(t)

	found, scanned, err := scanDirectTimeCalls(root, clockScannedRoots)
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}

	for _, call := range found {
		t.Errorf("%s:%d 直接调用了 time.%s；时间应从 platform/clock.Clock 注入", call.file, call.line, call.function)
	}
	t.Logf("已扫描 %s 下 %d 个生产代码文件，直接读时钟 %d 处", strings.Join(clockScannedRoots, "、"), scanned, len(found))
}

func TestScanDirectTimeCalls_DetectsEveryBannedFunction(t *testing.T) {
	// internal/core 目前只有 doc.go，仓库扫描恒为零违规。用临时目录树做正向对照，
	// 确认三个函数都会被抓到、别名导入躲不掉、豁免目录确实豁免。
	root := t.TempDir()

	writeGoSource(t, root, "internal/core/decision/decide.go", `
package decision

import "time"

func expired(deadline time.Time) bool { return time.Now().After(deadline) }
`)
	writeGoSource(t, root, "internal/core/lease/lease.go", `
package lease

import stdtime "time"

func elapsed(start stdtime.Time) stdtime.Duration { return stdtime.Since(start) }
func remaining(deadline stdtime.Time) stdtime.Duration { return stdtime.Until(deadline) }
`)
	writeGoSource(t, root, "internal/core/scope/scope.go", `
package scope

import "time"

func window() time.Duration { return 15 * time.Minute }
`)
	writeGoSource(t, root, "internal/core/risk/risk_test.go", `
package risk

import "time"

func helper() time.Time { return time.Now() }
`)

	found, scanned, err := scanDirectTimeCalls(root, clockScannedRoots)
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if scanned != 3 {
		t.Errorf("扫描到 %d 个生产代码文件，期望 3（_test.go 不计入）", scanned)
	}

	byFunction := make(map[string]int, len(found))
	for _, call := range found {
		byFunction[call.function]++
	}
	for function := range bannedTimeFunctions {
		if byFunction[function] != 1 {
			t.Errorf("time.%s 被报告了 %d 次，期望 1 次：%+v", function, byFunction[function], found)
		}
	}
	if len(found) != len(bannedTimeFunctions) {
		t.Errorf("共报告 %d 处，期望 %d 处：%+v", len(found), len(bannedTimeFunctions), found)
	}
}

func TestScanDirectTimeCalls_ExemptsClockPackageItself(t *testing.T) {
	root := t.TempDir()
	writeGoSource(t, root, "internal/platform/clock/clock.go", `
package clock

import "time"

func now() time.Time { return time.Now() }
`)

	found, _, err := scanDirectTimeCalls(root, clockExemptDirs)
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if len(found) != 0 {
		t.Errorf("clock 包自身被报告了 %d 处：%+v", len(found), found)
	}
}

// scanDirectTimeCalls 返回 roots 下所有直接调用 time.Now / Since / Until 的生产代码位置。
func scanDirectTimeCalls(root string, roots []string) (found []timeCall, scanned int, err error) {
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

			relative, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			relative = filepath.ToSlash(relative)
			if isClockExemptDir(filepath.ToSlash(filepath.Dir(relative))) {
				return nil
			}
			scanned++

			parsed, parseErr := parser.ParseFile(fset, path, nil, 0)
			if parseErr != nil {
				return parseErr
			}

			timePackage, imported := localNameOfTimeImport(parsed)
			if !imported {
				return nil
			}

			ast.Inspect(parsed, func(node ast.Node) bool {
				selector, isSelector := node.(*ast.SelectorExpr)
				if !isSelector {
					return true
				}
				pkg, isIdent := selector.X.(*ast.Ident)
				if !isIdent || pkg.Name != timePackage || !bannedTimeFunctions[selector.Sel.Name] {
					return true
				}
				found = append(found, timeCall{
					file:     relative,
					line:     fset.Position(selector.Pos()).Line,
					function: selector.Sel.Name,
				})
				return true
			})
			return nil
		})
		if walkErr != nil {
			return nil, scanned, walkErr
		}
	}

	return found, scanned, nil
}

// localNameOfTimeImport 返回 time 包在该文件中的名字，别名导入同样能被认出。
func localNameOfTimeImport(file *ast.File) (string, bool) {
	for _, spec := range file.Imports {
		if spec.Path.Value != `"time"` {
			continue
		}
		if spec.Name != nil {
			return spec.Name.Name, true
		}
		return "time", true
	}
	return "", false
}

func isClockExemptDir(dir string) bool {
	for _, exempt := range clockExemptDirs {
		if dir == exempt || strings.HasPrefix(dir, exempt+"/") {
			return true
		}
	}
	return false
}

func writeGoSource(t *testing.T, root, relative, source string) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("创建目录失败：%v", err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimLeft(source, "\n")), 0o600); err != nil {
		t.Fatalf("写入 %s 失败：%v", relative, err)
	}
}
