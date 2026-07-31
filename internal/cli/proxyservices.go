package cli

import (
	"context"
	"net/url"
	"strings"

	adapters "github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/transport/proxy"
)

/*
 * Proxy 的服务反查（REQ-PROXY-001）。
 *
 * 8788 上到达的是一次普通的 HTTP 请求：主机、方法、路径。要进决策链路，先得答出
 * 「这是哪个服务的哪个操作」。两步都不允许猜：
 *
 *   主机 → 服务：只认已启用声明里的 Base URL 主机，逐字比较。
 *   方法+路径 → 操作：交给 adapter 注册表按声明的路径模板反查。
 *
 * 两步任一答不出，结论都是「无法确定服务」或「Adapter 未声明能力」——
 * 两条都在 Fail Closed 的十种情况里。
 * 这里没有默认服务，也没有「未知主机直接放过去」的分支：那正是 L1 要挡的东西。
 */

// proxyServices 按已启用的 Adapter 声明反查服务与操作。
type proxyServices struct {
	declarations adapters.DeclarationRepository
	registry     *adapters.Registry
}

var _ proxy.Services = (*proxyServices)(nil)

// maxDeclarations 是一次反查最多考虑的声明数。
//
// 给个上限而不是无界查询。取 200 与分页上限一致：
// 一台机器上配到 200 个 Adapter 已经远超本产品的设想，真到那时该报错而不是慢慢扫。
const maxDeclarations = 200

// Lookup 把一次出站请求反查成服务与操作。
func (s *proxyServices) Lookup(ctx context.Context, target proxy.Target) (proxy.Route, error) {
	service, err := s.serviceFor(ctx, target.Host)
	if err != nil {
		return proxy.Route{}, err
	}

	capability, resource, err := s.registry.Resolve(service, target.Method, target.Path)
	if err != nil {
		return proxy.Route{}, err
	}
	return proxy.Route{Service: service, Operation: capability.Operation, Resource: resource}, nil
}

// serviceFor 按主机找服务。
//
// 只比主机不比路径前缀：Base URL 里的路径前缀属于出站时怎么拼，与「这是谁」无关；
// 拿它参与判断会让同一个服务的两条 Base URL 写法（带不带尾斜杠）得到不同答案。
func (s *proxyServices) serviceFor(ctx context.Context, host string) (string, error) {
	if host == "" {
		return "", apperr.New(apperr.CodeCapabilityNotOffered).
			WithDetail("请求没有指明目标主机")
	}

	enabled, err := s.declarations.EnabledDeclarations(ctx, maxDeclarations)
	if err != nil {
		return "", err
	}

	for _, declaration := range enabled {
		declaredHost, parseErr := hostOfBaseURL(declaration.BaseURL)
		if parseErr != nil {
			// 存下来的 Base URL 解析不了，说明这条声明已经不可用。跳过它，
			// 但不因此让整次反查失败 —— 别的服务不该被一条坏声明拖下水。
			// 结论仍然只可能是「找到」或「拒绝」，没有第三种。
			continue
		}
		if strings.EqualFold(declaredHost, host) {
			return declaration.Service, nil
		}
	}
	return "", apperr.New(apperr.CodeCapabilityNotOffered).
		WithDetail("没有已启用的 Adapter 负责主机 " + host)
}

func hostOfBaseURL(baseURL string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	if parsed.Host == "" {
		return "", apperr.New(apperr.CodeInvalidConfiguration).
			WithDetail("Adapter 声明的 Base URL 没有主机")
	}
	return parsed.Host, nil
}
