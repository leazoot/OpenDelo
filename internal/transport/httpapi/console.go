package httpapi

import (
	"bytes"
	"html"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

// indexFile 既是首屏，也是前端路由的回落目标。
const indexFile = "index.html"

// consoleContentTypes 是 Console 资源的 Content-Type 白名单。
//
// 不用 mime 包按扩展名推断：它会读 /etc/mime.types 与 Windows 注册表，而且系统表
// 会覆盖 Go 的内置映射 —— 一台把 .js 登记成 text/plain 的机器上，nosniff 之下浏览器
// 直接拒绝执行脚本，界面白屏，而同一个二进制在另一台机器上完全正常。
var consoleContentTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".json":  "application/json; charset=utf-8",
	".svg":   "image/svg+xml",
	".woff2": "font/woff2",
	".png":   "image/png",
	".webp":  "image/webp",
	".ico":   "image/x-icon",
	".txt":   "text/plain; charset=utf-8",
}

// SessionTokenMetaName 是 Console 读取会话令牌的位置。
const SessionTokenMetaName = "opendelo-session-token"

// consoleHandler 提供内嵌的 Console 静态资源。
type consoleHandler struct {
	assets fs.FS
	// index 是注入过会话令牌的入口文档，构造时算一次。
	index  []byte
	logger *slog.Logger
}

// newConsoleHandler 在资源不完整时拒绝构造。
//
// 少了 index.html 就没有界面可提供，此时启动只会让用户对着一片 404 猜发生了什么。
func newConsoleHandler(assets fs.FS, sessionToken string, logger *slog.Logger) (*consoleHandler, error) {
	if assets == nil {
		return nil, apperr.New(apperr.CodeInvalidConfiguration).WithDetail("未提供 Console 静态资源")
	}
	document, err := fs.ReadFile(assets, indexFile)
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInvalidConfiguration, err).
			WithDetail("内嵌的 Console 资源里没有 " + indexFile +
				"，请先运行 make web-build 生成 web/embedded/dist")
	}

	index, err := injectSessionToken(document, sessionToken)
	if err != nil {
		return nil, err
	}
	return &consoleHandler{assets: assets, index: index, logger: logger}, nil
}

// injectSessionToken 把会话令牌写进入口文档的 <head>。
//
// Console 必须在发出第一个 /v1 请求之前拿到令牌，而静态资源本身不要求令牌
// （要求了首屏就死锁）。CSP 不允许内联脚本，meta 是剩下的唯一位置。
//
// 这不削弱令牌的作用：令牌防的是浏览器里的其他页面，它们既读不到本源文档的内容
// （同源策略），也发不出带自定义头的跨源请求（预检不会被放行）。
func injectSessionToken(document []byte, token string) ([]byte, error) {
	const head = "<head>"
	at := bytes.Index(document, []byte(head))
	if at < 0 {
		return nil, apperr.New(apperr.CodeInvalidConfiguration).
			WithDetail("入口文档里没有 " + head + "，无法把会话令牌交给 Console")
	}

	meta := `<meta name="` + SessionTokenMetaName + `" content="` + html.EscapeString(token) + `">`
	at += len(head)

	injected := make([]byte, 0, len(document)+len(meta))
	injected = append(injected, document[:at]...)
	injected = append(injected, meta...)
	return append(injected, document[at:]...), nil
}

func (c *consoleHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if !c.isFile(name) {
		// Console 的路由在前端（/gate/folio/:id 这类），刷新时后端没有对应文件。
		// 目录同样回落：assets/ 之下不提供目录列表。
		name = indexFile
	}
	if contentType, known := consoleContentTypes[path.Ext(name)]; known {
		w.Header().Set(headerContentType, contentType)
	}
	if name == indexFile {
		// 入口文档从内存里发：磁盘上那份没有会话令牌。
		http.ServeContent(w, r, indexFile, time.Time{}, bytes.NewReader(c.index))
		return
	}
	http.ServeFileFS(w, r, c.assets, name)
}

func (c *consoleHandler) isFile(name string) bool {
	if name == "" || name == "." || !fs.ValidPath(name) {
		return false
	}
	info, err := fs.Stat(c.assets, name)
	if err != nil {
		return false
	}
	return !info.IsDir()
}
