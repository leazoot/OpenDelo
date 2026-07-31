package proxy

import (
	"context"
	"log/slog"
	"net/url"

	"github.com/Runcoor/opendelo/internal/core/gateway"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * Agent HTTP Proxy 的接线（8788，REQ-PROXY-001/002）。
 *
 * 本包只做三件事：认出调用方、按固定次序问三个问题、把答复写回去。
 * 三个问题的答案都不在这里 —— 哪个域名属于受控服务由 Adapter 的声明说了算，
 * 请求在不在授权范围内由 core/lease 说了算，凭据注入发生在 adapter 里
 * （出站请求只能从 adapter 包发出）。
 *
 * **次序本身就是这一层的契约**，也是这里唯一会坏掉的东西：
 * 认证 → 认服务 → 匹配 Lease → 出站。前一步不成立就不进入下一步，
 * 因此「无有效 Lease 时不产生任何出站流量」（REQ-PROXY-002 AC1）
 * 是结构上的性质，而不是某个分支里记得写的一句 return。
 */

// Caller 是认证出来的调用方。
//
// 与 mcpsrv.Caller 同形但不共用类型：两个接入面的认证各自独立，
// 共用一个类型迟早会变成共用一条认证路径。
type Caller struct {
	AgentID     string
	WorkspaceID string
}

// Authenticator 用 Agent Session Key 认出调用方。
//
// 认不出必须返回错误，不得返回一个空的 Caller ——「无法识别 Agent」是
// Fail Closed 的第一条。
type Authenticator interface {
	Authenticate(ctx context.Context, sessionKey string) (Caller, error)
}

// Target 是 Agent 想访问的外部端点，取自代理请求行。
type Target struct {
	// Host 是绝对形式请求行里的主机，可能带端口。
	Host   string
	Method string
	Path   string
	Query  url.Values
}

// Route 是 Target 对应的受控操作。
//
// Resource 是从路径里认出来的资源维度（owner、repo、zone…），
// 由 Lease 匹配用来判断这次请求落不落在已授权的范围内。
type Route struct {
	Service   string
	Operation string
	Resource  map[string]string
}

// Services 把一个外部端点认成受控服务的一次操作。
//
// 认不出即该域名不受本网关管辖：实现必须返回错误而不是一个空 Route。
// 默认拒绝是 REQ-PROXY-002 AC3 的默认分支，也是本包唯一支持的分支 ——
// 「直通」需要一条从 transport 直接发出的出站请求，而那条路被
// 出站请求只能从 adapter 包发出。放开它属于安全等级 L0，
// 见 REQ-GATEWAY-005。
type Services interface {
	Lookup(ctx context.Context, target Target) (Route, error)
}

// Grant 是一次已经匹配上并计过量的授权。
type Grant struct {
	LeaseID    string
	IdentityID string
}

// Leases 匹配一条覆盖本次请求的 Lease 并记一次使用。
//
// 匹配与计量必须在实现里一并完成：先答「可以」再另找地方计数，
// 两者之间就有一个能被并发穿过的窗口。请求超出 Lease 范围时返回错误，
// **不做部分放行**（REQ-PROXY-002 AC2）—— 那样的请求要重新进决策链路。
type Leases interface {
	Authorize(ctx context.Context, caller Caller, route Route) (Grant, error)
}

// Reply 是外部服务的答复，已经过 Adapter 的脱敏（REQ-ADAPTER-007）。
type Reply struct {
	StatusCode  int
	ContentType string
	Body        []byte
}

// Exchange 经 Adapter 注入凭据并完成一次出站请求。
//
// 凭据不出现在本包的任何签名里：明文只以 secret.Value 在 credential 与
// adapter 两个包之间流转。
// 本包只负责**触发**注入，注入本身在 adapter 里发生。
type Exchange interface {
	Send(ctx context.Context, grant Grant, route Route, body []byte) (Reply, error)
}

// Blocked 是一次被拦下的直连尝试，交给审计（REQ-GATEWAY-005 AC3）。
//
// 只有元数据，没有请求体、响应体与任何请求头 —— 账本记的是
// 「谁在什么时候想去哪里，被什么理由拦下」（PRD §22.1）。
type Blocked struct {
	Caller Caller
	Target Target
	Route  Route
	// Reason 是拦截理由的错误码名，例如 credential_not_authorized。
	Reason string
}

// Audits 记录被拦下的直连尝试。
//
// 记不下来不改变拦截结果：请求已经被拒了，审计写入失败只会让账本缺一条，
// 而那要让运维知道 —— 因此实现返回的错误由本包写进日志，不吞掉
type Audits interface {
	RecordBlocked(ctx context.Context, blocked Blocked) error
}

// Options 是 Proxy 的依赖，全部必填。
type Options struct {
	// Availability 是网关自身的可用状态（REQ-GATEWAY-003）。
	// 不服务时每个请求在认证之前就被回绝，出站因此不可能发生。
	Availability  *gateway.Availability
	Authenticator Authenticator
	Services      Services
	Leases        Leases
	Exchange      Exchange
	Audits        Audits
	Logger        *slog.Logger
	// MaxRequestBytes 为零时用 DefaultMaxRequestBytes。
	MaxRequestBytes int64
}

// DefaultMaxRequestBytes 是 Agent 请求体的上限。
//
// 与响应体上限同理：一个无界的请求体
// 足以让网关内存耗尽，而这个接入面的调用方按威胁模型就是不可信的。
const DefaultMaxRequestBytes int64 = 4 << 20

// Proxy 是 8788 面的处理逻辑。
type Proxy struct {
	availability  *gateway.Availability
	authenticator Authenticator
	services      Services
	leases        Leases
	exchange      Exchange
	audits        Audits
	logger        *slog.Logger
	maxBytes      int64
}

// New 校验依赖并构造 Proxy。
//
// 缺任何一项都拒绝构造：一个没有认证器或没有 Lease 匹配的代理，
// 等于把本机任意进程的出站请求都当成已获授权。
func New(options Options) (*Proxy, error) {
	switch {
	case options.Availability == nil:
		return nil, missing("可用状态")
	case options.Authenticator == nil:
		return nil, missing("认证器")
	case options.Services == nil:
		return nil, missing("受控服务解析")
	case options.Leases == nil:
		return nil, missing("Lease 匹配")
	case options.Exchange == nil:
		return nil, missing("出站通道")
	case options.Audits == nil:
		return nil, missing("审计入口")
	case options.Logger == nil:
		return nil, missing("日志")
	}

	maxBytes := options.MaxRequestBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxRequestBytes
	}
	return &Proxy{
		availability:  options.Availability,
		authenticator: options.Authenticator,
		services:      options.Services,
		leases:        options.Leases,
		exchange:      options.Exchange,
		audits:        options.Audits,
		logger:        options.Logger,
		maxBytes:      maxBytes,
	}, nil
}

func missing(what string) error {
	return apperr.New(apperr.CodeInternal).WithDetail("Agent Proxy 缺少" + what)
}
