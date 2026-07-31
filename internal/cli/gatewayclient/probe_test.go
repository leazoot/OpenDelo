package gatewayclient_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/cli/gatewayclient"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * 本包是「出站请求只能从 internal/adapter 发出」的唯一已登记例外。
 * 这些用例钉住让那个例外成立的三条理由。
 */

func newGateway(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func address(t *testing.T, server *httptest.Server) string {
	t.Helper()

	return strings.TrimPrefix(server.URL, "http://")
}

func TestProbe_SendsTheSessionTokenInTheHeaderOnly(t *testing.T) {
	// 令牌进 URL 会落进 shell 历史、进程列表与访问日志（REQ-API-005）。
	received := make(chan *http.Request, 1)
	server := newGateway(t, func(writer http.ResponseWriter, incoming *http.Request) {
		received <- incoming.Clone(incoming.Context())
		if _, err := writer.Write([]byte(`{"status":"running","version":"0.1.0"}`)); err != nil {
			panic(err)
		}
	})

	status, err := gatewayclient.Probe(t.Context(), address(t, server), sentinel.SentinelToken)
	if err != nil {
		t.Fatalf("探测失败：%v", err)
	}
	if status.Status != "running" {
		t.Errorf("状态为 %q，期望 running", status.Status)
	}

	incoming := <-received
	if got := incoming.Header.Get("Authorization"); got != "Bearer "+sentinel.SentinelToken {
		t.Errorf("Authorization 为 %q", got)
	}
	if strings.Contains(incoming.URL.String(), sentinel.SentinelToken) {
		t.Error("令牌出现在了 URL 里")
	}
}

func TestProbe_DoesNotFollowRedirects(t *testing.T) {
	// 跟过去就等于把会话令牌送到跳转目标那里，而目标由对端指定。
	elsewhere := newGateway(t, func(writer http.ResponseWriter, _ *http.Request) {
		if _, err := writer.Write([]byte(`{"status":"running"}`)); err != nil {
			panic(err)
		}
	})
	origin := newGateway(t, func(writer http.ResponseWriter, incoming *http.Request) {
		http.Redirect(writer, incoming, elsewhere.URL+"/v1/gateway/status", http.StatusFound)
	})

	_, err := gatewayclient.Probe(t.Context(), address(t, origin), sentinel.SentinelToken)
	if !apperr.Is(err, apperr.CodeGatewayUnavailable) {
		t.Fatalf("错误码为 %s，期望 gateway_unavailable（%v）", apperr.CodeOf(err), err)
	}
}

func TestProbe_ResponsesThatAreNotTheStatusStructure_AreRefused(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"状态码不是 200", http.StatusServiceUnavailable, `{"status":"running"}`},
		{"响应不是 JSON", http.StatusOK, `not json`},
		{"响应缺状态字段", http.StatusOK, `{"version":"0.1.0"}`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"时拒绝", func(t *testing.T) {
			server := newGateway(t, func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(testCase.status)
				if _, err := writer.Write([]byte(testCase.body)); err != nil {
					panic(err)
				}
			})

			_, err := gatewayclient.Probe(t.Context(), address(t, server), sentinel.SentinelToken)
			if !apperr.Is(err, apperr.CodeGatewayUnavailable) {
				t.Fatalf("错误码为 %s，期望 gateway_unavailable（%v）", apperr.CodeOf(err), err)
			}
		})
	}
}

func TestProbe_WhenNothingIsListening_IsGatewayUnavailable(t *testing.T) {
	server := newGateway(t, func(http.ResponseWriter, *http.Request) {})
	dead := address(t, server)
	server.Close()

	_, err := gatewayclient.Probe(t.Context(), dead, sentinel.SentinelToken)
	if !apperr.Is(err, apperr.CodeGatewayUnavailable) {
		t.Fatalf("错误码为 %s，期望 gateway_unavailable（%v）", apperr.CodeOf(err), err)
	}
}
