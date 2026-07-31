package approval

import (
	"context"
	"time"

	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/risk"
	"github.com/Runcoor/opendelo/internal/core/trust"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
)

/*
 * 审批生命周期：创建 → 用户给出五种操作之一 / 超时（REQ-APPROVAL-002、REQ-CAP-003）。
 *
 * 本包不签发 Lease、不生成记忆，只回答「这次确认之后该发生什么」。真正去做那两件事
 * 的是 pipeline —— 把它们分开，是为了让「同一个审批只能放行一次」这条保证
 * 落在一个地方：仓储的条件更新。
 */

const (
	// DefaultTimeout 是等待审批的默认时长（REQ-CAP-003）。
	DefaultTimeout = 5 * time.Minute
	// MinTimeout 与 MaxTimeout 是可配置范围。超出即拒绝构造 ——
	// 一个 24 小时的审批窗口等于一条实际上不会过期的授权入口。
	MinTimeout = 30 * time.Second
	MaxTimeout = 30 * time.Minute
)

// Options 是 Manager 的依赖。Timeout 为零时用 DefaultTimeout。
type Options struct {
	Approvals Repository
	Clock     clock.Clock
	IDs       *ulid.Generator
	Timeout   time.Duration
}

// Manager 管理审批项的生命周期。
type Manager struct {
	approvals Repository
	clock     clock.Clock
	ids       *ulid.Generator
	timeout   time.Duration
}

// NewManager 校验依赖与超时范围并构造 Manager。
func NewManager(options Options) (*Manager, error) {
	timeout := options.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}

	switch {
	case options.Approvals == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("审批 Manager 缺少仓储")
	case options.Clock == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("审批 Manager 缺少时钟")
	case options.IDs == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("审批 Manager 缺少 ID 生成器")
	case timeout < MinTimeout || timeout > MaxTimeout:
		return nil, apperr.New(apperr.CodeInvalidConfiguration).
			WithDetail("审批超时必须在 30 秒到 30 分钟之间")
	}
	return &Manager{
		approvals: options.Approvals,
		clock:     options.Clock,
		ids:       options.IDs,
		timeout:   timeout,
	}, nil
}

// Open 为一个需要人工确认的决策创建审批项。
//
// 幂等（REQ-API-004）：同一个决策重复调用返回首次创建的那一条，而不是产生
// 第二个可以分别放行的入口。
func (m *Manager) Open(ctx context.Context, decisionID string) (Approval, error) {
	if decisionID == "" {
		return Approval{}, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("审批项必须指向一个决策")
	}

	existing, err := m.approvals.ApprovalByDecisionID(ctx, decisionID)
	if err == nil {
		return existing, nil
	}
	if !apperr.Is(err, apperr.CodeNotFound) {
		return Approval{}, err
	}

	id, err := m.ids.NewID()
	if err != nil {
		return Approval{}, err
	}
	now := m.clock.Now()

	return m.approvals.CreateApproval(ctx, Approval{
		ID:         id,
		DecisionID: decisionID,
		Status:     StatusPending,
		ExpiresAt:  now.Add(m.timeout),
		CreatedAt:  now,
		UpdatedAt:  now,
	})
}

// Actions 返回这个风险等级下允许提供的操作（REQ-APPROVAL-002）。
//
// 高风险不提供两个学习类操作：「今后自动允许」由 PRD §13.2 明确排除，
// 「始终要求确认」被排除是因为 REQ-TRUST-003 下高风险不产生任何记忆 ——
// 提供一个必然做不成的选项，比不提供它更糟。
func Actions(level risk.Level) []Action {
	always := []Action{ActionDeny, ActionAllowOnce, ActionAllowUntilTaskEnd}
	if level == risk.LevelHigh {
		return always
	}
	return append(always, ActionAutoAllowInProject, ActionAlwaysAsk)
}

// SettleRequest 是用户在 Access Folio 上给出结果时的输入。
type SettleRequest struct {
	ApprovalID string
	Action     Action
	// RiskLevel 是这次决策算出的风险等级，决定哪些操作可选。
	RiskLevel risk.Level
	// StrongAuthCompleted 是强认证的挂钩点（REQ-APPROVAL-005）。
	// 决策要求 strong_auth 时它必须为真，具体怎么认证由接入面实现。
	StrongAuthCompleted bool
	// Requirement 与 RequiredConfirmations 来自 decision.Outcome：
	// 前者说确认要多强，后者说要确认几次（谨慎模式的高风险为 2）。
	Requirement           decision.ApprovalRequirement
	RequiredConfirmations int
	// Confirmations 是已经完成的确认次数。
	Confirmations int
}

// Settlement 是一次审批结论要带来的后果。
//
// 本包只说该发生什么，不去做。签发 Lease 与生成记忆由 pipeline 执行 ——
// 那样「同一个审批只能放行一次」就只由仓储的条件更新一处保证。
type Settlement struct {
	Approval Approval
	// Allowed 为假表示不放行，此时其余字段都无意义。
	Allowed bool
	// RequestLimit 是要签发的 Lease 的次数上限。0 表示沿用收敛好的 Scope。
	RequestLimit int
	// SessionBound 为真表示 Lease 绑定 Agent Session（REQ-APPROVAL-002 AC3）。
	SessionBound bool
	// Learn 非空表示要生成一条 Trust Memory 及其行为。
	Learn trust.Behavior
	// Replayed 为真表示这次提交只是重放了之前已经做出的同一个决定。
	// 调用方据此不再签发第二条 Lease、不再学第二条记忆（REQ-API-004）。
	Replayed bool
}

// consequences 是五种操作各自的后果。
//
// 写成一张字面上的表：加一个取值就得在这里给出它的后果，
// 而不是在某个 switch 的 default 里被悄悄当成「允许」。
var consequences = map[Action]Settlement{
	ActionDeny:               {Allowed: false},
	ActionAllowOnce:          {Allowed: true, RequestLimit: 1},
	ActionAllowUntilTaskEnd:  {Allowed: true, SessionBound: true},
	ActionAutoAllowInProject: {Allowed: true, Learn: trust.BehaviorAutoAllow},
	ActionAlwaysAsk:          {Allowed: true, Learn: trust.BehaviorAlwaysAsk},
}

// Settle 写入用户给出的结果。
//
// 「同一个审批只能被处理一次」由仓储的条件更新保证，本包不先读一次状态再判断 ——
// 那样两个并发请求会读到同一个 pending。条件更新失败之后才去读，此时结果已经定了。
//
// 换成另一个操作再提交返回 conflict（对应 409）：拒绝之后不能被改成允许。
func (m *Manager) Settle(ctx context.Context, request SettleRequest) (Settlement, error) {
	consequence, err := m.checkSettle(request)
	if err != nil {
		return Settlement{}, err
	}

	status := StatusApproved
	if !consequence.Allowed {
		status = StatusRejected
	}

	settled, err := m.approvals.Settle(
		ctx, request.ApprovalID, request.Action, status, m.clock.Now())
	if err != nil {
		if !apperr.Is(err, apperr.CodeConflict) {
			return Settlement{}, err
		}
		return m.replay(ctx, request, consequence)
	}
	consequence.Approval = settled
	return consequence, nil
}

// replay 处理「这个审批项之前已经被处理过」。
//
// 同一个操作重复提交返回首次的结果（REQ-API-004）：Console 的重复点击与
// 网络重发不该变成一次 409，也不该产生第二条 Lease —— 后者由调用方拿着
// Replayed 这个标记避免。
//
// 换了操作、或者上一次是超时关闭的，一律仍是冲突：超时不是任何人做出的决定，
// 把它当成「你刚才提交过的那次拒绝」会让账本上少掉「没人来处理」这个事实。
func (m *Manager) replay(
	ctx context.Context, request SettleRequest, consequence Settlement,
) (Settlement, error) {
	settled, err := m.approvals.ApprovalByID(ctx, request.ApprovalID)
	if err != nil {
		return Settlement{}, err
	}
	if settled.Action != request.Action || settled.Status == StatusExpired {
		return Settlement{}, apperr.New(apperr.CodeConflict).
			WithDetail("审批项 " + request.ApprovalID + " 已经是 " + string(settled.Status) +
				"，不能再改成 " + string(request.Action))
	}

	consequence.Approval = settled
	consequence.Replayed = true
	return consequence, nil
}

func (m *Manager) checkSettle(request SettleRequest) (Settlement, error) {
	consequence, known := consequences[request.Action]
	if !known {
		return Settlement{}, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("审批操作认不出来：" + string(request.Action))
	}
	if !offered(request.RiskLevel, request.Action) {
		return Settlement{}, apperr.New(apperr.CodeForbidden).
			WithDetail("风险等级 " + string(request.RiskLevel) +
				" 下不提供操作 " + string(request.Action))
	}

	// 拒绝不需要强认证也不需要凑够确认次数：它只会让权限更少。
	if !consequence.Allowed {
		return consequence, nil
	}

	if request.Requirement == decision.ApprovalStrongAuth && !request.StrongAuthCompleted {
		return Settlement{}, apperr.New(apperr.CodeUnauthenticated).
			WithDetail("这次放行需要先完成强认证")
	}
	if request.Confirmations < required(request.RequiredConfirmations) {
		return Settlement{}, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("确认次数不足")
	}
	return consequence, nil
}

// required 把「决策要求几次确认」归一化。零或负值按一次处理 ——
// 放行至少要有人点过一次头。
func required(count int) int {
	if count < 1 {
		return 1
	}
	return count
}

func offered(level risk.Level, action Action) bool {
	for _, available := range Actions(level) {
		if available == action {
			return true
		}
	}
	return false
}

// Expire 把超时未处理的审批项逐条关闭（REQ-CAP-003）。
//
// 超时的结果记为「拒绝」这个操作加上 expired 这个状态：前者表达最终没有放行，
// 后者表达是因为没人来处理。**超时不产生 Lease**（AC2）—— 本包的 Expire
// 不返回任何 Settlement，调用方拿不到可以据以签发的东西。
//
// 返回被关闭的审批项，供调用方逐条记 approval.expired 审计事件（AC3）。
func (m *Manager) Expire(ctx context.Context, limit int) ([]Approval, error) {
	now := m.clock.Now()
	due, err := m.approvals.PendingApprovalsDueBefore(ctx, now, limit)
	if err != nil {
		return nil, err
	}

	expired := make([]Approval, 0, len(due))
	for _, waiting := range due {
		result, err := m.approvals.Settle(ctx, waiting.ID, ActionDeny, StatusExpired, now)
		if err != nil {
			return expired, err
		}
		expired = append(expired, result)
	}
	return expired, nil
}

// ByID 读取一个审批项，不存在时返回 apperr.CodeNotFound。
func (m *Manager) ByID(ctx context.Context, id string) (Approval, error) {
	return m.approvals.ApprovalByID(ctx, id)
}

// Pending 列出等待处理的审批项，按超时先后（Gate 的待审批列表）。
func (m *Manager) Pending(ctx context.Context, limit int) ([]Approval, error) {
	return m.approvals.ApprovalsByStatus(ctx, StatusPending, limit)
}
