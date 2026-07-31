package httpapi_test

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/transport/httpapi"
)

func TestNew_ConsoleAssetsWithoutIndexHTML_AreRejected(t *testing.T) {
	// 没有 index.html 就没有界面可提供。此时启动只会让用户对着 404 猜原因。
	_, err := httpapi.New(func() httpapi.Options {
		options := testOptions(t)
		options.Console = fstest.MapFS{"assets/index-abc123.js": &fstest.MapFile{Data: []byte(scriptBody)}}
		return options
	}())
	if err == nil {
		t.Fatal("缺少 index.html 时仍然构造出了服务")
	}
	if !apperr.Is(err, apperr.CodeInvalidConfiguration) {
		t.Errorf("错误码为 %v，期望 invalid_configuration", err)
	}
	if !strings.Contains(err.Error(), "make build") {
		t.Errorf("错误信息未提示如何修复：%v", err)
	}
}

func TestNew_MissingRequiredOption_IsRejected(t *testing.T) {
	for name, blank := range map[string]func(*httpapi.Options){
		"Console": func(o *httpapi.Options) { o.Console = nil },
		"Clock":   func(o *httpapi.Options) { o.Clock = nil },
		"Logger":  func(o *httpapi.Options) { o.Logger = nil },
		"Version": func(o *httpapi.Options) { o.Version = "" },
	} {
		t.Run(name, func(t *testing.T) {
			options := testOptions(t)
			blank(&options)
			if _, err := httpapi.New(options); err == nil {
				t.Errorf("%s 为空时仍然构造出了服务", name)
			}
		})
	}
}

func TestNew_NonLoopbackAddressWithoutExplicitConsent_IsRejected(t *testing.T) {
	// 监听范围是安全边界，New 不接受一个未经 Validate 的配置直接起服务。
	options := testOptions(t)
	options.Config.ListenAddress = "0.0.0.0"

	if _, err := httpapi.New(options); err == nil {
		t.Fatal("非回环监听未经显式确认就被接受了")
	}
}

// freePort 返回一个当下空闲的端口。
//
// 配置层不接受 0（「让内核挑」在生产里等于没人知道 Gateway 在哪个端口上），
// 所以要监听的用例先探一个可用端口再交给服务。
func freePort(t *testing.T) int {
	t.Helper()

	probe, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("探测空闲端口失败：%v", err)
	}
	address, ok := probe.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("监听地址类型为 %T", probe.Addr())
	}
	if err := probe.Close(); err != nil {
		t.Fatalf("释放探测端口失败：%v", err)
	}
	return address.Port
}

// dial 用带超时的拨号确认某个地址上有没有人在听。
func dial(t *testing.T, target string) (net.Conn, error) {
	t.Helper()

	dialer := net.Dialer{Timeout: time.Second}
	return dialer.DialContext(t.Context(), "tcp", target)
}

func TestListen_BindsLoopbackOnly(t *testing.T) {
	// REQ-GATEWAY-001 AC1。
	server := newServer(t, func(o *httpapi.Options) { o.Config.WebAPIPort = freePort(t) })
	if err := server.Listen(t.Context()); err != nil {
		t.Fatalf("监听失败：%v", err)
	}
	t.Cleanup(func() { shutdown(t, server) })

	host, port, err := net.SplitHostPort(server.Addr())
	if err != nil {
		t.Fatalf("监听地址 %q 无法解析：%v", server.Addr(), err)
	}
	if address := net.ParseIP(host); address == nil || !address.IsLoopback() {
		t.Fatalf("监听在 %q，期望回环地址", host)
	}

	for _, address := range nonLoopbackAddresses(t) {
		target := net.JoinHostPort(address, port)
		connection, dialErr := dial(t, target)
		if dialErr == nil {
			if closeErr := connection.Close(); closeErr != nil {
				t.Errorf("关闭连接失败：%v", closeErr)
			}
			t.Errorf("%s 上也能连上，回环之外不应有监听", target)
		}
	}
}

// nonLoopbackAddresses 返回本机所有非回环的单播地址。
//
// 容器里可能一个都没有，那时循环体不执行，本用例仍由上面的回环断言承担检查，
// 不需要 t.Skip。
func nonLoopbackAddresses(t *testing.T) []string {
	t.Helper()

	interfaces, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatalf("枚举本机地址失败：%v", err)
	}

	addresses := make([]string, 0, len(interfaces))
	for _, entry := range interfaces {
		network, ok := entry.(*net.IPNet)
		if !ok || network.IP.IsLoopback() || !network.IP.IsGlobalUnicast() {
			continue
		}
		// 链路本地 IPv6 要带 zone 才能连，跳过它不影响本用例要证明的事。
		if network.IP.To4() == nil {
			continue
		}
		addresses = append(addresses, network.IP.String())
	}
	return addresses
}

func TestListen_PortAlreadyInUse_FailsAndDoesNotFallBackToAnotherPort(t *testing.T) {
	// REQ-GATEWAY-001 AC2：静默换端口会让 Console 与 CLI 都连不上，
	// 而用户以为一切正常。
	occupied, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占位监听失败：%v", err)
	}
	t.Cleanup(func() {
		if closeErr := occupied.Close(); closeErr != nil {
			t.Errorf("关闭占位监听失败：%v", closeErr)
		}
	})

	taken, ok := occupied.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("占位监听地址类型为 %T", occupied.Addr())
	}

	server := newServer(t, func(o *httpapi.Options) { o.Config.WebAPIPort = taken.Port })
	listenErr := server.Listen(t.Context())
	if listenErr == nil {
		t.Fatal("端口被占用时仍然启动成功")
	}
	if !strings.Contains(listenErr.Error(), taken.String()) {
		t.Errorf("错误信息未指明冲突的地址：%v", listenErr)
	}
	if server.Addr() != "" {
		t.Errorf("启动失败后监听在 %q，期望没有任何监听", server.Addr())
	}
}

func TestServe_ContextCancelled_StopsServing(t *testing.T) {
	server := newServer(t, func(o *httpapi.Options) { o.Config.WebAPIPort = freePort(t) })
	if err := server.Listen(t.Context()); err != nil {
		t.Fatalf("监听失败：%v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- server.Serve(ctx) }()

	address := server.Addr()
	response, err := http.Get("http://" + address + "/v1/gateway/status") //nolint:noctx // 用例里的一次性探测
	if err != nil {
		t.Fatalf("服务未接受请求：%v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("关闭响应体失败：%v", err)
	}

	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("优雅关闭返回错误：%v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("取消 context 后服务没有关闭")
	}

	if connection, err := dial(t, address); err == nil {
		if closeErr := connection.Close(); closeErr != nil {
			t.Errorf("关闭连接失败：%v", closeErr)
		}
		t.Error("关闭后端口仍然可连")
	}
}

func TestServe_BeforeListen_IsRejected(t *testing.T) {
	if err := newServer(t, nil).Serve(t.Context()); err == nil {
		t.Fatal("未监听就调用 Serve 却没有报错")
	}
}

func shutdown(t *testing.T, server *httpapi.Server) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := server.Serve(ctx); err != nil {
		t.Errorf("关闭服务失败：%v", err)
	}
}
