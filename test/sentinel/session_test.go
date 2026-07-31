package sentinel_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/config"
	"github.com/Runcoor/opendelo/internal/platform/logging"
	"github.com/Runcoor/opendelo/internal/transport/httpapi"
	"github.com/Runcoor/opendelo/test/fixtures"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * 八个面的哨兵扫描之三：Web API 响应与运行日志（REQ-NFR-002 AC1、REQ-API-005）。
 *
 * 这里的哨兵扮演会话令牌。它有一个**唯一的合法去处**：入口文档的 meta —— Console
 * 必须从那里拿到令牌，否则连第一个 /v1 请求都发不出去。
 * 除此之外的任何地方出现，都是泄漏：静态资源、API 响应、错误信息、响应头、日志。
 */

const entryDocument = `<!doctype html><html lang="zh-CN"><head><title>OpenDelo</title></head>` +
	`<body><div id="root"></div><script type="module" src="/assets/index.js"></script></body></html>`

// gatewayWithSentinelToken 起一个以哨兵为会话令牌的 Gateway，并返回它的日志缓冲。
func gatewayWithSentinelToken(t *testing.T) (http.Handler, *bytes.Buffer) {
	t.Helper()

	var logs bytes.Buffer
	server, err := httpapi.New(httpapi.Options{
		Config: config.Default(),
		Console: fstest.MapFS{
			"index.html":      &fstest.MapFile{Data: []byte(entryDocument)},
			"assets/index.js": &fstest.MapFile{Data: []byte("export const seam = 'boundary'\n")},
		},
		Clock:        clock.NewFixed(time.Date(2026, 7, 28, 9, 15, 30, 0, time.UTC)),
		Logger:       logging.New(logging.Options{Level: slog.LevelDebug, Writer: &logs}),
		Version:      "0.0.0-sentinel",
		SessionToken: sentinel.SentinelToken,
		Services:     fixtures.NewGateway(t).Services,
	})
	if err != nil {
		t.Fatalf("构造 Gateway 失败：%v", err)
	}
	return server.Handler(), &logs
}

func TestGateway_SessionTokenNeverLeavesTheEntryDocument(t *testing.T) {
	handler, logs := gatewayWithSentinelToken(t)

	// 覆盖会话校验的三条拒绝路径与两条放行路径：拒绝路径要写日志，最容易顺手
	// 把请求头也一并记下来。
	requests := map[string]func(*http.Request){
		"合法请求": func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+sentinel.SentinelToken)
			r.Header.Set(httpapi.HeaderRequestedBy, httpapi.RequestedByConsole)
		},
		"令牌不对": func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+sentinel.SentinelToken+"-wrong")
			r.Header.Set(httpapi.HeaderRequestedBy, httpapi.RequestedByConsole)
		},
		"缺自定义头": func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+sentinel.SentinelToken)
		},
		"Origin 不在白名单": func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+sentinel.SentinelToken)
			r.Header.Set(httpapi.HeaderRequestedBy, httpapi.RequestedByConsole)
			r.Header.Set("Origin", "http://evil.example.com")
		},
	}

	for name, adjust := range requests {
		for _, target := range []string{"/v1/gateway/status", "/v1/approvals", "/assets/index.js"} {
			t.Run(name+" "+target, func(t *testing.T) {
				recorder := httptest.NewRecorder()
				request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
				adjust(request)
				handler.ServeHTTP(recorder, request)

				assertNoSentinel(t, recorder.Body.String())
				for key, values := range recorder.Header() {
					assertNoSentinel(t, key+": "+strings.Join(values, " "))
				}
			})
		}
	}

	assertNoSentinel(t, logs.String())
}

func TestGateway_SentinelIsActuallyReachableThroughTheEntryDocument(t *testing.T) {
	// 反向对照：证明上面的扫描不是因为哨兵压根没进过这条链路才通过的。
	//
	// 入口文档是令牌唯一的合法去处：Console 从这里读它，静态资源本身不要求令牌，
	// 否则首屏就取不到。
	handler, _ := gatewayWithSentinelToken(t)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

	if !strings.Contains(recorder.Body.String(), sentinel.SentinelToken) {
		t.Fatal("入口文档里没有会话令牌，扫描等于没做")
	}
}
