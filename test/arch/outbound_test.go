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
 * 出站请求来源检查。
 *
 * 规则原文是「出站请求只能从 adapter 包发出」。这条边界的作用是让端点白名单、
 * 显式超时、禁跨主机重定向、响应大小上限、脱敏这五件事有一个统一的落点：
 * 只要别处能自己发一个 HTTP 请求，这五件事就都可以被绕过。
 *
 * 检查的是 net/http 里**发请求**的那些标识符，而不是 import 本身 ——
 * transport 层要用 net/http 起服务，按 import 判会把它一起误伤。
 */

// outboundAllowedDirs 是允许发出站请求的目录，新增例外必须登记在这里。
//
// internal/adapter 是规则本身；internal/cli/gatewayclient 是唯一已登记的例外 ——
// 它连的是本机回环上的自家 Gateway，请求带的是会话令牌而不是任何外部服务的凭据，
// 且不跟随重定向（见该包的包注释）。
//
// 清单精确到那一个包而不是整个 internal/cli：否则将来 opendelo run 在 CLI 里
// 加一次真正的外部调用，这条检查不会响。
var outboundAllowedDirs = []string{"internal/adapter", "internal/cli/gatewayclient"}

const adapterPackageDir = "internal/adapter"

func outboundAllowed(dir string) bool {
	for _, allowed := range outboundAllowedDirs {
		if dir == allowed || strings.HasPrefix(dir, allowed+"/") {
			return true
		}
	}
	return false
}

// outboundIdentifiers 是 net/http 中会产生出站请求的标识符。
var outboundIdentifiers = map[string]bool{
	"Client":                true,
	"DefaultClient":         true,
	"DefaultTransport":      true,
	"Transport":             true,
	"Get":                   true,
	"Head":                  true,
	"Post":                  true,
	"PostForm":              true,
	"NewRequest":            true,
	"NewRequestWithContext": true,
	"ReadResponse":          true,
	"ProxyFromEnvironment":  true,
	"ProxyURL":              true,
	"NewFileTransportFS":    true,
}

// outboundSite 是一处被报告的出站调用。
type outboundSite struct {
	dir        string
	file       string
	line       int
	identifier string
}

func TestOutboundRequests_OnlyFromAdapter(t *testing.T) {
	root := repoRoot(t)

	found, scanned, err := scanOutbound(root, productionRoots)
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if scanned == 0 {
		t.Fatal("没有扫描到任何 Go 文件，检查等于没做")
	}

	for _, site := range found {
		if outboundAllowed(site.dir) {
			continue
		}
		t.Errorf("%s:%d 用了 http.%s；出站请求只允许从 %v 发出",
			site.file, site.line, site.identifier, outboundAllowedDirs)
	}
	t.Logf("已扫描 %d 个生产代码文件，出站调用 %d 处", scanned, len(found))
}

func TestAdapter_ActuallyMakesOutboundRequests(t *testing.T) {
	// 上面的用例在「全仓库没有任何出站调用」时同样通过。这里确认 adapter 下确实
	// 还有出站调用，否则那条检查就成了永远为真的空断言。
	root := repoRoot(t)

	found, _, err := scanOutbound(root, []string{adapterPackageDir})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if len(found) == 0 {
		t.Errorf("%s 下没有任何出站调用", adapterPackageDir)
	}
}

func TestScanOutbound_CallOutsideAdapter_IsReported(t *testing.T) {
	// 仓库扫描恒为零违规，无法证明扫描器真的会报。用一棵临时目录树做正向对照。
	root := t.TempDir()
	writeOutboundFile(t, root, "internal/adapter/github/client.go", "http.NewRequestWithContext(ctx, m, u, nil)")
	writeOutboundFile(t, root, "internal/core/decision/engine.go", "http.Get(u)")
	writeOutboundFile(t, root, "internal/core/pipeline/run.go", "(&http.Client{}).Do(r)")
	writeOutboundFile(t, root, "internal/store/repo/agents.go", "http.DefaultClient.Do(r)")
	writeOutboundFile(t, root, "internal/cli/gatewayclient/probe.go", "http.NewRequest(m, u, nil)")
	writeOutboundFile(t, root, "internal/cli/run.go", "http.Post(u, c, b)")
	writeOutboundFile(t, root, "internal/transport/httpapi/server.go", "http.ListenAndServe(a, h)")
	writeOutboundFile(t, root, "internal/credential/keychain/source_test.go", "http.Get(u)")

	found, scanned, err := scanOutbound(root, productionRoots)
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if scanned != 7 {
		t.Errorf("扫描到 %d 个生产代码文件，期望 7（_test.go 不计入）", scanned)
	}

	outside := make([]outboundSite, 0, len(found))
	for _, site := range found {
		if !outboundAllowed(site.dir) {
			outside = append(outside, site)
		}
	}
	// 三处越界各用一个不同的标识符，标识符表被删掉任何一条都会让这里少报一处。
	reported := make(map[string]string, len(outside))
	for _, site := range outside {
		reported[site.dir] = site.identifier
	}
	// internal/cli/gatewayclient 在清单里，同一棵树下的 internal/cli 不在。
	expected := map[string]string{
		"internal/core/decision": "Get",
		"internal/core/pipeline": "Client",
		"internal/store/repo":    "DefaultClient",
		"internal/cli":           "Post",
	}
	if len(outside) != len(expected) {
		t.Fatalf("报告了 %d 处越界出站调用，期望 %d：%+v", len(outside), len(expected), outside)
	}
	for dir, identifier := range expected {
		if reported[dir] != identifier {
			t.Errorf("%s 报告的标识符是 %q，期望 %q", dir, reported[dir], identifier)
		}
	}
}

// scanOutbound 返回 roots 下全部 net/http 出站调用，以及扫描过的生产文件数。
func scanOutbound(root string, roots []string) (found []outboundSite, scanned int, err error) {
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

			parsed, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if parseErr != nil {
				return parseErr
			}
			relative, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}

			ast.Inspect(parsed, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := selector.X.(*ast.Ident)
				if !ok || pkg.Name != "http" || !outboundIdentifiers[selector.Sel.Name] {
					return true
				}
				found = append(found, outboundSite{
					dir:        filepath.ToSlash(filepath.Dir(relative)),
					file:       relative,
					line:       fset.Position(selector.Pos()).Line,
					identifier: selector.Sel.Name,
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

func writeOutboundFile(t *testing.T, root, relative, statement string) {
	t.Helper()

	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("建目录失败：%v", err)
	}
	body := "package sample\n\nimport \"net/http\"\n\nfunc sample() { _ = " + statement + " }\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("写文件失败：%v", err)
	}
}
