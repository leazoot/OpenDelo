package mcpsrv

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * HTTP 传输（REQ-MCP-003，8789）。
 *
 * 形状取自实测：单端点 Streamable HTTP，一切走 POST，
 * GET 用于可选的服务端推送而回 405 客户端完全接受。
 * 因此这里没有 SSE。
 */

// sessionHeader 是 Session Key 的承载位置。
//
// 实测：服务端返回的 Mcp-Session-Id 会被客户端在后续
// 每个请求上原样回带。这正好是 Session Key 需要的载体，不必自造一个头 ——
// 自造的头客户端不会回带。
const sessionHeader = "Mcp-Session-Id"

// maxBody 是请求体上限。出站那一侧同样要求响应体有上限，请求侧同理：
// 一个无界的 POST 足以让网关内存耗尽。
const maxBody = 1 << 20

// NewHTTPHandler 把 Server 挂成一个 HTTP 处理器。
//
// 只处理一个路径，路由由调用方决定。
func NewHTTPHandler(server *Server) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refused := refuseBrowser(w, r); refused {
			return
		}

		switch r.Method {
		case http.MethodPost:
			serveRPC(w, r, server)
		case http.MethodGet:
			// 服务端不主动推送：本面上没有任何「服务端要先说话」的场景，
			// 审批状态的变化由 Console 的 SSE 承载，Agent 侧是请求—回应。
			// 实测客户端接受 405 并照常工作。
			w.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodDelete:
			// 会话由 Session Key 的有效期决定，客户端断开不撤销授权。
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

// refuseBrowser 拒绝一切带 Origin 的请求，返回是否已经写出响应。
//
// 这是最要紧的一条实测结论：两个真实 MCP 客户端都**不发** Origin，
// 而浏览器发起的跨源请求**一定**带 Origin。
//
// Console 面的规则是「Origin 必须在允许列表内」，那条规则在这里行不通 ——
// 照搬会让所有 MCP 客户端连不上。
// 反过来「没有 Origin 就放行」又恰好放行了要挡的那一类。因此本面的规则是
// **带 Origin 一律拒绝**：比允许列表更严，且同样不接受「缺省即放行」。
func refuseBrowser(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Origin") == "" {
		return false
	}
	http.Error(w, "", http.StatusForbidden)
	return true
}

// serveRPC 处理一次 POST。
func serveRPC(w http.ResponseWriter, r *http.Request, server *Server) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		http.Error(w, "", http.StatusBadRequest)
		return
	}

	incoming, ok := decodeFrame(string(body))
	if !ok {
		writeRPC(w, r, newError(nil, codeParseError, "request is not valid JSON"))
		return
	}

	reply, handled := server.Dispatch(r.Context(), incoming, r.Header.Get(sessionHeader))
	if !handled {
		// 通知没有回应体。实测客户端接受 202。
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeRPC(w, r, reply)
}

// writeRPC 写出一条 JSON-RPC 回应。
func writeRPC(w http.ResponseWriter, r *http.Request, reply response) {
	encoded, err := json.Marshal(reply)
	if err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	// 回带 Session Key 让客户端在后续请求上原样送回。密钥本身由 Agent 在
	// 注册时拿到，这里只是让它有个固定的位置，不产生新的密钥。
	if key := r.Header.Get(sessionHeader); key != "" {
		w.Header().Set(sessionHeader, key)
	}
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(encoded); err != nil {
		return
	}
}

// CheckLoopback 校验监听地址在回环上（REQ-MCP-003 AC2）。
//
// 放在这里而不是 platform/config：这条约束属于 MCP 面本身 ——
// 8789 上的调用方是 Agent，而 Agent 与网关按 ADR-010 同机运行。
// 非回环监听意味着任何能到达这台机器的进程都能请求能力。
func CheckLoopback(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return apperr.Wrap(apperr.CodeInvalidConfiguration, err).
			WithDetail("MCP 监听地址解析失败：" + address)
	}

	if strings.EqualFold(host, "localhost") {
		return nil
	}
	parsed := net.ParseIP(host)
	if parsed == nil || !parsed.IsLoopback() {
		return apperr.New(apperr.CodeInvalidConfiguration).
			WithDetail("MCP 面只允许监听回环地址，收到 " + address)
	}
	return nil
}
