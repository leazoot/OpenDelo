package cli

import (
	"context"

	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/transport/mcpsrv"
	"github.com/Runcoor/opendelo/internal/transport/proxy"
)

/*
 * 两个 Agent 面的认证器。
 *
 * 8788 与 8789 各自声明了一个 Authenticator 接口，方法同名、形状相同，但返回的
 * Caller 是各自的类型 —— 接入面之间不互相 import 是对的，否则 MCP 的改动会波及
 * Proxy。代价是一个 Go 类型没法同时满足两者（同名方法不能有两种签名），因此这里是
 * 一个共用的 identifier 加两个各自实现接口的薄包装。
 *
 * 这处转换只能落在组装根：它本来就是唯一知道「这两个面装的是同一个
 * agentauth.Service」的地方。
 */

// agentIdentifier 用 Session Key 认出 Agent，是两个面共用的那一步。
//
// 认不出一律返回错误，绝不返回一个空的 Caller —— 「无法识别 Agent」是
// Fail Closed 的第一条。零值的 Caller 会让下游
// 把请求当成一个 agent_id 为空的主体处理，那正是最难查的一类越权。
type agentIdentifier struct {
	agents *agentauth.Service
}

// identify 不传 Presented：两个面拿到的都是一次网络请求，看不到对方的 PID 与
// 可执行文件哈希，自报的那两个值等于没校验（见 `core/agentauth` 对 Presented
// 零值的说明）。进程上下文的校验发生在注册时 —— 那时 `opendelo run` 是子进程的父进程。
func (a agentIdentifier) identify(ctx context.Context, sessionKey string) (agentauth.Agent, error) {
	return a.agents.Authenticate(ctx, agentauth.NewSessionKey(sessionKey), agentauth.Presented{})
}

// mcpAuthenticator 让 8789 用 Session Key 认出调用方。
type mcpAuthenticator struct{ agentIdentifier }

// proxyAuthenticator 让 8788 用 Session Key 认出调用方。
type proxyAuthenticator struct{ agentIdentifier }

// 编译期确认两个接入面的契约都被满足，而不是等到装配时才发现某个面收到的是 nil。
var (
	_ mcpsrv.Authenticator = mcpAuthenticator{}
	_ proxy.Authenticator  = proxyAuthenticator{}
)

func (a mcpAuthenticator) Authenticate(ctx context.Context, sessionKey string) (mcpsrv.Caller, error) {
	agent, err := a.identify(ctx, sessionKey)
	if err != nil {
		return mcpsrv.Caller{}, err
	}
	return mcpsrv.Caller{AgentID: agent.ID, WorkspaceID: agent.WorkspaceID}, nil
}

func (a proxyAuthenticator) Authenticate(ctx context.Context, sessionKey string) (proxy.Caller, error) {
	agent, err := a.identify(ctx, sessionKey)
	if err != nil {
		return proxy.Caller{}, err
	}
	return proxy.Caller{AgentID: agent.ID, WorkspaceID: agent.WorkspaceID}, nil
}
