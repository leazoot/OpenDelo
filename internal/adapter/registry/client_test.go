package registry_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/secret"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * 出站通道的行为用例（REQ-ADAPTER-005、REQ-ADAPTER-008）。
 *
 * 全部用 httptest 起的本地假服务，**不访问任何真实外部服务**
 */

const operationID = "01J0OPERATIONIDFORTESTS0"

// countingServer 是一个记下收到几次请求的假服务。
type countingServer struct {
	*httptest.Server
	requests atomic.Int64
	paths    chan string
}

func newServer(t *testing.T, handler http.HandlerFunc) *countingServer {
	t.Helper()

	server := &countingServer{paths: make(chan string, 16)}
	server.Server = httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			server.requests.Add(1)
			select {
			case server.paths <- request.URL.RequestURI():
			default:
			}
			handler(writer, request)
		}))
	t.Cleanup(server.Close)
	return server
}

// respond 让假服务写回一段响应。
//
// 写不出去只可能是用例自己提前断了连接，直接 panic 让它暴露 ——
// 悄悄吞掉会让后面的断言去解释一个空响应。
func respond(writer http.ResponseWriter, body string) {
	if _, err := io.WriteString(writer, body); err != nil {
		panic(err)
	}
}

// noWait 让重试立刻发生，并记下每次退避应该等多久。
type noWait struct{ delays []time.Duration }

func (w *noWait) wait(context.Context, time.Duration) error { return nil }

func (w *noWait) record(_ context.Context, delay time.Duration) error {
	w.delays = append(w.delays, delay)
	return nil
}

func newClient(t *testing.T, baseURL string, adjust func(*registry.ClientOptions)) *registry.Client {
	t.Helper()

	options := registry.ClientOptions{
		BaseURL: baseURL,
		Timeout: 200 * time.Millisecond,
		Wait:    (&noWait{}).wait,
	}
	if adjust != nil {
		adjust(&options)
	}
	client, err := registry.NewClient(options)
	if err != nil {
		t.Fatalf("构造出站通道失败：%v", err)
	}
	return client
}

func request(path string) registry.Request {
	return registry.Request{
		Capability:  readCapability(),
		Path:        path,
		AuthScheme:  registry.AuthNone,
		OperationID: operationID,
	}
}

func assertCodeAndOperationID(t *testing.T, err error, expected apperr.Code) {
	t.Helper()

	if !apperr.Is(err, expected) {
		t.Fatalf("错误码为 %s，期望 %s（%v）", apperr.CodeOf(err), expected, err)
	}
	public := apperr.PublicOf(err, "")
	if public.OperationID != operationID {
		t.Errorf("错误里的操作 ID 为 %q，期望 %q", public.OperationID, operationID)
	}
}

// ——— Base URL ———

func TestNewClient_BaseURLThatIsNotAnAbsoluteHTTPAddress_IsRefused(t *testing.T) {
	for _, baseURL := range []string{"", "/repos", "ftp://example.com", "example.com", "://x"} {
		_, err := registry.NewClient(registry.ClientOptions{BaseURL: baseURL})
		if !apperr.Is(err, apperr.CodeInvalidConfiguration) {
			t.Errorf("%q 被接受了，错误码 %s", baseURL, apperr.CodeOf(err))
		}
	}
}

// ——— 端点白名单（REQ-ADAPTER-005 AC1）———

func TestDo_PathOutsideTheDeclaredTemplate_SendsNothing(t *testing.T) {
	server := newServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	})
	client := newClient(t, server.URL, nil)

	cases := []struct {
		name string
		path string
	}{
		{"段数多了", "/repos/octocat/hello/collaborators"},
		{"段数少了", "/repos/octocat"},
		{"字面段对不上", "/orgs/octocat/hello"},
		{"有空段", "/repos//hello"},
		{"想跳出去", "/repos/octocat/../../admin"},
		{"不是绝对路径", "repos/octocat/hello"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"时拒绝且不发出请求", func(t *testing.T) {
			_, err := client.Do(t.Context(), request(testCase.path))
			assertCodeAndOperationID(t, err, apperr.CodePathNotAllowed)
		})
	}

	if got := server.requests.Load(); got != 0 {
		t.Fatalf("被拒绝的路径仍然产生了 %d 次出站请求", got)
	}
}

func TestDo_PathMatchingTheTemplate_IsSent(t *testing.T) {
	server := newServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		respond(writer, `{"id":1}`)
	})
	client := newClient(t, server.URL, nil)

	outbound := request("/repos/octocat/hello")
	outbound.Query = url.Values{"per_page": []string{"1"}}

	response, err := client.Do(t.Context(), outbound)
	if err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	if response.StatusCode != http.StatusOK || string(response.Body) != `{"id":1}` {
		t.Fatalf("响应为 %d %q", response.StatusCode, response.Body)
	}
	if got := <-server.paths; got != "/repos/octocat/hello?per_page=1" {
		t.Errorf("假服务收到的是 %q", got)
	}
}

// ——— 跨主机重定向（REQ-ADAPTER-005 AC3）———

func TestDo_CrossHostRedirect_IsRefused(t *testing.T) {
	elsewhere := newServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		respond(writer, `{"id":"stolen"}`)
	})
	origin := newServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		http.Redirect(writer, &http.Request{}, elsewhere.URL+"/repos/octocat/hello",
			http.StatusFound)
	})
	client := newClient(t, origin.URL, nil)

	_, err := client.Do(t.Context(), request("/repos/octocat/hello"))
	assertCodeAndOperationID(t, err, apperr.CodePathNotAllowed)

	if got := elsewhere.requests.Load(); got != 0 {
		t.Fatalf("跨主机跳转仍然到达了另一台主机 %d 次", got)
	}
}

func TestDo_SameHostRedirect_IsFollowed(t *testing.T) {
	// 反向对照：同主机跳转必须还能走通，否则上面那条用例可能只是
	// 因为「所有跳转都被拒」而通过。
	var server *countingServer
	server = newServer(t, func(writer http.ResponseWriter, incoming *http.Request) {
		if strings.HasSuffix(incoming.URL.Path, "/hello") {
			http.Redirect(writer, incoming, server.URL+"/repos/octocat/moved", http.StatusFound)
			return
		}
		respond(writer, `{"id":2}`)
	})
	client := newClient(t, server.URL, nil)

	response, err := client.Do(t.Context(), request("/repos/octocat/hello"))
	if err != nil {
		t.Fatalf("同主机跳转失败：%v", err)
	}
	if string(response.Body) != `{"id":2}` {
		t.Errorf("跳转后的响应为 %q", response.Body)
	}
}

// ——— 响应体上限 ———

func TestDo_ResponseLargerThanTheCap_IsRefused(t *testing.T) {
	// 外部服务返回多大就读多大，等于把内存交给对方决定。
	server := newServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		respond(writer, strings.Repeat("x", 64))
	})
	client := newClient(t, server.URL, func(options *registry.ClientOptions) {
		options.MaxResponseBytes = 16
	})

	_, err := client.Do(t.Context(), request("/repos/octocat/hello"))
	assertCodeAndOperationID(t, err, apperr.CodeInternal)
}

func TestDo_ResponseExactlyAtTheCap_IsAccepted(t *testing.T) {
	server := newServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		respond(writer, strings.Repeat("x", 16))
	})
	client := newClient(t, server.URL, func(options *registry.ClientOptions) {
		options.MaxResponseBytes = 16
	})

	response, err := client.Do(t.Context(), request("/repos/octocat/hello"))
	if err != nil {
		t.Fatalf("刚好到上限的响应被拒绝：%v", err)
	}
	if len(response.Body) != 16 {
		t.Errorf("读回 %d 字节，期望 16", len(response.Body))
	}
}

// ——— 超时、重试与幂等（REQ-ADAPTER-008）———

func blockingServer(t *testing.T) *countingServer {
	t.Helper()

	return newServer(t, func(_ http.ResponseWriter, incoming *http.Request) {
		<-incoming.Context().Done()
	})
}

func TestDo_NonIdempotentOperationThatTimesOut_IsNotRetried(t *testing.T) {
	// AC1：超时不等于没执行。重试可能创建出第二个 PR。
	server := blockingServer(t)
	client := newClient(t, server.URL, func(options *registry.ClientOptions) {
		options.Timeout = 30 * time.Millisecond
	})

	outbound := request("/repos/octocat/hello/issues")
	outbound.Capability = writeCapability()

	_, err := client.Do(t.Context(), outbound)
	assertCodeAndOperationID(t, err, apperr.CodeAdapterTimeout)

	if got := server.requests.Load(); got != 1 {
		t.Fatalf("非幂等操作超时后产生了 %d 次出站请求，期望 1", got)
	}
}

func TestDo_IdempotentOperationThatTimesOut_IsRetriedTwiceWithBackoff(t *testing.T) {
	server := blockingServer(t)
	waiter := &noWait{}
	client := newClient(t, server.URL, func(options *registry.ClientOptions) {
		options.Timeout = 30 * time.Millisecond
		options.Backoff = 10 * time.Millisecond
		options.Wait = waiter.record
	})

	_, err := client.Do(t.Context(), request("/repos/octocat/hello"))
	assertCodeAndOperationID(t, err, apperr.CodeAdapterTimeout)

	if got := server.requests.Load(); got != 3 {
		t.Fatalf("幂等操作产生了 %d 次出站请求，期望 1 次加 2 次重试", got)
	}
	expected := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond}
	if len(waiter.delays) != len(expected) {
		t.Fatalf("退避了 %v，期望 %v", waiter.delays, expected)
	}
	for index, delay := range expected {
		if waiter.delays[index] != delay {
			t.Errorf("第 %d 次退避为 %v，期望 %v（指数增长）", index+1, waiter.delays[index], delay)
		}
	}
}

func TestDo_IdempotentOperationThatRecovers_ReportsTheAttemptCount(t *testing.T) {
	// AC2：重试次数要能写进审计。
	var seen atomic.Int64
	server := newServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		if seen.Add(1) == 1 {
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		respond(writer, `{"id":3}`)
	})
	client := newClient(t, server.URL, nil)

	response, err := client.Do(t.Context(), request("/repos/octocat/hello"))
	if err != nil {
		t.Fatalf("重试后仍然失败：%v", err)
	}
	if response.Attempts != 2 {
		t.Errorf("报告了 %d 次尝试，期望 2", response.Attempts)
	}
	if response.StatusCode != http.StatusOK {
		t.Errorf("最终状态为 %d", response.StatusCode)
	}
}

func TestDo_NonIdempotentOperationGettingAServerError_IsNotRetried(t *testing.T) {
	server := newServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
	})
	client := newClient(t, server.URL, nil)

	outbound := request("/repos/octocat/hello/issues")
	outbound.Capability = writeCapability()

	response, err := client.Do(t.Context(), outbound)
	if err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	if got := server.requests.Load(); got != 1 {
		t.Fatalf("非幂等操作在 5xx 后产生了 %d 次出站请求，期望 1", got)
	}
	if response.Attempts != 1 {
		t.Errorf("报告了 %d 次尝试，期望 1", response.Attempts)
	}
}

func TestDo_PathRefusal_IsNotRetried(t *testing.T) {
	// 路径不允许重试多少次都是同一个结果，重试只会让账本上多几条无意义记录。
	waiter := &noWait{}
	server := newServer(t, func(http.ResponseWriter, *http.Request) {})
	client := newClient(t, server.URL, func(options *registry.ClientOptions) {
		options.Wait = waiter.record
	})

	_, err := client.Do(t.Context(), request("/orgs/octocat/hello"))
	assertCodeAndOperationID(t, err, apperr.CodePathNotAllowed)

	if len(waiter.delays) != 0 {
		t.Errorf("被拒绝的路径仍然退避重试了 %v", waiter.delays)
	}
}

// ——— 凭据注入 ———

func TestDo_CredentialInjection_GoesIntoTheHeaderOnly(t *testing.T) {
	cases := []struct {
		name       string
		scheme     registry.AuthScheme
		authHeader string
		wantHeader string
		wantValue  string
	}{
		{"Bearer 方案", registry.AuthBearer, "", "Authorization", "Bearer " + sentinel.SentinelToken},
		{"自定义头方案", registry.AuthHeader, "X-Auth-Key", "X-Auth-Key", sentinel.SentinelToken},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"把凭据放进请求头", func(t *testing.T) {
			received := make(chan *http.Request, 1)
			server := newServer(t, func(writer http.ResponseWriter, incoming *http.Request) {
				received <- incoming.Clone(context.Background())
				respond(writer, `{"id":1}`)
			})
			client := newClient(t, server.URL, nil)

			outbound := request("/repos/octocat/hello")
			outbound.AuthScheme = testCase.scheme
			outbound.AuthHeader = testCase.authHeader
			outbound.Credential = secret.New([]byte(sentinel.SentinelToken))
			defer outbound.Credential.Zero()

			if _, err := client.Do(t.Context(), outbound); err != nil {
				t.Fatalf("请求失败：%v", err)
			}

			incoming := <-received
			if got := incoming.Header.Get(testCase.wantHeader); got != testCase.wantValue {
				t.Fatalf("请求头 %s 为 %q", testCase.wantHeader, got)
			}
			// 凭据不进 URL、不进 Query、不进请求体：那三处都会被外部服务写进日志。
			if strings.Contains(incoming.URL.String(), sentinel.SentinelToken) {
				t.Error("凭据出现在了 URL 里")
			}
		})
	}
}

func TestDo_NoCredentialScheme_SendsNoAuthorizationHeader(t *testing.T) {
	received := make(chan *http.Request, 1)
	server := newServer(t, func(writer http.ResponseWriter, incoming *http.Request) {
		received <- incoming.Clone(context.Background())
		respond(writer, `{"id":1}`)
	})
	client := newClient(t, server.URL, nil)

	if _, err := client.Do(t.Context(), request("/repos/octocat/hello")); err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	if got := (<-received).Header.Get("Authorization"); got != "" {
		t.Errorf("没有声明凭据方案却发出了 Authorization: %q", got)
	}
}

func TestDo_UnknownCredentialScheme_IsRefused(t *testing.T) {
	server := newServer(t, func(http.ResponseWriter, *http.Request) {})
	client := newClient(t, server.URL, nil)

	cases := []struct {
		name       string
		scheme     registry.AuthScheme
		authHeader string
	}{
		{"认不出的注入方式", "query", ""},
		{"自定义头方案没有头名", registry.AuthHeader, "  "},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"时拒绝", func(t *testing.T) {
			outbound := request("/repos/octocat/hello")
			outbound.AuthScheme = testCase.scheme
			outbound.AuthHeader = testCase.authHeader

			_, err := client.Do(t.Context(), outbound)
			assertCodeAndOperationID(t, err, apperr.CodeInvalidConfiguration)
			if strings.Contains(err.Error(), sentinel.SentinelToken) {
				t.Error("错误信息里出现了哨兵")
			}
		})
	}
}

func TestDo_TransportFailure_IsGatewayUnavailable(t *testing.T) {
	// 连不上时同样必须拒绝，不能因为「可能只是抖动」就放行。
	server := newServer(t, func(http.ResponseWriter, *http.Request) {})
	address := server.URL
	server.Close()

	client := newClient(t, address, nil)
	_, err := client.Do(t.Context(), request("/repos/octocat/hello"))
	assertCodeAndOperationID(t, err, apperr.CodeGatewayUnavailable)
}

func TestDefaults_AreTheDocumentedOnes(t *testing.T) {
	if registry.DefaultTimeout != 30*time.Second {
		t.Errorf("默认超时为 %v，期望 30 秒", registry.DefaultTimeout)
	}
	if registry.MaxRetries != 2 {
		t.Errorf("重试上限为 %d，期望 2", registry.MaxRetries)
	}
	if registry.DefaultMaxResponseBytes != 4<<20 {
		t.Errorf("响应体上限为 %d，期望 4 MiB", registry.DefaultMaxResponseBytes)
	}
}
