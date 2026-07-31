package httpapi

import (
	"crypto/subtle"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/config"
)

const (
	headerOrigin        = "Origin"
	headerAuthorization = "Authorization"
	// HeaderRequestedBy 是 Console 必须带上的自定义头。
	//
	// 简单请求发不出自定义头，浏览器会先发预检；Gateway 不下发任何 CORS 响应头，
	// 预检因此不会被放行。
	HeaderRequestedBy = "X-Requested-By"
	// RequestedByConsole 是该头唯一被接受的取值。
	RequestedByConsole = "opendelo-console"

	bearerPrefix = "Bearer "
)

// sessionGuard 守着 /v1 之下的全部端点。
//
// 三道检查对应三种攻击方式：跨源页面（Origin）、无法发自定义头的简单请求与
// 资源加载（X-Requested-By）、拿不到令牌的一切（Authorization）。
type sessionGuard struct {
	token          string
	allowedOrigins map[string]struct{}
	logger         *slog.Logger
}

func newSessionGuard(token string, settings config.Config, logger *slog.Logger) (*sessionGuard, error) {
	if token == "" {
		return nil, apperr.New(apperr.CodeInvalidConfiguration).
			WithDetail("会话令牌为空，Console 将无法访问 /v1；运行 opendelo init 生成")
	}
	return &sessionGuard{
		token:          token,
		allowedOrigins: allowedOrigins(settings),
		logger:         logger,
	}, nil
}

// allowedOrigins 列出 Console 可能出现的来源。
//
// Console 由本 Gateway 自己提供，不存在第三方来源，所以只有回环上的几种写法。
func allowedOrigins(settings config.Config) map[string]struct{} {
	port := strconv.Itoa(settings.WebAPIPort)
	hosts := []string{settings.ListenAddress, "127.0.0.1", "localhost", "::1"}

	origins := make(map[string]struct{}, len(hosts))
	for _, host := range hosts {
		origins["http://"+net.JoinHostPort(host, port)] = struct{}{}
	}
	return origins
}

// guard 把 next 包在三道检查之后。
func (g *sessionGuard) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Origin 缺失是正常的：浏览器对同源 GET 不发这个头，非浏览器客户端也不发。
		// 跨源请求要么带 Origin（在这里被挡下），要么因为自定义头触发预检而根本发不出来。
		if origin := r.Header.Get(headerOrigin); origin != "" {
			if _, allowed := g.allowedOrigins[origin]; !allowed {
				g.deny(w, r, http.StatusForbidden, apperr.New(apperr.CodeForbidden).
					WithDetail("Origin "+origin+" 不在允许列表内"))
				return
			}
		}

		if r.Header.Get(HeaderRequestedBy) != RequestedByConsole {
			g.deny(w, r, http.StatusForbidden, apperr.New(apperr.CodeForbidden).
				WithDetail("缺少 "+HeaderRequestedBy+" 头"))
			return
		}

		if !g.tokenMatches(r.Header.Get(headerAuthorization)) {
			g.deny(w, r, http.StatusUnauthorized, apperr.New(apperr.CodeUnauthenticated).
				WithDetail("会话令牌缺失或不匹配"))
			return
		}

		next.ServeHTTP(w, r)
	})
}

// tokenMatches 用定长比较判断令牌。
//
// 按字节短路的比较会把「已经猜对多少个字符」透过响应时间漏出去。
func (g *sessionGuard) tokenMatches(authorization string) bool {
	presented, found := strings.CutPrefix(authorization, bearerPrefix)
	if !found {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(g.token)) == 1
}

// deny 记录并写出拒绝。
//
// 只记录方法与路径：Authorization 的值绝不进日志，Origin 已经在 detail 里，
// 而 detail 不会出现在对外响应中。
func (g *sessionGuard) deny(w http.ResponseWriter, r *http.Request, status int, err error) {
	g.logger.WarnContext(r.Context(), "拒绝未通过会话校验的请求",
		slog.Int("status", status),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path))
	writeError(w, r, g.logger, status, err)
}
