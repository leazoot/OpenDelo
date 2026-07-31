package repo_test

import (
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * agents 仓储中支撑「重启后重新认证」的两个方法（REQ-AGENT-001）。
 */

// fixtureBinding 是 fixtures.Agent 那条记录的身份绑定。
func fixtureBinding() agentauth.Binding {
	agent := fixtures.Agent(fixtures.DefaultDeviceID, fixtures.DefaultWorkspaceID)
	return agentauth.Binding{
		DeviceID:       agent.DeviceID,
		WorkspaceID:    agent.WorkspaceID,
		ExecutablePath: agent.ExecutablePath,
		OSUser:         agent.OSUser,
		ExecutableHash: agent.ExecutableHash,
	}
}

func TestAgents_ByBinding_FindsThePreviousRegistration(t *testing.T) {
	ctx := t.Context()
	chain := seededRequestChain(t)
	agents := repo.NewAgents(chain.db)

	found, err := agents.AgentByBinding(ctx, fixtureBinding())
	if err != nil {
		t.Fatalf("按身份绑定读取失败：%v", err)
	}
	if found.ID != fixtures.DefaultAgentID {
		t.Errorf("读到 %s，期望 %s", found.ID, fixtures.DefaultAgentID)
	}
}

func TestAgents_ByBinding_EachDimensionMustMatch(t *testing.T) {
	// 任何一维不同都是另一个身份。少了其中一维的查询会把别的进程认成自己人。
	cases := []struct {
		name   string
		change func(*agentauth.Binding)
	}{
		{"换一台设备", func(b *agentauth.Binding) { b.DeviceID = "01K1DEVICE0000000000000002" }},
		{"换一个工作区", func(b *agentauth.Binding) { b.WorkspaceID = "01K1WORKSPACE00000000000002" }},
		{"换一个可执行文件路径", func(b *agentauth.Binding) { b.ExecutablePath = "/opt/claude" }},
		{"换一个系统用户", func(b *agentauth.Binding) { b.OSUser = "someone-else" }},
		{"可执行文件改了一个字节", func(b *agentauth.Binding) { b.ExecutableHash = "sha256:6f1d0c9a2c" }},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := t.Context()
			chain := seededRequestChain(t)
			agents := repo.NewAgents(chain.db)

			binding := fixtureBinding()
			testCase.change(&binding)

			_, err := agents.AgentByBinding(ctx, binding)
			if !apperr.Is(err, apperr.CodeNotFound) {
				t.Fatalf("错误码为 %s，期望 not_found", apperr.CodeOf(err))
			}
		})
	}
}

func TestAgents_ByBinding_ReturnsTheMostRecentRegistration(t *testing.T) {
	// 同一绑定下有多条记录时取最近的一条：ULID 时间有序，主键最大即最近。
	ctx := t.Context()
	chain := seededRequestChain(t)
	agents := repo.NewAgents(chain.db)

	const laterID = "01K1AGENT00000000000000001"
	later := fixtures.Agent(fixtures.DefaultDeviceID, fixtures.DefaultWorkspaceID,
		fixtures.WithAgentID(laterID),
		fixtures.WithSessionKeyHash("sha256:session-0002"),
	)
	if _, err := agents.CreateAgent(ctx, later); err != nil {
		t.Fatalf("写入第二条 Agent 失败：%v", err)
	}

	found, err := agents.AgentByBinding(ctx, fixtureBinding())
	if err != nil {
		t.Fatalf("按身份绑定读取失败：%v", err)
	}
	if found.ID != laterID {
		t.Errorf("读到 %s，期望最近的一条 %s", found.ID, laterID)
	}
}

func TestAgents_Rebind_ReplacesTheProcessContextAndKeepsTheIdentity(t *testing.T) {
	ctx := t.Context()
	chain := seededRequestChain(t)
	agents := repo.NewAgents(chain.db)

	before, err := agents.AgentByID(ctx, fixtures.DefaultAgentID)
	if err != nil {
		t.Fatalf("读取 Agent 失败：%v", err)
	}
	if _, disconnectErr := agents.SetAgentStatus(
		ctx, before.ID, agentauth.StatusDisconnected, fixtures.Instant,
	); disconnectErr != nil {
		t.Fatalf("置为断开失败：%v", disconnectErr)
	}

	restartedAt := fixtures.Instant.Add(time.Minute)
	after, err := agents.RebindAgent(ctx, before.ID, agentauth.Rebind{
		PID:              5678,
		ParentPID:        5677,
		StartedAt:        restartedAt,
		SessionKeyHash:   "sha256:session-0002",
		SessionExpiresAt: restartedAt.Add(time.Hour),
		At:               restartedAt,
	})
	if err != nil {
		t.Fatalf("重新绑定失败：%v", err)
	}

	if after.PID != 5678 || after.ParentPID != 5677 {
		t.Errorf("进程号为 %d/%d，期望 5678/5677", after.PID, after.ParentPID)
	}
	if !after.StartedAt.Equal(restartedAt) {
		t.Errorf("启动时间为 %s，期望 %s", after.StartedAt, restartedAt)
	}
	if after.SessionKeyHash != "sha256:session-0002" {
		t.Error("会话密钥没有换新，旧密钥仍然有效")
	}
	if after.Status != agentauth.StatusActive {
		t.Errorf("重新绑定后状态为 %s，期望 active", after.Status)
	}

	// 身份与信任等级不因重启而改变，否则绑定其上的记忆与确认都要重来。
	if after.ID != before.ID {
		t.Errorf("主键从 %s 变成了 %s", before.ID, after.ID)
	}
	if after.TrustLevel != before.TrustLevel {
		t.Errorf("信任等级从 %s 变成了 %s", before.TrustLevel, after.TrustLevel)
	}
	if !after.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("创建时间从 %s 变成了 %s", before.CreatedAt, after.CreatedAt)
	}
	if after.ExecutableHash != before.ExecutableHash {
		t.Error("重新绑定改写了可执行文件哈希，那已经是另一个身份了")
	}
}

func TestAgents_Rebind_UnknownAgent_IsNotFound(t *testing.T) {
	ctx := t.Context()
	chain := seededRequestChain(t)
	agents := repo.NewAgents(chain.db)

	_, err := agents.RebindAgent(ctx, "01K1MISSING0000000000000000", agentauth.Rebind{
		PID:              5678,
		StartedAt:        fixtures.Instant,
		SessionKeyHash:   "sha256:session-0002",
		SessionExpiresAt: fixtures.Instant.Add(time.Hour),
		At:               fixtures.Instant,
	})
	if !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("错误码为 %s，期望 not_found", apperr.CodeOf(err))
	}
}
