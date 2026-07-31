package trust

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Runcoor/opendelo/internal/core/risk"
	"github.com/Runcoor/opendelo/internal/core/scope"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
)

/*
 * Trust Memory Manager：生成、匹配、失效（REQ-TRUST-001~005）。
 *
 * 「不得扩大」在这里是**生成时的前置条件**而不是事后检查：要记住的范围必须落在
 * 那次审批放行的范围之内，不落在里面就不生成（REQ-TRUST-002）。仓储侧也没有
 * 修改范围的方法 —— 改范围就是重新学习，只能由一次新的审批产生新记忆。
 */

const (
	// DefaultTTL 是记忆的默认有效期。
	//
	// 取 30 天与 REQ-TRUST-004 的「长期未使用」同一个数：一条 30 天没被用过的
	// 记忆会失效，一条 30 天没被用过又没到期的记忆没有意义。
	DefaultTTL = 30 * 24 * time.Hour
	// MaxTTL 是记忆有效期的上限。超过它即拒绝生成 ——
	// 时间是不得扩大的七个维度之一，「永久记忆」在这里不可表达。
	MaxTTL = 90 * 24 * time.Hour
	// UnusedTTL 是「长期未使用」的判定阈值（REQ-TRUST-004）。
	UnusedTTL = 30 * 24 * time.Hour
)

// Options 是 Manager 的依赖，全部必填。
type Options struct {
	Memories Repository
	Clock    clock.Clock
	IDs      *ulid.Generator
}

// Manager 管理授权记忆的生命周期。
type Manager struct {
	memories Repository
	clock    clock.Clock
	ids      *ulid.Generator
}

// NewManager 校验依赖并构造 Manager。
func NewManager(options Options) (*Manager, error) {
	switch {
	case options.Memories == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("Trust Manager 缺少仓储")
	case options.Clock == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("Trust Manager 缺少时钟")
	case options.IDs == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("Trust Manager 缺少 ID 生成器")
	}
	return &Manager{memories: options.Memories, clock: options.Clock, ids: options.IDs}, nil
}

// GenerateRequest 是一次学习的输入。
type GenerateRequest struct {
	// Approved 是那次审批实际放行的范围。
	Approved scope.Scope
	// Learned 是用户选择要记住的范围。它必须落在 Approved 之内 ——
	// 「今后在当前项目自动允许」改变的是时间，不是其余任何一维。
	Learned scope.Scope
	// ApprovalID 指向产生这条记忆的审批（REQ-TRUST-001 AC2）。
	ApprovalID string
	// RiskLevel 是那次审批的风险等级。记忆的风险上限不得高于它（REQ-TRUST-002 AC3）。
	RiskLevel risk.Level
	Behavior  Behavior
	// TTL 为零时用 DefaultTTL，超过 MaxTTL 即拒绝。
	TTL time.Duration
}

// Generate 生成一条授权记忆。
//
// 收敛校验不通过就不生成（REQ-TRUST-002），高风险审批不产生任何记忆
// （REQ-TRUST-003 AC1）—— 高风险永远需要人工确认，学出来的记忆没有用武之地，
// 存在本身就是一个可能被误用的东西。
func (m *Manager) Generate(ctx context.Context, request GenerateRequest) (Memory, error) {
	if err := m.checkGenerate(request); err != nil {
		return Memory{}, err
	}

	now := m.clock.Now()
	ttl := request.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}

	id, err := m.ids.NewID()
	if err != nil {
		return Memory{}, err
	}
	resourceScope, capabilityScope, err := encodeLearned(request.Learned)
	if err != nil {
		return Memory{}, err
	}

	return m.memories.CreateMemory(ctx, Memory{
		ID:              id,
		AgentID:         request.Learned.AgentID,
		WorkspaceID:     request.Learned.WorkspaceID,
		IdentityID:      request.Learned.IdentityID,
		Service:         request.Learned.Service,
		ResourceScope:   resourceScope,
		CapabilityScope: capabilityScope,
		Environment:     request.Learned.Environment,
		RiskCeiling:     lowerOf(request.RiskLevel, request.Learned.RiskCeiling),
		Behavior:        request.Behavior,
		CreatedFrom:     request.ApprovalID,
		Status:          StatusActive,
		ExpiresAt:       now.Add(ttl),
		CreatedAt:       now,
		UpdatedAt:       now,
	})
}

// checkGenerate 是七个维度的收敛校验加上三条不可协商的前置条件。
func (m *Manager) checkGenerate(request GenerateRequest) error {
	if err := request.Approved.Validate(); err != nil {
		return err
	}
	if err := request.Learned.Validate(); err != nil {
		return err
	}

	// REQ-TRUST-002：资源、操作、Agent、项目、身份、环境六维加上次数与风险上限，
	// 要记住的范围必须落在审批放行的范围之内。时间维由 TTL 单独限定 ——
	// 记忆本来就要活得比一次授权久，拿 15 分钟的窗口去比它没有意义。
	if !request.Approved.CoversIgnoringWindow(request.Learned) {
		return notConverged("要记住的范围超出了那次审批放行的范围")
	}

	// REQ-TRUST-003 AC1：高风险不产生任何记忆。
	if request.RiskLevel == risk.LevelHigh {
		return notConverged("高风险审批不产生授权记忆")
	}
	if request.RiskLevel != risk.LevelLow && request.RiskLevel != risk.LevelMedium {
		return notConverged("风险等级认不出来：" + string(request.RiskLevel))
	}

	switch {
	case request.ApprovalID == "":
		return notConverged("记忆必须指向产生它的那次审批")
	case request.Behavior != BehaviorAutoAllow && request.Behavior != BehaviorAlwaysAsk:
		return notConverged("记忆的行为认不出来：" + string(request.Behavior))
	case request.TTL < 0:
		return notConverged("记忆的有效期不能为负")
	case request.TTL > MaxTTL:
		return notConverged("记忆的有效期超过上限")
	}
	return nil
}

// lowerOf 取两者中更低的一级。
//
// 记忆的风险上限既不得高于那次审批的风险等级（REQ-TRUST-002 AC3），
// 也不得高于用户选择要记住的范围本身 —— 两个上限都成立，取更严的那个。
func lowerOf(left, right risk.Level) risk.Level {
	if rank(left) <= rank(right) {
		return left
	}
	return right
}

func rank(level risk.Level) int {
	switch level {
	case risk.LevelLow:
		return 1
	case risk.LevelMedium:
		return 2
	case risk.LevelHigh:
		return 3
	default:
		return 0
	}
}

func notConverged(detail string) error {
	return apperr.New(apperr.CodeInvalidRequest).WithDetail("不生成授权记忆：" + detail)
}

// Match 列出可用于本次请求的记忆。
//
// 仓储只返回状态为生效中的（REQ-TRUST-004 AC3），这里再滤掉已经到期的：
// 到期与失效是两件事，前者不需要有人来把它标记成失效才成立。
func (m *Manager) Match(
	ctx context.Context, agentID, workspaceID, service string, limit int,
) ([]Memory, error) {
	candidates, err := m.memories.MatchMemories(ctx, agentID, workspaceID, service, limit)
	if err != nil {
		return nil, err
	}

	now := m.clock.Now()
	usable := make([]Memory, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.ExpiresAt.After(now) {
			usable = append(usable, candidate)
		}
	}
	return usable, nil
}

// ByStatus 列出某个状态下的记忆，服务 Automation 页面。
//
// 失效的记忆读得到而不是消失（REQ-TRUST-004 AC2）：页面要说明它为什么失效。
func (m *Manager) ByStatus(ctx context.Context, status Status, limit int) ([]Memory, error) {
	if !validStatus(status) {
		return nil, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("记忆状态认不出来：" + string(status))
	}
	return m.memories.MemoriesByStatus(ctx, status, limit)
}

// ByID 读取一条记忆，不存在时返回 apperr.CodeNotFound。
func (m *Manager) ByID(ctx context.Context, id string) (Memory, error) {
	return m.memories.MemoryByID(ctx, id)
}

// TightenBehavior 把一条记忆改成「始终询问」（REQ-TRUST-005）。
//
// 这是本包唯一的修改入口，且只朝收紧的方向 —— 放宽范围就是重新学习，
// 只能由一次新的审批产生新记忆。
func (m *Manager) TightenBehavior(ctx context.Context, id string) (Memory, error) {
	return m.memories.TightenBehavior(ctx, id, m.clock.Now())
}

// Delete 删除一条记忆（REQ-TRUST-005 AC1）。
//
// 与 Invalidate 不同：失效是系统行为且保留记录与原因，删除是用户在
// Automation 页面上的动作。删掉之后对应请求下次重新进入审批。
func (m *Manager) Delete(ctx context.Context, id string) error {
	return m.memories.DeleteMemory(ctx, id)
}

// InvalidateForIdentity 使某个身份名下全部生效中的记忆失效。
//
// 返回被失效的记忆，供调用方逐条记审计事件。
func (m *Manager) InvalidateForIdentity(
	ctx context.Context, identityID string, reason InvalidationReason, limit int,
) ([]Memory, error) {
	active, err := m.memories.ActiveMemoriesByIdentity(ctx, identityID, limit)
	if err != nil {
		return nil, err
	}
	return m.invalidateEach(ctx, active, reason)
}

// Use 记一次使用，刷新 last_used_at（「长期未使用」的判定依据）。
func (m *Manager) Use(ctx context.Context, id string) (Memory, error) {
	return m.memories.Touch(ctx, id, m.clock.Now())
}

// Invalidate 使一条记忆失效并记下原因（REQ-TRUST-004）。
//
// 原因必须是八个条件之一：认不出的原因说明调用方在表达一件本包读不懂的事，
// 让它进库会让 Automation 页面显示不出「为什么失效」（AC2）。
func (m *Manager) Invalidate(
	ctx context.Context, id string, reason InvalidationReason,
) (Memory, error) {
	if !validReason(reason) {
		return Memory{}, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("失效原因认不出来：" + string(reason))
	}
	return m.memories.Invalidate(ctx, id, reason, m.clock.Now())
}

// InvalidateAll 使全部生效中的记忆失效，用于切换到谨慎模式（REQ-DECIDE-003）。
//
// 返回被失效的记忆，供调用方逐条记审计事件。
func (m *Manager) InvalidateAll(
	ctx context.Context, reason InvalidationReason, limit int,
) ([]Memory, error) {
	active, err := m.memories.MemoriesByStatus(ctx, StatusActive, limit)
	if err != nil {
		return nil, err
	}
	return m.invalidateEach(ctx, active, reason)
}

// InvalidateUnused 使长期未使用的记忆失效（REQ-TRUST-004 的第六个条件）。
//
// 从未使用过的记忆以创建时刻起算：否则一条创建后一直没被用过的记忆永远不会
// 因为「长期未使用」而失效，那一维就形同虚设。
func (m *Manager) InvalidateUnused(ctx context.Context, limit int) ([]Memory, error) {
	active, err := m.memories.MemoriesByStatus(ctx, StatusActive, limit)
	if err != nil {
		return nil, err
	}

	deadline := m.clock.Now().Add(-UnusedTTL)
	var stale []Memory
	for _, candidate := range active {
		if lastTouched(candidate).After(deadline) {
			continue
		}
		stale = append(stale, candidate)
	}
	return m.invalidateEach(ctx, stale, ReasonUnusedTooLong)
}

func (m *Manager) invalidateEach(
	ctx context.Context, targets []Memory, reason InvalidationReason,
) ([]Memory, error) {
	if !validReason(reason) {
		return nil, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("失效原因认不出来：" + string(reason))
	}

	now := m.clock.Now()
	invalidated := make([]Memory, 0, len(targets))
	for _, target := range targets {
		result, err := m.memories.Invalidate(ctx, target.ID, reason, now)
		if err != nil {
			return invalidated, err
		}
		invalidated = append(invalidated, result)
	}
	return invalidated, nil
}

// lastTouched 返回「上一次被用到」的时刻。从未使用过时以创建时刻代替。
func lastTouched(memory Memory) time.Time {
	if memory.LastUsedAt.IsZero() {
		return memory.CreatedAt
	}
	return memory.LastUsedAt
}

// validStatus 报告状态是否是两个取值之一。
func validStatus(status Status) bool {
	return status == StatusActive || status == StatusInvalidated
}

// reasons 是八个失效条件的封闭清单，顺序与常量声明一致。
var reasons = []InvalidationReason{
	ReasonProviderDisconnected,
	ReasonIdentityScopeChanged,
	ReasonAgentExecutableChanged,
	ReasonProjectFingerprintChanged,
	ReasonDeviceUntrusted,
	ReasonUnusedTooLong,
	ReasonCautiousModeSelected,
	ReasonAdapterRiskUpgraded,
}

// Reasons 返回八个失效条件的副本，供调用方与用例逐条核对。
func Reasons() []InvalidationReason {
	return append([]InvalidationReason(nil), reasons...)
}

func validReason(reason InvalidationReason) bool {
	for _, known := range reasons {
		if reason == known {
			return true
		}
	}
	return false
}

// ScopeOf 读回一条记忆记下的范围，供决策链路做匹配。
//
// 解析失败或十个维度不全时返回错误而不是给一个空范围：一个空范围什么都不覆盖，
// 看起来「安全」，但它会让这条记忆静静地失去作用而没有人知道。
func ScopeOf(memory Memory) (scope.Scope, error) {
	var learned scope.Scope
	if err := json.Unmarshal([]byte(memory.ResourceScope), &learned); err != nil {
		return scope.Scope{}, apperr.Wrap(apperr.CodeInternal, err).
			WithDetail("记忆 " + memory.ID + " 的范围无法解析")
	}
	if err := learned.Validate(); err != nil {
		return scope.Scope{}, err
	}
	return learned, nil
}

// encodeLearned 把要记住的范围编码成入库的两列。
//
// CapabilityScope 恒为单个操作：PRD §14.2 的反例正是把「修改某一条记录」
// 学成「修改任意 DNS」，而那在这里连表达都表达不出来。
func encodeLearned(learned scope.Scope) (resourceScope, capabilityScope string, err error) {
	encodedScope, err := json.Marshal(learned)
	if err != nil {
		return "", "", apperr.Wrap(apperr.CodeInternal, err).WithDetail("范围无法编码")
	}
	encodedCapabilities, err := json.Marshal([]string{learned.Operation})
	if err != nil {
		return "", "", apperr.Wrap(apperr.CodeInternal, err).WithDetail("能力清单无法编码")
	}
	return string(encodedScope), string(encodedCapabilities), nil
}
