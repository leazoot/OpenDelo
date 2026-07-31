package mcpsrv_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/core/gateway"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/transport/mcpsrv"
)

/*
 * 两种传输（REQ-MCP-003）。
 *
 * 重点是 AC1「两种传输下工具列表与调用结果一致」：用例把同一批帧分别喂给
 * stdio 与 HTTP，逐字节比对回应。
 */

const (
	goodKey = "SESSION_KEY_GOOD_a1b2c3"
	badKey  = "SESSION_KEY_WRONG_z9y8x7"
	// sentinelToken 是不该出现在任何回应里的凭据哨兵。
	sentinelToken = "SENTINEL_TOKEN_d3adb33f_DO_NOT_LEAK"
)

type stubAuth struct {
	calls int
}

func (s *stubAuth) Authenticate(_ context.Context, sessionKey string) (mcpsrv.Caller, error) {
	s.calls++
	if sessionKey != goodKey {
		return mcpsrv.Caller{}, apperr.New(apperr.CodeUnauthenticated).
			WithDetail("Session Key 无效")
	}
	return mcpsrv.Caller{AgentID: "agent_01", WorkspaceID: "ws_01"}, nil
}

type stubCalls struct {
	lastCall mcpsrv.Call
	outcome  mcpsrv.CallOutcome
	err      error
	// attempts 数出「执行发生过几次」，离线用例靠它断言一次都没发生。
	attempts int
}

func (s *stubCalls) Call(_ context.Context, call mcpsrv.Call) (mcpsrv.CallOutcome, error) {
	s.attempts++
	s.lastCall = call
	return s.outcome, s.err
}

// serving 造一个正在服务的可用状态。
func serving() *gateway.Availability {
	availability := gateway.New()
	availability.Serve()
	return availability
}

type harness struct {
	server       *mcpsrv.Server
	auth         *stubAuth
	calls        *stubCalls
	availability *gateway.Availability
}

func newHarness(t *testing.T) harness {
	t.Helper()

	auth := &stubAuth{}
	calls := &stubCalls{outcome: mcpsrv.CallOutcome{Text: "done"}}
	availability := serving()
	server, err := mcpsrv.NewServer(mcpsrv.Options{
		Availability:  availability,
		Catalog:       realCatalog(t),
		Authenticator: auth,
		Calls:         calls,
		Logger:        slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("构造 MCP Server 失败：%v", err)
	}
	return harness{server: server, auth: auth, calls: calls, availability: availability}
}

// overStdio 把若干行喂给 stdio 传输，返回逐行回应。
func overStdio(t *testing.T, all harness, key string, lines ...string) []string {
	t.Helper()

	var output bytes.Buffer
	input := strings.NewReader(strings.Join(lines, "\n") + "\n")
	if err := mcpsrv.ServeStdio(
		t.Context(), all.server, input, &output, key); err != nil {
		t.Fatalf("stdio 传输出错：%v", err)
	}

	trimmed := strings.TrimSpace(output.String())
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// overHTTP 把一行送进 HTTP 传输，返回状态码与响应体。
func overHTTP(t *testing.T, all harness, key, line string) (int, string) {
	t.Helper()

	request := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, "/mcp", strings.NewReader(line))
	if key != "" {
		request.Header.Set("Mcp-Session-Id", key)
	}
	recorder := httptest.NewRecorder()
	mcpsrv.NewHTTPHandler(all.server).ServeHTTP(recorder, request)
	return recorder.Code, strings.TrimSpace(recorder.Body.String())
}

func TestBothTransports_ProduceByteIdenticalRepliesForTheSameFrames(t *testing.T) {
	// REQ-MCP-003 AC1。逐字节比对而不是比对「都成功了」：
	// 工具清单的顺序、字段名、Schema 内容任何一处分叉都要被发现。
	frames := []string{
		`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"github.repository.read","arguments":{"owner":"acme","repo":"api"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"nonsense/method"}`,
	}

	stdioReplies := overStdio(t, newHarness(t), goodKey, frames...)
	if len(stdioReplies) != len(frames) {
		t.Fatalf("stdio 回了 %d 条，期望 %d 条", len(stdioReplies), len(frames))
	}

	for index, frame := range frames {
		fresh := newHarness(t)
		status, body := overHTTP(t, fresh, goodKey, frame)
		if status != http.StatusOK {
			t.Fatalf("第 %d 帧 HTTP 状态码为 %d", index, status)
		}
		if body != stdioReplies[index] {
			t.Errorf("第 %d 帧两种传输不一致：\n stdio: %s\n  http: %s",
				index, stdioReplies[index], body)
		}
	}
}

func TestDispatch_IDZero_IsARequestNotANotification(t *testing.T) {
	// 实测：客户端的第一个请求 id 就是 0。
	// 用零值判断通知会让 initialize 石沉大海，握手直接卡住。
	replies := overStdio(t, newHarness(t), goodKey,
		`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{}}`)
	if len(replies) != 1 {
		t.Fatalf("id 为 0 的请求收到 %d 条回应，期望 1 条", len(replies))
	}
	if !strings.Contains(replies[0], `"id":0`) {
		t.Errorf("回应里的 id 不是 0：%s", replies[0])
	}
}

func TestDispatch_Notification_GetsNoReplyOnEitherTransport(t *testing.T) {
	notification := `{"jsonrpc":"2.0","method":"notifications/initialized"}`

	if replies := overStdio(t, newHarness(t), goodKey, notification); len(replies) != 0 {
		t.Errorf("stdio 对通知回了 %d 条：%v", len(replies), replies)
	}

	status, body := overHTTP(t, newHarness(t), goodKey, notification)
	if status != http.StatusAccepted {
		t.Errorf("HTTP 对通知返回 %d，期望 202", status)
	}
	if body != "" {
		t.Errorf("HTTP 对通知回了内容：%s", body)
	}
}

func TestInitialize_DeclaresToolsOnlyAndAServerChosenProtocolVersion(t *testing.T) {
	// 只声明 tools：实测客户端只会请求被声明过的东西，
	// 声明得越少服务端表面积越小。
	replies := overStdio(t, newHarness(t), goodKey,
		`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{}}`)

	var decoded struct {
		Result struct {
			ProtocolVersion string                     `json:"protocolVersion"`
			Capabilities    map[string]json.RawMessage `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(replies[0]), &decoded); err != nil {
		t.Fatalf("initialize 回应解不开：%v", err)
	}

	if decoded.Result.ProtocolVersion != mcpsrv.ProtocolVersion {
		t.Errorf("协议版本为 %q", decoded.Result.ProtocolVersion)
	}
	if len(decoded.Result.Capabilities) != 1 {
		t.Errorf("声明了 %d 项能力，期望只有 tools：%v",
			len(decoded.Result.Capabilities), decoded.Result.Capabilities)
	}
	if _, declared := decoded.Result.Capabilities["tools"]; !declared {
		t.Error("没有声明 tools 能力")
	}
	for _, unwanted := range []string{"resources", "prompts", "logging", "completions"} {
		if _, declared := decoded.Result.Capabilities[unwanted]; declared {
			t.Errorf("声明了 %s 能力，它会让客户端来请求一个我们不提供的东西", unwanted)
		}
	}
}

func TestInitialize_NeedsNoSessionKey(t *testing.T) {
	// 客户端要先握上手才谈得上出示密钥。握手本身不暴露任何东西。
	all := newHarness(t)
	replies := overStdio(t, all, "", `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{}}`)
	if strings.Contains(replies[0], "error") {
		t.Errorf("没有密钥时 initialize 失败了：%s", replies[0])
	}
	if all.auth.calls != 0 {
		t.Errorf("initialize 走了 %d 次认证，它不该认证", all.auth.calls)
	}
}

func TestToolsListAndCall_WithoutAValidSessionKey_AreRefused(t *testing.T) {
	// 「无法识别 Agent」是 Fail Closed 的第一条。工具清单本身就是情报 ——
	// 它说明这台网关接了哪些服务。
	for name, key := range map[string]string{"没有密钥": "", "密钥不对": badKey} {
		for _, method := range []string{"tools/list", "tools/call"} {
			t.Run(name+" "+method, func(t *testing.T) {
				all := newHarness(t)
				frame := `{"jsonrpc":"2.0","id":1,"method":"` + method +
					`","params":{"name":"github.repository.read","arguments":{}}}`

				replies := overStdio(t, all, key, frame)
				if !strings.Contains(replies[0], `"error"`) {
					t.Errorf("未认证仍然拿到了结果：%s", replies[0])
				}
				if strings.Contains(replies[0], `"tools"`) {
					t.Errorf("未认证的请求拿到了工具清单：%s", replies[0])
				}
				if all.calls.lastCall.Service != "" {
					t.Error("未认证的请求走到了执行器")
				}
			})
		}
	}
}

func TestToolsCall_UnknownTool_IsRefusedWithoutReachingTheExecutor(t *testing.T) {
	// 未被声明的能力一律拒绝（Fail Closed），且走 isError 而不是协议错误 ——
	// 对模型来说这是「这里没有这个工具」，不是传输故障。
	all := newHarness(t)
	replies := overStdio(t, all, goodKey,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"github.repository.destroy","arguments":{}}}`)

	if strings.Contains(replies[0], `"error"`) {
		t.Errorf("未声明的工具走了协议错误：%s", replies[0])
	}
	if !strings.Contains(replies[0], `"isError":true`) {
		t.Errorf("未声明的工具没有被标成拒绝：%s", replies[0])
	}
	if all.calls.lastCall.Service != "" {
		t.Errorf("未声明的工具走到了执行器：%+v", all.calls.lastCall)
	}
}

func TestToolsCall_PolicyRefusal_IsAToolResultNotAProtocolError(t *testing.T) {
	// 走协议错误时模型看到「MCP error -32000」，
	// 那读起来像工具坏了，会诱导重试或换路径 —— 而一次「需要人工确认」
	// 不是故障，是产品的正常输出。
	all := newHarness(t)
	all.calls.outcome = mcpsrv.CallOutcome{
		Refused: true, Text: "Needs human approval. operation_id=op_01",
	}

	replies := overStdio(t, all, goodKey,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"github.repository.read","arguments":{}}}`)

	if strings.Contains(replies[0], `"error"`) {
		t.Errorf("策略性拒绝走了协议错误：%s", replies[0])
	}
	if !strings.Contains(replies[0], `"isError":true`) {
		t.Errorf("策略性拒绝没有被标成 isError：%s", replies[0])
	}
	if !strings.Contains(replies[0], "op_01") {
		t.Errorf("拒绝里没有 operation_id，用户无从在账本里找到它：%s", replies[0])
	}
}

func TestToolsCall_PassesTheDeclaredServiceAndOperationAlongsideTheToolName(t *testing.T) {
	// 服务与操作取自能力声明，执行器不必再解析一次工具名。
	//
	// 工具名同时也要带上，但它是另一件事：决策链路要拿它去查**数据库里那份声明**
	// 的映射表，而那份表与这里的清单是两个来源。让下游按服务与操作自己拼一个
	// 工具名出来，就等于把 REQ-MCP-001 的命名规则实现两遍。
	all := newHarness(t)
	overStdio(t, all, goodKey,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"github.repository.read","arguments":{"owner":"acme"}}}`)

	if all.calls.lastCall.Service != "github" {
		t.Errorf("service 为 %q", all.calls.lastCall.Service)
	}
	if all.calls.lastCall.Operation != "read_repository" {
		t.Errorf("operation 为 %q", all.calls.lastCall.Operation)
	}
	if all.calls.lastCall.Tool != "github.repository.read" {
		t.Errorf("tool 为 %q，期望客户端用的那个名字", all.calls.lastCall.Tool)
	}
	if all.calls.lastCall.Caller.AgentID != "agent_01" {
		t.Errorf("调用方为 %+v", all.calls.lastCall.Caller)
	}
}

func TestErrorReply_CarriesTheCodeTableTextAndNoInternalDetail(t *testing.T) {
	// 对外文本只能取自码表；detail 与 cause 不外泄。
	all := newHarness(t)
	all.calls.err = apperr.New(apperr.CodeProviderUnavailable).
		WithDetail("1Password CLI 未安装于 /usr/local/bin/op " + sentinelToken)

	replies := overStdio(t, all, goodKey,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"github.repository.read","arguments":{}}}`)

	if strings.Contains(replies[0], sentinelToken) {
		t.Errorf("回应里泄漏了哨兵：%s", replies[0])
	}
	if strings.Contains(replies[0], "/usr/local/bin/op") {
		t.Errorf("回应里泄漏了本机路径：%s", replies[0])
	}
	expected := apperr.New(apperr.CodeProviderUnavailable).Public().Message
	if !strings.Contains(replies[0], expected) {
		t.Errorf("回应里没有码表文本 %q：%s", expected, replies[0])
	}
}

func TestToolsList_CarriesNoCredentialMaterialAtAll(t *testing.T) {
	// REQ-MCP-002 AC2 在清单这一侧的形式：整份 tools/list 的序列化结果里
	// 不得出现哨兵、也不得出现任何看起来像凭据载体的字段名。
	// 调用结果那一侧由 Adapter 的 Redact() 负责（REQ-ADAPTER-007，S4 已覆盖）。
	replies := overStdio(t, newHarness(t), goodKey,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)

	listing := replies[0]
	if strings.Contains(listing, sentinelToken) {
		t.Error("工具清单里出现了哨兵")
	}
	for _, forbidden := range []string{
		"authorization", "api_key", "apikey", "private_key", "Bearer ", "password",
	} {
		if strings.Contains(strings.ToLower(listing), strings.ToLower(forbidden)) {
			t.Errorf("工具清单里出现了 %q", forbidden)
		}
	}
}

func TestHTTP_RequestsCarryingAnOrigin_AreRefused(t *testing.T) {
	// 真实 MCP 客户端不发 Origin，浏览器发起的跨源请求一定发。
	// 因此本面的规则是「带 Origin 一律拒绝」—— 比 Console 的允许列表更严。
	for _, origin := range []string{
		"http://evil.example", "http://localhost:3000", "null", "http://127.0.0.1:8787",
	} {
		t.Run(origin, func(t *testing.T) {
			all := newHarness(t)
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp",
				strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			request.Header.Set("Mcp-Session-Id", goodKey)
			request.Header.Set("Origin", origin)

			recorder := httptest.NewRecorder()
			mcpsrv.NewHTTPHandler(all.server).ServeHTTP(recorder, request)

			if recorder.Code != http.StatusForbidden {
				t.Errorf("带 Origin %q 的请求返回 %d，期望 403", origin, recorder.Code)
			}
			if strings.Contains(recorder.Body.String(), "tools") {
				t.Errorf("带 Origin 的请求拿到了工具清单：%s", recorder.Body.String())
			}
			if all.auth.calls != 0 {
				t.Error("带 Origin 的请求走到了认证，它该在更早的地方被挡住")
			}
		})
	}
}

func TestHTTP_GetIsRefusedAndDeleteIsAccepted(t *testing.T) {
	// 实测客户端会试一次 GET 开推送通道，收到 405 后照常工作。
	// 本面没有服务端主动推送的场景。
	all := newHarness(t)
	handler := mcpsrv.NewHTTPHandler(all.server)

	for method, expected := range map[string]int{
		http.MethodGet:    http.StatusMethodNotAllowed,
		http.MethodDelete: http.StatusNoContent,
		http.MethodPut:    http.StatusMethodNotAllowed,
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder,
			httptest.NewRequestWithContext(t.Context(), method, "/mcp", nil))
		if recorder.Code != expected {
			t.Errorf("%s 返回 %d，期望 %d", method, recorder.Code, expected)
		}
	}
}

func TestCheckLoopback_RefusesAnythingReachableFromOffMachine(t *testing.T) {
	// REQ-MCP-003 AC2。
	for address, allowed := range map[string]bool{
		"127.0.0.1:8789":    true,
		"[::1]:8789":        true,
		"localhost:8789":    true,
		"0.0.0.0:8789":      false,
		"192.168.1.10:8789": false,
		"[::]:8789":         false,
		"8789":              false,
	} {
		t.Run(address, func(t *testing.T) {
			err := mcpsrv.CheckLoopback(address)
			if allowed && err != nil {
				t.Errorf("回环地址被拒绝：%v", err)
			}
			if !allowed && err == nil {
				t.Error("非回环地址被接受了")
			}
		})
	}
}

func TestServeStdio_UnparseableLine_GetsAParseErrorAndTheSessionContinues(t *testing.T) {
	// 一行坏数据不该让整条连接静默死掉 —— 那样客户端只会一直等。
	replies := overStdio(t, newHarness(t), goodKey,
		`{not json`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)

	if len(replies) != 2 {
		t.Fatalf("收到 %d 条回应，期望 2 条：%v", len(replies), replies)
	}
	if !strings.Contains(replies[0], "-32700") {
		t.Errorf("坏帧没有得到 parse error：%s", replies[0])
	}
	if !strings.Contains(replies[1], `"tools"`) {
		t.Errorf("坏帧之后的正常请求没有被处理：%s", replies[1])
	}
}

func TestNewServer_MissingAnyDependency_IsRefused(t *testing.T) {
	complete := mcpsrv.Options{
		Availability:  serving(),
		Catalog:       realCatalog(t),
		Authenticator: &stubAuth{},
		Calls:         &stubCalls{},
		Logger:        slog.New(slog.NewJSONHandler(io.Discard, nil)),
	}

	for name, blank := range map[string]func(*mcpsrv.Options){
		"Availability":  func(o *mcpsrv.Options) { o.Availability = nil },
		"Catalog":       func(o *mcpsrv.Options) { o.Catalog = nil },
		"Authenticator": func(o *mcpsrv.Options) { o.Authenticator = nil },
		"Calls":         func(o *mcpsrv.Options) { o.Calls = nil },
		"Logger":        func(o *mcpsrv.Options) { o.Logger = nil },
	} {
		t.Run(name, func(t *testing.T) {
			options := complete
			blank(&options)
			if _, err := mcpsrv.NewServer(options); err == nil {
				t.Errorf("%s 为空时仍然构造出了 Server", name)
			}
		})
	}
}

func TestDispatch_GatewayStopped_RefusesToolsListAndToolsCall(t *testing.T) {
	// REQ-GATEWAY-003：清单是情报，调用是行动，离线时都不给。
	// initialize 与 ping 仍然应答 —— 客户端因此拿到的是明确的拒绝，
	// 而不是一条断掉的连接。
	all := newHarness(t)
	all.availability.Stop()

	replies := overStdio(t, all, goodKey,
		`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"github.repository.read","arguments":{}}}`,
	)

	if len(replies) != 4 {
		t.Fatalf("收到 %d 条回应，期望 4 条", len(replies))
	}
	for index, reply := range replies[:2] {
		if strings.Contains(reply, `"error"`) {
			t.Errorf("第 %d 条握手类消息被拒绝了：%s", index, reply)
		}
	}
	// MCP 的错误帧带的是码表文本而不是码名（协议里 code 是数字），
	// 因此这里比对的是 gateway_unavailable 那一条固定文本。
	unavailable := apperr.New(apperr.CodeGatewayUnavailable).Public().Message
	for index, reply := range replies[2:] {
		if !strings.Contains(reply, unavailable) {
			t.Errorf("第 %d 条能力类消息没有被网关不可用拒绝：%s", index+2, reply)
		}
	}
	if all.calls.attempts != 0 {
		t.Errorf("网关已停止却执行了 %d 次调用", all.calls.attempts)
	}
}
