//go:build e2e

package cli

import (
	"os"

	"github.com/Runcoor/opendelo/internal/adapter/cloudflare"
	"github.com/Runcoor/opendelo/internal/adapter/github"
	"github.com/Runcoor/opendelo/internal/adapter/model"
)

/*
 * 只在 `-tags e2e` 下编译：把出站地址指向本地假服务。
 *
 * `.claude/rules/testing.md` §5 允许（并且要求）用 fake 替代外部 HTTP 服务，
 * `.claude/rules/security.md` §13.5 禁止对真实账户发起任何操作。E2E 跑的是
 * 一个真实进程，因此那个 fake 必须由进程外指定 —— 这就是这个文件存在的理由。
 *
 * **它改的只有出站地址。** 决策链路、Lease、审计、脱敏、凭据取用全部照常走，
 * 没有一处判断因为这个 tag 而被跳过。地址之外的任何开关都不属于这里。
 */

// 各服务的出站地址环境变量。只有 `-tags e2e` 的构建认得它们。
const (
	envGitHubBaseURL     = "OPENDELO_E2E_GITHUB_BASE_URL"
	envCloudflareBaseURL = "OPENDELO_E2E_CLOUDFLARE_BASE_URL"
	envOpenAIBaseURL     = "OPENDELO_E2E_OPENAI_BASE_URL"
	envAnthropicBaseURL  = "OPENDELO_E2E_ANTHROPIC_BASE_URL"
)

// outboundBaseURLs 从环境变量读出各服务的假服务地址。
//
// 未设置的服务不进表，于是回落到自己的 DefaultBaseURL —— 那意味着一次真实的
// 出站请求，用例因此必须把用到的每个服务都指过来。
func outboundBaseURLs() map[string]string {
	overrides := map[string]string{}
	for service, variable := range map[string]string{
		github.Service:                  envGitHubBaseURL,
		cloudflare.Service:              envCloudflareBaseURL,
		string(model.ProviderOpenAI):    envOpenAIBaseURL,
		string(model.ProviderAnthropic): envAnthropicBaseURL,
	} {
		if address := os.Getenv(variable); address != "" {
			overrides[service] = address
		}
	}
	return overrides
}
