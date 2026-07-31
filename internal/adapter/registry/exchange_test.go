package registry_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/adapter/github"
	"github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/secret"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * 已授权请求执行的用例（REQ-PROXY-002、REQ-NFR-002）。
 *
 * 这段路是凭据明文唯一经过的地方，因此用例分两类：
 *   一类守「凭据确实到了外部服务」——否则请求根本成不了；
 *   一类守「凭据没有到别的任何地方」——尤其是返回给 Agent 的那份内容。
 * 两类都要有：只测前者，一个把 Authorization 原样回显的实现照样全绿。
 */

const exchangeOperationID = "operation_exchange_test"

// countingCredentials 记下取了几次、清没清零。
type countingCredentials struct {
	value  secret.Value
	fetch  int
	err    error
	lastID string
}

func (c *countingCredentials) Fetch(_ context.Context, referenceID string) (secret.Value, error) {
	c.fetch++
	c.lastID = referenceID
	if c.err != nil {
		return secret.Value{}, c.err
	}
	return c.value, nil
}

type stubReferences struct {
	referenceID string
	err         error
}

func (s stubReferences) ReferenceFor(context.Context, string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.referenceID, nil
}

// upstream 是一个记下收到的 Authorization 的假 GitHub。
type upstream struct {
	server        *httptest.Server
	authorization string
}

func newUpstream(t *testing.T, status int, body string) *upstream {
	t.Helper()

	recorded := &upstream{}
	recorded.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorded.authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("假服务写响应失败：%v", err)
		}
	}))
	t.Cleanup(recorded.server.Close)
	return recorded
}

func newExchange(t *testing.T, baseURL string, credentials registry.Credentials) *registry.Exchange {
	t.Helper()

	adapter, err := github.New(github.Options{BaseURL: baseURL})
	if err != nil {
		t.Fatalf("构造 Adapter 失败：%v", err)
	}
	all, err := registry.New(adapter)
	if err != nil {
		t.Fatalf("构造注册表失败：%v", err)
	}
	exchange, err := registry.NewExchange(all, credentials, stubReferences{referenceID: "reference_1"})
	if err != nil {
		t.Fatalf("构造 Exchange 失败：%v", err)
	}
	return exchange
}

func sentinelCredential(t *testing.T) *countingCredentials {
	t.Helper()

	value := secret.New([]byte(sentinel.SentinelToken))
	t.Cleanup(value.Zero)
	return &countingCredentials{value: value}
}

func readRepository() registry.ExchangeRequest {
	return registry.ExchangeRequest{
		Service: github.Service, Operation: "read_repository", IdentityID: "identity_1",
		Resource: map[string]string{"owner": "runcoor", "repo": "opendelo"},
		Body:     nil, OperationID: exchangeOperationID,
	}
}

func TestExchangeSend_InjectsTheCredentialOutboundAndNeverReturnsIt(t *testing.T) {
	fake := newUpstream(t, http.StatusOK,
		`{"id":1,"name":"opendelo","full_name":"runcoor/opendelo","private":false,`+
			`"token":"`+sentinel.SentinelToken+`","default_branch":"main"}`)
	credentials := sentinelCredential(t)
	exchange := newExchange(t, fake.server.URL, credentials)

	reply, err := exchange.Send(t.Context(), readRepository())
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}

	// 一、凭据确实到了外部服务。
	if !strings.Contains(fake.authorization, sentinel.SentinelToken) {
		t.Errorf("外部服务收到的 Authorization 为 %q，期望带上凭据", fake.authorization)
	}
	if credentials.fetch != 1 {
		t.Errorf("取了 %d 次凭据，期望恰好 1 次（不缓存也不重复取）", credentials.fetch)
	}

	// 二、凭据没有到 Agent 那份内容里 —— 包括外部服务在响应体里回显的那次。
	if strings.Contains(string(reply.Body), sentinel.SentinelToken) {
		t.Errorf("返回给 Agent 的内容里出现了凭据哨兵：%s", reply.Body)
	}
	if reply.StatusCode != http.StatusOK {
		t.Errorf("状态码为 %d，期望 200", reply.StatusCode)
	}

	var envelope registry.Result
	if err := json.Unmarshal(reply.Body, &envelope); err != nil {
		t.Fatalf("答复不是 Result 的形状：%v", err)
	}
	if !envelope.OK || envelope.OperationID != exchangeOperationID {
		t.Errorf("答复没有带上成功标记与 operation_id：%+v", envelope)
	}
	if len(envelope.Data) == 0 {
		t.Error("答复里没有任何数据 —— 脱敏把该返回的也滤掉了")
	}

	// 三、用完即清零。不清零的话明文会一直躺在进程内存里，直到 GC 决定回收它
	if len(credentials.value.Reveal()) != 0 {
		t.Error("请求结束后凭据没有被清零")
	}
}

func TestExchangeSend_UpstreamFailure_IsReportedWithoutTheOriginalStatus(t *testing.T) {
	// 外部服务的原始状态码是它的内部信息。Agent 需要知道的只是「成没成」。
	fake := newUpstream(t, http.StatusTeapot, `{"message":"I am a teapot"}`)
	exchange := newExchange(t, fake.server.URL, sentinelCredential(t))

	reply, err := exchange.Send(t.Context(), readRepository())
	if err != nil {
		t.Fatalf("上游失败被折成了错误而不是一条失败结果：%v", err)
	}

	// 502 而不是 418：外部服务的原始状态码是它的内部信息。只断言「不等于 418」
	// 是不够的 —— 那样把失败一律报成 200 也能通过。
	if reply.StatusCode != http.StatusBadGateway {
		t.Errorf("状态码为 %d，期望 502", reply.StatusCode)
	}

	var envelope registry.Result
	if err := json.Unmarshal(reply.Body, &envelope); err != nil {
		t.Fatalf("答复不是 Result 的形状：%v", err)
	}
	if envelope.OK {
		t.Error("上游失败却报成了成功")
	}
	if envelope.Error == nil {
		t.Error("失败的答复里没有可解释的错误")
	}
}

func TestExchangeSend_UndeclaredOperation_IsRefusedBeforeTheCredentialIsFetched(t *testing.T) {
	// 顺序在这里是安全属性：未声明的操作不该让凭据在内存里存在过一次。
	fake := newUpstream(t, http.StatusOK, `{}`)
	credentials := sentinelCredential(t)
	exchange := newExchange(t, fake.server.URL, credentials)

	request := readRepository()
	request.Operation = "delete_organization"

	if _, err := exchange.Send(t.Context(), request); !apperr.Is(err, apperr.CodeCapabilityNotOffered) {
		t.Fatalf("错误码为 %s，期望 capability_not_offered（%v）", apperr.CodeOf(err), err)
	}
	if credentials.fetch != 0 {
		t.Errorf("未声明的操作取了 %d 次凭据，期望 0 次", credentials.fetch)
	}
	if fake.authorization != "" {
		t.Error("未声明的操作产生了出站流量")
	}
}

func TestExchangeSend_UnknownService_IsRefusedBeforeTheCredentialIsFetched(t *testing.T) {
	fake := newUpstream(t, http.StatusOK, `{}`)
	credentials := sentinelCredential(t)
	exchange := newExchange(t, fake.server.URL, credentials)

	request := readRepository()
	request.Service = "not-a-registered-service"

	if _, err := exchange.Send(t.Context(), request); !apperr.Is(err, apperr.CodeCapabilityNotOffered) {
		t.Fatalf("错误码为 %s，期望 capability_not_offered（%v）", apperr.CodeOf(err), err)
	}
	if credentials.fetch != 0 {
		t.Errorf("未知服务取了 %d 次凭据，期望 0 次", credentials.fetch)
	}
}

func TestExchangeSend_CredentialSourceUnavailable_ProducesNoOutboundTraffic(t *testing.T) {
	// 凭据源不可用是 Fail Closed 的十种情况之一。取不到就不发请求 ——
	// 发一个不带凭据的请求出去只会拿到 401，而账本上会多一条没有意义的出站记录。
	fake := newUpstream(t, http.StatusOK, `{}`)
	credentials := sentinelCredential(t)
	credentials.err = apperr.New(apperr.CodeProviderUnavailable).WithDetail("用例注入")
	exchange := newExchange(t, fake.server.URL, credentials)

	if _, err := exchange.Send(t.Context(), readRepository()); !apperr.Is(
		err, apperr.CodeProviderUnavailable,
	) {
		t.Fatalf("错误码为 %s，期望 provider_unavailable（%v）", apperr.CodeOf(err), err)
	}
	if fake.authorization != "" {
		t.Error("取不到凭据却仍然发出了出站请求")
	}
}

func TestNewExchange_MissingDependency_IsRefused(t *testing.T) {
	all, err := registry.New()
	if err != nil {
		t.Fatalf("构造空注册表失败：%v", err)
	}

	cases := []struct {
		name        string
		registry    *registry.Registry
		credentials registry.Credentials
		references  registry.CredentialReferences
	}{
		{"缺注册表", nil, &countingCredentials{}, stubReferences{}},
		{"缺凭据来源", all, nil, stubReferences{}},
		{"缺引用解析", all, &countingCredentials{}, nil},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := registry.NewExchange(
				testCase.registry, testCase.credentials, testCase.references,
			); err == nil {
				t.Error("依赖不全却构造成功了")
			}
		})
	}
}
