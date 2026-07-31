package proxy

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/logging"
)

/*
 * 代理请求的处理（8788）。
 *
 * 请求行只接受**绝对形式**（GET http://api.github.com/repos/x/y HTTP/1.1）——
 * 那是 HTTP 客户端在 HTTP_PROXY 生效时发出的形状。相对形式的请求不是在用代理，
 * 而是有人直接把 8788 当成了一个网站，那里没有任何可以放行的东西。
 *
 * CONNECT 一律拒绝，见 refuseTunnel 的说明。
 */

// sessionHeader 是 Agent Session Key 的承载位置。
//
// 用 Proxy-Authorization 而不是自造一个头：这是 HTTP 客户端在配置了带认证的
// 代理时本来就会发的头，且它是逐跳的 —— 标准库与各语言的客户端都不会把它
// 转发到下一跳去。
const sessionHeader = "Proxy-Authorization"

// NewHandler 把 Proxy 挂成一个 HTTP 处理器。
func NewHandler(proxy *Proxy) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			refuseTunnel(w, r, proxy)
			return
		}
		proxy.serve(w, r)
	})
}

// refuseTunnel 拒绝 CONNECT。
//
// 隧道一旦建立，网关就只剩下转发字节：看不见路径、认不出操作、匹配不了 Lease、
// 也无处注入凭据。要在隧道里做这些只有一条路 —— 用本机签发的根证书拆开 TLS，
// 而那是把一张能冒充任意站点的证书装进用户的信任库，属于
// 必须先 `Decision Required` 的那一类。
//
// 因此这里的答案是拒绝，而不是「先放过去以后再说」：放过去的隧道正是
// L1 Enforced 要挡的那条直连（PRD §21）。Agent 要访问受控服务，走 MCP 面。
func refuseTunnel(w http.ResponseWriter, r *http.Request, proxy *Proxy) {
	host, _, _ := strings.Cut(r.Host, ":")
	proxy.logAccess(r, host, r.Host, Route{}, Grant{}, http.StatusForbidden, "tunnel_refused")
	writeError(w, apperr.New(apperr.CodeForbidden).
		WithDetail("8788 不建立隧道"), logging.OperationIDFrom(r.Context()))
}

// serve 跑完认证 → 认服务 → 匹配 Lease → 出站这条次序。
func (p *Proxy) serve(w http.ResponseWriter, r *http.Request) {
	operationID := logging.OperationIDFrom(r.Context())

	// 网关不服务时在这里就断掉：这一步之后的每一步都可能通向出站
	// （REQ-GATEWAY-003 AC1，成功标准 S10）。
	if err := p.availability.Check(); err != nil {
		p.refuse(w, r, hostOf(r), Route{}, err, operationID)
		return
	}

	target, err := targetOf(r)
	if err != nil {
		p.refuse(w, r, "", Route{}, err, operationID)
		return
	}

	caller, err := p.authenticator.Authenticate(r.Context(), r.Header.Get(sessionHeader))
	if err != nil {
		p.refuse(w, r, target.Host, Route{}, err, operationID)
		return
	}

	route, err := p.services.Lookup(r.Context(), target)
	if err != nil {
		p.refuse(w, r, target.Host, Route{}, err, operationID)
		return
	}

	// 请求体在匹配 Lease 之前读完：读不动的请求不该先去占用一次 Lease 计数。
	body, err := p.readBody(r)
	if err != nil {
		p.refuse(w, r, target.Host, route, err, operationID)
		return
	}

	grant, err := p.leases.Authorize(r.Context(), caller, route)
	if err != nil {
		p.refuse(w, r, target.Host, route, err, operationID)
		return
	}

	reply, err := p.exchange.Send(r.Context(), grant, route, body)
	if err != nil {
		p.refuse(w, r, target.Host, route, err, operationID)
		return
	}

	p.logAccess(r, target.Host, target.Path, route, grant, reply.StatusCode, "forwarded")
	writeReply(w, reply)
}

// refuse 记一条访问日志并写出拒绝。
//
// 出站在这条路径上没有发生过：调用它的四个位置全部在 exchange.Send 之前，
// 或者是 Send 本身返回了错误。
func (p *Proxy) refuse(
	w http.ResponseWriter, r *http.Request,
	host string, route Route, err error, operationID string,
) {
	status := statusFor(err)
	p.logAccess(r, host, pathOf(r), route, Grant{}, status, "refused")
	p.audit(r, host, route, err)

	if status == http.StatusProxyAuthRequired {
		// RFC 9110 §15.5.8：407 必须带上这个头，否则客户端不知道该怎么认证。
		w.Header().Set("Proxy-Authenticate", `OpenDelo realm="opendelo"`)
	}
	writeError(w, err, operationID)
}

// audit 记一条「直连尝试被拦下」（REQ-GATEWAY-005 AC3）。
//
// 记的是尝试本身，不区分它为什么被拦：被拦的原因（没有 Lease、超出范围、
// 域名不受控、网关离线）都属于「Agent 想直接去外部服务而没走成」，
// 而账本要回答的正是这个问题。
func (p *Proxy) audit(r *http.Request, host string, route Route, reason error) {
	blocked := Blocked{
		Target: Target{Host: host, Method: r.Method, Path: pathOf(r)},
		Route:  route,
		Reason: apperr.CodeOf(reason).String(),
	}
	if err := p.audits.RecordBlocked(r.Context(), blocked); err != nil {
		p.logger.ErrorContext(r.Context(), "拦截记录写入失败",
			slog.String("host", host), slog.String("error", err.Error()))
	}
}

// readBody 读回有上限的请求体。
func (p *Proxy) readBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, p.maxBytes+1))
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInvalidRequest, err).WithDetail("读取请求体失败")
	}
	if int64(len(body)) > p.maxBytes {
		return nil, apperr.New(apperr.CodeInvalidRequest).WithDetail("请求体超过上限")
	}
	return body, nil
}

// targetOf 从请求行取出目标端点。
func targetOf(r *http.Request) (Target, error) {
	if r.URL == nil || r.URL.Host == "" {
		return Target{}, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("8788 只接受绝对形式的代理请求")
	}
	if scheme := strings.ToLower(r.URL.Scheme); scheme != "http" && scheme != "https" {
		return Target{}, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("认不出的请求协议：" + r.URL.Scheme)
	}

	return Target{
		Host:   strings.ToLower(r.URL.Host),
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.Query(),
	}, nil
}

// hostOf 取请求行里的主机，取不到时退回 Host 头。离线路径上还没解析过目标，
// 但访问日志仍要记下 Agent 想去哪里。
func hostOf(r *http.Request) string {
	if r.URL != nil && r.URL.Host != "" {
		return strings.ToLower(r.URL.Host)
	}
	host, _, _ := strings.Cut(r.Host, ":")
	return host
}

func pathOf(r *http.Request) string {
	if r.URL == nil {
		return ""
	}
	return r.URL.Path
}

// writeReply 把外部服务的答复写回给 Agent。
//
// 只写状态码、内容类型与已脱敏的正文。外部服务的响应头不透传：那里面有
// Set-Cookie 与各种 X- 头，转发过去等于把一条本包没有检查过的通道交给 Agent。
func writeReply(w http.ResponseWriter, reply Reply) {
	contentType := reply.ContentType
	if contentType == "" {
		contentType = "application/json"
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(reply.StatusCode)
	if _, err := w.Write(reply.Body); err != nil {
		return
	}
}

// writeError 写出脱敏后的错误（REQ-ADAPTER-007 AC3：保留 operation_id）。
func writeError(w http.ResponseWriter, err error, operationID string) {
	public := apperr.PublicOf(err, operationID)
	encoded, marshalErr := json.Marshal(map[string]apperr.Public{"error": public})
	if marshalErr != nil {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusFor(err))
	if _, writeErr := w.Write(encoded); writeErr != nil {
		return
	}
}

// statusFor 把错误码折成 HTTP 状态。
//
// 认不出的码走 500 而不是 403：那是网关自己出了问题，不是一次拒绝，
// 把它说成拒绝会让账本上的「被拒绝的请求」混进故障。两者都不产生出站流量。
func statusFor(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case apperr.Is(err, apperr.CodeUnauthenticated),
		apperr.Is(err, apperr.CodeSessionExpired),
		apperr.Is(err, apperr.CodeAgentIdentityUnverifiable):
		return http.StatusProxyAuthRequired
	case apperr.Is(err, apperr.CodeForbidden),
		apperr.Is(err, apperr.CodeCredentialNotAuthorized),
		apperr.Is(err, apperr.CodeCapabilityNotOffered),
		apperr.Is(err, apperr.CodePathNotAllowed):
		return http.StatusForbidden
	case apperr.Is(err, apperr.CodeInvalidRequest):
		return http.StatusBadRequest
	case apperr.Is(err, apperr.CodeAdapterTimeout):
		return http.StatusGatewayTimeout
	case apperr.Is(err, apperr.CodeGatewayUnavailable),
		apperr.Is(err, apperr.CodeProviderUnavailable),
		apperr.Is(err, apperr.CodeVaultLocked),
		apperr.Is(err, apperr.CodeProviderLockedTimeout):
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

// CheckLoopback 校验监听地址在回环上。
//
// 8788 上的调用方是本机的 Agent 进程。
// 非回环监听意味着网络上任何一台机器都能借这个代理去用别人的授权。
func CheckLoopback(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return apperr.Wrap(apperr.CodeInvalidConfiguration, err).
			WithDetail("Agent Proxy 监听地址解析失败：" + address)
	}

	if strings.EqualFold(host, "localhost") {
		return nil
	}
	parsed := net.ParseIP(host)
	if parsed == nil || !parsed.IsLoopback() {
		return apperr.New(apperr.CodeInvalidConfiguration).
			WithDetail("Agent Proxy 只允许监听回环地址，收到 " + address)
	}
	return nil
}
