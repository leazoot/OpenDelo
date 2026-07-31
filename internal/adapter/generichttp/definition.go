package generichttp

import (
	"context"
	"net"
	"net/url"
	"strings"

	"github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * 用户自定义 HTTP Adapter 的定义与校验（REQ-ADAPTER-005、PRD §18.6）。
 *
 * 这是唯一一个「内容由用户填」的 Adapter，因此校验比另外三个都严：
 *
 *  1. **不能允许任意未声明的 URL**：每个操作都要给出方法与路径模板，
 *     模板之外的路径发不出去。
 *  2. **不声明风险等级就存不下来**：没有等级的操作在决策链路上是「风险未知」，
 *     而风险未知一律拒绝 —— 那样这个 Adapter 存下来也只是个摆设。
 *  3. **Base URL 不能指向本机与内网**：Gateway 跑在用户机器上，一个指向
 *     127.0.0.1 或 192.168.0.0/16 的 Adapter 等于把 Agent 的手伸进内网（SSRF）。
 *     域名要**解析之后**再校验，否则一个解析到 127.0.0.1 的域名就绕过去了。
 */

// Resolver 把主机名解析成 IP。用例用它避免真实 DNS 查询。
type Resolver func(ctx context.Context, host string) ([]net.IP, error)

// OperationDefinition 是用户为一个操作填写的内容。
type OperationDefinition struct {
	Operation      string
	Method         string
	Path           string
	InputSchema    string
	RiskLabel      registry.RiskLabel
	RedactionRules []string
	ResponseFields []string
	Rollback       registry.Rollback
	Idempotency    registry.Idempotency
	Nature         registry.Nature
	// ResourceKeys 是最小 Scope 的资源维度。路径里的每个占位符都要在这里出现。
	ResourceKeys []string
}

// Definition 是一个用户自定义 Adapter 的完整定义。
type Definition struct {
	Service     string
	DisplayName string
	BaseURL     string
	AuthScheme  registry.AuthScheme
	AuthHeader  string
	Operations  []OperationDefinition
}

// capabilities 把用户填的内容翻成能力声明。
//
// 翻译之后交给 registry.Capability.Validate 校验：自定义 Adapter 与内置 Adapter
// 过的是同一套规则，没有「用户填的就放宽一点」这种口子。
func (d Definition) capabilities() ([]registry.Capability, error) {
	if len(d.Operations) == 0 {
		return nil, invalidDefinition("没有定义任何操作")
	}

	declared := make([]registry.Capability, 0, len(d.Operations))
	for _, operation := range d.Operations {
		capability := registry.Capability{
			Operation:      operation.Operation,
			InputSchema:    operation.InputSchema,
			MinimumScope:   registry.MinimumScope{ResourceKeys: operation.ResourceKeys, RequiresAccount: true},
			RiskLabel:      operation.RiskLabel,
			Method:         operation.Method,
			Path:           operation.Path,
			RedactionRules: operation.RedactionRules,
			ResponseFields: operation.ResponseFields,
			Rollback:       operation.Rollback,
			Idempotency:    operation.Idempotency,
			Nature:         operation.Nature,
		}
		if err := capability.Validate(); err != nil {
			return nil, err
		}
		declared = append(declared, capability)
	}
	return declared, nil
}

// validateBaseURL 校验 Base URL 的形状与目标地址。
//
// 要求 https：这个 Adapter 指向的是外部服务，而明文 HTTP 会让注入的凭据
// 在链路上可见。内网的 http 场景由上面那条 SSRF 规则挡掉，不存在冲突。
func validateBaseURL(ctx context.Context, rawURL string, resolve Resolver) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return invalidDefinitionWrap(err, "Base URL 解析失败")
	}
	if parsed.Scheme != "https" {
		return invalidDefinition("Base URL 必须是 https：" + rawURL)
	}
	host := parsed.Hostname()
	if host == "" {
		return invalidDefinition("Base URL 没有主机名：" + rawURL)
	}
	if parsed.User != nil {
		// URL 里的用户名密码会被写进日志与进程列表。
		return invalidDefinition("Base URL 不能带用户名或密码")
	}

	return checkHost(ctx, host, resolve)
}

// checkHost 解析主机名并逐个校验 IP。
//
// 字面 IP 直接校验；域名解析之后校验**每一个**返回的地址 ——
// 只看第一个的话，一个同时解析到公网与 127.0.0.1 的域名就绕过去了。
func checkHost(ctx context.Context, host string, resolve Resolver) error {
	if literal := net.ParseIP(host); literal != nil {
		return checkIP(literal, host)
	}

	addresses, err := resolve(ctx, host)
	if err != nil {
		// 解析不了就拒绝：不确定目标是谁的时候不发请求（Fail Closed）。
		return invalidDefinitionWrap(err, "主机名 "+host+" 解析失败")
	}
	if len(addresses) == 0 {
		return invalidDefinition("主机名 " + host + " 没有解析出任何地址")
	}

	for _, address := range addresses {
		if err = checkIP(address, host); err != nil {
			return err
		}
	}
	return nil
}

// blockedRanges 是不允许被指向的网段。
var blockedRanges = []string{
	"0.0.0.0/8",      // 本网络
	"10.0.0.0/8",     // 私有
	"100.64.0.0/10",  // 运营商级 NAT
	"127.0.0.0/8",    // 回环
	"169.254.0.0/16", // link-local，云上的元数据端点在这里
	"172.16.0.0/12",  // 私有
	"192.168.0.0/16", // 私有
	"::1/128",        // 回环
	"fc00::/7",       // 唯一本地地址
	"fe80::/10",      // link-local
}

var blockedNetworks = parseBlocked()

func parseBlocked() []*net.IPNet {
	networks := make([]*net.IPNet, 0, len(blockedRanges))
	for _, raw := range blockedRanges {
		_, network, err := net.ParseCIDR(raw)
		if err != nil {
			// 这张表是编译期常量，解析失败说明表本身写错了。
			panic("generichttp: 网段表写错了：" + raw)
		}
		networks = append(networks, network)
	}
	return networks
}

func checkIP(address net.IP, host string) error {
	if address.IsUnspecified() || address.IsMulticast() || address.IsInterfaceLocalMulticast() {
		return blockedTarget(host, address)
	}
	for _, network := range blockedNetworks {
		if network.Contains(address) {
			return blockedTarget(host, address)
		}
	}
	return nil
}

func blockedTarget(host string, address net.IP) error {
	return invalidDefinition("Base URL 指向本机或内网地址，已拒绝：" +
		host + " → " + address.String())
}

func (d Definition) validateShape() error {
	switch {
	case strings.TrimSpace(d.Service) == "":
		return invalidDefinition("没有给出服务名")
	case strings.TrimSpace(d.DisplayName) == "":
		return invalidDefinition("没有给出显示名")
	case d.AuthScheme != registry.AuthNone &&
		d.AuthScheme != registry.AuthBearer &&
		d.AuthScheme != registry.AuthHeader:
		return invalidDefinition("认不出的凭据注入方式：" + string(d.AuthScheme))
	case d.AuthScheme == registry.AuthHeader && strings.TrimSpace(d.AuthHeader) == "":
		return invalidDefinition("自定义请求头方案没有给出头名")
	}
	return nil
}

func invalidDefinition(detail string) error {
	return apperr.New(apperr.CodeInvalidConfiguration).
		WithDetail("自定义 HTTP Adapter 的定义不合法：" + detail)
}

func invalidDefinitionWrap(cause error, detail string) error {
	return apperr.Wrap(apperr.CodeInvalidConfiguration, cause).
		WithDetail("自定义 HTTP Adapter 的定义不合法：" + detail)
}

func defaultResolver(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}
