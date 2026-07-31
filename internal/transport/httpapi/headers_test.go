package httpapi_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/transport/httpapi"
)

func TestContentSecurityPolicy_MatchesTheSecurityRules(t *testing.T) {
	// 逐字对照安全规则里的策略。写死而不是拼接，是因为这条策略
	// 每放宽一处都是一次安全决策，必须在 diff 里看得见。
	const want = "default-src 'self'; connect-src 'self'; img-src 'self' data:; " +
		"font-src 'self'; script-src 'self'; object-src 'none'; " +
		"frame-ancestors 'none'; base-uri 'none'"

	if httpapi.ContentSecurityPolicy != want {
		t.Errorf("CSP 为 %q，期望 %q", httpapi.ContentSecurityPolicy, want)
	}
	for _, forbidden := range []string{"unsafe-inline", "unsafe-eval", "*", "http:", "https:"} {
		if strings.Contains(httpapi.ContentSecurityPolicy, forbidden) {
			t.Errorf("CSP 中出现 %q", forbidden)
		}
	}
}

func TestSecurityHeaders_AreSetOnEveryResponse(t *testing.T) {
	// 静态资源、成功的 API、404、405 走的是不同分支，漏掉任何一条都等于这些头没生效。
	responses := map[string]struct {
		method string
		target string
	}{
		"首屏":     {http.MethodGet, "/"},
		"静态资源":   {http.MethodGet, "/assets/index-abc123.js"},
		"状态端点":   {http.MethodGet, statusPath},
		"未注册端点":  {http.MethodGet, "/v1/approvals"},
		"方法不被允许": {http.MethodPost, statusPath},
	}

	wanted := map[string]string{
		"Content-Security-Policy": httpapi.ContentSecurityPolicy,
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
	}

	for name, request := range responses {
		t.Run(name, func(t *testing.T) {
			header := do(t, request.method, request.target).Header()
			for key, want := range wanted {
				if got := header.Get(key); got != want {
					t.Errorf("%s 为 %q，期望 %q", key, got, want)
				}
			}
		})
	}
}

func TestSecurityHeaders_FrameAncestorsBlocksEmbedding(t *testing.T) {
	// Console 被嵌进第三方页面就等于点击劫持，CSP 里的 frame-ancestors 'none' 是这条防线。
	if !strings.Contains(httpapi.ContentSecurityPolicy, "frame-ancestors 'none'") {
		t.Error("CSP 中缺少 frame-ancestors 'none'")
	}
}
