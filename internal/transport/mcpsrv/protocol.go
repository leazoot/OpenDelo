package mcpsrv

import (
	"encoding/json"
	"strings"
)

/*
 * JSON-RPC 2.0 的帧与编解码（REQ-MCP-003）。
 *
 * 形状取自实测记录，不是取自规范阅读：
 * 两个客户端都用换行分隔的 JSON，没有 Content-Length 分帧。
 */

// ProtocolVersion 是本服务端声明的协议版本。
//
// 实测：客户端报什么版本无关紧要，服务端回什么就是什么，
// 后续 HTTP 请求带的 Mcp-Protocol-Version 也是服务端回的这个值。
// 因此这里是一个常量而不是一张协商表。
const ProtocolVersion = "2025-06-18"

// serverName 与 serverVersion 出现在 initialize 的回应里，供客户端展示。
const (
	serverName    = "opendelo"
	serverVersion = "0.1.0"
)

// JSON-RPC 错误码。只用到规范定义的这几个加一个应用码。
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInternalError  = -32603
	// codeApplicationError 用于协议之外的失败（认证不通过、Adapter 出错）。
	// 策略性拒绝**不走这里**，走 isError 的工具结果。
	codeApplicationError = -32000
)

// 方法名。清单是封闭的：没出现在这里的方法一律 method not found。
const (
	methodInitialize  = "initialize"
	methodInitialized = "notifications/initialized"
	methodToolsList   = "tools/list"
	methodToolsCall   = "tools/call"
	methodPing        = "ping"
)

// frame 是一条进来的 JSON-RPC 消息。
//
// ID 用 json.RawMessage 而不是 any：实测客户端的第一个请求 id 就是 0，
// 用零值判断通知会把它当成通知而不回应。
// 这里判的是「字段在不在」。
type frame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// isNotification 报告这条消息是否不需要回应。
func (f frame) isNotification() bool { return len(f.ID) == 0 }

// response 是一条回应。Result 与Error 互斥。
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError 是协议层错误。
//
// Message 不含请求内容、路径或主机名 —— 它与 apperr 的对外 message 遵循
// 同一条约束。
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func newResult(id json.RawMessage, result any) response {
	return response{JSONRPC: "2.0", ID: id, Result: result}
}

func newError(id json.RawMessage, code int, message string) response {
	return response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}}
}

// initializeResult 是 initialize 的回应。
//
// 只声明 tools 能力。实测：不声明 resources 与 prompts，
// 客户端就不会请求它们 —— 能力声明就是服务端的接口面，声明得越少表面积越小。
type initializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    serverCapabilities `json:"capabilities"`
	ServerInfo      serverInfo         `json:"serverInfo"`
}

type serverCapabilities struct {
	Tools toolsCapability `json:"tools"`
}

// toolsCapability 的 listChanged 恒为 false：工具清单在启动时由 Adapter 声明
// 生成，进程运行期间不会变（ADR-009 禁止动态加载）。
type toolsCapability struct {
	ListChanged bool `json:"listChanged"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func newInitializeResult() initializeResult {
	return initializeResult{
		ProtocolVersion: ProtocolVersion,
		Capabilities:    serverCapabilities{Tools: toolsCapability{ListChanged: false}},
		ServerInfo:      serverInfo{Name: serverName, Version: serverVersion},
	}
}

// callParams 是 tools/call 的参数。
//
// 只取 name 与 arguments：实测客户端还会带 _meta（progressToken、toolUseId），
// 那些是客户端自己的追踪信息，服务端读它们没有意义，也不该让它们影响任何判断。
type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// callResult 是 tools/call 的回应。
//
// IsError 为真表示「工具正常工作，答复是拒绝」；协议性错误走 rpcError。
// 这个区分来自实测：走协议错误时模型看到的是
// 「MCP error -32000: ...」，那读起来像工具坏了，会诱导重试或换路径。
type callResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func textResult(text string) callResult {
	return callResult{Content: []contentBlock{{Type: "text", Text: text}}, IsError: false}
}

func refusalResult(text string) callResult {
	return callResult{Content: []contentBlock{{Type: "text", Text: text}}, IsError: true}
}

// decodeFrame 解一行。空行返回 ok=false，由调用方跳过。
func decodeFrame(line string) (frame, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return frame{}, false
	}

	var decoded frame
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return frame{}, false
	}
	return decoded, true
}
