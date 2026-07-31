package httpapi_test

import (
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/transport/httpapi"
)

// get 驱动完整的处理链，不占用端口，带上 Console 会带的那套凭证。
func get(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, http.MethodGet, target)
}

func do(t *testing.T, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	return send(t, method, target, withConsoleCredentials)
}

// withConsoleCredentials 装上 Console 正常情况下会带的头。
func withConsoleCredentials(request *http.Request) {
	request.Header.Set("Authorization", "Bearer "+testSessionToken)
	request.Header.Set(httpapi.HeaderRequestedBy, httpapi.RequestedByConsole)
}

// send 让用例自己决定这次请求带什么头，供会话校验的用例使用。
func send(t *testing.T, method, target string, adjust func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), method, target, nil)
	if adjust != nil {
		adjust(request)
	}
	newServer(t, nil).Handler().ServeHTTP(recorder, request)
	return recorder
}

func TestConsole_Root_ServesIndexHTML(t *testing.T) {
	// 访问根路径拿到 Console 空壳。
	response := get(t, "/")

	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，期望 200", response.Code)
	}
	if body := response.Body.String(); body != indexWithSession {
		t.Errorf("正文为 %q，期望 index.html 的内容", body)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "text/html; charset=utf-8" {
		t.Errorf("Content-Type 为 %q", contentType)
	}
}

func TestConsole_Asset_ContentTypeIsExplicit(t *testing.T) {
	// nosniff 之下 Content-Type 判错就等于资源加载不出来，而 mime 包的推断
	// 依赖 /etc/mime.types 与注册表，结果随机器而变，所以这里逐个钉死。
	for target, wantContentType := range map[string]string{
		"/assets/index-abc123.js":      "text/javascript; charset=utf-8",
		"/assets/index-abc123.css":     "text/css; charset=utf-8",
		"/assets/instrument-xyz.woff2": "font/woff2",
	} {
		t.Run(target, func(t *testing.T) {
			response := get(t, target)
			if response.Code != http.StatusOK {
				t.Fatalf("状态码为 %d，期望 200", response.Code)
			}
			if contentType := response.Header().Get("Content-Type"); contentType != wantContentType {
				t.Errorf("Content-Type 为 %q，期望 %q", contentType, wantContentType)
			}
		})
	}
}

func TestConsole_ContentType_IgnoresTheSystemMimeTable(t *testing.T) {
	// 系统的 /etc/mime.types 会覆盖 Go 内置的扩展名映射。某些机器上 .js 被登记成
	// text/plain，nosniff 之下浏览器就拒绝执行它，界面白屏 —— 同一个二进制在另一台
	// 机器上却一切正常。这里把 .mjs 故意登记成 text/plain，证明我们的 Content-Type
	// 不受系统表左右。
	//
	// mime 包没有注销接口，这处全局改动无法恢复；.mjs 只有本用例用到，
	// 不会影响同一个测试二进制里的其他用例。
	if err := mime.AddExtensionType(".mjs", "text/plain; charset=utf-8"); err != nil {
		t.Fatalf("注册探测用的 MIME 类型失败：%v", err)
	}

	response := get(t, "/assets/worker-abc123.mjs")

	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，期望 200", response.Code)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "text/javascript; charset=utf-8" {
		t.Errorf("Content-Type 为 %q，说明系统 MIME 表决定了响应类型", contentType)
	}
}

func TestConsole_FrontendRoute_FallsBackToIndexHTML(t *testing.T) {
	// Folio 是路由态，在这个地址上刷新时
	// 后端没有对应文件，必须交回前端接管而不是 404。
	response := get(t, "/gate/folio/01K1AAAAAAAAAAAAAAAAAAAAAA")

	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，期望 200", response.Code)
	}
	if body := response.Body.String(); body != indexWithSession {
		t.Errorf("正文为 %q，期望 index.html 的内容", body)
	}
}

func TestConsole_AssetsDirectory_DoesNotListContents(t *testing.T) {
	// 目录列表会把内嵌产物的文件名全部暴露出去，也不是任何界面需要的东西。
	response := get(t, "/assets/")

	body := response.Body.String()
	// 字体文件名只会出现在目录列表里，index.html 不引用它。
	if strings.Contains(body, "instrument-xyz.woff2") {
		t.Errorf("目录列表被输出了：%q", body)
	}
	if body != indexWithSession {
		t.Errorf("目录请求未回落到 index.html，正文为 %q", body)
	}
}

func TestConsole_PathTraversal_StaysInsideAssets(t *testing.T) {
	for _, target := range []string{"/../../etc/passwd", "/assets/../../etc/passwd"} {
		t.Run(target, func(t *testing.T) {
			response := do(t, http.MethodGet, target)
			if body := response.Body.String(); strings.Contains(body, "root:") {
				t.Fatalf("读到了资源目录之外的内容：%q", body)
			}
		})
	}
}

func TestConsole_NonReadMethod_IsRejectedWithErrorEnvelope(t *testing.T) {
	// 静态资源只读。REQ-API-003 要求错误响应也是统一结构，不能是裸文本。
	response := do(t, http.MethodPost, "/")

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("状态码为 %d，期望 405", response.Code)
	}
	if allow := response.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Errorf("Allow 为 %q，期望 %q", allow, "GET, HEAD")
	}
	if code := decodeErrorCode(t, response); code != "invalid_request" {
		t.Errorf("错误码为 %q，期望 invalid_request", code)
	}
}

func TestConsole_Head_ReturnsHeadersWithoutBody(t *testing.T) {
	response := do(t, http.MethodHead, "/")

	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，期望 200", response.Code)
	}
	if response.Body.Len() != 0 {
		t.Errorf("HEAD 响应带了 %d 字节正文", response.Body.Len())
	}
}
