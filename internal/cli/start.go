package cli

import (
	"context"
	"errors"
	"flag"
	"log/slog"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/config"
	"github.com/Runcoor/opendelo/internal/platform/logging"
	"github.com/Runcoor/opendelo/web"
)

func runStart(ctx context.Context, args []string, options Options) error {
	set := newFlagSet("start", "在前台启动 Gateway，收到 SIGINT / SIGTERM 后优雅关闭。", options)
	configDir := configDirFlag(set)
	listenAddress := set.String("listen-address", "", "监听地址，默认 127.0.0.1")
	webAPIPort := set.Int("web-api-port", 0, "Console 与 Web API 端口，默认 8787")
	agentProxyPort := set.Int("agent-proxy-port", 0, "Agent Proxy 端口，默认 8788")
	mcpPort := set.Int("mcp-port", 0, "MCP over HTTP 端口，默认 8789")
	logLevel := set.String("log-level", "", "日志级别：debug / info / warn / error")
	proceed, err := parse(set, args, options)
	if err != nil || !proceed {
		return err
	}

	settings, warnings, err := config.Load(config.LoadParams{
		Dir: *configDir,
		Flags: commandLineOverrides(set, portFlags{
			listenAddress: listenAddress, webAPIPort: webAPIPort,
			agentProxyPort: agentProxyPort, mcpPort: mcpPort, logLevel: logLevel,
		}),
	})
	if err != nil {
		return err
	}
	printWarnings(options.Stderr, warnings)

	logger := logging.New(logging.Options{Level: settings.SlogLevel(), Writer: options.Stdout})

	console, err := web.ConsoleFS()
	if err != nil {
		return err
	}

	sessionToken, _, err := config.EnsureSessionToken(settings.Dir)
	if err != nil {
		return err
	}

	assembled, err := Assemble(ctx, AssembleParams{
		Config:       settings,
		Clock:        options.Clock,
		Logger:       logger,
		Version:      options.Version,
		SessionToken: sessionToken,
		Console:      console,
	})
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := assembled.Close(); closeErr != nil {
			logger.ErrorContext(ctx, "关闭时有资源未能释放", slog.String("error", closeErr.Error()))
		}
	}()

	return assembled.Run(ctx, logger, options.Version)
}

// Run 监听并服务，直到 ctx 被取消。
//
// 关停顺序是 `Availability.Stop` 在前、HTTP 优雅关闭在后（REQ-GATEWAY-003 AC1）。
// 反过来的话，优雅关闭那几秒里新到的请求会被正常受理并放行 —— 而那时用户
// 已经按下了 Ctrl-C，网关在他看来已经停了。
func (g *Gateway) Run(ctx context.Context, logger *slog.Logger, version string) error {
	if err := g.Web.Listen(ctx); err != nil {
		return err
	}
	if err := g.AgentProxy.Listen(ctx); err != nil {
		return err
	}
	if err := g.MCP.Listen(ctx); err != nil {
		return err
	}
	if !g.Availability.Serve() {
		return apperr.New(apperr.CodeGatewayUnavailable).
			WithDetail("网关在开始服务前已经被停止")
	}
	logger.InfoContext(ctx, "Gateway 已启动",
		slog.String("web_api", g.Web.Addr()),
		slog.String("agent_proxy", g.AgentProxy.Addr()),
		slog.String("mcp", g.MCP.Addr()),
		slog.String("version", version))

	// 三个面共用一个可取消的 ctx：任一个先退出，另外两个也跟着停。
	// 不这么写的话，一个面异常退出之后进程会停在「等另外两个」上，
	// 而它们要等到用户按 Ctrl-C 才会退 —— 网关此时既没在服务也没有退出。
	serving, stopFaces := context.WithCancel(ctx)
	defer stopFaces()

	// 到期清扫与三个面同生共死：ctx 一结束它自己退出（REQ-CAP-003）。
	// 不进 served：它没有「异常退出」这一说，停下来就是该停了。
	swept := make(chan struct{})
	go func() {
		defer close(swept)
		sweepApprovals(serving, g.expiry, logger)
	}()

	const faces = 3
	served := make(chan error, faces)
	go func() { served <- g.Web.Serve(serving) }()
	go func() { served <- g.AgentProxy.Serve(serving) }()
	go func() { served <- g.MCP.Serve(serving) }()

	// 任一个面先退出都意味着该停了：一个只剩两个面在服务的网关，用户看不出
	// 少了哪一个，而少掉的那个如果是 8788，Agent 的出站就直接失败了。
	closing := make([]error, 0, faces)
	pending := faces
	select {
	case err := <-served:
		closing = append(closing, err)
		pending--
	case <-ctx.Done():
		logger.InfoContext(ctx, "Gateway 停止接受新请求，正在关闭")
	}

	// 先停止受理，再等优雅关闭走完（REQ-GATEWAY-003 AC1）。
	g.Availability.Stop()
	// 事件流在优雅关闭**之前**结束：SSE 的处理器在广播器上等着，而
	// `http.Server.Shutdown` 只等处理器返回、不会取消它们的 context。
	// 不先收掉的话，一条开着的 Console 会把关闭拖满整个余量，
	// 进程带着「context deadline exceeded」以退出码 1 结束。
	g.events.Close()
	stopFaces()
	for range pending {
		closing = append(closing, <-served)
	}
	// 等清扫也停下来：进程退出时留一个还在写数据库的 goroutine，
	// 下一次启动会撞上一个没关干净的连接（`.claude/rules/backend.md` §11.5）。
	<-swept
	return errors.Join(closing...)
}

// commandLineOverrides 只收集显式给出的参数。
//
// 未给出的参数保持 nil，否则 flag 的零值会盖掉配置文件里的设置
// （配置优先级顺序）。
// portFlags 是 start 的全部端口与地址参数。
//
// 打成一个结构体而不是继续加形参：三个面各有一个端口，逐个往下传会让签名越来越长，
// 而漏传其中一个不会有编译错误 —— 那个面就会静默地用默认端口。
type portFlags struct {
	listenAddress  *string
	webAPIPort     *int
	agentProxyPort *int
	mcpPort        *int
	logLevel       *string
}

func commandLineOverrides(set *flag.FlagSet, flags portFlags) config.Overrides {
	var overrides config.Overrides
	set.Visit(func(given *flag.Flag) {
		switch given.Name {
		case "listen-address":
			overrides.ListenAddress = flags.listenAddress
		case "web-api-port":
			overrides.WebAPIPort = flags.webAPIPort
		case "agent-proxy-port":
			overrides.AgentProxyPort = flags.agentProxyPort
		case "mcp-port":
			overrides.MCPPort = flags.mcpPort
		case "log-level":
			overrides.LogLevel = flags.logLevel
		}
	})
	return overrides
}
