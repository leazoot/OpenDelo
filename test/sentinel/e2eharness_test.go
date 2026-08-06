package sentinel_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * E2E 夹具与 Go 侧的对账。
 *
 * E2E 是另一个语言里的另一份实现，它与本仓库之间有两处逐字重复的常量。
 * 两处都不会因为改错而编译失败，症状却都很严重：
 *
 *   - **哨兵值对不上** → 八个面扫的不是 Gateway 真正取到的那个值，全绿说明不了任何事。
 *   - **出站地址的环境变量名对不上** → `-tags e2e` 的构建收不到覆盖，
 *     于是回落到真实地址，E2E 会去访问真实的 GitHub / Cloudflare
 *     （`.claude/rules/security.md` §13.5 明确禁止）。
 *
 * 因此在这里静态对账。读的是文件文本而不是导入代码：TS 这一侧 Go 编译不到，
 * 而 outbound_e2e.go 带构建标签，正常构建下也不参与编译。
 */

// e2eHarnessFiles 是需要与 Go 常量保持一致的 E2E 文件。
const (
	sentinelTS = "../e2e/harness/sentinel.ts"
	gatewayTS  = "../e2e/harness/gateway.ts"
	outboundGo = "../../internal/cli/outbound_e2e.go"
)

func TestE2EHarness_UsesTheSameSentinelValues(t *testing.T) {
	source := read(t, sentinelTS)

	for _, value := range sentinel.All() {
		if !strings.Contains(source, value) {
			t.Errorf("%s 里没有哨兵 %q —— E2E 扫的将不是同一个值", sentinelTS, value)
		}
	}
}

// TestE2EHarness_SetsEveryOutboundOverride：`-tags e2e` 的构建认得几个环境变量，
// 夹具就得设几个。少设一个，那个服务会照着真实地址出站。
func TestE2EHarness_SetsEveryOutboundOverride(t *testing.T) {
	declared := environmentVariablesIn(read(t, outboundGo))
	if len(declared) == 0 {
		t.Fatalf("%s 里没有找到任何 OPENDELO_E2E_ 环境变量，对账无从谈起", outboundGo)
	}

	harness := read(t, gatewayTS)
	for _, variable := range declared {
		if !strings.Contains(harness, variable) {
			t.Errorf("%s 没有设置 %s —— 那个服务会照真实地址出站", gatewayTS, variable)
		}
	}
}

// environmentVariablesIn 取出源码里出现的全部 OPENDELO_E2E_ 变量名。
func environmentVariablesIn(source string) []string {
	const prefix = "OPENDELO_E2E_"

	seen := map[string]struct{}{}
	names := make([]string, 0, 4)
	for rest := source; ; {
		start := strings.Index(rest, prefix)
		if start < 0 {
			return names
		}
		rest = rest[start:]
		end := len(rest)
		for index, letter := range rest {
			if !partOfName(letter) {
				end = index
				break
			}
		}
		name := rest[:end]
		if _, repeated := seen[name]; !repeated {
			seen[name] = struct{}{}
			names = append(names, name)
		}
		rest = rest[end:]
	}
}

// partOfName 判断一个字符还属不属于环境变量名。
func partOfName(letter rune) bool {
	return letter == '_' || (letter >= 'A' && letter <= 'Z') || (letter >= '0' && letter <= '9')
}

func read(t *testing.T, relative string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Clean(relative))
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", relative, err)
	}
	return string(content)
}
