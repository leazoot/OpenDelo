//go:build !e2e

package cli

import (
	"testing"

	"github.com/Runcoor/opendelo/internal/adapter/cloudflare"
	"github.com/Runcoor/opendelo/internal/adapter/github"
	"github.com/Runcoor/opendelo/internal/adapter/model"
)

/*
 * 正式构建的出站地址是编译期常量。
 *
 * E2E 要让二进制指向本地假服务，唯一的办法是 `-tags e2e`。这两个用例守住
 * 「那条路只在 tag 下存在」：把 E2E 用的环境变量全设上，分发出去的这份构建
 * 仍然只认各 Adapter 自己声明的地址。
 *
 * 为什么值得单独测：出站地址与凭据注入在同一条路径上。一个能被环境变量改写的
 * 出站地址等于「把 GitHub 令牌发给任意主机」，而那种改动加起来只有一行，
 * 评审时看不出来。
 */

// e2eVariables 是 outbound_e2e.go 认得的全部环境变量。
// 这份构建必须一个都不认。
var e2eVariables = []string{
	"OPENDELO_E2E_GITHUB_BASE_URL",
	"OPENDELO_E2E_CLOUDFLARE_BASE_URL",
	"OPENDELO_E2E_OPENAI_BASE_URL",
	"OPENDELO_E2E_ANTHROPIC_BASE_URL",
}

func TestOutboundBaseURLs_InAReleaseBuild_IgnoresTheEnvironment(t *testing.T) {
	for _, variable := range e2eVariables {
		t.Setenv(variable, "http://127.0.0.1:1/impostor")
	}

	if overrides := outboundBaseURLs(); len(overrides) != 0 {
		t.Fatalf("正式构建接受了出站地址覆盖：%v", overrides)
	}
}

func TestAssembleAdapters_InAReleaseBuild_KeepsTheDeclaredAddresses(t *testing.T) {
	for _, variable := range e2eVariables {
		t.Setenv(variable, "http://127.0.0.1:1/impostor")
	}

	registry, err := assembleAdapters()
	if err != nil {
		t.Fatalf("装配 Adapter 失败：%v", err)
	}

	for service, want := range map[string]string{
		github.Service:                  github.DefaultBaseURL,
		cloudflare.Service:              cloudflare.DefaultBaseURL,
		string(model.ProviderOpenAI):    model.DefaultOpenAIBaseURL,
		string(model.ProviderAnthropic): model.DefaultAnthropicBaseURL,
	} {
		adapter, lookupErr := registry.Adapter(service)
		if lookupErr != nil {
			t.Errorf("找不到 %s 的 Adapter：%v", service, lookupErr)
			continue
		}
		if got := adapter.BaseURL(); got != want {
			t.Errorf("%s 的出站地址是 %q，期望 %q", service, got, want)
		}
	}
}
