package orchestration_test

import (
	"encoding/json"
	"testing"

	adapters "github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/core/intent"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * 连接服务的用例（关闭 R-24）。
 *
 * 这里问的是「落库的那份声明，决策链路读得懂吗」—— 编码与解码分家之后，
 * 少一个字段不会报错，只会让某一次决策悄悄少一项输入。
 */

func TestEnsureDeclared_WritesADeclarationTheCatalogCanRead(t *testing.T) {
	all := fixtures.NewGateway(t)

	if err := all.Services.Declarer.EnsureDeclared(t.Context(), fixtures.DefaultServiceLabel); err != nil {
		t.Fatalf("声明失败：%v", err)
	}

	declarations, err := all.Declarations.EnabledDeclarations(t.Context(), 200)
	if err != nil {
		t.Fatalf("读取声明失败：%v", err)
	}
	if len(declarations) != 1 {
		t.Fatalf("落库了 %d 条声明，期望 1 条", len(declarations))
	}

	// 决策链路真的读得懂它 —— 这一条不成立的话，连接之后每次调用仍然
	// capability_not_offered，而那正是 R-24 的表现。
	catalog, err := intent.NewCatalog(declarations)
	if err != nil {
		t.Fatalf("落库的声明解析不出目录：%v", err)
	}
	if _, found := catalog.ToolFor(fixtures.DefaultServiceLabel, operationOf(t, declarations[0])); !found {
		t.Error("目录里查不到这个服务的操作")
	}
}

func TestEnsureDeclared_DerivesEverythingFromTheAdapter(t *testing.T) {
	all := fixtures.NewGateway(t)

	if err := all.Services.Declarer.EnsureDeclared(t.Context(), fixtures.DefaultServiceLabel); err != nil {
		t.Fatalf("声明失败：%v", err)
	}
	declaration, err := all.Declarations.DeclarationByService(t.Context(), fixtures.DefaultServiceLabel)
	if err != nil {
		t.Fatalf("读取声明失败：%v", err)
	}

	if declaration.Status != adapters.StatusEnabled {
		t.Errorf("声明状态为 %q，连接出来的服务应当启用", declaration.Status)
	}
	if declaration.BaseURL == "" {
		t.Error("声明没有出站地址")
	}
	if declaration.AuthScheme == "" {
		t.Error("声明没有注入形式")
	}
	if declaration.DefaultRiskLevel == "" {
		t.Error("声明没有兜底风险等级 —— REQ-ADAPTER-005 AC2 要求它非空")
	}

	// 白名单不是空的，且与能力声明对得上：两份清单分开维护迟早会出现
	// 「能力里有、白名单里没有」的组合，那时操作会在执行的最后一步才被拒。
	var paths, methods []string
	decode(t, declaration.AllowedPaths, &paths)
	decode(t, declaration.AllowedMethods, &methods)
	if len(paths) == 0 || len(methods) == 0 {
		t.Fatalf("白名单为空：路径 %v，方法 %v", paths, methods)
	}

	var capabilities []intent.Capability
	decode(t, declaration.Capabilities, &capabilities)
	for _, capability := range capabilities {
		if !contains(paths, capability.Path) {
			t.Errorf("能力 %s 的路径 %q 不在白名单里", capability.Tool, capability.Path)
		}
	}
}

// TestEnsureDeclared_IsIdempotent：声明是审计里历史请求的解释依据，
// 每次连接都覆盖一遍会让「当时允许的是什么」随最近一次连接而变。
func TestEnsureDeclared_IsIdempotent(t *testing.T) {
	all := fixtures.NewGateway(t)

	for range 3 {
		if err := all.Services.Declarer.EnsureDeclared(
			t.Context(), fixtures.DefaultServiceLabel); err != nil {
			t.Fatalf("声明失败：%v", err)
		}
	}

	declarations, err := all.Declarations.EnabledDeclarations(t.Context(), 200)
	if err != nil {
		t.Fatalf("读取声明失败：%v", err)
	}
	if len(declarations) != 1 {
		t.Errorf("声明了 %d 次落库 %d 条", 3, len(declarations))
	}
}

// TestEnsureDeclared_AnUnknownServiceIsRefused：没有 Adapter 的服务
// 连上了也执行不了，不该写一份空声明占着位置。
func TestEnsureDeclared_AnUnknownServiceIsRefused(t *testing.T) {
	all := fixtures.NewGateway(t)

	err := all.Services.Declarer.EnsureDeclared(t.Context(), "myspace")
	if err == nil {
		t.Fatal("认不出的服务被声明成功了")
	}
	if apperr.CodeOf(err) == apperr.CodeInternal {
		t.Errorf("错误码为 internal，说不清是什么问题：%v", err)
	}

	declarations, listErr := all.Declarations.EnabledDeclarations(t.Context(), 200)
	if listErr != nil {
		t.Fatalf("读取声明失败：%v", listErr)
	}
	if len(declarations) != 0 {
		t.Errorf("认不出的服务留下了 %d 条声明", len(declarations))
	}
}

func operationOf(t *testing.T, declaration adapters.Declaration) string {
	t.Helper()

	var capabilities []intent.Capability
	decode(t, declaration.Capabilities, &capabilities)
	if len(capabilities) == 0 {
		t.Fatal("声明里没有任何能力")
	}
	return capabilities[0].Operation
}

func decode(t *testing.T, text string, into any) {
	t.Helper()

	if err := json.Unmarshal([]byte(text), into); err != nil {
		t.Fatalf("解析 %q 失败：%v", text, err)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
