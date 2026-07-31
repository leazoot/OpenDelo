package cli_test

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/cli"
	"github.com/Runcoor/opendelo/internal/platform/config"
	"github.com/Runcoor/opendelo/internal/transport/httpapi"
)

// configDirWith 写出一个指向给定端口的配置目录。
//
// 权限按 config.Load 的要求给到位，否则加载阶段就会失败，用例测不到探测那一步。
func configDirWith(t *testing.T, port int) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "opendelo")
	if err := os.Mkdir(dir, config.DirPermission); err != nil {
		t.Fatalf("创建配置目录失败：%v", err)
	}

	settings := config.Default()
	settings.WebAPIPort = port
	content, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("序列化配置失败：%v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, config.FileName), content, config.FilePermission); err != nil {
		t.Fatalf("写配置文件失败：%v", err)
	}
	if _, _, err := config.EnsureSessionToken(dir); err != nil {
		t.Fatalf("生成会话令牌失败：%v", err)
	}
	return dir
}

// sessionTokenIn 读出用例配置目录里的会话令牌，用来断言 status 确实带上了它。
func sessionTokenIn(t *testing.T, dir string) string {
	t.Helper()

	token, err := config.SessionToken(dir)
	if err != nil {
		t.Fatalf("读取会话令牌失败：%v", err)
	}
	return token
}

// serveOnLoopback 在回环上起一个测试服务，返回指向它的配置目录与它的端口。
func serveOnLoopback(t *testing.T, handler http.Handler) (string, int) {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	_, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatalf("解析测试服务地址 %q 失败：%v", server.URL, err)
	}
	number, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("端口 %q 不是数字：%v", port, err)
	}
	return configDirWith(t, number), number
}

// closedPort 返回一个当下没有人监听的端口。
func closedPort(t *testing.T) int {
	t.Helper()

	probe, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("探测端口失败：%v", err)
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

func statusHandler(t *testing.T, status httpapi.Status) http.Handler {
	t.Helper()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/gateway/status" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if err := json.NewEncoder(w).Encode(status); err != nil {
			t.Errorf("写出状态失败：%v", err)
		}
	})
}

func TestStatus_GatewayRunning_PrintsPortAndVersion(t *testing.T) {
	running := httpapi.Status{
		Status:        httpapi.StatusRunning,
		Version:       "9.9.9-gateway",
		ListenAddress: "127.0.0.1",
		StartedAt:     "2026-07-28T09:15:30.123Z",
	}
	dir, port := serveOnLoopback(t, statusHandler(t, running))

	got := execute(t, t.Context(), "status", "--config-dir", dir)

	if got.code != cli.ExitOK {
		t.Fatalf("退出码为 %d，stderr 为 %q", got.code, got.stderr)
	}
	for _, want := range []string{httpapi.StatusRunning, running.Version, strconv.Itoa(port), running.StartedAt} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("输出里没有 %q：%q", want, got.stdout)
		}
	}
}

func TestStatus_GatewayNotRunning_FailsWithActionableMessage(t *testing.T) {
	// REQ-CLI-001 AC3。
	dir := configDirWith(t, closedPort(t))

	got := execute(t, t.Context(), "status", "--config-dir", dir)

	if got.code == cli.ExitOK {
		t.Fatal("Gateway 未启动时退出码是 0")
	}
	if !strings.Contains(got.stderr, "opendelo start") {
		t.Errorf("stderr 未给出可执行的下一步：%q", got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("失败时向 stdout 写了状态：%q", got.stdout)
	}
}

func TestStatus_ForeignServiceOnThePort_IsReportedAsUnusable(t *testing.T) {
	// 端口上有东西在应答不等于 Gateway 在跑。把别人的服务认成自己的，
	// 会让「Gateway 正常」这句话变得不可信。
	responses := map[string]http.Handler{
		"不是 JSON": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if _, err := w.Write([]byte("hello from another service")); err != nil {
				t.Errorf("写出响应失败：%v", err)
			}
		}),
		// 正文是合法状态、状态码却不是 200：只看正文能不能解析，就会把
		// 「服务不可用」读成「运行中」。
		"状态码不是 200": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			statusHandler(t, httpapi.Status{Status: httpapi.StatusRunning, Version: "9.9.9"}).ServeHTTP(w, r)
		}),
		"JSON 里没有状态字段": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if _, err := w.Write([]byte(`{"unrelated":"payload"}`)); err != nil {
				t.Errorf("写出响应失败：%v", err)
			}
		}),
	}

	for name, handler := range responses {
		t.Run(name, func(t *testing.T) {
			dir, _ := serveOnLoopback(t, handler)
			got := execute(t, t.Context(), "status", "--config-dir", dir)

			if got.code == cli.ExitOK {
				t.Fatalf("退出码是 0，stdout 为 %q", got.stdout)
			}
			if !strings.Contains(got.stderr, "opendelo") {
				t.Errorf("stderr 未说明该端口上跑的可能不是 opendelo：%q", got.stderr)
			}
		})
	}
}

func TestStatus_SendsSessionTokenInHeadersOnly(t *testing.T) {
	// REQ-API-005：令牌只走请求头。URL 会进 shell 历史、进程列表与访问日志。
	seen := make(chan *http.Request, 1)
	dir, _ := serveOnLoopback(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Clone(r.Context())
		statusHandler(t, httpapi.Status{Status: httpapi.StatusRunning, Version: "9.9.9"}).ServeHTTP(w, r)
	}))

	if got := execute(t, t.Context(), "status", "--config-dir", dir); got.code != cli.ExitOK {
		t.Fatalf("退出码为 %d，stderr 为 %q", got.code, got.stderr)
	}

	request := <-seen
	token := sessionTokenIn(t, dir)

	if authorization := request.Header.Get("Authorization"); authorization != "Bearer "+token {
		t.Errorf("Authorization 为 %q", authorization)
	}
	if requestedBy := request.Header.Get(httpapi.HeaderRequestedBy); requestedBy != httpapi.RequestedByConsole {
		t.Errorf("%s 为 %q", httpapi.HeaderRequestedBy, requestedBy)
	}
	if strings.Contains(request.URL.String(), token) {
		t.Errorf("令牌出现在 URL 里：%q", request.URL.String())
	}
}

func TestStatus_WithoutSessionToken_TellsUserToRunInit(t *testing.T) {
	dir, _ := serveOnLoopback(t, statusHandler(t, httpapi.Status{Status: httpapi.StatusRunning}))
	if err := os.Remove(filepath.Join(dir, config.SessionTokenFileName)); err != nil {
		t.Fatalf("删除会话令牌失败：%v", err)
	}

	got := execute(t, t.Context(), "status", "--config-dir", dir)

	if got.code == cli.ExitOK {
		t.Fatal("没有会话令牌时退出码是 0")
	}
	if !strings.Contains(got.stderr, "opendelo init") {
		t.Errorf("stderr 未给出可执行的下一步：%q", got.stderr)
	}
}

func TestStatus_ConfigDirWithLoosePermissions_IsRejected(t *testing.T) {
	// 配置目录里将来会放会话令牌，权限不对就不该继续。
	dir := configDirWith(t, closedPort(t))
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("放松目录权限失败：%v", err)
	}

	got := execute(t, t.Context(), "status", "--config-dir", dir)

	if got.code == cli.ExitOK {
		t.Fatal("配置目录权限过松时退出码是 0")
	}
	if !strings.Contains(got.stderr, "权限") {
		t.Errorf("stderr 未说明是权限问题：%q", got.stderr)
	}
}
