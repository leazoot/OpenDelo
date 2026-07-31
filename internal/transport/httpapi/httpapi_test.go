package httpapi_test

import (
	"io"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/config"
	"github.com/Runcoor/opendelo/internal/platform/logging"
	"github.com/Runcoor/opendelo/internal/transport/httpapi"
	"github.com/Runcoor/opendelo/test/fixtures"
)

const (
	testVersion = "1.2.3-test"
	indexBody   = `<!doctype html><html lang="zh-CN"><head>` +
		`<script type="module" crossorigin src="/assets/index-abc123.js"></script>` +
		`<link rel="stylesheet" crossorigin href="/assets/index-abc123.css">` +
		`</head><body><div id="root"></div></body></html>`
	scriptBody = "export const seam = 'boundary'\n"
	styleBody  = ":root{--seam:5px}\n"
)

// startedAt 是注入时钟停留的时刻，用例据此断言 started_at 是确定值。
var startedAt = time.Date(2026, 7, 28, 9, 15, 30, 123_000_000, time.UTC)

const startedAtText = "2026-07-28T09:15:30.123Z"

// zeroTime 是「不设游标，从最新一条开始」。
var zeroTime = time.Time{}

// consoleAssets 模拟 Vite 产物的形状：入口 HTML + 带内容哈希的资源目录。
func consoleAssets() fstest.MapFS {
	return fstest.MapFS{
		"index.html":                  &fstest.MapFile{Data: []byte(indexBody)},
		"assets/index-abc123.js":      &fstest.MapFile{Data: []byte(scriptBody)},
		"assets/index-abc123.css":     &fstest.MapFile{Data: []byte(styleBody)},
		"assets/instrument-xyz.woff2": &fstest.MapFile{Data: []byte("woff2 bytes")},
		"assets/worker-abc123.mjs":    &fstest.MapFile{Data: []byte(scriptBody)},
	}
}

// testPort 是默认端口。绝大多数用例直接驱动 Handler，不占用它；
// 真正要监听的用例用 freePort 换一个。
const testPort = 8787

// testSessionToken 是固定值，便于断言注入进入口文档的正是它。
// 真实令牌由 crypto/rand 生成，见 platform/config。
const testSessionToken = "test-session-token-4c1f0a9e"

// indexWithSession 是 Console 实际拿到的入口文档：Gateway 会把会话令牌注入 <head>。
var indexWithSession = strings.Replace(indexBody, "<head>",
	`<head><meta name="`+httpapi.SessionTokenMetaName+`" content="`+testSessionToken+`">`, 1)

// backend 是端点背后那一整套真实组件，装配集中在 test/fixtures。
type backend = fixtures.Gateway

func newBackend(t *testing.T) backend {
	t.Helper()
	return fixtures.NewGateway(t)
}

func testOptions(t *testing.T) httpapi.Options {
	t.Helper()

	return httpapi.Options{
		Config:       config.Default(),
		SessionToken: testSessionToken,
		Console:      consoleAssets(),
		Clock:        clock.NewFixed(startedAt),
		Logger:       logging.New(logging.Options{Writer: io.Discard}),
		Version:      testVersion,
		Services:     newBackend(t).Services,
	}
}

func newServer(t *testing.T, adjust func(*httpapi.Options)) *httpapi.Server {
	t.Helper()

	options := testOptions(t)
	if adjust != nil {
		adjust(&options)
	}
	server, err := httpapi.New(options)
	if err != nil {
		t.Fatalf("构造服务失败：%v", err)
	}
	return server
}
