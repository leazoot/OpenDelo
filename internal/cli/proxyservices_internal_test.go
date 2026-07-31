package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/adapter/github"
	adapters "github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/transport/proxy"
)

/*
 * Proxy 服务反查的用例（REQ-PROXY-001）。
 *
 * 重点全在「答不出的时候」。答得出是容易的；难的是 8788 上会到达任意主机的
 * 任意路径，而其中绝大多数**不该**被反查成任何服务。一个宽松的反查会让
 * 未声明的请求带着凭据出去，而那时决策链路以为自己在授权一个已声明的操作。
 */

// stubDeclarations 是一份内存里的声明表。
//
// 不用真数据库：这里要测的是反查逻辑，而落库与读取有 store 自己的用例。
type stubDeclarations struct {
	items []adapters.Declaration
	err   error
	limit int
}

func (s *stubDeclarations) EnabledDeclarations(_ context.Context, limit int) ([]adapters.Declaration, error) {
	s.limit = limit
	return s.items, s.err
}

func (s *stubDeclarations) CreateDeclaration(context.Context, adapters.Declaration) (adapters.Declaration, error) {
	panic("反查不该写声明")
}

func (s *stubDeclarations) DeclarationByID(context.Context, string) (adapters.Declaration, error) {
	panic("反查不该按主键取声明")
}

func (s *stubDeclarations) DeclarationByService(context.Context, string) (adapters.Declaration, error) {
	panic("反查不该按服务名取声明")
}

func (s *stubDeclarations) SetDeclarationStatus(
	context.Context, string, adapters.Status, time.Time,
) (adapters.Declaration, error) {
	panic("反查不该改声明状态")
}

func (s *stubDeclarations) UpdateDeclaration(
	context.Context, adapters.Declaration,
) (adapters.Declaration, error) {
	panic("反查不该改声明")
}

func newProxyServices(t *testing.T, declarations *stubDeclarations) *proxyServices {
	t.Helper()

	adapter, err := github.New(github.Options{})
	if err != nil {
		t.Fatalf("构造 GitHub Adapter 失败：%v", err)
	}
	registry, err := adapters.New(adapter)
	if err != nil {
		t.Fatalf("构造注册表失败：%v", err)
	}
	return &proxyServices{declarations: declarations, registry: registry}
}

func githubDeclarations() *stubDeclarations {
	return &stubDeclarations{items: []adapters.Declaration{{
		Service: github.Service, Kind: adapters.KindGitHub,
		BaseURL: github.DefaultBaseURL, Status: adapters.StatusEnabled,
	}}}
}

func TestProxyLookup_DeclaredOperation_IsResolvedWithItsResourceDimensions(t *testing.T) {
	services := newProxyServices(t, githubDeclarations())

	route, err := services.Lookup(t.Context(), proxy.Target{
		Host: "api.github.com", Method: "GET", Path: "/repos/runcoor/opendelo",
	})
	if err != nil {
		t.Fatalf("反查失败：%v", err)
	}

	if route.Service != github.Service {
		t.Errorf("服务为 %q，期望 %q", route.Service, github.Service)
	}
	if route.Operation == "" {
		t.Error("没有反查出操作名 —— 决策链路拿不到能力声明")
	}
	// 资源维度是 Scope 收敛的输入。取不出来的话 Scope 定不下来，请求会被拒 ——
	// 拒得对，但原因是反查漏了东西，那种错很难在账本上看出来。
	if route.Resource["owner"] != "runcoor" || route.Resource["repo"] != "opendelo" {
		t.Errorf("资源维度为 %v，期望 owner=runcoor repo=opendelo", route.Resource)
	}
}

func TestProxyLookup_Unresolvable_IsRefusedWithoutGuessing(t *testing.T) {
	cases := []struct {
		name   string
		target proxy.Target
	}{
		{"未声明的主机", proxy.Target{Host: "evil.example.com", Method: "GET", Path: "/repos/a/b"}},
		{"主机对但路径未声明", proxy.Target{Host: "api.github.com", Method: "GET", Path: "/admin/tokens"}},
		{"路径对但方法未声明", proxy.Target{Host: "api.github.com", Method: "PUT", Path: "/repos/a/b"}},
		{"路径穿越（段数与模板相同）", proxy.Target{Host: "api.github.com", Method: "GET", Path: "/repos/a/.."}},
		{"路径穿越（占位段里）", proxy.Target{Host: "api.github.com", Method: "GET", Path: "/repos/../etc"}},
		{"段数不足", proxy.Target{Host: "api.github.com", Method: "GET", Path: "/repos/a"}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			services := newProxyServices(t, githubDeclarations())

			route, err := services.Lookup(t.Context(), testCase.target)
			if !apperr.Is(err, apperr.CodeCapabilityNotOffered) {
				t.Fatalf("错误码为 %s，期望 capability_not_offered（%v）", apperr.CodeOf(err), err)
			}
			if route.Service != "" || route.Operation != "" {
				t.Errorf("拒绝时仍然给出了路由 %+v", route)
			}
		})
	}
}

func TestProxyLookup_HostComparisonIgnoresCaseButNotPort(t *testing.T) {
	// 主机名大小写不敏感是 DNS 的性质；端口不是主机名的一部分的写法则是另一台服务。
	services := newProxyServices(t, githubDeclarations())

	if _, err := services.Lookup(t.Context(), proxy.Target{
		Host: "API.GitHub.com", Method: "GET", Path: "/repos/a/b",
	}); err != nil {
		t.Errorf("大小写不同的主机没有被认出来：%v", err)
	}
	if _, err := services.Lookup(t.Context(), proxy.Target{
		Host: "api.github.com:8443", Method: "GET", Path: "/repos/a/b",
	}); !apperr.Is(err, apperr.CodeCapabilityNotOffered) {
		t.Errorf("带了另一个端口的主机被当成了同一台服务：%v", err)
	}
}

func TestProxyLookup_BrokenDeclaration_DoesNotBlockTheOthers(t *testing.T) {
	// 一条 Base URL 坏掉的声明不该让别的服务也反查不出来；但它自己也不能
	// 因此被当成「匹配任意主机」。
	declarations := githubDeclarations()
	declarations.items = append([]adapters.Declaration{{
		Service: "broken", Kind: adapters.KindGenericHTTP,
		BaseURL: "://not-a-url", Status: adapters.StatusEnabled,
	}}, declarations.items...)
	services := newProxyServices(t, declarations)

	route, err := services.Lookup(t.Context(), proxy.Target{
		Host: "api.github.com", Method: "GET", Path: "/repos/a/b",
	})
	if err != nil {
		t.Fatalf("坏声明挡住了正常的反查：%v", err)
	}
	if route.Service != github.Service {
		t.Errorf("反查到了 %q，期望 %q", route.Service, github.Service)
	}
}

func TestProxyLookup_DeclarationQuery_IsBounded(t *testing.T) {
	// 无界查询会在声明表变大时把每个请求都拖慢。
	declarations := githubDeclarations()
	services := newProxyServices(t, declarations)

	if _, err := services.Lookup(t.Context(), proxy.Target{
		Host: "api.github.com", Method: "GET", Path: "/repos/a/b",
	}); err != nil {
		t.Fatalf("反查失败：%v", err)
	}
	if declarations.limit <= 0 || declarations.limit > 200 {
		t.Errorf("声明查询的上限为 %d，期望在 1–200 之间", declarations.limit)
	}
}

func TestProxyLookup_NoHost_IsRefusedForThatReason(t *testing.T) {
	// 只断言错误码的话，这条会因为「没有任何声明匹配空主机」而通过 ——
	// 那样删掉专门的空主机检查也不会有用例变红。理由必须是它自己的。
	services := newProxyServices(t, githubDeclarations())

	_, err := services.Lookup(t.Context(), proxy.Target{Host: "", Method: "GET", Path: "/repos/a/b"})
	if !apperr.Is(err, apperr.CodeCapabilityNotOffered) {
		t.Fatalf("错误码为 %s，期望 capability_not_offered（%v）", apperr.CodeOf(err), err)
	}
	if !strings.Contains(err.Error(), "没有指明目标主机") {
		t.Errorf("拒绝理由为 %q，期望指出请求没有主机", err.Error())
	}
}
