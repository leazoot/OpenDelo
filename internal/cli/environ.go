package cli

import (
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/Runcoor/opendelo/internal/platform/config"
)

/*
 * 子进程的环境（REQ-CLI-002，PRD §21 L1 Enforced）。
 *
 * 两件事在这里发生：把凭据从环境里拿掉，把网关的位置放进去。
 *
 * 顺序是先清后注：注入的几个变量名不在清理规则里，但把它们写在前面就得靠
 * 「清理规则永远不会命中它们」这个巧合来保证正确。
 */

// credentialVariables 是默认清理的变量名。
//
// 精确名字用来兜住那些不带可识别后缀的（GH_TOKEN 有后缀，AWS_ACCESS_KEY_ID 没有）。
var credentialVariables = []string{
	"ANTHROPIC_API_KEY",
	"AWS_ACCESS_KEY_ID",
	"AWS_SECRET_ACCESS_KEY",
	"AWS_SESSION_TOKEN",
	"CLOUDFLARE_API_TOKEN",
	"CLOUDFLARE_EMAIL",
	"CLOUDFLARE_GLOBAL_API_KEY",
	"GH_TOKEN",
	"GITHUB_TOKEN",
	"GOOGLE_API_KEY",
	"OPENAI_API_KEY",
	"VERCEL_TOKEN",
}

// credentialSuffixes 把没被点名的服务一并兜住。
//
// 只按名字后缀判断，不看值：一个「看起来像令牌」的字符串无法被可靠识别
// （与 platform/logging 的脱敏同一条理由）。宁可多清 —— 少清一个的代价是
// Agent 手里多一把不该有的钥匙，而多清一个的代价是用户加一次 --keep-env。
var credentialSuffixes = []string{
	"_API_KEY",
	"_APIKEY",
	"_CREDENTIALS",
	"_PASSWORD",
	"_PRIVATE_KEY",
	"_SECRET",
	"_SECRET_KEY",
	"_TOKEN",
}

// proxyEnvironment 是注入给子进程的网关位置（REQ-CLI-002 AC2）。
//
// 大小写两套都写：Go 的 http.ProxyFromEnvironment 认大写，curl 与不少工具认小写。
// 只写一套就等于让「拦不拦得住」取决于 Agent 用了哪个库。
func proxyEnvironment(settings config.Config) map[string]string {
	proxy := "http://" + net.JoinHostPort(settings.ListenAddress, strconv.Itoa(settings.AgentProxyPort))
	mcp := "http://" + net.JoinHostPort(settings.ListenAddress, strconv.Itoa(settings.MCPPort)) + "/mcp"

	return map[string]string{
		"HTTP_PROXY":  proxy,
		"http_proxy":  proxy,
		"HTTPS_PROXY": proxy,
		"https_proxy": proxy,
		// 网关自己的三个端口不能再绕回代理，否则 Agent 连 MCP 面都要先过一次代理。
		"NO_PROXY": "localhost,127.0.0.1,::1",
		"no_proxy": "localhost,127.0.0.1,::1",
		// MCP 面的位置。Agent 拿不到凭据之后，能力要从这里取。
		"OPENDELO_MCP_ENDPOINT": mcp,
	}
}

// sessionKeyVariable 是子进程取得自己会话凭证的地方。
//
// 会话凭证走环境变量，是因为子进程的环境必须在它启动之前固定下来，而它没有
// 别的办法在第一次调用之前拿到这把钥匙。这不与「凭据不进环境变量」冲突：
// 那条规则说的是**外部服务的**凭据，那种东西永远留在缝内。这把钥匙只能证明
// 「我是那个 Agent」，拿着它发出的每一次请求仍要走完决策链路。
const sessionKeyVariable = "OPENDELO_SESSION_KEY"

// environPlan 是一次环境改写的结果。
//
// Removed 只有变量名，没有值 —— 值是凭据本身，连中间结构都不该持有
type environPlan struct {
	Environ []string
	Removed []string
}

// planEnviron 从父进程环境算出子进程环境。
//
// parent 是 os.Environ() 的形状（KEY=VALUE），由调用方传入而不是在这里读，
// 使用例不必改动进程状态。sessionKey 是网关刚刚签发的会话凭证。
func planEnviron(
	parent []string, settings config.Config, extra, keep []string, sessionKey string,
) environPlan {
	clear := make(map[string]bool, len(credentialVariables)+len(extra))
	for _, name := range credentialVariables {
		clear[name] = true
	}
	for _, name := range extra {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			clear[trimmed] = true
		}
	}

	kept := make(map[string]bool, len(keep))
	for _, name := range keep {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			kept[trimmed] = true
		}
	}

	injected := proxyEnvironment(settings)
	injected[sessionKeyVariable] = sessionKey
	// 清理只在 L1 发生（REQ-GATEWAY-005 AC2）。L0 Relay 只代理与审计，
	// 按 PRD §21 它本来就承认「Agent 可能仍有其他直连出口」——
	// 在 L0 下清理环境会造出一种它没有承诺的保护感。
	enforced := settings.Enforced()
	plan := environPlan{Environ: make([]string, 0, len(parent)+len(injected))}

	for _, entry := range parent {
		name, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if _, overwritten := injected[name]; overwritten {
			// 注入的变量以网关为准：父进程里已有的 HTTP_PROXY 指向别处，
			// 留着它就等于让 Agent 绕过 8788。
			continue
		}
		if enforced && !kept[name] && carriesCredential(name, clear) {
			plan.Removed = append(plan.Removed, name)
			continue
		}
		plan.Environ = append(plan.Environ, entry)
	}

	for _, name := range sortedKeys(injected) {
		plan.Environ = append(plan.Environ, name+"="+injected[name])
	}
	sort.Strings(plan.Removed)
	return plan
}

// carriesCredential 判断一个变量名是不是凭据位。
func carriesCredential(name string, clear map[string]bool) bool {
	if clear[name] {
		return true
	}
	upper := strings.ToUpper(name)
	for _, suffix := range credentialSuffixes {
		if strings.HasSuffix(upper, suffix) {
			return true
		}
	}
	return false
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
