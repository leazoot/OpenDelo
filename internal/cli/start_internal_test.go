package cli

import (
	"context"
	"flag"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/config"
)

func TestCommandLineOverrides_OnlyIncludesExplicitlyGivenFlags(t *testing.T) {
	// 没给出的参数必须保持 nil：flag 的零值一旦被当成「用户的意见」，
	// 就会把配置文件里的端口盖成 0、把日志级别盖成空串，四层优先级也就失效了。
	set := flag.NewFlagSet("start", flag.ContinueOnError)
	listenAddress := set.String("listen-address", "", "")
	webAPIPort := set.Int("web-api-port", 0, "")
	logLevel := set.String("log-level", "", "")

	if err := set.Parse([]string{"--web-api-port", "9001"}); err != nil {
		t.Fatalf("解析参数失败：%v", err)
	}

	overrides := commandLineOverrides(set, portFlags{
		listenAddress: listenAddress, webAPIPort: webAPIPort, logLevel: logLevel,
	})

	if overrides.ListenAddress != nil {
		t.Errorf("未给出的 listen-address 变成了 %q", *overrides.ListenAddress)
	}
	if overrides.LogLevel != nil {
		t.Errorf("未给出的 log-level 变成了 %q", *overrides.LogLevel)
	}
	if overrides.WebAPIPort == nil {
		t.Fatal("显式给出的 web-api-port 没有被收集")
	}
	if *overrides.WebAPIPort != 9001 {
		t.Errorf("web-api-port 为 %d，期望 9001", *overrides.WebAPIPort)
	}
}

// TestGatewayRun_Cancelled_StopsAcceptingBeforeShuttingDown 守住关停顺序。
//
// REQ-GATEWAY-003 AC1 要的是「停止接受新的受保护请求」。优雅关闭那几秒里 8788 与
// 8789 上新到的请求如果还能走完决策链路并拿到 Lease，用户按下 Ctrl-C 之后网关
// 其实还在放行 —— 顺序颠倒不会让任何别的用例变红，只能在这里守。
func TestGatewayRun_Cancelled_StopsAcceptingBeforeShuttingDown(t *testing.T) {
	gateway := assembledForTest(t)

	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- gateway.Run(ctx, discardLogger(), "test") }()

	waitUntil(t, gateway.Availability.Serving, "网关没有进入服务状态")

	cancel()
	if err := <-served; err != nil {
		t.Fatalf("Run 返回错误：%v", err)
	}
	if gateway.Availability.Serving() {
		t.Error("Run 返回后网关仍然在服务状态")
	}
	if err := gateway.Availability.Check(); !apperr.Is(err, apperr.CodeGatewayUnavailable) {
		t.Errorf("关停后的 Check 返回 %v，期望 gateway_unavailable", err)
	}
}

/*
 * 有人开着 Console 时也要关得掉（回归）。
 *
 * SSE 是一条长连接：处理器在事件流上等着，一等就是几十分钟。而
 * `http.Server.Shutdown` 只等处理器返回，不会去取消它们的 context ——
 * 于是一条订阅着的 Console 会把优雅关闭拖满整个余量（5 秒），Run 带着
 * `context deadline exceeded` 返回，进程以退出码 1 结束。
 *
 * 用户看到的是：开着界面的时候按 Ctrl-C，网关报错退出。
 * 由 Firefox 上的兼容性用例先撞出来 —— 它比另外两个引擎更晚放开连接。
 */
func TestGatewayRun_WithAConsoleWatchingTheEventStream_StillShutsDownCleanly_Regression(t *testing.T) {
	gateway := assembledForTest(t)

	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- gateway.Run(ctx, discardLogger(), "test") }()

	waitUntil(t, gateway.Availability.Serving, "网关没有进入服务状态")

	// 直接订阅广播器：这与 Console 连上 `GET /v1/events` 之后处理器所处的状态
	// 是同一个 —— 一条挂在事件流上、等着下一条事件的连接。
	events, unsubscribe := gateway.events.Subscribe()
	t.Cleanup(unsubscribe)

	cancel()
	if err := <-served; err != nil {
		t.Fatalf("有人订阅着事件流时 Run 返回错误：%v", err)
	}

	// 关停必须把订阅一起结束掉，否则那条连接会一直挂着。
	// 给个上限而不是直接收：没关掉的话这里会永远等下去，而一个挂住的用例
	// 说不出是哪一条断言不成立。
	select {
	case _, open := <-events:
		if open {
			t.Error("关停之后订阅仍然是开的")
		}
	case <-time.After(time.Second):
		t.Error("关停之后订阅没有被结束 —— 那条连接会把优雅关闭拖满整个余量")
	}
}

func TestGatewayRun_AlreadyStopped_RefusesToServe(t *testing.T) {
	// Availability 的停止是单向的（REQ-GATEWAY-003 AC3）。一个已经停掉的网关
	// 再被 Run 一次必须失败，而不是监听着端口却对每个请求都回绝。
	gateway := assembledForTest(t)
	gateway.Availability.Stop()

	err := gateway.Run(t.Context(), discardLogger(), "test")
	if !apperr.Is(err, apperr.CodeGatewayUnavailable) {
		t.Fatalf("Run 返回 %v，期望 gateway_unavailable", err)
	}
}

func assembledForTest(t *testing.T) *Gateway {
	t.Helper()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, config.DataDirName), config.DirPermission); err != nil {
		t.Fatalf("建立数据目录失败：%v", err)
	}
	if err := os.Chmod(dir, config.DirPermission); err != nil {
		t.Fatalf("收紧配置目录权限失败：%v", err)
	}

	settings := config.Default()
	settings.Dir = dir
	settings.WebAPIPort = freeTestPort(t)
	settings.AgentProxyPort = freeTestPort(t)
	settings.MCPPort = freeTestPort(t)

	gateway, err := Assemble(t.Context(), AssembleParams{
		Config: settings, Clock: clock.System{}, Logger: discardLogger(),
		Version: "test", SessionToken: "session-token-for-tests", Console: testConsole(),
	})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	t.Cleanup(func() {
		if closeErr := gateway.Close(); closeErr != nil {
			t.Errorf("关闭失败：%v", closeErr)
		}
	})
	return gateway
}

// testConsole 是一份最小的静态资源：httpapi 要求根目录下有 index.html。
func testConsole() fs.FS {
	return fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html><html><head></head><body></body></html>")}}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func freeTestPort(t *testing.T) int {
	t.Helper()

	listener, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("挑选空闲端口失败：%v", err)
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("监听地址不是 TCP 地址：%T", listener.Addr())
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("释放端口失败：%v", err)
	}
	return address.Port
}

// waitUntil 轮询到条件成立为止，有上限。
func waitUntil(t *testing.T, condition func() bool, message string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(message)
}

func TestFaceServer_NonLoopbackAddress_IsRefusedEvenWhenConfigAllowsIt(t *testing.T) {
	// allow_non_loopback 只放行 8787：Console 可以被同一网段的浏览器访问是一回事，
	// 8788 与 8789 认的却是发给**本机进程**的 Session Key（REQ-MCP-002）。
	// 把它们开到网络上，等于让同网段任何人拿到密钥就能用这台网关的凭据。
	face := newFaceServer(faceOptions{
		Name: "agent-proxy", Address: "0.0.0.0:8788",
		Handler: http.NewServeMux(), Logger: discardLogger(),
	})

	err := face.Listen(t.Context())
	if err == nil {
		t.Fatal("非回环地址上监听成功了")
	}
	if face.Addr() != "" {
		t.Errorf("被拒之后仍然在 %s 上监听", face.Addr())
	}
}
