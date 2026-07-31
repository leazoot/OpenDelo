package mcpsrv

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/Runcoor/opendelo/internal/core/gateway"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/logging"
)

/*
 * 方法分发（REQ-MCP-003 AC1）。
 *
 * 两种传输共用这一个 Dispatch。「两种传输下工具列表与调用结果一致」因此是
 * 结构上的性质，而不是靠两份实现互相对齐 —— 后者迟早会在某个分支上分叉。
 *
 * 本文件不含任何业务判断：认证交给 Authenticator，调用交给 Calls
 * （接入面只做协议转换）。
 */

// Caller 是认证出来的调用方。
type Caller struct {
	AgentID     string
	WorkspaceID string
}

// Authenticator 用 Session Key 认出 Agent。
//
// 认不出必须返回错误，不得返回一个空的 Caller —— 「无法识别 Agent」是
// Fail Closed 的第一条。
type Authenticator interface {
	Authenticate(ctx context.Context, sessionKey string) (Caller, error)
}

// Call 是一次工具调用的输入。
//
// Tool 与 Service/Operation 并存不是冗余：前者是客户端在线路上用的名字，
// 后者是它背后的服务与操作。决策链路按工具名查自己那份能力映射表，
// 而那份表来自数据库里的声明 —— 名字对不上就是「网关未声明这个工具」，
// 由本包在这里替它猜一个反而会让两份声明的分歧被掩盖过去。
type Call struct {
	Caller    Caller
	Tool      string
	Service   string
	Operation string
	Arguments json.RawMessage
}

// CallOutcome 是一次工具调用的结论。
//
// Refused 为真表示「按策略拒绝」——那不是故障，是产品的正常输出，
// 因此走 isError 的工具结果而不是协议错误。
type CallOutcome struct {
	Refused bool
	Text    string
}

// Calls 执行一次工具调用。实现在 core 侧的编排里，本包只做转换。
type Calls interface {
	Call(ctx context.Context, call Call) (CallOutcome, error)
}

// Options 是 Server 的依赖，全部必填。
type Options struct {
	// Availability 是网关自身的可用状态（REQ-GATEWAY-003）。
	Availability  *gateway.Availability
	Catalog       *Catalog
	Authenticator Authenticator
	Calls         Calls
	Logger        *slog.Logger
}

// Server 分发 MCP 方法。
type Server struct {
	availability  *gateway.Availability
	catalog       *Catalog
	authenticator Authenticator
	calls         Calls
	logger        *slog.Logger
}

// NewServer 校验依赖并构造 Server。缺任何一项都拒绝构造：
// 一个没有认证器的 MCP 面等于把网关直接开给任何本机进程。
func NewServer(options Options) (*Server, error) {
	switch {
	case options.Availability == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("MCP Server 缺少可用状态")
	case options.Catalog == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("MCP Server 缺少工具清单")
	case options.Authenticator == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("MCP Server 缺少认证器")
	case options.Calls == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("MCP Server 缺少调用执行器")
	case options.Logger == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("MCP Server 缺少日志器")
	}
	return &Server{
		availability:  options.Availability,
		catalog:       options.Catalog,
		authenticator: options.Authenticator,
		calls:         options.Calls,
		logger:        options.Logger,
	}, nil
}

// Dispatch 处理一条消息。通知返回 handled=false，调用方据此不回写任何东西。
//
// sessionKey 由传输层从各自的位置取出（stdio 从环境变量，HTTP 从
// Mcp-Session-Id），到这里已经是一个字符串。
func (s *Server) Dispatch(
	ctx context.Context, incoming frame, sessionKey string,
) (response, bool) {
	if incoming.isNotification() {
		return response{}, false
	}

	switch incoming.Method {
	case methodInitialize:
		// initialize 不要求认证：客户端要先握上手才谈得上出示密钥，
		// 而握手本身不暴露任何东西 —— 回应里只有协议版本与服务端名字。
		return newResult(incoming.ID, newInitializeResult()), true
	case methodPing:
		return newResult(incoming.ID, struct{}{}), true
	case methodToolsList, methodToolsCall:
		// 网关不服务时两条都断在这里：清单是情报，调用是行动，离线时都不给
		// （REQ-GATEWAY-003）。initialize 与 ping 仍然应答，客户端因此拿到的是
		// 一个明确的拒绝，而不是一条断掉的连接。
		if err := s.availability.Check(); err != nil {
			return s.errorFrom(ctx, incoming.ID, err), true
		}
		if incoming.Method == methodToolsList {
			return s.list(ctx, incoming, sessionKey), true
		}
		return s.call(ctx, incoming, sessionKey), true
	default:
		return newError(incoming.ID, codeMethodNotFound,
			"method not found: "+incoming.Method), true
	}
}

// list 返回工具清单。
//
// 需要认证：清单本身就是情报 —— 它说明这台网关接了哪些服务。
func (s *Server) list(ctx context.Context, incoming frame, sessionKey string) response {
	if _, err := s.authenticate(ctx, sessionKey); err != nil {
		return s.errorFrom(ctx, incoming.ID, err)
	}
	return newResult(incoming.ID, map[string]any{"tools": s.catalog.Tools()})
}

// call 执行一次工具调用。
func (s *Server) call(ctx context.Context, incoming frame, sessionKey string) response {
	caller, err := s.authenticate(ctx, sessionKey)
	if err != nil {
		return s.errorFrom(ctx, incoming.ID, err)
	}

	var params callParams
	if decodeErr := json.Unmarshal(incoming.Params, &params); decodeErr != nil {
		return newError(incoming.ID, codeInvalidRequest, "tools/call params are not valid")
	}

	name := strings.TrimSpace(params.Name)
	target, declared := s.catalog.Target(name)
	if !declared {
		// 未被声明的能力一律拒绝，且不透露「这个名字存不存在」之外的任何信息。
		// 走 isError 而不是协议错误：
		// 对模型来说这是一个「这里没有这个工具」的答复，不是传输故障。
		return newResult(incoming.ID, refusalResult(
			"No such tool. Call tools/list for what this gateway offers."))
	}

	outcome, err := s.calls.Call(ctx, Call{
		Caller:    caller,
		Tool:      name,
		Service:   target.Service,
		Operation: target.Operation,
		Arguments: params.Arguments,
	})
	if err != nil {
		return s.errorFrom(ctx, incoming.ID, err)
	}
	if outcome.Refused {
		return newResult(incoming.ID, refusalResult(outcome.Text))
	}
	return newResult(incoming.ID, textResult(outcome.Text))
}

// authenticate 认出调用方。
func (s *Server) authenticate(ctx context.Context, sessionKey string) (Caller, error) {
	if strings.TrimSpace(sessionKey) == "" {
		return Caller{}, apperr.New(apperr.CodeUnauthenticated).
			WithDetail("MCP 请求未携带 Session Key")
	}
	return s.authenticator.Authenticate(ctx, sessionKey)
}

// errorFrom 把内部错误转成协议错误。
//
// 走 apperr.PublicOf：对外文本只能取自码表，detail 与 cause 不外泄。
// operation_id 带上，它是用户在账本里
// 找到这次失败的唯一线索。
func (s *Server) errorFrom(ctx context.Context, id json.RawMessage, err error) response {
	operationID := logging.OperationIDFrom(ctx)
	public := apperr.PublicOf(err, operationID)

	s.logger.ErrorContext(ctx, "MCP 请求失败",
		slog.String("operation_id", public.OperationID),
		slog.String("code", public.Code.String()))

	message := public.Message
	if public.OperationID != "" {
		message += " (operation_id=" + public.OperationID + ")"
	}
	return newError(id, codeApplicationError, message)
}
