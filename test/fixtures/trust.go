package fixtures

import (
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/risk"
	"github.com/Runcoor/opendelo/internal/core/trust"
	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/repo"
)

// MemoryOption 调整授权记忆夹具。
type MemoryOption func(*trust.Memory)

// WithMemoryID 换掉记忆主键。
func WithMemoryID(id string) MemoryOption {
	return func(memory *trust.Memory) { memory.ID = id }
}

// WithMemoryCreatedFrom 换掉产生该记忆的审批项。
func WithMemoryCreatedFrom(approvalID string) MemoryOption {
	return func(memory *trust.Memory) { memory.CreatedFrom = approvalID }
}

// WithMemoryAgentID 换掉记忆覆盖的 Agent。
func WithMemoryAgentID(agentID string) MemoryOption {
	return func(memory *trust.Memory) { memory.AgentID = agentID }
}

// WithMemoryWorkspaceID 换掉记忆覆盖的工作区。
func WithMemoryWorkspaceID(workspaceID string) MemoryOption {
	return func(memory *trust.Memory) { memory.WorkspaceID = workspaceID }
}

// WithMemoryService 换掉服务名。
func WithMemoryService(service string) MemoryOption {
	return func(memory *trust.Memory) { memory.Service = service }
}

// WithMemoryResourceScope 换掉资源范围文本。
func WithMemoryResourceScope(scope string) MemoryOption {
	return func(memory *trust.Memory) { memory.ResourceScope = scope }
}

// WithMemoryRiskCeiling 换掉风险上限。
func WithMemoryRiskCeiling(ceiling risk.Level) MemoryOption {
	return func(memory *trust.Memory) { memory.RiskCeiling = ceiling }
}

// WithMemoryBehavior 换掉命中后的行为。
func WithMemoryBehavior(behavior trust.Behavior) MemoryOption {
	return func(memory *trust.Memory) { memory.Behavior = behavior }
}

// WithMemoryEnvironment 换掉环境维度。
func WithMemoryEnvironment(environment matcher.Environment) MemoryOption {
	return func(memory *trust.Memory) { memory.Environment = environment }
}

// WithMemoryInvalidated 把记忆构造成已失效状态。
func WithMemoryInvalidated(reason trust.InvalidationReason) MemoryOption {
	return func(memory *trust.Memory) {
		memory.Status = trust.StatusInvalidated
		memory.InvalidationReason = reason
	}
}

// WithMemoryStatus 只换状态，不动失效原因，用于验证「同进同出」约束。
func WithMemoryStatus(status trust.Status) MemoryOption {
	return func(memory *trust.Memory) { memory.Status = status }
}

// WithMemoryLastUsedAt 换掉最近一次使用时刻。
func WithMemoryLastUsedAt(at time.Time) MemoryOption {
	return func(memory *trust.Memory) { memory.LastUsedAt = at }
}

// Memory 构造一条合法的生效中授权记忆。
//
// 七个维度都取自「一次具体的审批」：一个 Agent、一个项目、一个身份、
// 一个服务、一个资源、一组操作、一个环境，外加 30 天的有效期。
// LastUsedAt 默认为零值，表示从未使用过。
func Memory(options ...MemoryOption) trust.Memory {
	memory := trust.Memory{
		ID:              DefaultMemoryID,
		AgentID:         DefaultAgentID,
		WorkspaceID:     DefaultWorkspaceID,
		IdentityID:      DefaultIdentityID,
		Service:         DefaultServiceLabel,
		ResourceScope:   `{"repo":"Runcoor/opendelo"}`,
		CapabilityScope: `["pull_request.create"]`,
		Environment:     matcher.EnvironmentProduction,
		RiskCeiling:     risk.LevelMedium,
		Behavior:        trust.BehaviorAutoAllow,
		CreatedFrom:     DefaultApprovalID,
		Status:          trust.StatusActive,
		ExpiresAt:       Instant.Add(30 * 24 * time.Hour),
		CreatedAt:       Instant,
		UpdatedAt:       Instant,
	}
	for _, apply := range options {
		apply(&memory)
	}
	return memory
}

// SeedMemoryChain 把一条授权记忆所需的全部前置记录一次写好：设备、工作区、
// Agent、凭据来源、凭据引用、身份、Adapter 声明、能力请求、决策、审批，最后是记忆本身。
//
// 全部使用本包的默认主键与默认字段，因此写出的 Agent 与 Registration 夹具描述的是
// 同一个进程 —— 用它铺底的用例可以直接观察「注册命中已有身份」与「哈希变化后
// 记忆匹配不到」两条路径。
func SeedMemoryChain(t testing.TB, db *store.DB, options ...MemoryOption) trust.Memory {
	t.Helper()
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
	if _, err := repo.NewServiceAdapters(db).CreateDeclaration(ctx, Declaration()); err != nil {
		t.Fatalf("写入 Adapter 声明失败：%v", err)
	}
	if _, err := repo.NewCapabilityRequests(db).CreateRequest(ctx, Request()); err != nil {
		t.Fatalf("写入能力请求失败：%v", err)
	}
	if _, err := repo.NewDecisions(db).CreateDecision(ctx, Decision()); err != nil {
		t.Fatalf("写入决策失败：%v", err)
	}
	if _, err := repo.NewApprovals(db).CreateApproval(ctx, Approval()); err != nil {
		t.Fatalf("写入审批项失败：%v", err)
	}

	memory := Memory(options...)
	created, err := repo.NewTrustMemories(db).CreateMemory(ctx, memory)
	if err != nil {
		t.Fatalf("写入授权记忆失败：%v", err)
	}
	return created
}
