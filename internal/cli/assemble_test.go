package cli_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/cli"
	"github.com/Runcoor/opendelo/internal/platform/config"
	"github.com/Runcoor/opendelo/web"
)

/*
 * 组装根的用例。
 *
 * 这里测的不是某一个部件，而是「它们接得上」—— 在此之前每个部件都有自己的用例，
 * 而 `opendelo start` 拿到的却是一个填不满 httpapi.Services 的空壳，跑起来必然失败，
 * 没有任何一条用例会因此变红。本文件的存在就是为了让那种情况变红。
 */

// initialized 建好配置目录与数据目录，返回配置目录路径。
//
// 走真正的 `opendelo init` 而不是自己 MkdirAll：目录权限、会话令牌、配置文件
// 都是 start 的前置条件，自己造一份等于让本文件对「init 做了什么」另有一套看法。
func initialized(t *testing.T) string {
	t.Helper()

	dir := newConfigDir(t)
	if got := execute(t, t.Context(), "init", "--config-dir", dir); got.code != cli.ExitOK {
		t.Fatalf("init 失败，退出码 %d，stderr 为 %q", got.code, got.stderr)
	}
	return dir
}

// freePort 占一个端口再放掉，返回端口号。
//
// 配置校验拒绝端口 0（`platform/config`），所以不能让内核在监听时挑；
// 只能先挑好再写进参数里。
func freePort(t *testing.T) int {
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

// ports 是一次 start 占用的三个端口。
type ports struct {
	web        int
	agentProxy int
	mcp        int
}

// startInBackground 在后台跑 start，返回端口与「等它退出」的函数。
func startInBackground(t *testing.T, dir string) (ports, func() result) {
	t.Helper()

	occupied := ports{web: freePort(t), agentProxy: freePort(t), mcp: freePort(t)}
	port := occupied.web
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan result, 1)
	go func() {
		done <- execute(t, ctx, "start",
			"--config-dir", dir,
			// 三个面各占一个端口。不指定的话用例之间会撞在默认的 8788 / 8789 上，
			// 而撞上的表现是随机的挂起，不是一条清楚的失败。
			"--web-api-port", strconv.Itoa(occupied.web),
			"--agent-proxy-port", strconv.Itoa(occupied.agentProxy),
			"--mcp-port", strconv.Itoa(occupied.mcp))
	}()

	waitFor(t, port)
	return occupied, func() result {
		cancel()
		select {
		case got := <-done:
			return got
		case <-time.After(10 * time.Second):
			t.Fatal("start 在取消后 10 秒内没有退出")
			return result{}
		}
	}
}

// waitFor 轮询直到端口可连接。轮询有上限，不是靠 sleep 猜一个够长的时间。
func waitFor(t *testing.T, port int) {
	t.Helper()

	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	dialer := &net.Dialer{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := dialer.DialContext(t.Context(), "tcp", address)
		if err == nil {
			if closeErr := conn.Close(); closeErr != nil {
				t.Fatalf("关闭探测连接失败：%v", closeErr)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Gateway 在 10 秒内没有在 %s 上开始监听%s", address, whyItCannotStart())
}

// whyItCannotStart 在超时信息后面补一句真正的原因，能查出来的话。
//
// 「端口上没人监听」是症状，不是原因。裸克隆上最常见的那一种（Console 资源
// 还没构建，`opendelo start` 因此拒绝启动）会让人先去查端口与超时 ——
// 2026-08-06 的裸克隆验证里我自己就是这么查的。
func whyItCannotStart() string {
	assets, err := web.ConsoleFS()
	if err != nil {
		return "：读不到内嵌的 Console 资源（" + err.Error() + "）"
	}
	if _, statErr := fs.Stat(assets, "index.html"); statErr != nil {
		return "：Console 资源还没构建，先跑 make web-build"
	}
	return ""
}

func TestStart_OnAnInitializedDirectory_ServesTheGatewayStatus(t *testing.T) {
	// 在组装根落地之前这条会失败：httpapi.New 因为 Services 为空而拒绝构造，
	// start 根本走不到监听那一步。
	dir := initialized(t)
	occupied, stop := startInBackground(t, dir)

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		"http://127.0.0.1:"+strconv.Itoa(occupied.web)+"/v1/gateway/status", nil)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	request.Header.Set("X-Requested-By", "opendelo-console")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("请求网关状态失败：%v", err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			t.Errorf("关闭响应体失败：%v", closeErr)
		}
	}()

	// 过了前两道检查、卡在会话令牌上 → 401。能走到这一步就说明整条处理链
	// （安全响应头 → operation_id → 会话守卫 → 路由）都已经装起来了。
	if response.StatusCode != http.StatusUnauthorized {
		t.Errorf("状态码为 %d，期望 401", response.StatusCode)
	}

	got := stop()
	if got.code != cli.ExitOK {
		t.Errorf("取消后退出码为 %d，stderr 为 %q", got.code, got.stderr)
	}
}

func TestStart_CreatesTheDatabaseWithTightPermissions(t *testing.T) {
	// 数据文件 0600。
	dir := initialized(t)
	_, stop := startInBackground(t, dir)
	defer stop()

	path := filepath.Join(dir, config.DataDirName, "opendelo.db")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("数据库文件不存在：%v", err)
	}
	if info.Mode().Perm() != config.FilePermission {
		t.Errorf("数据库文件权限为 %v，期望 %v", info.Mode().Perm(), config.FilePermission)
	}
}

func TestStart_RunsMigrations_SoTheBusinessEndpointsAnswer(t *testing.T) {
	// 迁移没跑的话，第一条 SQL 会撞上「表不存在」，端点返回 500。
	// 这里要的是一个**业务**端点真的查得动数据库。
	dir := initialized(t)
	occupied, stop := startInBackground(t, dir)
	defer stop()

	token, err := os.ReadFile(filepath.Join(dir, config.SessionTokenFileName))
	if err != nil {
		t.Fatalf("读取会话令牌失败：%v", err)
	}

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		"http://127.0.0.1:"+strconv.Itoa(occupied.web)+"/v1/leases", nil)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	request.Header.Set("Origin", "http://127.0.0.1:"+strconv.Itoa(occupied.web))
	request.Header.Set("X-Requested-By", "opendelo-console")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("请求 Lease 列表失败：%v", err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			t.Errorf("关闭响应体失败：%v", closeErr)
		}
	}()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("状态码为 %d，期望 200 —— 迁移没跑时这里是 500", response.StatusCode)
	}

	var body struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("响应不是预期的列表形状：%v", err)
	}
	if len(body.Items) != 0 {
		t.Errorf("全新的数据库里有 %d 条 Lease", len(body.Items))
	}
}

func TestStart_OnAnUninitializedDirectory_FailsInsteadOfCreatingOneQuietly(t *testing.T) {
	// 数据目录不存在时不自行创建：`opendelo init` 会把目录权限设成 0700，
	// 这里悄悄补一个出来就绕过了那一步。
	got := execute(t, t.Context(), "start",
		"--config-dir", newConfigDir(t),
		"--web-api-port", strconv.Itoa(freePort(t)),
		"--agent-proxy-port", strconv.Itoa(freePort(t)),
		"--mcp-port", strconv.Itoa(freePort(t)))

	if got.code == cli.ExitOK {
		t.Fatal("未初始化的目录上 start 成功了")
	}
	if got.stdout != "" {
		t.Errorf("失败时向 stdout 写了内容：%q", got.stdout)
	}
}

func TestStart_AgentProxy_ListensAndRefusesWithoutASessionKey(t *testing.T) {
	// 成功标准 S10 的一半：没有 Lease 的请求被拒且无出站流量。这里守的是更前一步 ——
	// 连 Agent 都认不出来时就已经拒了，压根走不到「有没有 Lease」。
	dir := initialized(t)
	occupied, stop := startInBackground(t, dir)
	defer stop()

	waitFor(t, occupied.agentProxy)

	// 绝对形式的请求行，正是代理协议的样子。不带 Proxy-Authorization。
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		"http://api.github.com/repos/runcoor/opendelo", nil)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}

	client := &http.Client{Transport: &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) {
			return url.Parse("http://127.0.0.1:" + strconv.Itoa(occupied.agentProxy))
		},
	}}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("请求代理失败：%v", err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			t.Errorf("关闭响应体失败：%v", closeErr)
		}
	}()

	// 407 是代理协议里「你得先认证」的说法。
	if response.StatusCode != http.StatusProxyAuthRequired {
		t.Errorf("状态码为 %d，期望 407", response.StatusCode)
	}
}

func TestStart_MCP_ListensAndRefusesTheToolListWithoutASessionKey(t *testing.T) {
	// 清单本身就是情报：它说明这台网关接了哪些服务。认不出调用方就不给
	//（REQ-MCP-002）。握手不需要认证，因此用它证明这个面确实起来了 ——
	// 只测「连得上」的话，一个把所有请求都回 404 的面也能通过。
	dir := initialized(t)
	occupied, stop := startInBackground(t, dir)
	defer stop()

	waitFor(t, occupied.mcp)
	endpoint := "http://127.0.0.1:" + strconv.Itoa(occupied.mcp)

	handshake := callMCP(t, endpoint, "",
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if handshake.Error != nil {
		t.Fatalf("握手被拒了：%+v", handshake.Error)
	}
	if len(handshake.Result) == 0 {
		t.Fatal("握手没有返回任何结果")
	}

	listed := callMCP(t, endpoint, "",
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	if listed.Error == nil {
		t.Fatalf("没有 Session Key 也拿到了工具清单：%s", listed.Result)
	}
}

// rpcReply 是一条 JSON-RPC 回应里用例关心的两个字段。
type rpcReply struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func callMCP(t *testing.T, endpoint, sessionKey, body string) rpcReply {
	t.Helper()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if sessionKey != "" {
		request.Header.Set("Mcp-Session-Id", sessionKey)
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("请求 MCP 面失败：%v", err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			t.Errorf("关闭响应体失败：%v", closeErr)
		}
	}()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("状态码为 %d，期望 200", response.StatusCode)
	}

	var reply rpcReply
	if err := json.NewDecoder(response.Body).Decode(&reply); err != nil {
		t.Fatalf("回应不是 JSON-RPC 的形状：%v", err)
	}
	return reply
}

/*
 * operation_id 必须在三个面上都成立（回归，见 docs/12_PROGRESS.md 的 TASK-0702）。
 *
 * 它原本只由 8787 的中间件生成，8788 与 8789 上恒为空。后果不是「日志少一个字段」：
 * `core/pipeline` 把空的 operation_id 当作输入不成立而拒绝（ADR-004 —— 追溯不了的
 * 请求不能执行），于是**每一次 MCP 工具调用与每一次代理请求都以 invalid_request 结束**，
 * 而账本上没有任何记录。`CLAUDE.md` §12.6 要求的正是「无未审计路径」。
 *
 * 两条用例都打在对外可见的位置上：MCP 的错误消息带 `(operation_id=…)`，
 * 代理的错误体带 `operation_id` 字段。看不见它就说明这次请求没有身份。
 */

func TestStart_MCP_GivesEveryRequestAnOperationID_Regression(t *testing.T) {
	dir := initialized(t)
	occupied, stop := startInBackground(t, dir)
	defer stop()

	waitFor(t, occupied.mcp)

	refused := callMCP(t, "http://127.0.0.1:"+strconv.Itoa(occupied.mcp), "",
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	if refused.Error == nil {
		t.Fatalf("没有 Session Key 也拿到了工具清单：%s", refused.Result)
	}
	if !strings.Contains(refused.Error.Message, "operation_id=") {
		t.Errorf("MCP 的拒绝没有带 operation_id，这次调用无法在账本里定位：%q",
			refused.Error.Message)
	}
}

func TestStart_AgentProxy_GivesEveryRequestAnOperationID_Regression(t *testing.T) {
	dir := initialized(t)
	occupied, stop := startInBackground(t, dir)
	defer stop()

	waitFor(t, occupied.agentProxy)

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		"http://api.github.com/repos/runcoor/opendelo", nil)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	client := &http.Client{Transport: &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) {
			return url.Parse("http://127.0.0.1:" + strconv.Itoa(occupied.agentProxy))
		},
	}}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("请求代理失败：%v", err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			t.Errorf("关闭响应体失败：%v", closeErr)
		}
	}()

	var refusal struct {
		Error struct {
			OperationID string `json:"operation_id"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&refusal); err != nil {
		t.Fatalf("拒绝的响应体不是错误契约的形状：%v", err)
	}
	if refusal.Error.OperationID == "" {
		t.Error("代理的拒绝没有带 operation_id，这次请求无法在账本里定位")
	}
}
