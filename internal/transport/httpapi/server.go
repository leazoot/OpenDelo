package httpapi

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/config"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
)

// 连接级超时。这几个值不进配置：它们防的是「连接建立后不发完整请求头」这类占用，
// 属于服务端自我保护，调小会误伤正常请求，调大等于关掉保护，没有让用户调整的理由。
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	idleTimeout       = 120 * time.Second
	maxHeaderBytes    = 1 << 20
	// 关闭时给在途请求的余量。
	shutdownGrace = 5 * time.Second
)

// Options 是构造 Server 所需的一切，全部必填。
type Options struct {
	// Config 必须已经过 Validate，New 会再校验一次。
	Config config.Config
	// Console 是内嵌的静态资源，根目录下必须有 index.html。
	Console fs.FS
	// Clock 提供启动时间，注入以便用例断言固定值。
	Clock clock.Clock
	// Logger 承载运行日志，已按 platform/logging 的规则脱敏。
	Logger *slog.Logger
	// Version 是二进制版本号，出现在 /v1/gateway/status 中。
	Version string
	// SessionToken 是 Console 访问 /v1 的凭证（REQ-API-005），由 platform/config 生成。
	SessionToken string
	// Services 是业务端点依赖的核心组件（REQ-API-001）。
	Services Services
}

// Server 是 Console 与 Web API 的接入面（8787）。
//
// 只做协议转换：路由、序列化、安全响应头。任何「是否允许」的判断都不在这里。
type Server struct {
	config   config.Config
	logger   *slog.Logger
	http     *http.Server
	listener net.Listener
}

// New 组装路由与中间件。资源不完整或配置不合法时拒绝构造，不返回一个「能起但不对」的服务。
func New(options Options) (*Server, error) {
	if err := options.validate(); err != nil {
		return nil, err
	}
	if err := options.Config.Validate(); err != nil {
		return nil, err
	}

	console, err := newConsoleHandler(options.Console, options.SessionToken, options.Logger)
	if err != nil {
		return nil, err
	}

	guard, err := newSessionGuard(options.SessionToken, options.Config, options.Logger)
	if err != nil {
		return nil, err
	}

	status := newStatusHandler(Status{
		Status:        StatusRunning,
		Version:       options.Version,
		ListenAddress: options.Config.ListenAddress,
		WebAPIPort:    options.Config.WebAPIPort,
		StartedAt:     formatTime(options.Clock.Now()),
	}, options.Logger)

	// /v1 之下一律要过会话校验；静态资源不要求令牌，否则 Console 还没拿到令牌
	// 就连首屏都取不到。
	versioned := http.NewServeMux()
	versioned.Handle("/v1/gateway/status", allowMethods(options.Logger, status, http.MethodGet, http.MethodHead))
	registerBusinessRoutes(versioned,
		newEndpoints(options.Services, options.Services.Events, options.Logger), options.Logger)
	versioned.Handle("/v1/", notFoundHandler(options.Logger))

	// 过了会话校验的一定是 Console：这个面只认会话令牌。Agent 的调用方身份
	// 由 MCP 与 Proxy 两个面各自填入（见 caller.go）。
	mux := http.NewServeMux()
	mux.Handle("/v1/", guard.guard(withCaller(Caller{}, versioned)))
	mux.Handle("/", allowMethods(options.Logger, console, http.MethodGet, http.MethodHead))

	handler := withSecurityHeaders(withOperationID(ulid.New(options.Clock), options.Logger, mux))

	return &Server{
		config: options.Config,
		logger: options.Logger,
		http: &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: readHeaderTimeout,
			ReadTimeout:       readTimeout,
			IdleTimeout:       idleTimeout,
			MaxHeaderBytes:    maxHeaderBytes,
			// net/http 默认把连接层错误写进标准库 log，绕开脱敏与结构化字段。
			ErrorLog: slog.NewLogLogger(options.Logger.Handler(), slog.LevelWarn),
			// WriteTimeout 留空：SSE（/v1/events，S5）是长连接，写超时会把它掐断。
		},
	}, nil
}

func (o Options) validate() error {
	missing := ""
	switch {
	case o.Console == nil:
		missing = "Console"
	case o.Clock == nil:
		missing = "Clock"
	case o.Logger == nil:
		missing = "Logger"
	case o.Version == "":
		missing = "Version"
	case o.SessionToken == "":
		missing = "SessionToken"
	}
	if missing != "" {
		return apperr.New(apperr.CodeInvalidConfiguration).WithDetail("httpapi.Options." + missing + " 未提供")
	}
	return o.Services.validate()
}

// Handler 返回完整的处理链，供契约测试直接驱动，不必真的占用端口。
func (s *Server) Handler() http.Handler { return s.http.Handler }

// Listen 绑定监听端口。
//
// 端口被占用时返回错误并保持未监听状态：不静默换端口（REQ-GATEWAY-001 AC2）——
// 换了端口，Console 与 CLI 就都连不上了，而用户以为一切正常。
func (s *Server) Listen(ctx context.Context) error {
	address := net.JoinHostPort(s.config.ListenAddress, strconv.Itoa(s.config.WebAPIPort))
	listener, err := new(net.ListenConfig).Listen(ctx, "tcp", address)
	if err != nil {
		return apperr.Wrap(apperr.CodeInvalidConfiguration, err).
			WithDetail("无法在 " + address + " 上监听。端口被占用时不会自动改用其他端口，请释放该端口或修改配置")
	}
	s.listener = listener
	return nil
}

// Addr 返回实际监听的地址，未监听时为空字符串。
//
// 配置里的端口可能是 0（由内核挑选），所以要问监听器而不是问配置。
func (s *Server) Addr() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Serve 处理请求直到 ctx 被取消，随后优雅关闭。必须先调用 Listen。
func (s *Server) Serve(ctx context.Context) error {
	if s.listener == nil {
		return apperr.New(apperr.CodeInternal).WithDetail("Serve 在 Listen 之前被调用")
	}

	served := make(chan error, 1)
	go func() { served <- s.http.Serve(s.listener) }()

	select {
	case err := <-served:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		// 用 WithoutCancel：ctx 已经结束，再从它派生的话关闭余量会立刻到期。
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
		defer cancel()
		return closeGracefully(shutdownCtx, s.http, s.logger, "Web API")
	}
}

/*
 * closeGracefully 先优雅关闭，余量用完就强制断开。
 *
 * 余量到期**不是失败**。浏览器会预开连接、也会在页面关掉之后再握一会儿手，
 * 而 `http.Server.Shutdown` 要等这些连接自己散掉。把它当成错误的后果是：
 * 开着 Console 按 Ctrl-C，网关以退出码 1 结束 —— 用户什么也没做错。
 *
 * 强制断开仍然写一条 warn：真的有处理器卡住时，这条日志是唯一的线索。
 * 由 Firefox 上的兼容性用例撞出来（它比另外两个引擎更晚放开连接）。
 */
func closeGracefully(
	ctx context.Context, server *http.Server, logger *slog.Logger, face string,
) error {
	err := server.Shutdown(ctx)
	if err == nil {
		return nil
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	logger.WarnContext(ctx, "优雅关闭的余量用完，强制断开剩余连接",
		slog.String("face", face))
	return server.Close()
}

func notFoundHandler(logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, logger, http.StatusNotFound,
			apperr.New(apperr.CodeNotFound).WithDetail("没有这个端点："+r.Method+" "+r.URL.Path))
	})
}
