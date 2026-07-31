package httpapi_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/transport/httpapi"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * Agent 会话的注册与断开（REQ-API-002 的补充端点、REQ-CLI-002 AC3）。
 */

// completeRegistration 是一份九项齐备的注册请求体。
func completeRegistration() string {
	return `{
		"name": "claude",
		"type": "claude-code",
		"executable_hash": "sha256:0f1e2d3c4b5a6978",
		"executable_path": "/usr/local/bin/claude",
		"pid": 4242,
		"parent_pid": 4241,
		"os_user": "agent",
		"device_fingerprint": "sha256:device",
		"device_name": "workbench",
		"workspace_path": "/home/agent/project",
		"project_fingerprint": "sha256:project",
		"started_at": "2026-07-27T09:15:30Z"
	}`
}

func TestRegisterAgent_IssuesASessionKeyExactlyOnce(t *testing.T) {
	all := newAPI(t)

	response := all.call(t, http.MethodPost, "/v1/agents/register", completeRegistration())
	if response.Code != http.StatusCreated {
		t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
	}

	var registered httpapi.RegisteredAgentView
	decodeInto(t, response, &registered)
	if registered.SessionKey == "" {
		t.Fatal("没有签发会话凭证，Agent 无从在 8788 / 8789 上被认出来")
	}
	if registered.Agent.ID == "" {
		t.Error("响应里没有 Agent 主键，调用方无从断开这次会话")
	}
	if registered.Agent.TrustLevel != "unverified" {
		t.Errorf("新注册的信任等级为 %s，期望 unverified（REQ-AGENT-002 AC1）",
			registered.Agent.TrustLevel)
	}

	// 会话凭证只在这一个响应里出现一次：库里存的是哈希，
	// 列表端点里不该有任何与它相关的东西。
	listed := all.call(t, http.MethodGet, "/v1/agents", "")
	if strings.Contains(listed.Body.String(), registered.SessionKey) {
		t.Error("Agent 列表里回显了会话凭证")
	}
}

func TestRegisterAgent_IncompleteIdentityBinding_IsRefused(t *testing.T) {
	// REQ-AGENT-001 AC4：只报个名字认不出这是谁，而认不出一律拒绝。
	all := newAPI(t)

	cases := map[string]string{
		"只有名字": `{"name":"claude","type":"claude-code"}`,
		"缺可执行文件哈希": strings.Replace(completeRegistration(),
			`"executable_hash": "sha256:0f1e2d3c4b5a6978",`, "", 1),
		"缺进程号": strings.Replace(completeRegistration(), `"pid": 4242,`, "", 1),
		"缺启动时间": strings.Replace(completeRegistration(),
			`"started_at": "2026-07-27T09:15:30Z"`, `"started_at": ""`, 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			response := all.call(t, http.MethodPost, "/v1/agents/register", body)
			if response.Code == http.StatusCreated {
				t.Fatalf("身份绑定不完整却签发了会话：%s", response.Body.String())
			}
		})
	}
}

func TestRegisterAgent_FromAnAgent_IsRefused(t *testing.T) {
	// 这条是整个设计成立的前提：自报九项等于没有绑定（REQ-DECIDE-004）。
	all := newAPIFor(t, httpapi.Caller{AgentID: fixtures.DefaultAgentID})

	response := all.call(t, http.MethodPost, "/v1/agents/register", completeRegistration())
	if response.Code != http.StatusForbidden {
		t.Fatalf("状态码为 %d，期望 403", response.Code)
	}
}

func TestDisconnectAgent_EndsTheSession(t *testing.T) {
	all := newAPI(t)

	response := all.call(t, http.MethodPost,
		"/v1/agents/"+fixtures.DefaultAgentID+"/disconnect", "")
	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
	}

	var revocation httpapi.SessionRevocationView
	decodeInto(t, response, &revocation)
	if revocation.Agent.Status != "disconnected" {
		t.Errorf("Agent 状态为 %s，期望 disconnected", revocation.Agent.Status)
	}
}

func TestDisconnectAgent_FromAnAgent_IsRefused(t *testing.T) {
	// 一个 Agent 能断开别人的会话，就等于有了一个不经审批的收权入口。
	all := newAPIFor(t, httpapi.Caller{AgentID: fixtures.DefaultAgentID})

	response := all.call(t, http.MethodPost,
		"/v1/agents/"+fixtures.DefaultAgentID+"/disconnect", "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("状态码为 %d，期望 403", response.Code)
	}
}

func TestDisconnectAgent_UnknownAgent_IsNotFound(t *testing.T) {
	all := newAPI(t)

	response := all.call(t, http.MethodPost,
		"/v1/agents/01JNOSUCHAGENT0000000000/disconnect", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("状态码为 %d，期望 404", response.Code)
	}
}
