//go:build e2e

package cli

import (
	"testing"

	"github.com/Runcoor/opendelo/internal/adapter/cloudflare"
	"github.com/Runcoor/opendelo/internal/adapter/github"
	"github.com/Runcoor/opendelo/internal/adapter/model"
)

/*
 * `-tags e2e` 这一侧的对照。
 *
 * E2E 的全部结论都建立在「出站真的去了假服务」之上：这一条不成立时，
 * 用例要么访问真实外部服务（`.claude/rules/security.md` §13.5 禁止），
 * 要么在超时后失败而看不出原因。
 */

func TestAssembleAdapters_UnderTheE2ETag_PointsAtTheFakes(t *testing.T) {
	fakes := map[string]string{
		github.Service:                  "http://127.0.0.1:19001",
		cloudflare.Service:              "http://127.0.0.1:19002",
		string(model.ProviderOpenAI):    "http://127.0.0.1:19003",
		string(model.ProviderAnthropic): "http://127.0.0.1:19004",
	}
	for variable, service := range map[string]string{
		"OPENDELO_E2E_GITHUB_BASE_URL":     github.Service,
		"OPENDELO_E2E_CLOUDFLARE_BASE_URL": cloudflare.Service,
		"OPENDELO_E2E_OPENAI_BASE_URL":     string(model.ProviderOpenAI),
		"OPENDELO_E2E_ANTHROPIC_BASE_URL":  string(model.ProviderAnthropic),
	} {
		t.Setenv(variable, fakes[service])
	}

	registry, err := assembleAdapters()
	if err != nil {
		t.Fatalf("装配 Adapter 失败：%v", err)
	}

	for service, want := range fakes {
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

// TestAssembleAdapters_UnderTheE2ETag_WithoutOverrides_StillPointsOutside：
// 没设环境变量的服务会去真实地址。用例据此知道「忘了指某个服务」不会静默通过。
func TestAssembleAdapters_UnderTheE2ETag_WithoutOverrides_StillPointsOutside(t *testing.T) {
	registry, err := assembleAdapters()
	if err != nil {
		t.Fatalf("装配 Adapter 失败：%v", err)
	}

	adapter, err := registry.Adapter(github.Service)
	if err != nil {
		t.Fatalf("找不到 GitHub 的 Adapter：%v", err)
	}
	if got := adapter.BaseURL(); got != github.DefaultBaseURL {
		t.Errorf("未覆盖时的出站地址是 %q，期望 %q", got, github.DefaultBaseURL)
	}
}
