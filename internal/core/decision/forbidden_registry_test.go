package decision_test

import (
	"testing"

	"github.com/Runcoor/opendelo/internal/adapter/cloudflare"
	"github.com/Runcoor/opendelo/internal/adapter/github"
	"github.com/Runcoor/opendelo/internal/adapter/model"
	adapters "github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/core/decision"
)

/*
 * 已声明的能力不得落进禁止列表（REQ-DECIDE-004 的边界，R-38 的常驻守卫）。
 *
 * 禁止列表认的是关键词，而关键词是会误伤的：`manage_token` 曾经因为
 * `manage` 与 `token` 的接缝处凑出一个 `get` 而被永久拒绝 —— 那是 PRD §12.3
 * 明确列出的高风险操作，本该由人点头。
 *
 * 于是这里把两边对起来：**凡是 Adapter 声明过的操作，都不该落进禁止列表。**
 * 禁止列表针对的是「Agent 索取凭据、导出保险库、扩权、关审计、改 OpenDelo
 * 自己」——那五件事没有任何 Adapter 会声明。反过来说，一旦某个操作名被误判，
 * 它对应的产品功能就被永久关掉了，而用户在界面上连拒绝以外的选择都没有。
 *
 * 本文件是 `_test.go`，因此不参与 `test/arch` 的依赖方向扫描 ——
 * 证明一条边界生效，恰恰要从边界的两侧同时看。
 */
func TestDecide_NoDeclaredCapabilityFallsIntoTheForbiddenList(t *testing.T) {
	registry := declaredAdapters(t)

	checked := 0
	for _, service := range registry.Services() {
		for _, operation := range registry.Operations(service) {
			t.Run(service+"/"+operation, func(t *testing.T) {
				input := lowRiskRead()
				input.Scope.Scope.Service = service
				input.Scope.Scope.Operation = operation

				outcome := decision.Decide(input)
				if outcome.Reason == decision.ReasonForbidden {
					t.Errorf("被判成 %s —— 这是 Adapter 声明过的能力，"+
						"判进禁止列表等于把它永久关掉，而用户连拒绝以外的选择都没有",
						outcome.Forbidden)
				}
			})
			checked++
		}
	}

	// 一条都没扫到就是空跑：注册表构造失败或 Operations 返回空时，
	// 上面的循环什么也不做，而用例照样绿。
	if checked < 20 {
		t.Fatalf("只检查了 %d 个操作，四个 Adapter 声明的能力远不止这些", checked)
	}
}

// declaredAdapters 装配编译期就在的四个 Adapter。
//
// 与 `internal/cli` 的 assembleAdapters 同一份名单，但不走它：那个函数是
// 未导出的，而这里要的只是「声明了哪些操作」，不需要出站地址与凭据。
func declaredAdapters(t *testing.T) *adapters.Registry {
	t.Helper()

	gitHub, err := github.New(github.Options{})
	if err != nil {
		t.Fatalf("构造 github Adapter 失败：%v", err)
	}
	cloudFlare, err := cloudflare.New(cloudflare.Options{})
	if err != nil {
		t.Fatalf("构造 cloudflare Adapter 失败：%v", err)
	}
	openAI, err := model.New(model.Options{Provider: model.ProviderOpenAI})
	if err != nil {
		t.Fatalf("构造 openai Adapter 失败：%v", err)
	}
	anthropic, err := model.New(model.Options{Provider: model.ProviderAnthropic})
	if err != nil {
		t.Fatalf("构造 anthropic Adapter 失败：%v", err)
	}

	registry, err := adapters.New(gitHub, cloudFlare, openAI, anthropic)
	if err != nil {
		t.Fatalf("装配注册表失败：%v", err)
	}
	return registry
}
