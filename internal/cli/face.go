package cli

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/transport/proxy"
)

/*
 * 一个接入面的监听与关闭。
 *
 * 8788 与 8789 都只导出 http.Handler，服务器与监听器由组装根提供。写成一个共用
 * 类型而不是各写一份：三个面的连接级超时、端口占用行为、关闭余量必须一致，
 * 两份实现迟早会有一处漏掉其中一项，而漏掉的那一项通常是超时。
 *
 * 8787 不走这里 —— httpapi.Server 自己带监听与优雅关闭，那是它在 S1 就有的能力。
 */

const (
	faceReadHeaderTimeout = 10 * time.Second
	faceReadTimeout       = 30 * time.Second
	faceIdleTimeout       = 120 * time.Second
	faceMaxHeaderBytes    = 1 << 20
	faceShutdownGrace     = 5 * time.Second
)

// faceOptions 是一个接入面监听所需的一切。
type faceOptions struct {
	// Name 出现在日志里，用来区分是哪个面。
	Name    string
	Address string
	Handler http.Handler
	Logger  *slog.Logger
}

// faceServer 是一个接入面的 HTTP 服务器。
type faceServer struct {
	name     string
	address  string
	logger   *slog.Logger
	http     *http.Server
	listener net.Listener
}

func newFaceServer(options faceOptions) *faceServer {
	return &faceServer{
		name: options.Name, address: options.Address, logger: options.Logger,
		http: &http.Server{
			Handler:           options.Handler,
			ReadHeaderTimeout: faceReadHeaderTimeout,
			ReadTimeout:       faceReadTimeout,
			IdleTimeout:       faceIdleTimeout,
			MaxHeaderBytes:    faceMaxHeaderBytes,
			// net/http 默认把连接层错误写进标准库 log，绕开脱敏与结构化字段。
			ErrorLog: slog.NewLogLogger(options.Logger.Handler(), slog.LevelWarn),
		},
	}
}

// Listen 绑定端口。
//
// 只在回环上监听是这两个面的硬约束：8788 与 8789 认的是 Agent 的 Session Key，
// 而那把密钥是发给本机进程的（REQ-MCP-002）。
// 端口被占用时返回错误并保持未监听状态，不静默换端口。
func (f *faceServer) Listen(ctx context.Context) error {
	if err := proxy.CheckLoopback(f.address); err != nil {
		return err
	}

	listener, err := new(net.ListenConfig).Listen(ctx, "tcp", f.address)
	if err != nil {
		return apperr.Wrap(apperr.CodeInvalidConfiguration, err).
			WithDetail("无法在 " + f.address + " 上监听 " + f.name +
				"。端口被占用时不会自动改用其他端口，请释放该端口或修改配置")
	}
	f.listener = listener
	return nil
}

// Addr 返回实际监听的地址，未监听时为空字符串。
func (f *faceServer) Addr() string {
	if f.listener == nil {
		return ""
	}
	return f.listener.Addr().String()
}

// Serve 处理请求直到 ctx 被取消，随后优雅关闭。必须先调用 Listen。
func (f *faceServer) Serve(ctx context.Context) error {
	if f.listener == nil {
		return apperr.New(apperr.CodeInternal).
			WithDetail(f.name + " 的 Serve 在 Listen 之前被调用")
	}

	served := make(chan error, 1)
	go func() { served <- f.http.Serve(f.listener) }()

	select {
	case err := <-served:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		// 用 WithoutCancel：ctx 已经结束，再从它派生的话关闭余量会立刻到期。
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), faceShutdownGrace)
		defer cancel()
		return f.http.Shutdown(shutdownCtx)
	}
}
