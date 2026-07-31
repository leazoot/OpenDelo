package httpapi_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/transport/httpapi"
)

// consoleOrigin 是 Console 自身的来源：Gateway 就在这个地址上提供它。
const consoleOrigin = "http://127.0.0.1:8787"

func TestSession_NoToken_IsRejectedWith401(t *testing.T) {
	// REQ-API-005 AC1。
	missing := map[string]func(*http.Request){
		"完全不带 Authorization": func(r *http.Request) {
			r.Header.Set(httpapi.HeaderRequestedBy, httpapi.RequestedByConsole)
		},
		"带了别的令牌": func(r *http.Request) {
			r.Header.Set(httpapi.HeaderRequestedBy, httpapi.RequestedByConsole)
			r.Header.Set("Authorization", "Bearer "+testSessionToken+"x")
		},
		"令牌只对了前缀": func(r *http.Request) {
			r.Header.Set(httpapi.HeaderRequestedBy, httpapi.RequestedByConsole)
			r.Header.Set("Authorization", "Bearer "+testSessionToken[:8])
		},
		"用了 Basic 而不是 Bearer": func(r *http.Request) {
			r.Header.Set(httpapi.HeaderRequestedBy, httpapi.RequestedByConsole)
			r.Header.Set("Authorization", "Basic "+testSessionToken)
		},
		"少了 Bearer 前缀": func(r *http.Request) {
			r.Header.Set(httpapi.HeaderRequestedBy, httpapi.RequestedByConsole)
			r.Header.Set("Authorization", testSessionToken)
		},
	}

	for name, adjust := range missing {
		t.Run(name, func(t *testing.T) {
			response := send(t, http.MethodGet, statusPath, adjust)

			if response.Code != http.StatusUnauthorized {
				t.Fatalf("状态码为 %d，期望 401", response.Code)
			}
			if code := decodeErrorCode(t, response); code != "unauthenticated" {
				t.Errorf("错误码为 %q，期望 unauthenticated", code)
			}
			if strings.Contains(response.Body.String(), testSessionToken[:8]) {
				t.Errorf("响应里回显了令牌片段：%q", response.Body.String())
			}
		})
	}
}

func TestSession_ForeignOrigin_IsRejectedWith403(t *testing.T) {
	// REQ-API-005 AC2。本机任意网页都能向 127.0.0.1 发请求，
	// Origin 校验是挡住它们的第一道。
	foreign := []string{
		"http://evil.example.com",
		"https://evil.example.com",
		// 端口不同就是另一个源，哪怕主机相同。
		"http://127.0.0.1:9999",
		// 前缀相同但主机不同，防的是 startsWith 式的松散比较。
		"http://127.0.0.1.evil.example.com:8787",
		"http://localhost.evil.example.com:8787",
		// null 来源来自沙箱化的 iframe 与本地文件。
		"null",
	}

	for _, origin := range foreign {
		t.Run(origin, func(t *testing.T) {
			response := send(t, http.MethodGet, statusPath, func(r *http.Request) {
				withConsoleCredentials(r)
				r.Header.Set("Origin", origin)
			})

			if response.Code != http.StatusForbidden {
				t.Fatalf("状态码为 %d，期望 403", response.Code)
			}
			if code := decodeErrorCode(t, response); code != "forbidden" {
				t.Errorf("错误码为 %q，期望 forbidden", code)
			}
		})
	}
}

func TestSession_ConsoleOrigin_IsAccepted(t *testing.T) {
	for _, origin := range []string{
		consoleOrigin,
		"http://localhost:8787",
		"http://[::1]:8787",
	} {
		t.Run(origin, func(t *testing.T) {
			response := send(t, http.MethodGet, statusPath, func(r *http.Request) {
				withConsoleCredentials(r)
				r.Header.Set("Origin", origin)
			})

			if response.Code != http.StatusOK {
				t.Fatalf("状态码为 %d，期望 200，正文为 %q", response.Code, response.Body.String())
			}
		})
	}
}

func TestSession_MissingOrigin_IsAcceptedButStillNeedsTheCustomHeader(t *testing.T) {
	// 浏览器对同源 GET 不发 Origin，非浏览器客户端（CLI）也不发，所以缺失不能当成越权。
	// 挡住「没有 Origin 的浏览器请求」（<img src>、<script src> 这类）的是自定义头：
	// 它们发不出自定义头，也无法触发预检。
	authenticated := send(t, http.MethodGet, statusPath, withConsoleCredentials)
	if authenticated.Code != http.StatusOK {
		t.Fatalf("不带 Origin 的合法请求被拒了：%d %q", authenticated.Code, authenticated.Body.String())
	}

	resourceLoad := send(t, http.MethodGet, statusPath, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+testSessionToken)
	})
	if resourceLoad.Code != http.StatusForbidden {
		t.Fatalf("缺少 %s 的请求状态码为 %d，期望 403", httpapi.HeaderRequestedBy, resourceLoad.Code)
	}
}

func TestSession_WrongRequestedByValue_IsRejected(t *testing.T) {
	response := send(t, http.MethodGet, statusPath, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+testSessionToken)
		r.Header.Set(httpapi.HeaderRequestedBy, "some-other-client")
	})

	if response.Code != http.StatusForbidden {
		t.Fatalf("状态码为 %d，期望 403", response.Code)
	}
}

func TestSession_StaticAssets_DoNotRequireCredentials(t *testing.T) {
	// 首屏若要求令牌，Console 还没拿到令牌就取不到入口文档，死锁。
	for _, target := range []string{"/", "/assets/index-abc123.js", "/gate/folio/01K1AAAAAAAAAAAAAAAAAAAAAA"} {
		t.Run(target, func(t *testing.T) {
			response := send(t, http.MethodGet, target, nil)

			if response.Code != http.StatusOK {
				t.Errorf("状态码为 %d，期望 200", response.Code)
			}
		})
	}
}

func TestSession_UnknownVersionedPath_StillRequiresCredentials(t *testing.T) {
	// 未注册的 /v1 路径不能成为绕过会话校验的探测通道：先拒绝，再谈路径存不存在。
	response := send(t, http.MethodGet, "/v1/approvals", nil)

	if response.Code != http.StatusForbidden {
		t.Fatalf("状态码为 %d，期望 403", response.Code)
	}
}

func TestSession_TokenIsInjectedIntoTheEntryDocument(t *testing.T) {
	// Console 得先拿到令牌。CSP 不允许内联脚本，meta 是唯一位置。
	body := get(t, "/").Body.String()

	want := `<meta name="` + httpapi.SessionTokenMetaName + `" content="` + testSessionToken + `">`
	if !strings.Contains(body, want) {
		t.Errorf("入口文档里没有会话令牌的 meta：%q", body)
	}
	if !strings.Contains(body, `<div id="root">`) {
		t.Errorf("注入破坏了入口文档：%q", body)
	}
}

func TestSession_TokenNeverAppearsInAssetsOrAPIResponses(t *testing.T) {
	// 令牌只出现在入口文档里。除此之外的任何地方出现，都是一次泄漏。
	for _, target := range []string{
		"/assets/index-abc123.js",
		"/assets/index-abc123.css",
		statusPath,
		"/v1/approvals",
	} {
		t.Run(target, func(t *testing.T) {
			response := do(t, http.MethodGet, target)

			if strings.Contains(response.Body.String(), testSessionToken) {
				t.Errorf("%s 的响应里出现了会话令牌", target)
			}
			for _, value := range response.Header() {
				if strings.Contains(strings.Join(value, " "), testSessionToken) {
					t.Errorf("%s 的响应头里出现了会话令牌", target)
				}
			}
		})
	}
}

func TestNew_WithoutSessionToken_IsRejected(t *testing.T) {
	// 空令牌意味着任何人都能通过校验，宁可不启动。
	options := testOptions(t)
	options.SessionToken = ""

	if _, err := httpapi.New(options); err == nil {
		t.Fatal("没有会话令牌却构造出了服务")
	}
}
