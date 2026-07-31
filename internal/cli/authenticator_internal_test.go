package cli

import (
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * 两个 Agent 面认证器的用例（REQ-AGENT-001）。
 *
 * 重点不是「认得出」——那是 core/agentauth 自己的用例守的 —— 而是**认不出的时候
 * 两个面各自返回了什么**。一个把错误吞掉、返回零值 Caller 的认证器编译得过、
 * 大部分用例也都过，但它会让下游把请求当成一个 agent_id 为空的主体处理。
 */

func newAuthenticatorHarness(t *testing.T) (agentIdentifier, agentauth.Registered, *clock.Fixed) {
	t.Helper()

	moment := clock.NewFixed(time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC))
	database, err := store.Open(t.Context(), store.Options{Path: t.TempDir() + "/opendelo.db"})
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	t.Cleanup(func() {
		if closeErr := database.Close(); closeErr != nil {
			t.Errorf("关闭数据库失败：%v", closeErr)
		}
	})
	if _, migrateErr := store.Migrate(t.Context(), database); migrateErr != nil {
		t.Fatalf("迁移失败：%v", migrateErr)
	}

	service, err := agentauth.NewService(agentauth.Options{
		Agents: repo.NewAgents(database), Devices: repo.NewDevices(database),
		Workspaces: repo.NewWorkspaces(database), Clock: moment, IDs: ulid.New(moment),
	})
	if err != nil {
		t.Fatalf("构造 agentauth 失败：%v", err)
	}

	registered, err := service.Register(t.Context(), fixtures.Registration())
	if err != nil {
		t.Fatalf("注册 Agent 失败：%v", err)
	}
	return agentIdentifier{agents: service}, registered, moment
}

func TestAgentAuthenticator_ValidSessionKey_IsRecognisedOnBothFaces(t *testing.T) {
	// 两个面装的是同一个 agentauth.Service，认出来的必须是同一个 Agent ——
	// 否则「Agent 在 MCP 上是谁、在 Proxy 上是谁」会有两个答案。
	identifier, registered, _ := newAuthenticatorHarness(t)

	fromMCP, err := mcpAuthenticator{identifier}.Authenticate(t.Context(), registered.SessionKey.Reveal())
	if err != nil {
		t.Fatalf("MCP 面认证失败：%v", err)
	}
	fromProxy, err := proxyAuthenticator{identifier}.Authenticate(t.Context(), registered.SessionKey.Reveal())
	if err != nil {
		t.Fatalf("Proxy 面认证失败：%v", err)
	}

	if fromMCP.AgentID != registered.Agent.ID {
		t.Errorf("MCP 面认出的 AgentID 为 %q，期望 %q", fromMCP.AgentID, registered.Agent.ID)
	}
	if fromMCP.AgentID != fromProxy.AgentID || fromMCP.WorkspaceID != fromProxy.WorkspaceID {
		t.Errorf("两个面认出了不同的调用方：%+v / %+v", fromMCP, fromProxy)
	}
	if fromProxy.WorkspaceID == "" {
		t.Error("认出的调用方没有工作区 —— Scope 的 workspace 维度会因此定不下来")
	}
}

func TestAgentAuthenticator_Rejected_ReturnsNoCallerOnEitherFace(t *testing.T) {
	// Fail Closed 的第一条：认不出即拒绝，且不得返回一个能被当成主体使用的 Caller。
	identifier, registered, moment := newAuthenticatorHarness(t)

	cases := []struct {
		name     string
		key      string
		advance  time.Duration
		expected apperr.Code
	}{
		{"空的 Session Key", "", 0, apperr.CodeUnauthenticated},
		{"不存在的 Session Key", "not-a-real-session-key", 0, apperr.CodeUnauthenticated},
		{"过期的 Session Key", registered.SessionKey.Reveal(), 400 * 24 * time.Hour, apperr.CodeSessionExpired},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.advance > 0 {
				moment.Advance(testCase.advance)
				t.Cleanup(func() { moment.Advance(-testCase.advance) })
			}

			caller, err := mcpAuthenticator{identifier}.Authenticate(t.Context(), testCase.key)
			if !apperr.Is(err, testCase.expected) {
				t.Errorf("MCP 面返回 %v，期望 %s", err, testCase.expected)
			}
			if caller.AgentID != "" || caller.WorkspaceID != "" {
				t.Errorf("MCP 面在拒绝时仍然给出了调用方 %+v", caller)
			}

			proxyCaller, err := proxyAuthenticator{identifier}.Authenticate(t.Context(), testCase.key)
			if !apperr.Is(err, testCase.expected) {
				t.Errorf("Proxy 面返回 %v，期望 %s", err, testCase.expected)
			}
			if proxyCaller.AgentID != "" || proxyCaller.WorkspaceID != "" {
				t.Errorf("Proxy 面在拒绝时仍然给出了调用方 %+v", proxyCaller)
			}
		})
	}
}

func TestAgentAuthenticator_DisconnectedAgent_IsRefused(t *testing.T) {
	// 断开是「让这把密钥立刻失效」的手段（REQ-AGENT-001）。断开之后还认得出，
	// 就等于断开只是界面上的一个状态。
	identifier, registered, _ := newAuthenticatorHarness(t)

	if _, err := identifier.agents.Disconnect(t.Context(), registered.Agent.ID); err != nil {
		t.Fatalf("断开失败：%v", err)
	}

	caller, err := proxyAuthenticator{identifier}.Authenticate(t.Context(), registered.SessionKey.Reveal())
	if !apperr.Is(err, apperr.CodeUnauthenticated) {
		t.Errorf("断开后仍然返回 %v，期望 unauthenticated", err)
	}
	if caller.AgentID != "" {
		t.Errorf("断开后仍然给出了调用方 %+v", caller)
	}
}
