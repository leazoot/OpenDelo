package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/platform/config"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * 子进程环境的用例（REQ-CLI-002 AC1/AC2）。
 *
 * 断言的是**环境快照**：把改写后的 KEY=VALUE 列表整体扫一遍哨兵，
 * 而不是逐个变量去查。逐个查只能证明「点名的那几个没了」，
 * 扫描能证明「一个都没剩下」。
 */

func testSettings() config.Config {
	return config.Config{
		ListenAddress:  "127.0.0.1",
		WebAPIPort:     8787,
		AgentProxyPort: 8788,
		MCPPort:        8789,
		SecurityLevel:  config.LevelEnforced,
	}
}

// testSessionKey 站在网关刚刚签发的那把钥匙的位置上。
//
// 不用 test/sentinel 里的值：那些值代表**外部服务的**凭据，而本文件同时还要断言
// 它们一个都不许留在子进程环境里。会话凭证恰恰相反 —— 它必须出现在那里。
// 两者用同一个字符串会让两条断言互相矛盾。
const testSessionKey = "session-key-only-used-by-tests"

func valueOf(environ []string, name string) (string, bool) {
	for _, entry := range environ {
		if key, value, found := strings.Cut(entry, "="); found && key == name {
			return value, true
		}
	}
	return "", false
}

func TestPlanEnviron_KnownCredentialVariables_AreRemoved(t *testing.T) {
	// 安全规则点名的四个，各放一个哨兵。
	parent := []string{
		"GITHUB_TOKEN=" + sentinel.SentinelToken,
		"CLOUDFLARE_API_TOKEN=" + sentinel.SentinelToken,
		"OPENAI_API_KEY=" + sentinel.SentinelAPIKey,
		"ANTHROPIC_API_KEY=" + sentinel.SentinelAPIKey,
		"PATH=/usr/bin",
		"HOME=/home/agent",
	}

	plan := planEnviron(parent, testSettings(), nil, nil, testSessionKey)

	snapshot := strings.Join(plan.Environ, "\n")
	for _, value := range sentinel.All() {
		if strings.Contains(snapshot, value) {
			t.Errorf("子进程环境里留下了哨兵 %s：\n%s", value, snapshot)
		}
	}
	if path, found := valueOf(plan.Environ, "PATH"); !found || path != "/usr/bin" {
		t.Error("PATH 被误清理了 —— 清理必须只针对凭据位")
	}
	if len(plan.Removed) != 4 {
		t.Errorf("报告清理了 %v，期望四个", plan.Removed)
	}
}

func TestPlanEnviron_RemovedNames_CarryNoValues(t *testing.T) {
	// Removed 会被 --verbose 打印出来。它带上值就等于 REQ-CLI-003 AC2 失守。
	plan := planEnviron([]string{"GITHUB_TOKEN=" + sentinel.SentinelToken}, testSettings(), nil, nil, testSessionKey)

	for _, name := range plan.Removed {
		if strings.ContainsAny(name, "=") || strings.Contains(name, sentinel.SentinelToken) {
			t.Errorf("被清理的变量名里带上了值：%q", name)
		}
	}
}

func TestPlanEnviron_NamedVariablesWithoutACredentialSuffix_AreStillRemoved(t *testing.T) {
	// 点名列表的存在理由：这两个名字都不以任何凭据后缀结尾，后缀规则兜不住它们。
	// 少了这条用例，把整张点名列表删掉，上面那条也照样绿。
	for _, name := range []string{"AWS_ACCESS_KEY_ID", "CLOUDFLARE_EMAIL"} {
		t.Run(name, func(t *testing.T) {
			plan := planEnviron([]string{name + "=" + sentinel.SentinelToken}, testSettings(), nil, nil, testSessionKey)
			if _, found := valueOf(plan.Environ, name); found {
				t.Errorf("%s 留在了子进程环境里 —— 它只被点名列表兜着", name)
			}
		})
	}
}

func TestPlanEnviron_UnknownServiceWithACredentialSuffix_IsAlsoRemoved(t *testing.T) {
	// 点名列表兜不住没听说过的服务。宁可多清一个。
	cases := []string{
		"ACME_TOKEN", "ACME_API_KEY", "ACME_APIKEY", "ACME_SECRET",
		"ACME_SECRET_KEY", "ACME_PASSWORD", "ACME_PRIVATE_KEY", "ACME_CREDENTIALS",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			plan := planEnviron([]string{name + "=" + sentinel.SentinelToken}, testSettings(), nil, nil, testSessionKey)
			if _, found := valueOf(plan.Environ, name); found {
				t.Errorf("%s 留在了子进程环境里", name)
			}
		})
	}
}

func TestPlanEnviron_OrdinaryVariables_AreLeftAlone(t *testing.T) {
	// 反向对照：没有这条，上面的用例可以靠「什么都清掉了」通过。
	parent := []string{"PATH=/usr/bin", "LANG=en_US.UTF-8", "TERM=xterm", "EDITOR=vim"}

	plan := planEnviron(parent, testSettings(), nil, nil, testSessionKey)

	for _, entry := range parent {
		if !slices.Contains(plan.Environ, entry) {
			t.Errorf("%q 被清掉了", entry)
		}
	}
	if len(plan.Removed) != 0 {
		t.Errorf("报告清理了 %v，期望一个都没有", plan.Removed)
	}
}

func TestPlanEnviron_ExtraNames_AreRemovedToo(t *testing.T) {
	// REQ-CLI-002 AC1「列表可配置」。
	plan := planEnviron([]string{"COMPANY_VPN=" + sentinel.SentinelPassword},
		testSettings(), []string{"COMPANY_VPN"}, nil, testSessionKey)

	if _, found := valueOf(plan.Environ, "COMPANY_VPN"); found {
		t.Error("--clear-env 指定的变量没有被清理")
	}
}

func TestPlanEnviron_KeptName_SurvivesTheSuffixRule(t *testing.T) {
	// 后缀规则会误伤 —— 用户要能把误伤的那个要回来，否则只能整条规则关掉。
	plan := planEnviron([]string{"NPM_TOKEN=needed-by-the-build"},
		testSettings(), nil, []string{"NPM_TOKEN"}, testSessionKey)

	if value, found := valueOf(plan.Environ, "NPM_TOKEN"); !found || value != "needed-by-the-build" {
		t.Error("--keep-env 指定的变量没有被保留")
	}
}

func TestPlanEnviron_GatewayAddressesAreInjected(t *testing.T) {
	// REQ-CLI-002 AC2。
	plan := planEnviron([]string{"PATH=/usr/bin"}, testSettings(), nil, nil, testSessionKey)

	expected := map[string]string{
		"HTTP_PROXY":            "http://127.0.0.1:8788",
		"http_proxy":            "http://127.0.0.1:8788",
		"HTTPS_PROXY":           "http://127.0.0.1:8788",
		"https_proxy":           "http://127.0.0.1:8788",
		"OPENDELO_MCP_ENDPOINT": "http://127.0.0.1:8789/mcp",
		"NO_PROXY":              "localhost,127.0.0.1,::1",
	}
	for name, want := range expected {
		if value, found := valueOf(plan.Environ, name); !found || value != want {
			t.Errorf("%s 为 %q（存在：%v），期望 %q", name, value, found, want)
		}
	}
}

func TestPlanEnviron_ParentProxySettings_AreOverwrittenNotKept(t *testing.T) {
	// 父进程里指向别处的 HTTP_PROXY 留着，就等于让 Agent 绕过 8788。
	parent := []string{
		"HTTP_PROXY=http://corporate-proxy:3128",
		"https_proxy=http://corporate-proxy:3128",
	}

	plan := planEnviron(parent, testSettings(), nil, nil, testSessionKey)

	for _, name := range []string{"HTTP_PROXY", "https_proxy"} {
		value, _ := valueOf(plan.Environ, name)
		if value != "http://127.0.0.1:8788" {
			t.Errorf("%s 为 %q，期望被网关地址覆盖", name, value)
		}
	}
	if count := strings.Count(strings.Join(plan.Environ, "\n"), "corporate-proxy"); count != 0 {
		t.Errorf("父进程的代理设置还留着 %d 处", count)
	}
}

func TestPlanEnviron_PortsComeFromTheConfiguration(t *testing.T) {
	// 端口硬编码在代码里的话，改了配置的用户会得到一个指向空端口的 Agent。
	settings := testSettings()
	settings.AgentProxyPort = 18788
	settings.MCPPort = 18789

	plan := planEnviron(nil, settings, nil, nil, testSessionKey)

	if value, _ := valueOf(plan.Environ, "HTTP_PROXY"); value != "http://127.0.0.1:18788" {
		t.Errorf("HTTP_PROXY 为 %q", value)
	}
	if value, _ := valueOf(plan.Environ, "OPENDELO_MCP_ENDPOINT"); value != "http://127.0.0.1:18789/mcp" {
		t.Errorf("OPENDELO_MCP_ENDPOINT 为 %q", value)
	}
}

func TestPlanEnviron_RelayLevel_LeavesTheEnvironmentAlone(t *testing.T) {
	// REQ-GATEWAY-005：清理是 L1 的手段。L0 Relay 按 PRD §21 本来就承认
	// 「Agent 可能仍有其他直连出口」，在那一级清理环境会造出它没有承诺的保护感。
	settings := testSettings()
	settings.SecurityLevel = config.LevelRelay

	plan := planEnviron([]string{"GITHUB_TOKEN=" + sentinel.SentinelToken}, settings, nil, nil, testSessionKey)

	if value, found := valueOf(plan.Environ, "GITHUB_TOKEN"); !found || value != sentinel.SentinelToken {
		t.Error("L0 下凭据变量被清理了")
	}
	if len(plan.Removed) != 0 {
		t.Errorf("L0 下报告清理了 %v", plan.Removed)
	}
	// 代理配置仍然注入：L0 也要走代理与审计，它放弃的只是「阻止直连」。
	if _, found := valueOf(plan.Environ, "HTTP_PROXY"); !found {
		t.Error("L0 下没有注入网关地址")
	}
}

func TestPlanEnviron_DefaultConfiguration_IsEnforced(t *testing.T) {
	// REQ-GATEWAY-005 AC1：默认配置为 L1。
	settings := config.Default()
	if !settings.Enforced() {
		t.Fatalf("默认等级为 %q，期望 L1", settings.SecurityLevel)
	}

	plan := planEnviron([]string{"GITHUB_TOKEN=" + sentinel.SentinelToken}, settings, nil, nil, testSessionKey)
	if _, found := valueOf(plan.Environ, "GITHUB_TOKEN"); found {
		t.Error("默认配置下凭据变量没有被清理")
	}
}

func TestPlanEnviron_SessionKey_IsInjectedAndTheParentsIsDropped(t *testing.T) {
	// REQ-CLI-002 AC3 的前提：子进程要能在两个 Agent 面上被认出来。
	// 父进程环境里那把旧钥匙必须被顶掉 —— 留着它，子进程会拿着上一次的会话
	// 去敲门，而那次会话早已断开。
	plan := planEnviron([]string{"OPENDELO_SESSION_KEY=key-from-a-previous-session"},
		testSettings(), nil, nil, testSessionKey)

	value, found := valueOf(plan.Environ, "OPENDELO_SESSION_KEY")
	if !found {
		t.Fatal("子进程环境里没有会话凭证")
	}
	if value != testSessionKey {
		t.Errorf("子进程拿到的是 %q，期望这次刚签发的那把", value)
	}
}
