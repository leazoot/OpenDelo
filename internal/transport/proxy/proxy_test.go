package proxy

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
	"github.com/Runcoor/opendelo/internal/platform/logging"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * 8788 面的用例（REQ-PROXY-001/002）。
 *
 * 这一层唯一会坏掉的东西是**次序**：认证 → 认服务 → 匹配 Lease → 出站。
 * 因此几乎每个用例都断言两件事 —— 返回了什么，以及 exchange 被调用了几次。
 * 后者是「无出站流量」的落点：出站通道是一个假实现，它被调用过就是流量发生过。
 */

type recordedCall struct {
	Grant Grant
	Route Route
	Body  []byte
}

type fakeAuthenticator struct {
	caller Caller
	err    error
	keys   []string
	order  *[]string
}

func (f *fakeAuthenticator) Authenticate(_ context.Context, sessionKey string) (Caller, error) {
	f.keys = append(f.keys, sessionKey)
	record(f.order, "authenticate")
	return f.caller, f.err
}

type fakeServices struct {
	route   Route
	err     error
	targets []Target
	order   *[]string
}

func (f *fakeServices) Lookup(_ context.Context, target Target) (Route, error) {
	f.targets = append(f.targets, target)
	record(f.order, "lookup")
	return f.route, f.err
}

type fakeLeases struct {
	grant   Grant
	err     error
	callers []Caller
	routes  []Route
	order   *[]string
}

func (f *fakeLeases) Authorize(_ context.Context, caller Caller, route Route) (Grant, error) {
	f.callers = append(f.callers, caller)
	f.routes = append(f.routes, route)
	record(f.order, "authorize")
	return f.grant, f.err
}

type fakeExchange struct {
	reply Reply
	err   error
	calls []recordedCall
	order *[]string
}

func (f *fakeExchange) Send(_ context.Context, grant Grant, route Route, body []byte) (Reply, error) {
	f.calls = append(f.calls, recordedCall{Grant: grant, Route: route, Body: body})
	record(f.order, "send")
	return f.reply, f.err
}

type fakeAudits struct {
	blocked []Blocked
	err     error
}

func (f *fakeAudits) RecordBlocked(_ context.Context, blocked Blocked) error {
	f.blocked = append(f.blocked, blocked)
	return f.err
}

// serving 造一个正在服务的可用状态。
func serving() *gateway.Availability {
	availability := gateway.New()
	availability.Serve()
	return availability
}

func record(order *[]string, step string) {
	if order != nil {
		*order = append(*order, step)
	}
}

// harness 是一套默认全部成功的依赖，用例只改自己关心的那一项。
type harness struct {
	availability  *gateway.Availability
	authenticator *fakeAuthenticator
	services      *fakeServices
	leases        *fakeLeases
	exchange      *fakeExchange
	audits        *fakeAudits
	logs          *bytes.Buffer
	order         []string
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	h := &harness{logs: &bytes.Buffer{}, availability: gateway.New()}
	h.availability.Serve()
	h.authenticator = &fakeAuthenticator{
		caller: Caller{AgentID: "agent_1", WorkspaceID: "workspace_1"}, order: &h.order,
	}
	h.services = &fakeServices{
		route: Route{
			Service:   "github",
			Operation: "read_repository",
			Resource:  map[string]string{"owner": "acme", "repo": "console"},
		},
		order: &h.order,
	}
	h.leases = &fakeLeases{grant: Grant{LeaseID: "lease_1", IdentityID: "identity_1"}, order: &h.order}
	h.audits = &fakeAudits{}
	h.exchange = &fakeExchange{
		reply: Reply{StatusCode: http.StatusOK, ContentType: "application/json", Body: []byte(`{"name":"console"}`)},
		order: &h.order,
	}
	return h
}

func (h *harness) handler(t *testing.T) http.Handler {
	t.Helper()

	proxy, err := New(Options{
		Availability:  h.availability,
		Authenticator: h.authenticator,
		Services:      h.services,
		Leases:        h.leases,
		Exchange:      h.exchange,
		Audits:        h.audits,
		Logger:        logging.New(logging.Options{Level: slog.LevelDebug, Writer: h.logs}),
	})
	if err != nil {
		t.Fatalf("构造 Proxy 失败：%v", err)
	}
	return NewHandler(proxy)
}

// proxyRequest 造一个 HTTP 客户端在 HTTP_PROXY 生效时会发出的请求：绝对形式 + Session Key。
func proxyRequest(t *testing.T, method, absoluteURL string, body io.Reader) *http.Request {
	t.Helper()

	request := httptest.NewRequestWithContext(t.Context(), method, absoluteURL, body)
	request.Header.Set(sessionHeader, "session_key_1")
	return request
}

func (h *harness) do(t *testing.T, request *http.Request) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	h.handler(t).ServeHTTP(recorder, request)
	return recorder
}

func TestNew_MissingAnyDependency_RefusesToConstruct(t *testing.T) {
	// 一个缺了认证器或缺了 Lease 匹配的代理，等于把本机任意进程的出站请求
	// 当成已获授权。构造期拒绝比运行期发现要早得多。
	complete := func() Options {
		return Options{
			Availability:  serving(),
			Authenticator: &fakeAuthenticator{},
			Services:      &fakeServices{},
			Leases:        &fakeLeases{},
			Exchange:      &fakeExchange{},
			Audits:        &fakeAudits{},
			Logger:        slog.New(slog.DiscardHandler),
		}
	}

	cases := map[string]func(*Options){
		"缺可用状态":      func(o *Options) { o.Availability = nil },
		"缺认证器":       func(o *Options) { o.Authenticator = nil },
		"缺受控服务解析":    func(o *Options) { o.Services = nil },
		"缺 Lease 匹配": func(o *Options) { o.Leases = nil },
		"缺出站通道":      func(o *Options) { o.Exchange = nil },
		"缺审计入口":      func(o *Options) { o.Audits = nil },
		"缺日志":        func(o *Options) { o.Logger = nil },
	}
	for name, remove := range cases {
		t.Run(name, func(t *testing.T) {
			options := complete()
			remove(&options)
			if _, err := New(options); err == nil {
				t.Fatal("依赖不全却构造成功了")
			}
		})
	}

	if _, err := New(complete()); err != nil {
		t.Fatalf("依赖齐全时构造失败：%v —— 上面的用例可能是因为别的原因通过的", err)
	}
}

func TestNew_ZeroMaxRequestBytes_FallsBackToTheDefault(t *testing.T) {
	proxy, err := New(Options{
		Availability: serving(), Authenticator: &fakeAuthenticator{}, Services: &fakeServices{},
		Leases: &fakeLeases{}, Exchange: &fakeExchange{}, Audits: &fakeAudits{},
		Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	if proxy.maxBytes != DefaultMaxRequestBytes {
		t.Errorf("请求体上限为 %d，期望回落到默认值 %d —— 零值意味着无上限",
			proxy.maxBytes, DefaultMaxRequestBytes)
	}
}

func TestServe_ControlledRequest_ReachesTheOutboundLegWithTheGrant(t *testing.T) {
	h := newHarness(t)
	request := proxyRequest(t, http.MethodPost,
		"http://api.github.com/repos/acme/console/issues", strings.NewReader(`{"title":"x"}`))

	recorder := h.do(t, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，期望 200", recorder.Code)
	}
	if body := recorder.Body.String(); body != `{"name":"console"}` {
		t.Errorf("回给 Agent 的正文是 %q，期望原样返回 Adapter 脱敏后的答复", body)
	}
	if len(h.exchange.calls) != 1 {
		t.Fatalf("出站被调用 %d 次，期望恰好一次", len(h.exchange.calls))
	}

	call := h.exchange.calls[0]
	if call.Grant.LeaseID != "lease_1" {
		t.Errorf("出站时带的 Lease 是 %q，期望是刚匹配上的那条", call.Grant.LeaseID)
	}
	if string(call.Body) != `{"title":"x"}` {
		t.Errorf("出站正文为 %q，与 Agent 发来的不一致", call.Body)
	}
	if h.services.targets[0].Host != "api.github.com" || h.services.targets[0].Method != http.MethodPost {
		t.Errorf("解析服务时拿到的目标是 %+v", h.services.targets[0])
	}
}

func TestServe_TheFourStepsRunInOrder(t *testing.T) {
	// 次序是这一层的全部内容。认证放在认服务之前，是因为「这个域名归谁管」
	// 本身也是不该回答给一个认不出的调用方的。
	h := newHarness(t)
	h.do(t, proxyRequest(t, http.MethodGet, "http://api.github.com/repos/acme/console", nil))

	expected := []string{"authenticate", "lookup", "authorize", "send"}
	if strings.Join(h.order, ",") != strings.Join(expected, ",") {
		t.Errorf("实际次序 %v，期望 %v", h.order, expected)
	}
}

func TestServe_NoLease_Returns403AndProducesNoOutboundTraffic(t *testing.T) {
	// REQ-PROXY-002 AC1。
	h := newHarness(t)
	h.leases.err = apperr.New(apperr.CodeCredentialNotAuthorized).
		WithDetail("没有覆盖本次请求的 Lease")

	recorder := h.do(t, proxyRequest(t, http.MethodGet, "http://api.github.com/repos/acme/console", nil))

	if recorder.Code != http.StatusForbidden {
		t.Errorf("状态码为 %d，期望 403", recorder.Code)
	}
	if len(h.exchange.calls) != 0 {
		t.Fatalf("无 Lease 却发生了 %d 次出站", len(h.exchange.calls))
	}
}

func TestServe_RequestOutsideTheLeaseScope_IsRefusedWholeNotPartially(t *testing.T) {
	// REQ-PROXY-002 AC2：不做部分放行。超范围的请求不是「砍掉超出的部分再发」，
	// 而是整条不发 —— 部分放行等于让请求方自己决定范围。
	h := newHarness(t)
	h.leases.err = apperr.New(apperr.CodeCredentialNotAuthorized).
		WithDetail("请求超出 Lease lease_1 的范围")

	recorder := h.do(t, proxyRequest(t, http.MethodDelete,
		"http://api.github.com/repos/acme/other-repo", nil))

	if recorder.Code != http.StatusForbidden {
		t.Errorf("状态码为 %d，期望 403", recorder.Code)
	}
	if len(h.exchange.calls) != 0 {
		t.Errorf("超出 Lease 范围却仍然发出了 %d 次出站请求", len(h.exchange.calls))
	}

	var envelope struct {
		Error struct {
			Code        string `json:"code"`
			Message     string `json:"message"`
			OperationID string `json:"operation_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("拒绝的响应不是合法 JSON：%v", err)
	}
	if envelope.Error.Code != apperr.CodeCredentialNotAuthorized.String() {
		t.Errorf("错误码为 %q，期望 credential_not_authorized", envelope.Error.Code)
	}
	if strings.Contains(envelope.Error.Message, "lease_1") {
		t.Errorf("对外的错误信息里带上了内部细节：%q", envelope.Error.Message)
	}
}

func TestServe_UncontrolledHost_IsDeniedByDefault(t *testing.T) {
	// REQ-PROXY-002 AC3 的默认分支。直通属于安全等级 L0，不在本任务范围内。
	h := newHarness(t)
	h.services.err = apperr.New(apperr.CodeCapabilityNotOffered).
		WithDetail("没有 Adapter 负责 evil.example.com")

	recorder := h.do(t, proxyRequest(t, http.MethodGet, "http://evil.example.com/exfiltrate", nil))

	if recorder.Code != http.StatusForbidden {
		t.Errorf("状态码为 %d，期望 403", recorder.Code)
	}
	if len(h.exchange.calls) != 0 {
		t.Errorf("非受控域名却发生了 %d 次出站", len(h.exchange.calls))
	}
	if len(h.leases.routes) != 0 {
		t.Errorf("非受控域名却还去匹配了 Lease %d 次", len(h.leases.routes))
	}
}

func TestServe_UnknownSessionKey_Returns407AndStopsBeforeLookup(t *testing.T) {
	h := newHarness(t)
	h.authenticator.err = apperr.New(apperr.CodeUnauthenticated).WithDetail("认不出这个 Session Key")

	recorder := h.do(t, proxyRequest(t, http.MethodGet, "http://api.github.com/repos/acme/console", nil))

	if recorder.Code != http.StatusProxyAuthRequired {
		t.Errorf("状态码为 %d，期望 407", recorder.Code)
	}
	if header := recorder.Header().Get("Proxy-Authenticate"); header == "" {
		t.Error("407 没有带 Proxy-Authenticate，客户端无从知道该怎么认证")
	}
	if len(h.services.targets) != 0 || len(h.exchange.calls) != 0 {
		t.Error("认证失败后仍然继续往下走了")
	}
}

func TestServe_SessionKeyComesFromProxyAuthorization(t *testing.T) {
	h := newHarness(t)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"http://api.github.com/repos/acme/console", nil)
	request.Header.Set(sessionHeader, "session_key_7")

	h.do(t, request)

	if len(h.authenticator.keys) != 1 || h.authenticator.keys[0] != "session_key_7" {
		t.Errorf("认证器收到的 Session Key 是 %v", h.authenticator.keys)
	}
}

func TestConnect_IsRefused_AndProducesNoOutboundTraffic(t *testing.T) {
	// 隧道一旦建立，网关就只剩下转发字节：路径、操作、Lease、注入全部落空。
	// 这正是 L1 Enforced 要挡的直连。
	h := newHarness(t)
	request := httptest.NewRequestWithContext(t.Context(),
		http.MethodConnect, "http://api.github.com:443", nil)
	request.Host = "api.github.com:443"
	request.Header.Set(sessionHeader, "session_key_1")

	recorder := h.do(t, request)

	if recorder.Code != http.StatusForbidden {
		t.Errorf("CONNECT 的状态码为 %d，期望 403", recorder.Code)
	}
	if len(h.exchange.calls) != 0 {
		t.Errorf("CONNECT 却发生了 %d 次出站", len(h.exchange.calls))
	}
	if len(h.authenticator.keys) != 0 {
		t.Error("CONNECT 走到了认证：这条路应该在更早的地方就断掉")
	}
}

func TestServe_RelativeFormRequest_Returns400(t *testing.T) {
	// 相对形式不是在用代理，而是有人把 8788 当成了一个网站。
	h := newHarness(t)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/repos/acme/console", nil)
	request.Header.Set(sessionHeader, "session_key_1")

	recorder := h.do(t, request)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("状态码为 %d，期望 400", recorder.Code)
	}
	if len(h.exchange.calls) != 0 {
		t.Errorf("相对形式请求却发生了 %d 次出站", len(h.exchange.calls))
	}
}

func TestTargetOf_OnlyAbsoluteHTTPRequestLinesAreAccepted(t *testing.T) {
	// 分开测「有没有主机」与「协议对不对」：两者在相对形式那个用例里恰好都命中，
	// 只测那一个的话，去掉其中任意一条检查用例都照样绿。
	cases := map[string]struct {
		target   string
		accepted bool
	}{
		"绝对形式 http":  {"http://api.github.com/repos/acme/console", true},
		"绝对形式 https": {"https://api.github.com/repos/acme/console", true},
		"相对形式":       {"/repos/acme/console", false},
		"有协议但没有主机":   {"http:///repos/acme/console", false},
		"认不出的协议":     {"ftp://files.example.com/x", false},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, testCase.target, nil)
			parsed, err := targetOf(request)

			switch {
			case testCase.accepted && err != nil:
				t.Fatalf("%s 被拒绝了：%v", testCase.target, err)
			case !testCase.accepted && err == nil:
				t.Fatalf("%s 被接受了，解析结果 %+v", testCase.target, parsed)
			case !testCase.accepted:
				return
			}
			if parsed.Host != "api.github.com" || parsed.Path != "/repos/acme/console" {
				t.Errorf("解析结果为 %+v", parsed)
			}
		})
	}
}

func TestServe_NonHTTPScheme_Returns400(t *testing.T) {
	h := newHarness(t)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "ftp://files.example.com/x", nil)
	request.Header.Set(sessionHeader, "session_key_1")

	recorder := h.do(t, request)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("状态码为 %d，期望 400", recorder.Code)
	}
	if len(h.authenticator.keys) != 0 {
		t.Error("认不出的协议却继续走到了认证")
	}
}

func TestServe_BodyOverTheLimit_Returns400AndProducesNoOutboundTraffic(t *testing.T) {
	h := newHarness(t)
	proxy, err := New(Options{
		Availability: h.availability, Authenticator: h.authenticator,
		Services: h.services, Leases: h.leases,
		Exchange: h.exchange, Audits: h.audits,
		Logger: slog.New(slog.DiscardHandler), MaxRequestBytes: 8,
	})
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}

	recorder := httptest.NewRecorder()
	NewHandler(proxy).ServeHTTP(recorder, proxyRequest(t, http.MethodPost,
		"http://api.github.com/repos/acme/console/issues", strings.NewReader(strings.Repeat("x", 64))))

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("状态码为 %d，期望 400", recorder.Code)
	}
	if len(h.exchange.calls) != 0 {
		t.Errorf("超限的请求体却发生了 %d 次出站", len(h.exchange.calls))
	}
	if len(h.leases.routes) != 0 {
		t.Error("超限的请求体却已经占用了一次 Lease 计数")
	}
}

func TestServe_ExchangeFailure_KeepsTheErrorClassVisible(t *testing.T) {
	cases := map[string]struct {
		err    error
		status int
	}{
		"外部服务超时":  {apperr.New(apperr.CodeAdapterTimeout), http.StatusGatewayTimeout},
		"出站失败":    {apperr.New(apperr.CodeGatewayUnavailable), http.StatusBadGateway},
		"凭据源不可用":  {apperr.New(apperr.CodeProviderUnavailable), http.StatusBadGateway},
		"路径不在白名单": {apperr.New(apperr.CodePathNotAllowed), http.StatusForbidden},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.exchange.err = testCase.err

			recorder := h.do(t, proxyRequest(t, http.MethodGet,
				"http://api.github.com/repos/acme/console", nil))

			if recorder.Code != testCase.status {
				t.Errorf("状态码为 %d，期望 %d", recorder.Code, testCase.status)
			}
		})
	}
}

func TestStatusFor_UnknownError_IsFiveHundredNotAForbidden(t *testing.T) {
	// 网关自己出问题不是一次「拒绝」。把它说成 403 会让账本里的被拒请求
	// 混进故障，两者的处理方式完全不同。
	if status := statusFor(errString("驱动层的某个错误")); status != http.StatusInternalServerError {
		t.Errorf("认不出的错误折成 %d，期望 500", status)
	}
	if status := statusFor(nil); status != http.StatusOK {
		t.Errorf("nil 折成 %d，期望 200", status)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestCheckLoopback(t *testing.T) {
	cases := map[string]struct {
		address string
		allowed bool
	}{
		"回环 IPv4":    {"127.0.0.1:8788", true},
		"回环网段内的其他地址": {"127.0.0.5:8788", true},
		"回环 IPv6":    {"[::1]:8788", true},
		"localhost":  {"localhost:8788", true},
		"任意地址 IPv4":  {"0.0.0.0:8788", false},
		"任意地址 IPv6":  {"[::]:8788", false},
		"局域网地址":      {"192.168.1.10:8788", false},
		"没有端口":       {"127.0.0.1", false},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			err := CheckLoopback(testCase.address)
			if testCase.allowed && err != nil {
				t.Errorf("%s 被拒绝了：%v", testCase.address, err)
			}
			if !testCase.allowed && err == nil {
				t.Errorf("%s 被放行了", testCase.address)
			}
		})
	}
}

func TestServe_AgentSuppliedAuthorization_NeverReachesTheOutboundLeg(t *testing.T) {
	// REQ-PROXY-001 AC1 的前半句。这里不是「记得把头删掉」——
	// Exchange 的签名里根本没有请求头，Agent 送来的任何头在类型上就到不了出站。
	h := newHarness(t)
	request := proxyRequest(t, http.MethodGet, "http://api.github.com/repos/acme/console", nil)
	request.Header.Set("Authorization", "Bearer "+sentinel.SentinelToken)

	h.do(t, request)

	if len(h.exchange.calls) != 1 {
		t.Fatalf("出站被调用 %d 次", len(h.exchange.calls))
	}
	encoded, err := json.Marshal(h.exchange.calls[0])
	if err != nil {
		t.Fatalf("序列化出站调用失败：%v", err)
	}
	for _, value := range sentinel.All() {
		if bytes.Contains(encoded, []byte(value)) {
			t.Errorf("出站调用里出现了哨兵 %s：%s", value, encoded)
		}
	}
}

func TestServe_GatewayStopped_RefusesBeforeAnythingElse(t *testing.T) {
	// REQ-GATEWAY-003 AC1 / 成功标准 S10：停止后请求失败并收到 gateway_unavailable，
	// 且**不得**直接到达外部服务。
	h := newHarness(t)
	h.availability.Stop()

	recorder := h.do(t, proxyRequest(t, http.MethodGet, "http://api.github.com/repos/acme/console", nil))

	if len(h.exchange.calls) != 0 {
		t.Fatalf("网关已停止却发生了 %d 次出站", len(h.exchange.calls))
	}
	if len(h.authenticator.keys) != 0 || len(h.services.targets) != 0 || len(h.leases.routes) != 0 {
		t.Error("网关已停止却仍然走了认证 / 认服务 / 匹配 Lease")
	}

	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	if envelope.Error.Code != apperr.CodeGatewayUnavailable.String() {
		t.Errorf("错误码为 %q，期望 gateway_unavailable", envelope.Error.Code)
	}
}

func TestServe_StoppedThenRestarted_ReplaysNothing(t *testing.T) {
	// REQ-GATEWAY-003 AC3：恢复后之前失败的请求不自动重放。
	// 「恢复」在这里是一个新的 Availability（进程重启）——旧的那个不会回来。
	h := newHarness(t)
	h.availability.Stop()

	for range 3 {
		h.do(t, proxyRequest(t, http.MethodGet, "http://api.github.com/repos/acme/console", nil))
	}
	if len(h.exchange.calls) != 0 {
		t.Fatalf("离线期间发生了 %d 次出站", len(h.exchange.calls))
	}

	// 重启：换一个正在服务的可用状态，不发新请求。
	h.availability = serving()
	h.handler(t)

	if len(h.exchange.calls) != 0 {
		t.Errorf("恢复之后重放了 %d 次离线期间的请求 —— 那三条请求已经有过明确答复",
			len(h.exchange.calls))
	}
}

func TestConnect_WhenStopped_IsStillRefusedWithoutOutbound(t *testing.T) {
	// 离线路径与隧道路径都不产生出站，但走的是两个分支，各测一次。
	h := newHarness(t)
	h.availability.Stop()

	request := proxyRequest(t, http.MethodConnect, "http://api.github.com:443", nil)
	request.Host = "api.github.com:443"
	recorder := h.do(t, request)

	if recorder.Code != http.StatusForbidden {
		t.Errorf("状态码为 %d，期望 403", recorder.Code)
	}
	if len(h.exchange.calls) != 0 {
		t.Errorf("发生了 %d 次出站", len(h.exchange.calls))
	}
}

func TestServe_BlockedDirectAttempt_IsRecordedInTheLedger(t *testing.T) {
	// REQ-GATEWAY-005 AC3：L1 下 Agent 直连受控服务的尝试被拦截**并记审计**。
	h := newHarness(t)
	h.leases.err = apperr.New(apperr.CodeCredentialNotAuthorized)

	h.do(t, proxyRequest(t, http.MethodDelete, "http://api.github.com/repos/acme/console", nil))

	if len(h.audits.blocked) != 1 {
		t.Fatalf("记了 %d 条拦截，期望一条", len(h.audits.blocked))
	}
	record := h.audits.blocked[0]
	if record.Target.Host != "api.github.com" || record.Target.Method != http.MethodDelete {
		t.Errorf("记下的目标是 %+v", record.Target)
	}
	if record.Route.Service != "github" {
		t.Errorf("记下的服务是 %q", record.Route.Service)
	}
	if record.Reason != apperr.CodeCredentialNotAuthorized.String() {
		t.Errorf("记下的理由是 %q", record.Reason)
	}
}

func TestServe_AllowedRequest_IsNotRecordedAsBlocked(t *testing.T) {
	// 反向对照：没有这条，上面那条可以靠「每个请求都记一条拦截」通过。
	h := newHarness(t)
	h.do(t, proxyRequest(t, http.MethodGet, "http://api.github.com/repos/acme/console", nil))

	if len(h.audits.blocked) != 0 {
		t.Errorf("放行的请求被记成了 %d 条拦截", len(h.audits.blocked))
	}
}

func TestServe_BlockedRecord_CarriesNoRequestHeadersOrBody(t *testing.T) {
	// 账本记元数据，不记正文与请求头（PRD §22.1）。
	h := newHarness(t)
	h.leases.err = apperr.New(apperr.CodeCredentialNotAuthorized)
	request := proxyRequest(t, http.MethodPost,
		"http://api.github.com/repos/acme/console/issues", strings.NewReader(sentinel.SentinelToken))
	request.Header.Set("Authorization", "Bearer "+sentinel.SentinelToken)

	h.do(t, request)

	encoded, err := json.Marshal(h.audits.blocked)
	if err != nil {
		t.Fatalf("序列化拦截记录失败：%v", err)
	}
	for _, value := range sentinel.All() {
		if bytes.Contains(encoded, []byte(value)) {
			t.Errorf("拦截记录里出现了哨兵 %s：%s", value, encoded)
		}
	}
}

func TestServe_AuditWriteFails_IsLoggedAndDoesNotChangeTheRefusal(t *testing.T) {
	// 记不下来不改变拦截结果 —— 请求本来就被拒了。但不能吞掉，
	// 账本缺一条要让运维知道。
	h := newHarness(t)
	h.leases.err = apperr.New(apperr.CodeCredentialNotAuthorized)
	h.audits.err = apperr.New(apperr.CodeInternal)

	recorder := h.do(t, proxyRequest(t, http.MethodGet, "http://api.github.com/repos/acme/console", nil))

	if recorder.Code != http.StatusForbidden {
		t.Errorf("状态码为 %d，期望仍然是 403", recorder.Code)
	}
	if len(h.exchange.calls) != 0 {
		t.Errorf("审计失败却发生了 %d 次出站", len(h.exchange.calls))
	}
	if !strings.Contains(h.logs.String(), "拦截记录写入失败") {
		t.Errorf("审计写入失败没有留下任何痕迹：\n%s", h.logs.String())
	}
}
