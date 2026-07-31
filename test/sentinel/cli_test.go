package sentinel_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/cli"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/config"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * 八个面的哨兵扫描之二：CLI 输出（REQ-CLI-003 AC1）。
 *
 * 真实的泄漏路径是「命令把它读到的文件内容回显到终端」：配置目录里将来会放会话令牌，
 * 配置文件本身也可能被用户塞进不该塞的东西。所以这里把哨兵写进配置文件，
 * 再让每条命令都读一遍，扫描它们的全部输出。
 */

// corruptConfigWithSentinels 写出一个含哨兵且无法解析的配置文件。
//
// 故意让它损坏：解析失败是最容易顺手把文件内容打进错误信息的地方。
func corruptConfigWithSentinels(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "opendelo")
	if err := os.Mkdir(dir, config.DirPermission); err != nil {
		t.Fatalf("创建配置目录失败：%v", err)
	}

	content := "{\n"
	for _, value := range sentinel.All() {
		content += "  \"" + value + "\": \"" + value + "\",\n"
	}
	content += "  这里开始就不是合法 JSON 了\n"

	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte(content), config.FilePermission); err != nil {
		t.Fatalf("写配置文件失败：%v", err)
	}
	return dir
}

func TestCLI_OutputNeverContainsSentinel(t *testing.T) {
	dir := corruptConfigWithSentinels(t)

	// 覆盖三条命令的正常路径与失败路径。status 在这里必然失败（没有 Gateway 在跑），
	// 失败路径恰恰是错误信息最容易带出上下文的地方。
	invocations := map[string][]string{
		"init":        {"init", "--config-dir", dir},
		"status":      {"status", "--config-dir", dir},
		"start":       {"start", "--config-dir", dir, "--web-api-port", "1"},
		"init --help": {"init", "--help"},
		"未知命令":        {"run", "--config-dir", dir},
		"非法参数":        {"status", "--config-dir", dir, "--no-such-flag"},
		"非法端口":        {"status", "--config-dir", dir, "--web-api-port", "-1"},
		"version":     {"--version"},
		"无参数":         {},
		"status 二次执行": {"status", "--config-dir", dir},
	}

	for name, args := range invocations {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			cli.Run(t.Context(), cli.Options{
				Args:    args,
				Stdout:  &stdout,
				Stderr:  &stderr,
				Version: "0.0.0-sentinel",
				Clock:   clock.System{},
			})
			assertNoSentinel(t, stdout.String()+stderr.String())
		})
	}
}

func TestCLI_SentinelIsActuallyPresentInTheConfigFile(t *testing.T) {
	// 反向对照：证明上面的扫描不是因为哨兵压根没进过这条链路才通过的。
	dir := corruptConfigWithSentinels(t)

	content, err := os.ReadFile(filepath.Join(dir, config.FileName)) //nolint:gosec // 路径由用例构造
	if err != nil {
		t.Fatalf("读取配置文件失败：%v", err)
	}
	for _, value := range sentinel.All() {
		if !strings.Contains(string(content), value) {
			t.Errorf("配置文件里没有哨兵 %s，扫描等于没做", value)
		}
	}
}
