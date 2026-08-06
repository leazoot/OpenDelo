package cli_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/cli"
	"github.com/Runcoor/opendelo/internal/platform/config"
	"github.com/Runcoor/opendelo/internal/transport/httpapi"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * opendelo run 的用例（REQ-CLI-001 AC1、REQ-CLI-002、REQ-CLI-003）。
 *
 * 子进程用 /bin/sh 造：它在每台开发机与 CI 上都在，且能把自己看到的环境
 * 原样打印出来 —— 那正是 AC1 要的「进程环境快照」。
 *
 * 每条用例都起一个真网关。run 现在先向网关注册会话再启动子进程，
 * 拿掉网关这一步就测不到 —— 而那一步正是子进程之后能不能取得任何能力的前提。
 */

// runningGateway 起一个网关，返回它的配置目录与三个端口。
//
// 端口写进 config.json 而不是走命令行：run 没有端口参数，它必须和 start
// 从同一处读到同一组端口，否则它会去敲默认的 8787。
func runningGateway(t *testing.T) (string, ports) {
	t.Helper()

	dir := initialized(t)
	occupied := ports{web: freePort(t), agentProxy: freePort(t), mcp: freePort(t)}
	writePorts(t, dir, occupied)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan result, 1)
	go func() { done <- execute(t, ctx, "start", "--config-dir", dir) }()
	waitFor(t, occupied.web)

	t.Cleanup(func() {
		cancel()
		<-done
	})
	return dir, occupied
}

// writePorts 把挑好的端口写进配置文件。
func writePorts(t *testing.T, dir string, occupied ports) {
	t.Helper()

	encoded, err := json.Marshal(map[string]any{
		"listen_address":   "127.0.0.1",
		"web_api_port":     occupied.web,
		"agent_proxy_port": occupied.agentProxy,
		"mcp_port":         occupied.mcp,
	})
	if err != nil {
		t.Fatalf("构造配置失败：%v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, config.FileName), encoded, 0o600); err != nil {
		t.Fatalf("写入配置失败：%v", err)
	}
}

// runAgent 在一个跑着的网关下执行 opendelo run。
func runAgent(t *testing.T, environ map[string]string, args ...string) (int, string, string) {
	t.Helper()

	for name, value := range environ {
		t.Setenv(name, value)
	}
	dir, _ := runningGateway(t)

	got := execute(t, t.Context(),
		append([]string{"run", "--config-dir", dir}, args...)...)
	return got.code, got.stdout, got.stderr
}

func TestRun_ChildEnvironmentSnapshot_ContainsNoCredentials(t *testing.T) {
	// REQ-CLI-002 AC1：断言的是子进程**自己看到的**环境，不是父进程算出来的计划。
	code, stdout, stderr := runAgent(t,
		map[string]string{
			"GITHUB_TOKEN":         sentinel.SentinelToken,
			"CLOUDFLARE_API_TOKEN": sentinel.SentinelToken,
			"OPENAI_API_KEY":       sentinel.SentinelAPIKey,
			"ANTHROPIC_API_KEY":    sentinel.SentinelAPIKey,
			"DEVICE_PRIVATE_KEY":   sentinel.SentinelPrivateKey,
		},
		"--", "/bin/sh", "-c", "env")

	if code != cli.ExitOK {
		t.Fatalf("退出码为 %d，stderr：%s", code, stderr)
	}
	for _, value := range sentinel.All() {
		if strings.Contains(stdout, value) {
			t.Errorf("子进程环境里出现了哨兵 %s：\n%s", value, stdout)
		}
	}
	if !strings.Contains(stdout, "HTTP_PROXY=http://127.0.0.1:") {
		t.Errorf("子进程环境里没有网关地址（REQ-CLI-002 AC2）：\n%s", stdout)
	}
}

func TestRun_ChildEnvironment_CarriesASessionKey(t *testing.T) {
	// REQ-CLI-002 AC3 的前提：子进程要能在 8788 / 8789 上认得出来，
	// 而它只能从环境里拿到这把钥匙。没有它，两个 Agent 面对它一律 407。
	code, stdout, stderr := runAgent(t, nil,
		"--", "/bin/sh", "-c", "echo key=${OPENDELO_SESSION_KEY:-missing}")

	if code != cli.ExitOK {
		t.Fatalf("退出码为 %d，stderr：%s", code, stderr)
	}
	if strings.Contains(stdout, "key=missing") || !strings.Contains(stdout, "key=") {
		t.Errorf("子进程没有拿到会话凭证：%q", stdout)
	}
}

// unreachableGateway 建一个配置目录，其中的三个端口上**确实没有人在听**。
//
// 不能只用 initialized：run 从配置里读端口，配置里没写就去敲默认的 8787，
// 于是「网关不在跑」这个前提取决于本机 8787 恰好没被别的程序占着。占着的时候
// run 拿到的是那个程序的 404，走进「响应体不是预期的错误结构」那一支 ——
// 断言看到的失败与被测的行为无关（R-36）。
func unreachableGateway(t *testing.T) string {
	t.Helper()

	dir := initialized(t)
	writePorts(t, dir, ports{web: freePort(t), agentProxy: freePort(t), mcp: freePort(t)})
	return dir
}

func TestRun_WithoutARunningGateway_RefusesToStartTheChild(t *testing.T) {
	// 网关不在时不能「照常启动、少个会话而已」：子进程的环境里已经没有凭据，
	// 网关又不认识它，那样启动出来的是一个什么都做不了的 Agent，
	// 而原因早已滚出屏幕。
	dir := unreachableGateway(t)
	marker := filepath.Join(t.TempDir(), "the-child-ran")

	got := execute(t, t.Context(), "run", "--config-dir", dir,
		"--", "/bin/sh", "-c", "touch "+marker)

	if got.code == cli.ExitOK {
		t.Fatal("网关没在跑却成功了")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("网关不可用时子进程仍然被启动了")
	}
	if !strings.Contains(got.stderr, "opendelo start") {
		t.Errorf("失败信息没有指出该怎么办：%q", got.stderr)
	}
}

func TestRun_AfterTheChildExits_TheSessionIsEnded(t *testing.T) {
	// REQ-CLI-002 AC3。问的是网关那一侧的状态而不是 run 打印了什么：
	// run 说自己结束了会话，与会话真的结束了，是两件事。
	dir, occupied := runningGateway(t)

	got := execute(t, t.Context(), "run", "--config-dir", dir, "--", "/bin/sh", "-c", "true")
	if got.code != cli.ExitOK {
		t.Fatalf("退出码为 %d，stderr：%s", got.code, got.stderr)
	}

	registered := agentsOf(t, dir, occupied.web)
	if len(registered) == 0 {
		t.Fatal("网关那边一个 Agent 都没有，这条用例什么也没检查")
	}
	for _, agent := range registered {
		if agent.Status != "disconnected" {
			t.Errorf("子进程已退出，Agent %s 的状态却仍是 %s", agent.ID, agent.Status)
		}
	}
}

// agentsOf 以 Console 的身份读取网关上的 Agent 列表。
func agentsOf(t *testing.T, dir string, webPort int) []httpapi.AgentView {
	t.Helper()

	token, err := os.ReadFile(filepath.Join(dir, config.SessionTokenFileName))
	if err != nil {
		t.Fatalf("读取会话令牌失败：%v", err)
	}

	origin := "http://127.0.0.1:" + strconv.Itoa(webPort)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, origin+"/v1/agents", nil)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	request.Header.Set("Origin", origin)
	request.Header.Set("X-Requested-By", "opendelo-console")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("请求 Agent 列表失败：%v", err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			t.Errorf("关闭响应体失败：%v", closeErr)
		}
	}()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Agent 列表返回 %d", response.StatusCode)
	}

	var envelope struct {
		Items []httpapi.AgentView `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("Agent 列表不是预期的结构：%v", err)
	}
	return envelope.Items
}

func TestRun_TheSnapshotWouldCatchALeak(t *testing.T) {
	// 反向对照：换一个不该被清理的变量名，同一个哨兵就会出现在快照里。
	// 没有这条，上面的用例可能只是因为 env 根本没打印出东西而通过。
	_, stdout, _ := runAgent(t,
		map[string]string{"HARMLESS_VARIABLE": sentinel.SentinelToken},
		"--", "/bin/sh", "-c", "env")

	if !strings.Contains(stdout, sentinel.SentinelToken) {
		t.Fatalf("对照变量没有出现在快照里，说明这套用例看不到子进程环境：\n%s", stdout)
	}
}

func TestRun_VerboseOutput_NamesTheVariablesWithoutTheirValues(t *testing.T) {
	// REQ-CLI-003 AC2。会话凭证也在被注入之列，它的值同样不许出现。
	_, stdout, stderr := runAgent(t,
		map[string]string{"GITHUB_TOKEN": sentinel.SentinelToken},
		"--verbose", "--", "/bin/sh", "-c", "echo ${OPENDELO_SESSION_KEY}")

	if !strings.Contains(stderr, "GITHUB_TOKEN") {
		t.Errorf("--verbose 没有报告被清理的变量名：\n%s", stderr)
	}
	for _, value := range sentinel.All() {
		if strings.Contains(stderr, value) {
			t.Errorf("--verbose 的输出里出现了哨兵 %s：\n%s", value, stderr)
		}
	}

	// 子进程把自己那把钥匙打印了出来 —— 拿它去 stderr 里找，一个字符都不该有。
	key := strings.TrimSpace(stdout)
	if key == "" {
		t.Fatal("子进程没有打印出会话凭证，这条用例什么也没检查")
	}
	if strings.Contains(stderr, key) {
		t.Errorf("--verbose 的输出里出现了会话凭证：\n%s", stderr)
	}
}

func TestRun_ChildExitCode_IsPassedThrough(t *testing.T) {
	// REQ-CLI-001 AC1。折成 1 会让 `opendelo run -- make test` 丢掉真实的失败原因。
	cases := map[string]int{"成功": 0, "普通失败": 1, "自定义退出码": 42}
	for name, expected := range cases {
		t.Run(name, func(t *testing.T) {
			code, _, _ := runAgent(t, nil, "--", "/bin/sh", "-c", "exit "+itoa(expected))
			if code != expected {
				t.Errorf("退出码为 %d，期望 %d", code, expected)
			}
		})
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

func TestRun_WithoutACommand_FailsWithGuidance(t *testing.T) {
	got := execute(t, t.Context(), "run", "--config-dir", initialized(t))

	if got.code == cli.ExitOK {
		t.Fatal("没给命令却成功了")
	}
	if !strings.Contains(got.stderr, "opendelo run --") {
		t.Errorf("失败信息里没有用法：%s", got.stderr)
	}
}

func TestRun_ArgumentsAfterTheSeparator_BelongToTheChild(t *testing.T) {
	// `opendelo run -- claude --verbose` 里的 --verbose 是给 claude 的。
	code, stdout, stderr := runAgent(t, nil,
		"--", "/bin/sh", "-c", "echo $0 $@", "--verbose")

	if code != cli.ExitOK {
		t.Fatalf("退出码为 %d，stderr：%s", code, stderr)
	}
	if !strings.Contains(stdout, "--verbose") {
		t.Errorf("子进程没有收到 --verbose：%q", stdout)
	}
	if strings.Contains(stderr, "已清理") {
		t.Error("--verbose 被 opendelo 自己吃掉了")
	}
}

func TestRun_UnknownCommand_FailsWithoutPanicking(t *testing.T) {
	code, _, stderr := runAgent(t, nil, "--", "opendelo-no-such-binary-1a6d80")

	if code == cli.ExitOK {
		t.Fatal("启动一个不存在的命令却成功了")
	}
	if stderr == "" {
		t.Error("失败时没有给出任何说明")
	}
}

func TestRun_Help_GoesToStdoutAndSucceeds(t *testing.T) {
	// REQ-CLI-001 AC1：六个命令均有 --help 且退出码语义正确。
	got := execute(t, t.Context(), "run", "--help")

	if got.code != cli.ExitOK {
		t.Errorf("--help 的退出码为 %d", got.code)
	}
	if !strings.Contains(got.stdout, "clear-env") || !strings.Contains(got.stdout, "keep-env") {
		t.Errorf("--help 没有列出参数：\n%s", got.stdout)
	}
}
