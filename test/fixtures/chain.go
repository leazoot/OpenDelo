package fixtures

import (
	"testing"

	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/repo"
)

/*
 * 前置行的链条。
 *
 * Lease 的外键要求 agents、identities 与 approvals 都已存在，而这三张表各自
 * 又有自己的前置行。把这条链集中在一处，避免每个需要它的测试文件各写一份
 */

// SeededChain 返回一个已经写好全部前置行的数据库：
// 设备 → 工作区 → Agent → 凭据来源 → 凭据引用 → 身份 → 能力请求 → 决策 → 审批项。
//
// 每个用例一份独立的临时数据库，用例之间不共享任何状态。
func SeededChain(t *testing.T) *store.DB {
	t.Helper()

	db := SeededDecisionChain(t)
	if _, err := repo.NewApprovals(db).CreateApproval(t.Context(), Approval()); err != nil {
		t.Fatalf("写入审批项失败：%v", err)
	}
	return db
}

// SeededDecisionChain 与 SeededChain 相同，但**不写审批项** ——
// 审批生命周期本身的用例要自己创建它，库里先有一条会让「一个决策只有一个审批项」
// 这条断言测不到真正想测的东西。
func SeededDecisionChain(t *testing.T) *store.DB {
	t.Helper()

	db := SeededRequestChain(t)
	if _, err := repo.NewDecisions(db).CreateDecision(t.Context(), Decision()); err != nil {
		t.Fatalf("写入决策失败：%v", err)
	}
	return db
}

// SeededRequestChain 停在能力请求，**不写决策也不写审批项** ——
// 决策链路本身的用例要自己产出决策，库里先有一条会撞上
// 「一个请求只有一个决策」的唯一索引。
func SeededRequestChain(t *testing.T) *store.DB {
	t.Helper()

	db := MigratedDB(t)
	ctx := t.Context()

	if _, err := repo.NewDevices(db).CreateDevice(ctx, Device()); err != nil {
		t.Fatalf("写入设备失败：%v", err)
	}
	if _, err := repo.NewWorkspaces(db).CreateWorkspace(ctx, Workspace()); err != nil {
		t.Fatalf("写入工作区失败：%v", err)
	}
	if _, err := repo.NewAgents(db).CreateAgent(ctx, Agent(DefaultDeviceID, DefaultWorkspaceID)); err != nil {
		t.Fatalf("写入 Agent 失败：%v", err)
	}
	if _, err := repo.NewCredentialProviders(db).CreateProvider(ctx, Provider()); err != nil {
		t.Fatalf("写入凭据来源失败：%v", err)
	}
	if _, err := repo.NewCredentialReferences(db).CreateReference(ctx, Reference()); err != nil {
		t.Fatalf("写入凭据引用失败：%v", err)
	}
	if _, err := repo.NewIdentities(db).CreateIdentity(ctx, Identity()); err != nil {
		t.Fatalf("写入身份失败：%v", err)
	}
	if _, err := repo.NewCapabilityRequests(db).CreateRequest(ctx, Request()); err != nil {
		t.Fatalf("写入能力请求失败：%v", err)
	}
	return db
}
