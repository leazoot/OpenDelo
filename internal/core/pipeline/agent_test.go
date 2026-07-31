package pipeline_test

import (
	"testing"

	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/core/approval"
	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/audit"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * 会话结束的级联（REQ-CLI-002 AC3）。
 */

func TestDisconnectAgent_RevokesTheTaskScopedLeaseAndEndsTheSession(t *testing.T) {
	all := newHarness(t)
	item := pending(t, all)
	granted := settle(t, all, item.ID, approval.ActionAllowUntilTaskEnd)

	if granted.Lease == nil || !granted.Lease.IsSessionBound {
		t.Fatalf("这条用例需要一条绑定会话的 Lease，实际拿到 %+v", granted.Lease)
	}

	revocation, err := all.pipeline.DisconnectAgent(
		t.Context(), fixtures.DefaultAgentID, operationID)
	if err != nil {
		t.Fatalf("结束会话失败：%v", err)
	}

	if len(revocation.RevokedLeases) != 1 {
		t.Errorf("收回了 %d 条 Lease，期望 1 条", len(revocation.RevokedLeases))
	}
	if revocation.Agent.Status != agentauth.StatusDisconnected {
		t.Errorf("Agent 状态为 %s，会话结束之后不该还是 active", revocation.Agent.Status)
	}

	assertNoLease(t, all)
	assertHasEvent(t, all, audit.EventLeaseRevoked)
}

func TestDisconnectAgent_LeavesLeasesThatOutliveTheSession(t *testing.T) {
	// 「到项目结束」的授权不随进程退出而消失。在这里一并收回等于悄悄缩小
	// 用户已经允许的范围 —— 而用户点的是「在这个项目里都可以」。
	all := newHarness(t)
	item := pending(t, all)
	granted := settle(t, all, item.ID, approval.ActionAutoAllowInProject)

	if granted.Lease == nil || granted.Lease.IsSessionBound {
		t.Fatalf("这条用例需要一条不绑定会话的 Lease，实际拿到 %+v", granted.Lease)
	}

	revocation, err := all.pipeline.DisconnectAgent(
		t.Context(), fixtures.DefaultAgentID, operationID)
	if err != nil {
		t.Fatalf("结束会话失败：%v", err)
	}

	if len(revocation.RevokedLeases) != 0 {
		t.Errorf("收回了 %d 条 Lease，期望一条都不收", len(revocation.RevokedLeases))
	}
	assertLeaseTotal(t, all, 1)
}

func TestDisconnectAgent_LedgerWriteFails_LeavesTheLeaseActive(t *testing.T) {
	// 顺序是先记账再收回（ADR-004）：账本写不进去时那条 Lease 必须保持原样，
	// 否则账本上会缺一段「这条授权是什么时候没的」。
	all := newHarness(t)
	item := pending(t, all)
	settle(t, all, item.ID, approval.ActionAllowUntilTaskEnd)

	blind := rebuildWithAudit(t, all, failingAudit{err: apperr.New(apperr.CodeInternal)})
	if _, err := blind.DisconnectAgent(
		t.Context(), fixtures.DefaultAgentID, operationID); err == nil {
		t.Fatal("账本写不进去却报告结束成功")
	}

	issued, err := all.leases.LeasesByStatus(t.Context(), lease.StatusActive, 10)
	if err != nil {
		t.Fatalf("列出 Lease 失败：%v", err)
	}
	if len(issued) != 1 {
		t.Fatalf("库里有 %d 条生效中的 Lease，账本写失败时应保持原样", len(issued))
	}

	// 会话却已经断开了 —— 这是「先断会话再收 Lease」这个顺序留下的唯一痕迹。
	// 反过来的顺序会在这里留下一个活着的会话，而那一瞬进来的请求会走完决策链路
	// 并签出一条新的会话绑定 Lease，它不在刚刚列出的那批里。
	agent, err := repo.NewAgents(all.db).AgentByID(t.Context(), fixtures.DefaultAgentID)
	if err != nil {
		t.Fatalf("读取 Agent 失败：%v", err)
	}
	if agent.Status != agentauth.StatusDisconnected {
		t.Errorf("Agent 状态为 %s，会话应在收回授权之前就已断开", agent.Status)
	}
}

func TestDisconnectAgent_CalledTwice_IsIdempotent(t *testing.T) {
	// `opendelo run` 在子进程退出后无条件调用它，而那时会话可能已经断开过了。
	all := newHarness(t)
	item := pending(t, all)
	settle(t, all, item.ID, approval.ActionAllowUntilTaskEnd)

	if _, err := all.pipeline.DisconnectAgent(
		t.Context(), fixtures.DefaultAgentID, operationID); err != nil {
		t.Fatalf("第一次结束会话失败：%v", err)
	}

	second, err := all.pipeline.DisconnectAgent(
		t.Context(), fixtures.DefaultAgentID, operationID)
	if err != nil {
		t.Fatalf("第二次结束会话失败：%v", err)
	}
	if len(second.RevokedLeases) != 0 {
		t.Errorf("第二次又收回了 %d 条 Lease，期望一条都没有", len(second.RevokedLeases))
	}
	if second.Agent.Status != agentauth.StatusDisconnected {
		t.Errorf("Agent 状态为 %s，期望 disconnected", second.Agent.Status)
	}
}

func TestDisconnectAgent_TheSessionKeyStopsWorkingImmediately(t *testing.T) {
	// AC3 的「立即失效」：断开之后，那把钥匙必须当场认不出来，
	// 而不是等到 SessionExpiresAt。
	all := newHarness(t)
	sessions := sessionServiceOn(t, all)

	registered, err := sessions.Register(t.Context(), registration())
	if err != nil {
		t.Fatalf("注册失败：%v", err)
	}
	if _, err = sessions.Authenticate(
		t.Context(), registered.SessionKey, agentauth.Presented{}); err != nil {
		t.Fatalf("刚签发的会话就认不出来：%v", err)
	}

	if _, err = all.pipeline.DisconnectAgent(
		t.Context(), registered.Agent.ID, operationID); err != nil {
		t.Fatalf("结束会话失败：%v", err)
	}

	if _, err = sessions.Authenticate(
		t.Context(), registered.SessionKey, agentauth.Presented{}); err == nil {
		t.Error("会话已断开，那把钥匙却仍然通得过认证")
	}
}

func TestDisconnectAgent_MissingArgumentsAreRefused(t *testing.T) {
	all := newHarness(t)

	if _, err := all.pipeline.DisconnectAgent(t.Context(), "", operationID); err == nil {
		t.Error("没有 Agent 主键却结束成功")
	}
	if _, err := all.pipeline.DisconnectAgent(
		t.Context(), fixtures.DefaultAgentID, ""); err == nil {
		// 没有 operation_id 就写不出可追溯的账本记录（ADR-004）。
		t.Error("没有 operation_id 却结束成功")
	}
}

func TestDisconnectAgent_UnknownAgentIsNotFound(t *testing.T) {
	all := newHarness(t)

	_, err := all.pipeline.DisconnectAgent(t.Context(), "01JNOSUCHAGENT0000000000", operationID)
	if !apperr.Is(err, apperr.CodeNotFound) {
		t.Errorf("结束一个不存在的会话得到 %v，期望 not_found", err)
	}
}

// sessionServiceOn 在同一个数据库上构造一个会话服务，用来观察认证的结果。
func sessionServiceOn(t *testing.T, all harness) *agentauth.Service {
	t.Helper()

	service, err := agentauth.NewService(agentauth.Options{
		Agents: repo.NewAgents(all.db), Devices: repo.NewDevices(all.db),
		Workspaces: repo.NewWorkspaces(all.db), Clock: all.clock,
		IDs: ulid.New(all.clock),
	})
	if err != nil {
		t.Fatalf("构造 Agent 会话服务失败：%v", err)
	}
	return service
}

// registration 是一份九项齐备的注册请求。
func registration() agentauth.Registration {
	return agentauth.Registration{
		Name:               "claude",
		Type:               agentauth.TypeClaudeCode,
		ExecutableHash:     "sha256:0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0",
		ExecutablePath:     "/usr/local/bin/claude",
		PID:                4242,
		ParentPID:          4241,
		OSUser:             "agent",
		DeviceFingerprint:  "sha256:device",
		DeviceName:         "workbench",
		WorkspacePath:      "/home/agent/project",
		ProjectFingerprint: "sha256:project",
		StartedAt:          fixtures.Instant,
	}
}
