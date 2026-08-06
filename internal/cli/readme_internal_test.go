package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

/*
 * README 里写的命令必须真的存在（TASK-0713 的验收标准）。
 *
 * 「全新环境按 README 能走通」这句话没法自动跑完 —— 那要一台干净的机器、
 * 一个真实的凭据来源和一个真实的 Agent。但它失败得最多的那一步是可以守住的：
 * 文档里的命令改了名或没了，而照着做的人只会看到「未知命令」。
 *
 * 只查命令名。参数与输出由各命令自己的用例覆盖。
 */

// documented 是要扫的文档。中英两份都扫：只对一份，另一份会安静地过期。
var documented = []string{"README.md", "README.zh-CN.md", "CHANGELOG.md"}

// mentioned 抓出 `opendelo <子命令>` 这样的写法。
var mentioned = regexp.MustCompile(`\bopendelo ([a-z][a-z-]*)`)

// notACommand 是文档里跟在 opendelo 后面、但本来就不是子命令的词。
var notACommand = map[string]bool{
	// 「装到 /usr/local/bin/opendelo」这类路径与散文里的产品名。
	"binaries": true,
	"holds":    true,
	"stores":   true,
}

func TestREADME_EveryCommandItMentionsExists(t *testing.T) {
	known := knownCommands(t)
	if len(known) < 5 {
		t.Fatalf("只认出 %d 个命令，扫描本身出了问题", len(known))
	}

	scanned := 0
	for _, name := range documented {
		text := readRepoFile(t, name)
		for _, match := range mentioned.FindAllStringSubmatch(text, -1) {
			command := match[1]
			if notACommand[command] {
				continue
			}
			scanned++
			if !slices.Contains(known, command) {
				t.Errorf("%s 里写着 `opendelo %s`，但没有这个命令 —— 照着做的人会看到「未知命令」",
					name, command)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("文档里一条命令都没扫到，这条检查是空跑的")
	}
}

// knownCommands 从 Run 的分发处取出全部子命令。
//
// 读源码而不是维护第二份清单：清单会与 switch 分头演化，
// 而分头之后这条检查守的就是清单自己。
func knownCommands(t *testing.T) []string {
	t.Helper()

	source := readRepoFile(t, filepath.Join("internal", "cli", "cli.go"))
	dispatch := regexp.MustCompile(`(?m)^\tcase "([a-z][a-z-]*)"`)

	commands := make([]string, 0, 12)
	for _, match := range dispatch.FindAllStringSubmatch(source, -1) {
		commands = append(commands, match[1])
	}
	return commands
}

func readRepoFile(t *testing.T, name string) string {
	t.Helper()

	// 用例的工作目录是包目录，仓库根在两层之上。
	content, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", name, err)
	}
	return strings.ReplaceAll(string(content), "\r\n", "\n")
}
