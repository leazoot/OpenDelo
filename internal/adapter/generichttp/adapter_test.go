package generichttp_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Runcoor/opendelo/internal/adapter/generichttp"
	"github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/secret"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * Generic HTTP Adapter 的行为用例（REQ-ADAPTER-005）。
 *
 * 假服务用 httptest 的 TLS 服务器，Base URL 写 https://example.com
 * （httptest 的自带证书正好签了这个名字），解析器与拨号都被接到本地 ——
 * **生产路径上的 Base URL 校验与目标校验照常运行，没有跳过开关**
 */

const operationID = "01J0GENERICHTTPOPERATION"

// publicAddress 是 TEST-NET-3，公网保留段，用来扮演一个合法的解析结果。
var publicAddress = net.ParseIP("203.0.113.20")

func resolveTo(addresses ...net.IP) generichttp.Resolver {
	return func(context.Context, string) ([]net.IP, error) { return addresses, nil }
}

type fakeService struct {
	*httptest.Server
	requests atomic.Int64
	received chan *http.Request
	status   int
	body     string
}

func newFakeService(t *testing.T, status int, body string) *fakeService {
	t.Helper()

	fake := &fakeService{received: make(chan *http.Request, 4), status: status, body: body}
	fake.Server = httptest.NewTLSServer(http.HandlerFunc(
		func(writer http.ResponseWriter, incoming *http.Request) {
			fake.requests.Add(1)
			select {
			case fake.received <- incoming.Clone(incoming.Context()):
			default:
			}
			writer.WriteHeader(fake.status)
			if _, err := io.WriteString(writer, fake.body); err != nil {
				panic(err)
			}
		}))
	t.Cleanup(fake.Close)
	return fake
}

// clientTo 把 https://example.com 的出站接到本地假服务上，TLS 校验照常。
func clientTo(t *testing.T, fake *fakeService) *registry.Client {
	t.Helper()

	transport, ok := fake.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatal("httptest 的客户端传输层不是 *http.Transport")
	}
	routed := transport.Clone()
	address := strings.TrimPrefix(fake.URL, "https://")
	routed.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}

	client, err := registry.NewClient(registry.ClientOptions{
		BaseURL: "https://example.com", Transport: routed,
	})
	if err != nil {
		t.Fatalf("构造出站通道失败：%v", err)
	}
	return client
}

func definition() generichttp.Definition {
	return generichttp.Definition{
		Service:     "acme",
		DisplayName: "ACME 内部工单",
		BaseURL:     "https://example.com",
		AuthScheme:  registry.AuthBearer,
		Operations: []generichttp.OperationDefinition{
			{
				Operation:      "read_ticket",
				Method:         "GET",
				Path:           "/tickets/{ticket_id}",
				InputSchema:    `{"type":"object"}`,
				RiskLabel:      registry.RiskLabelLow,
				RedactionRules: []string{},
				ResponseFields: []string{"id", "title", "state"},
				Rollback:       registry.RollbackNone,
				Idempotency:    registry.Idempotent,
				ResourceKeys:   []string{"ticket_id"},
			},
			{
				Operation:      "create_ticket",
				Method:         "POST",
				Path:           "/tickets",
				InputSchema:    `{"type":"object"}`,
				RiskLabel:      registry.RiskLabelMedium,
				RedactionRules: []string{},
				ResponseFields: []string{"id", "title"},
				Rollback:       registry.RollbackManual,
				Idempotency:    registry.NonIdempotent,
				ResourceKeys:   []string{"ticket_id"},
			},
		},
	}
}

func newAdapter(t *testing.T, fake *fakeService, resolve generichttp.Resolver) *generichttp.Adapter {
	t.Helper()

	adapter, err := generichttp.New(t.Context(), definition(), generichttp.Options{
		Resolver: resolve, Client: clientTo(t, fake),
	})
	if err != nil {
		t.Fatalf("构造 Adapter 失败：%v", err)
	}
	return adapter
}

func credential(t *testing.T) secret.Value {
	t.Helper()

	value := secret.New([]byte(sentinel.SentinelToken))
	t.Cleanup(value.Zero)
	return value
}

func assertConfigError(t *testing.T, err error) {
	t.Helper()

	if !apperr.Is(err, apperr.CodeInvalidConfiguration) {
		t.Fatalf("错误码为 %s，期望 invalid_configuration（%v）", apperr.CodeOf(err), err)
	}
}

// ——— SSRF：Base URL 不能指向本机或内网 ———

func TestNew_BaseURLPointingInside_IsRefused(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		resolve generichttp.Resolver
	}{
		{"回环字面量", "https://127.0.0.1", nil},
		{"IPv6 回环字面量", "https://[::1]", nil},
		{"私有网段 10/8", "https://10.1.2.3", nil},
		{"私有网段 172.16/12", "https://172.20.0.1", nil},
		{"私有网段 192.168/16", "https://192.168.1.1", nil},
		{"link-local 元数据端点", "https://169.254.169.254", nil},
		{"运营商级 NAT", "https://100.100.0.1", nil},
		{"未指定地址", "https://0.0.0.0", nil},
		{"唯一本地 IPv6", "https://[fd00::1]", nil},
		{"域名解析到回环", "https://inside.example.com", resolveTo(net.ParseIP("127.0.0.1"))},
		{"域名解析到私有网段", "https://inside.example.com", resolveTo(net.ParseIP("10.0.0.5"))},
		{
			"域名同时解析到公网与回环",
			"https://mixed.example.com",
			resolveTo(publicAddress, net.ParseIP("127.0.0.1")),
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"时拒绝保存", func(t *testing.T) {
			target := definition()
			target.BaseURL = testCase.baseURL

			resolve := testCase.resolve
			if resolve == nil {
				resolve = resolveTo(publicAddress)
			}
			_, err := generichttp.New(t.Context(), target, generichttp.Options{Resolver: resolve})
			assertConfigError(t, err)
		})
	}
}

func TestNew_BaseURLShape_IsChecked(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
	}{
		{"明文 HTTP", "http://example.com"},
		{"没有主机名", "https://"},
		{"不是绝对地址", "/tickets"},
		{"带用户名密码", "https://user:pass@example.com"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"时拒绝保存", func(t *testing.T) {
			target := definition()
			target.BaseURL = testCase.baseURL

			_, err := generichttp.New(t.Context(), target,
				generichttp.Options{Resolver: resolveTo(publicAddress)})
			assertConfigError(t, err)
		})
	}
}

func TestNew_WhenTheHostCannotBeResolved_IsRefused(t *testing.T) {
	// 不确定目标是谁的时候不发请求。
	// 两种失败要分别落在自己那条检查上：只断言错误码的话，把其中一条删掉
	// 用例仍会因为另一条而通过。
	failing := func(context.Context, string) ([]net.IP, error) {
		return nil, &net.DNSError{Err: "no such host", Name: "example.com"}
	}
	_, err := generichttp.New(t.Context(), definition(), generichttp.Options{Resolver: failing})
	assertConfigError(t, err)
	if !strings.Contains(err.Error(), "解析失败") {
		t.Errorf("拒绝理由为 %q，期望来自解析失败那条检查", err)
	}

	empty := func(context.Context, string) ([]net.IP, error) { return nil, nil }
	_, err = generichttp.New(t.Context(), definition(), generichttp.Options{Resolver: empty})
	assertConfigError(t, err)
	if !strings.Contains(err.Error(), "没有解析出任何地址") {
		t.Errorf("拒绝理由为 %q，期望来自空结果那条检查", err)
	}
}

func TestExecute_WhenTheHostStartsResolvingInside_TheRequestIsRefused(t *testing.T) {
	// DNS 重绑定：保存时解析到公网，请求时解析到回环。
	fake := newFakeService(t, http.StatusOK, `{"id":"t1"}`)

	var calls atomic.Int64
	flipping := func(context.Context, string) ([]net.IP, error) {
		if calls.Add(1) == 1 {
			return []net.IP{publicAddress}, nil
		}
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}

	adapter, err := generichttp.New(t.Context(), definition(), generichttp.Options{
		Resolver: flipping, Client: clientTo(t, fake),
	})
	if err != nil {
		t.Fatalf("保存时应当通过：%v", err)
	}

	_, err = adapter.Execute(t.Context(), generichttp.ExecuteRequest{
		Operation: "read_ticket", Resource: map[string]string{"ticket_id": "t1"},
		Credential: credential(t), OperationID: operationID,
	})
	if !apperr.Is(err, apperr.CodePathNotAllowed) {
		t.Fatalf("错误码为 %s，期望 path_not_allowed（%v）", apperr.CodeOf(err), err)
	}
	if got := fake.requests.Load(); got != 0 {
		t.Errorf("目标已经指向本机，却仍然产生了 %d 次出站请求", got)
	}
}

// ——— 未声明风险等级则存不下来（AC2）———

func TestNew_OperationWithoutARiskLevel_CannotBeSaved(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*generichttp.OperationDefinition)
	}{
		{"没有风险等级", func(o *generichttp.OperationDefinition) { o.RiskLabel = "" }},
		{"风险等级认不出来", func(o *generichttp.OperationDefinition) { o.RiskLabel = "critical" }},
		{"没有请求方法", func(o *generichttp.OperationDefinition) { o.Method = "" }},
		{"没有路径", func(o *generichttp.OperationDefinition) { o.Path = "tickets" }},
		{"没有输入 Schema", func(o *generichttp.OperationDefinition) { o.InputSchema = "" }},
		{"没有声明脱敏规则", func(o *generichttp.OperationDefinition) { o.RedactionRules = nil }},
		{"没有响应过滤白名单", func(o *generichttp.OperationDefinition) { o.ResponseFields = nil }},
		{"没有回滚能力", func(o *generichttp.OperationDefinition) { o.Rollback = "" }},
		{"没有幂等性", func(o *generichttp.OperationDefinition) { o.Idempotency = "" }},
		{"最小 Scope 少了路径占位符", func(o *generichttp.OperationDefinition) { o.ResourceKeys = []string{"other"} }},
		{"不可逆的写操作标成 medium", func(o *generichttp.OperationDefinition) {
			o.Method = "DELETE"
			o.Rollback = registry.RollbackNone
			o.RiskLabel = registry.RiskLabelMedium
			o.Idempotency = registry.NonIdempotent
		}},
		{"删除类操作标成 low", func(o *generichttp.OperationDefinition) {
			o.Nature = registry.Nature{Destructive: true}
		}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"时拒绝保存", func(t *testing.T) {
			target := definition()
			testCase.mutate(&target.Operations[0])

			_, err := generichttp.New(t.Context(), target,
				generichttp.Options{Resolver: resolveTo(publicAddress)})
			assertConfigError(t, err)
		})
	}
}

func TestNew_DefinitionShape_IsChecked(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*generichttp.Definition)
	}{
		{"没有服务名", func(d *generichttp.Definition) { d.Service = "  " }},
		{"没有显示名", func(d *generichttp.Definition) { d.DisplayName = "" }},
		{"认不出的注入方式", func(d *generichttp.Definition) { d.AuthScheme = "query" }},
		{"自定义头方案没有头名", func(d *generichttp.Definition) {
			d.AuthScheme = registry.AuthHeader
			d.AuthHeader = " "
		}},
		{"没有定义任何操作", func(d *generichttp.Definition) { d.Operations = nil }},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"时拒绝保存", func(t *testing.T) {
			target := definition()
			testCase.mutate(&target)

			_, err := generichttp.New(t.Context(), target,
				generichttp.Options{Resolver: resolveTo(publicAddress)})
			assertConfigError(t, err)
		})
	}
}

// ——— 路径白名单（AC1）———

func TestExecute_UndeclaredOperation_SendsNothing(t *testing.T) {
	fake := newFakeService(t, http.StatusOK, `{"id":"t1"}`)
	adapter := newAdapter(t, fake, resolveTo(publicAddress))

	_, err := adapter.Execute(t.Context(), generichttp.ExecuteRequest{
		Operation: "delete_ticket", Resource: map[string]string{"ticket_id": "t1"},
		Credential: credential(t), OperationID: operationID,
	})
	if !apperr.Is(err, apperr.CodeCapabilityNotOffered) {
		t.Fatalf("错误码为 %s，期望 capability_not_offered", apperr.CodeOf(err))
	}
	if got := fake.requests.Load(); got != 0 {
		t.Errorf("未声明的操作产生了 %d 次出站请求", got)
	}
}

func TestExecute_ResourceValueThatWouldChangeTheEndpoint_SendsNothing(t *testing.T) {
	cases := []string{"", "   ", "t1/../admin", "t1?x=1", "t1#frag", ".."}

	for _, value := range cases {
		t.Run("取值 "+value+" 被拒绝", func(t *testing.T) {
			fake := newFakeService(t, http.StatusOK, `{"id":"t1"}`)
			adapter := newAdapter(t, fake, resolveTo(publicAddress))

			_, err := adapter.Execute(t.Context(), generichttp.ExecuteRequest{
				Operation: "read_ticket", Resource: map[string]string{"ticket_id": value},
				Credential: credential(t), OperationID: operationID,
			})
			if !apperr.Is(err, apperr.CodeInvalidRequest) {
				t.Fatalf("错误码为 %s，期望 invalid_request（%v）", apperr.CodeOf(err), err)
			}
			if got := fake.requests.Load(); got != 0 {
				t.Errorf("被拒绝的取值仍然产生了 %d 次出站请求", got)
			}
		})
	}
}

// ——— 正常路径与脱敏 ———

func TestExecute_DeclaredOperations_AreCallableAndRedacted(t *testing.T) {
	body := `{"id":"t1","title":"打不开","state":"open",` +
		`"api_token":"` + sentinel.SentinelToken + `","internal_note":"leak"}`

	cases := []struct {
		operation string
		method    string
		path      string
		input     string
	}{
		{"read_ticket", "GET", "/tickets/t1", ""},
		{"create_ticket", "POST", "/tickets", `{"title":"打不开"}`},
	}

	for _, testCase := range cases {
		t.Run(testCase.operation+"可调用且结果已脱敏", func(t *testing.T) {
			fake := newFakeService(t, http.StatusOK, body)
			adapter := newAdapter(t, fake, resolveTo(publicAddress))

			result, err := adapter.Execute(t.Context(), generichttp.ExecuteRequest{
				Operation:  testCase.operation,
				Resource:   map[string]string{"ticket_id": "t1"},
				Input:      json.RawMessage(testCase.input),
				Credential: credential(t), OperationID: operationID,
			})
			if err != nil {
				t.Fatalf("执行失败：%v", err)
			}
			if !result.OK {
				t.Fatalf("结果为失败：%+v", result.Error)
			}

			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("序列化失败：%v", err)
			}
			for _, value := range sentinel.All() {
				if strings.Contains(string(encoded), value) {
					t.Fatalf("输出里出现了哨兵：%s", encoded)
				}
			}
			if strings.Contains(string(encoded), "internal_note") {
				t.Error("白名单之外的字段被返回了")
			}
			if !strings.Contains(string(result.Data), `"id":"t1"`) {
				t.Errorf("白名单允许的字段没有返回：%s", result.Data)
			}

			incoming := <-fake.received
			if incoming.Method != testCase.method || incoming.URL.Path != testCase.path {
				t.Errorf("假服务收到 %s %s，期望 %s %s",
					incoming.Method, incoming.URL.Path, testCase.method, testCase.path)
			}
			if got := incoming.Header.Get("Authorization"); got != "Bearer "+sentinel.SentinelToken {
				t.Errorf("凭据没有注入到 Authorization：%q", got)
			}
		})
	}
}

func TestCapabilities_RegisterWithoutAnyDeclarationError(t *testing.T) {
	fake := newFakeService(t, http.StatusOK, `{}`)
	adapter := newAdapter(t, fake, resolveTo(publicAddress))

	if _, err := registry.New(adapter); err != nil {
		t.Fatalf("注册失败：%v", err)
	}
	if adapter.Service() != "acme" || adapter.Kind() != registry.KindGenericHTTP {
		t.Errorf("服务名为 %q，种类为 %q", adapter.Service(), adapter.Kind())
	}
}

func TestExecute_UpstreamFailure_IsExplainedWithoutTheRawBody(t *testing.T) {
	fake := newFakeService(t, http.StatusForbidden,
		`{"message":"`+sentinel.SentinelToken+`"}`)
	adapter := newAdapter(t, fake, resolveTo(publicAddress))

	result, err := adapter.Execute(t.Context(), generichttp.ExecuteRequest{
		Operation: "read_ticket", Resource: map[string]string{"ticket_id": "t1"},
		Credential: credential(t), OperationID: operationID,
	})
	if err != nil {
		t.Fatalf("执行返回了传输层错误：%v", err)
	}
	if result.OK {
		t.Fatal("403 被当成了成功")
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	for _, value := range sentinel.All() {
		if strings.Contains(string(encoded), value) {
			t.Fatalf("输出里出现了哨兵：%s", encoded)
		}
	}
}
