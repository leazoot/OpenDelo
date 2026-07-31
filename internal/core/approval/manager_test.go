package approval_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/core/approval"
	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/risk"
	"github.com/Runcoor/opendelo/internal/core/scope"
	"github.com/Runcoor/opendelo/internal/core/trust"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * core/approval 的行为用例（REQ-APPROVAL-001/002、REQ-CAP-003、REQ-API-004）。
 *
 * 用真实的 SQLite 仓储：「同一个审批只能被放行一次」由条件更新保证，
 * 「一个决策只有一个审批项」由唯一索引保证 —— 换成替身两条都测不到。
 */

type harness struct {
	manager   *approval.Manager
	approvals *repo.Approvals
	db        *store.DB
	clock     *clock.Fixed
}

func newHarness(t *testing.T) harness {
	t.Helper()

	return newHarnessWithTimeout(t, 0)
}

func newHarnessWithTimeout(t *testing.T, timeout time.Duration) harness {
	t.Helper()

	// 用不含审批项的链条：库里先有一条会让「一个决策只有一个审批项」
	// 这条断言测不到真正想测的东西。
	db := fixtures.SeededDecisionChain(t)

	fixed := clock.NewFixed(fixtures.Instant)
	approvals := repo.NewApprovals(db)
	manager, err := approval.NewManager(approval.Options{
		Approvals: approvals,
		Clock:     fixed,
		IDs:       ulid.New(fixed),
		Timeout:   timeout,
	})
	if err != nil {
		t.Fatalf("构造审批 Manager 失败：%v", err)
	}
	return harness{manager: manager, approvals: approvals, db: db, clock: fixed}
}

func open(t *testing.T, all harness) approval.Approval {
	t.Helper()

	item, err := all.manager.Open(t.Context(), fixtures.DefaultDecisionID)
	if err != nil {
		t.Fatalf("创建审批项失败：%v", err)
	}
	return item
}

func settleRequest(id string, action approval.Action) approval.SettleRequest {
	return approval.SettleRequest{
		ApprovalID:            id,
		Action:                action,
		RiskLevel:             risk.LevelMedium,
		Requirement:           decision.ApprovalStandard,
		RequiredConfirmations: 1,
		Confirmations:         1,
	}
}

func assertCode(t *testing.T, err error, expected apperr.Code) {
	t.Helper()

	if err == nil {
		t.Fatalf("期望失败并返回 %s，实际成功", expected)
	}
	if !apperr.Is(err, expected) {
		t.Fatalf("错误码为 %s，期望 %s（%v）", apperr.CodeOf(err), expected, err)
	}
}

// ——— 创建与幂等（REQ-API-004）———

func TestOpen_SetsATimeoutThatCannotBeSkipped(t *testing.T) {
	// 一个永远等下去的审批项等于一条永久授权的入口。
	all := newHarness(t)
	item := open(t, all)

	if item.Status != approval.StatusPending {
		t.Errorf("状态为 %s，期望 pending", item.Status)
	}
	if want := fixtures.Instant.Add(approval.DefaultTimeout); !item.ExpiresAt.Equal(want) {
		t.Errorf("超时时刻为 %v，期望 %v", item.ExpiresAt, want)
	}
	if approval.DefaultTimeout != 5*time.Minute {
		t.Errorf("默认超时为 %v，REQ-CAP-003 要求 5 分钟", approval.DefaultTimeout)
	}
	if item.Action != "" {
		t.Errorf("未决的审批项带了操作 %q", item.Action)
	}
	if !item.DecidedAt.IsZero() {
		t.Errorf("未决的审批项带了决出时刻 %v", item.DecidedAt)
	}
}

func TestOpen_SameDecisionTwice_ReturnsTheFirstItem(t *testing.T) {
	// REQ-API-004：重复调用返回首次结果，而不是产生第二个可以分别放行的入口。
	all := newHarness(t)
	first := open(t, all)
	second := open(t, all)

	if first.ID != second.ID {
		t.Errorf("第二次创建出了新的审批项 %s，期望复用 %s", second.ID, first.ID)
	}

	pending, err := all.manager.Pending(t.Context(), 10)
	if err != nil {
		t.Fatalf("列出待审批失败：%v", err)
	}
	if len(pending) != 1 {
		t.Errorf("待审批有 %d 条，期望 1 条", len(pending))
	}
}

func TestOpen_WithoutADecision_IsRefused(t *testing.T) {
	all := newHarness(t)

	_, err := all.manager.Open(t.Context(), "")
	assertCode(t, err, apperr.CodeInvalidRequest)
	// 说明必须来自本包：外键也会拦下空的 decision_id，而仓储把约束冲突
	// 同样翻译成 invalid_request，只看错误码分不出是谁拒的。
	if !strings.Contains(err.Error(), "必须指向一个决策") {
		t.Errorf("拒绝理由为 %v，期望来自本包的校验", err)
	}
}

// failingApprovals 在指定的方法上返回哨兵错误，其余转交真实仓储。
type failingApprovals struct {
	*repo.Approvals
	failOn string
	err    error
}

var errRepositoryDown = apperr.New(apperr.CodeInternal).WithDetail("SENTINEL_REPO_DOWN")

func (f failingApprovals) ApprovalByDecisionID(
	ctx context.Context, decisionID string,
) (approval.Approval, error) {
	if f.failOn == "ApprovalByDecisionID" {
		return approval.Approval{}, f.err
	}
	return f.Approvals.ApprovalByDecisionID(ctx, decisionID)
}

func (f failingApprovals) Settle(
	ctx context.Context, id string, action approval.Action,
	status approval.Status, at time.Time,
) (approval.Approval, error) {
	if f.failOn == "Settle" {
		return approval.Approval{}, f.err
	}
	return f.Approvals.Settle(ctx, id, action, status, at)
}

func TestManager_RepositoryFailure_IsPassedUpUnchanged(t *testing.T) {
	// 仓储失败必须原样上传，不能被当成「这个决策还没有审批项」而新建一条，
	// 也不能在超时清扫里被跳过 —— 后者会让一批审批项静静地留在待处理。
	cases := []struct {
		method string
		call   func(*approval.Manager, string) error
	}{
		{"ApprovalByDecisionID", func(m *approval.Manager, _ string) error {
			_, err := m.Open(t.Context(), fixtures.DefaultDecisionID)
			return err
		}},
		{"Settle", func(m *approval.Manager, _ string) error {
			_, err := m.Expire(t.Context(), 10)
			return err
		}},
	}

	for _, testCase := range cases {
		t.Run(testCase.method+" 失败时错误上传", func(t *testing.T) {
			all := newHarness(t)
			item := open(t, all)
			all.clock.Set(item.ExpiresAt.Add(time.Second))

			fixed := clock.NewFixed(all.clock.Now())
			manager, err := approval.NewManager(approval.Options{
				Approvals: failingApprovals{
					Approvals: all.approvals, failOn: testCase.method, err: errRepositoryDown,
				},
				Clock: fixed,
				IDs:   ulid.New(fixed),
			})
			if err != nil {
				t.Fatalf("构造 Manager 失败：%v", err)
			}

			if got := testCase.call(manager, item.ID); !errors.Is(got, errRepositoryDown) {
				t.Errorf("返回的错误为 %v，期望原样传出哨兵错误", got)
			}
		})
	}
}

func TestNewManager_TimeoutOutOfRange_IsRefused(t *testing.T) {
	// REQ-CAP-003：可配置范围 30 秒–30 分钟。
	db := fixtures.MigratedDB(t)
	fixed := clock.NewFixed(fixtures.Instant)
	base := approval.Options{
		Approvals: repo.NewApprovals(db),
		Clock:     fixed,
		IDs:       ulid.New(fixed),
	}

	for _, timeout := range []time.Duration{
		time.Second, approval.MinTimeout - time.Millisecond,
		approval.MaxTimeout + time.Millisecond, 24 * time.Hour, -time.Minute,
	} {
		options := base
		options.Timeout = timeout
		if _, err := approval.NewManager(options); err == nil {
			t.Errorf("超时 %v 被接受了", timeout)
		}
	}

	for _, timeout := range []time.Duration{
		0, approval.MinTimeout, approval.DefaultTimeout, approval.MaxTimeout,
	} {
		options := base
		options.Timeout = timeout
		if _, err := approval.NewManager(options); err != nil {
			t.Errorf("超时 %v 被拒绝了：%v", timeout, err)
		}
	}
}

func TestNewManager_MissingAnyDependency_IsRefused(t *testing.T) {
	db := fixtures.MigratedDB(t)
	fixed := clock.NewFixed(fixtures.Instant)
	complete := approval.Options{
		Approvals: repo.NewApprovals(db),
		Clock:     fixed,
		IDs:       ulid.New(fixed),
	}

	cases := map[string]approval.Options{
		"缺仓储":      {Clock: complete.Clock, IDs: complete.IDs},
		"缺时钟":      {Approvals: complete.Approvals, IDs: complete.IDs},
		"缺 ID 生成器": {Approvals: complete.Approvals, Clock: complete.Clock},
	}
	for name, options := range cases {
		t.Run(name+"时拒绝构造", func(t *testing.T) {
			if _, err := approval.NewManager(options); err == nil {
				t.Error("依赖不全仍构造成功")
			}
		})
	}
}

// ——— 五种操作（REQ-APPROVAL-002）———

func TestSettle_EveryActionHasItsOwnConsequence(t *testing.T) {
	// 五种操作各一个用例，逐条断言它带来的后果。
	cases := []struct {
		action   approval.Action
		status   approval.Status
		expected approval.Settlement
	}{
		{
			approval.ActionDeny, approval.StatusRejected,
			approval.Settlement{Allowed: false},
		},
		{
			approval.ActionAllowOnce, approval.StatusApproved,
			approval.Settlement{Allowed: true, RequestLimit: 1},
		},
		{
			approval.ActionAllowUntilTaskEnd, approval.StatusApproved,
			approval.Settlement{Allowed: true, SessionBound: true},
		},
		{
			approval.ActionAutoAllowInProject, approval.StatusApproved,
			approval.Settlement{Allowed: true, Learn: trust.BehaviorAutoAllow},
		},
		{
			approval.ActionAlwaysAsk, approval.StatusApproved,
			approval.Settlement{Allowed: true, Learn: trust.BehaviorAlwaysAsk},
		},
	}

	if len(cases) != 5 {
		t.Fatalf("覆盖了 %d 种操作，REQ-APPROVAL-002 列了 5 种", len(cases))
	}

	for _, testCase := range cases {
		t.Run(string(testCase.action), func(t *testing.T) {
			all := newHarness(t)
			item := open(t, all)

			settlement, err := all.manager.Settle(
				t.Context(), settleRequest(item.ID, testCase.action))
			if err != nil {
				t.Fatalf("写入审批结果失败：%v", err)
			}

			if settlement.Approval.Status != testCase.status {
				t.Errorf("状态为 %s，期望 %s", settlement.Approval.Status, testCase.status)
			}
			if settlement.Approval.Action != testCase.action {
				t.Errorf("操作为 %s，期望 %s", settlement.Approval.Action, testCase.action)
			}
			if settlement.Approval.DecidedAt.IsZero() {
				t.Error("已决的审批项没有记下决出结果的时刻")
			}

			settlement.Approval = approval.Approval{}
			if !reflect.DeepEqual(settlement, testCase.expected) {
				t.Errorf("后果为 %+v，期望 %+v", settlement, testCase.expected)
			}
		})
	}
}

func TestActions_HighRiskDoesNotOfferLearning(t *testing.T) {
	// AC1：high 风险不提供「今后自动允许」。「始终要求确认」一并不提供 ——
	// REQ-TRUST-003 下高风险不产生任何记忆，给一个必然做不成的选项更糟。
	high := approval.Actions(risk.LevelHigh)
	expectedHigh := []approval.Action{
		approval.ActionDeny, approval.ActionAllowOnce, approval.ActionAllowUntilTaskEnd,
	}
	if !reflect.DeepEqual(high, expectedHigh) {
		t.Errorf("高风险可选操作为 %v，期望 %v", high, expectedHigh)
	}

	for _, level := range []risk.Level{risk.LevelLow, risk.LevelMedium} {
		available := approval.Actions(level)
		expected := append(append([]approval.Action(nil), expectedHigh...),
			approval.ActionAutoAllowInProject, approval.ActionAlwaysAsk)
		if !reflect.DeepEqual(available, expected) {
			t.Errorf("%s 风险可选操作为 %v，期望 %v", level, available, expected)
		}
	}
}

func TestSettle_LearningActionOnHighRisk_IsRefused(t *testing.T) {
	// 界面上不显示不够：接入面直接发过来同样要被拒。
	for _, action := range []approval.Action{
		approval.ActionAutoAllowInProject, approval.ActionAlwaysAsk,
	} {
		t.Run(string(action)+" 在高风险下被拒", func(t *testing.T) {
			all := newHarness(t)
			item := open(t, all)

			request := settleRequest(item.ID, action)
			request.RiskLevel = risk.LevelHigh
			request.Requirement = decision.ApprovalStrongAuth
			request.StrongAuthCompleted = true

			_, err := all.manager.Settle(t.Context(), request)
			assertCode(t, err, apperr.CodeForbidden)

			still, err := all.approvals.ApprovalByID(t.Context(), item.ID)
			if err != nil {
				t.Fatalf("读取审批项失败：%v", err)
			}
			if still.Status != approval.StatusPending {
				t.Errorf("被拒绝的操作把审批项推进到了 %s", still.Status)
			}
		})
	}
}

func TestSettle_UnknownAction_IsRefused(t *testing.T) {
	all := newHarness(t)
	item := open(t, all)

	for _, action := range []approval.Action{"", "allow", "approve", "DENY"} {
		_, err := all.manager.Settle(t.Context(), settleRequest(item.ID, action))
		assertCode(t, err, apperr.CodeInvalidRequest)
	}
}

// ——— 强认证与二次确认的挂钩点 ———

func TestSettle_StrongAuthRequiredButNotCompleted_IsRefused(t *testing.T) {
	// REQ-APPROVAL-005 的挂钩点：怎么认证由接入面实现，
	// 但「没认证就不能放行」在这一层就成立。
	all := newHarness(t)
	item := open(t, all)

	request := settleRequest(item.ID, approval.ActionAllowOnce)
	request.RiskLevel = risk.LevelHigh
	request.Requirement = decision.ApprovalStrongAuth

	_, err := all.manager.Settle(t.Context(), request)
	assertCode(t, err, apperr.CodeUnauthenticated)

	request.StrongAuthCompleted = true
	if _, err := all.manager.Settle(t.Context(), request); err != nil {
		t.Fatalf("完成强认证后仍被拒绝：%v", err)
	}
}

func TestSettle_NotEnoughConfirmations_IsRefused(t *testing.T) {
	// 谨慎模式下的高风险要确认两次（PRD §11.1）。
	all := newHarness(t)
	item := open(t, all)

	request := settleRequest(item.ID, approval.ActionAllowOnce)
	request.RiskLevel = risk.LevelHigh
	request.Requirement = decision.ApprovalStrongAuth
	request.StrongAuthCompleted = true
	request.RequiredConfirmations = 2
	request.Confirmations = 1

	_, err := all.manager.Settle(t.Context(), request)
	assertCode(t, err, apperr.CodeInvalidRequest)

	request.Confirmations = 2
	if _, err := all.manager.Settle(t.Context(), request); err != nil {
		t.Fatalf("确认两次后仍被拒绝：%v", err)
	}
}

func TestSettle_ZeroConfirmations_IsStillNotEnough(t *testing.T) {
	// 放行至少要有人点过一次头：决策没说要几次时按一次处理。
	all := newHarness(t)
	item := open(t, all)

	request := settleRequest(item.ID, approval.ActionAllowOnce)
	request.RequiredConfirmations = 0
	request.Confirmations = 0

	_, err := all.manager.Settle(t.Context(), request)
	assertCode(t, err, apperr.CodeInvalidRequest)
}

func TestSettle_Deny_NeedsNeitherStrongAuthNorConfirmations(t *testing.T) {
	// 拒绝只会让权限更少，不该被强认证挡住 —— 那会让用户在拿不到 Passkey 时
	// 连「不许」都说不出口。
	all := newHarness(t)
	item := open(t, all)

	request := settleRequest(item.ID, approval.ActionDeny)
	request.RiskLevel = risk.LevelHigh
	request.Requirement = decision.ApprovalStrongAuth
	request.StrongAuthCompleted = false
	request.RequiredConfirmations = 2
	request.Confirmations = 0

	settlement, err := all.manager.Settle(t.Context(), request)
	if err != nil {
		t.Fatalf("拒绝被挡住了：%v", err)
	}
	if settlement.Allowed {
		t.Error("拒绝的结果是允许")
	}
}

// ——— 幂等与并发（REQ-API-004）———

func TestSettle_AfterDeny_AllowingReturnsConflict(t *testing.T) {
	// 已 deny 后 allow 返回 409：拒绝之后不能被改成允许。
	all := newHarness(t)
	item := open(t, all)

	if _, err := all.manager.Settle(
		t.Context(), settleRequest(item.ID, approval.ActionDeny)); err != nil {
		t.Fatalf("拒绝失败：%v", err)
	}

	_, err := all.manager.Settle(t.Context(), settleRequest(item.ID, approval.ActionAllowOnce))
	assertCode(t, err, apperr.CodeConflict)

	still, err := all.approvals.ApprovalByID(t.Context(), item.ID)
	if err != nil {
		t.Fatalf("读取审批项失败：%v", err)
	}
	if still.Status != approval.StatusRejected {
		t.Errorf("状态被改成了 %s", still.Status)
	}
}

func TestSettle_RepeatedAllowOnce_ProducesOnlyOneLease(t *testing.T) {
	// 「重复 allow-once 只产生一个 Lease」：第二次 Settle 拿不到 Settlement，
	// 因此没有可以据以签发的东西；即便绕过它，Lease 的唯一索引也会拦下第二条。
	all := newHarness(t)
	item := open(t, all)

	leases := repo.NewLeases(all.db)
	manager, err := lease.NewManager(lease.Options{
		Leases: leases, Clock: all.clock, IDs: ulid.New(all.clock),
	})
	if err != nil {
		t.Fatalf("构造 Lease Manager 失败：%v", err)
	}

	settlement, err := all.manager.Settle(
		t.Context(), settleRequest(item.ID, approval.ActionAllowOnce))
	if err != nil {
		t.Fatalf("放行失败：%v", err)
	}

	granted := grantedScope()
	granted.RequestLimit = settlement.RequestLimit
	if _, issueErr := manager.Issue(t.Context(), lease.IssueRequest{
		Granted: granted, ApprovalID: item.ID,
	}); issueErr != nil {
		t.Fatalf("签发 Lease 失败：%v", issueErr)
	}

	// 第二次提交同一个操作拿回首次的结果并标记为重放（REQ-API-004）——
	// 调用方据此不再签发第二条，而不是收到一个 409。
	replayed, err := all.manager.Settle(
		t.Context(), settleRequest(item.ID, approval.ActionAllowOnce))
	if err != nil {
		t.Fatalf("重复提交同一个操作失败：%v", err)
	}
	if !replayed.Replayed {
		t.Error("第二次提交没有被标记为重放，调用方会据此再签一条 Lease")
	}
	if replayed.Approval.DecidedAt != settlement.Approval.DecidedAt {
		t.Errorf("重放拿到的决定时刻是 %v，首次是 %v",
			replayed.Approval.DecidedAt, settlement.Approval.DecidedAt)
	}

	// 就算有人绕过审批直接签发，唯一索引仍然只允许一条。
	_, err = manager.Issue(t.Context(), lease.IssueRequest{
		Granted: granted, ApprovalID: item.ID,
	})
	assertCode(t, err, apperr.CodeConflict)

	issued, err := leases.LeasesByStatus(t.Context(), lease.StatusActive, 10)
	if err != nil {
		t.Fatalf("列出 Lease 失败：%v", err)
	}
	if len(issued) != 1 {
		t.Fatalf("签发了 %d 条 Lease，期望 1 条", len(issued))
	}
	if issued[0].RequestLimit != 1 {
		t.Errorf("次数上限为 %d，「仅允许这一次」要求 1", issued[0].RequestLimit)
	}
}

func grantedScope() scope.Scope {
	return scope.Scope{
		AgentID:      fixtures.DefaultAgentID,
		WorkspaceID:  fixtures.DefaultWorkspaceID,
		Service:      fixtures.DefaultServiceLabel,
		IdentityID:   fixtures.DefaultIdentityID,
		Account:      "work",
		Resource:     map[string]string{"repo": "Runcoor/opendelo"},
		ResourceKey:  "repo=Runcoor/opendelo",
		Operation:    "pull_request.create",
		NotBefore:    fixtures.Instant,
		ExpiresAt:    fixtures.Instant.Add(scope.DefaultDuration),
		RequestLimit: 1,
		Environment:  matcher.EnvironmentProduction,
		RiskCeiling:  risk.LevelMedium,
	}
}

// ——— 超时（REQ-CAP-003）———

func TestExpire_TurnsUnhandledApprovalsIntoExpired(t *testing.T) {
	// AC1：无人处理的审批在配置时长后状态变为 expired。
	all := newHarness(t)
	item := open(t, all)

	all.clock.Set(item.ExpiresAt.Add(time.Second))
	expired, err := all.manager.Expire(t.Context(), 10)
	if err != nil {
		t.Fatalf("超时清扫失败：%v", err)
	}
	if len(expired) != 1 || expired[0].ID != item.ID {
		t.Fatalf("清扫关闭了 %d 条：%+v", len(expired), expired)
	}
	if expired[0].Status != approval.StatusExpired {
		t.Errorf("状态为 %s，期望 expired", expired[0].Status)
	}
	if expired[0].DecidedAt.IsZero() {
		t.Error("超时的审批项没有记下时刻")
	}
}

func TestExpire_LeavesApprovalsThatAreNotDueYet(t *testing.T) {
	all := newHarness(t)
	item := open(t, all)

	all.clock.Set(item.ExpiresAt.Add(-time.Second))
	expired, err := all.manager.Expire(t.Context(), 10)
	if err != nil {
		t.Fatalf("超时清扫失败：%v", err)
	}
	if len(expired) != 0 {
		t.Errorf("还没到点就关闭了 %d 条", len(expired))
	}
}

func TestExpire_ProducesNothingToIssueALeaseFrom(t *testing.T) {
	// AC2：超时请求不产生 Lease。在这一层表现为 Expire 拿不出 Settlement，
	// 而超时之后再想放行也已经不可能。
	all := newHarness(t)
	item := open(t, all)
	all.clock.Set(item.ExpiresAt.Add(time.Second))

	expired, err := all.manager.Expire(t.Context(), 10)
	if err != nil {
		t.Fatalf("超时清扫失败：%v", err)
	}
	if reflect.TypeOf(expired).Elem() != reflect.TypeOf(approval.Approval{}) {
		t.Fatalf("Expire 返回的是 %v，不该是可以据以签发的 Settlement", reflect.TypeOf(expired))
	}

	_, err = all.manager.Settle(t.Context(), settleRequest(item.ID, approval.ActionAllowOnce))
	assertCode(t, err, apperr.CodeConflict)
}

func TestExpire_TwiceInARow_FindsNothingTheSecondTime(t *testing.T) {
	// 超时不续期：清扫过的审批项不会重新回到待处理。
	all := newHarness(t)
	item := open(t, all)
	all.clock.Set(item.ExpiresAt.Add(time.Second))

	if _, err := all.manager.Expire(t.Context(), 10); err != nil {
		t.Fatalf("第一次清扫失败：%v", err)
	}
	again, err := all.manager.Expire(t.Context(), 10)
	if err != nil {
		t.Fatalf("第二次清扫失败：%v", err)
	}
	if len(again) != 0 {
		t.Errorf("第二次又关闭了 %d 条", len(again))
	}
}

func TestPendingAndExpire_RejectNonPositiveLimits(t *testing.T) {
	all := newHarness(t)

	for _, limit := range []int{0, -1} {
		if _, err := all.manager.Pending(t.Context(), limit); err == nil {
			t.Errorf("上限 %d 的列表查询被接受了", limit)
		}
		if _, err := all.manager.Expire(t.Context(), limit); err == nil {
			t.Errorf("上限 %d 的清扫被接受了", limit)
		}
	}
}

// ——— 重复提交（REQ-API-004 的「返回首次结果」）———

func TestSettle_RepeatingTheSameActionReturnsTheFirstResult(t *testing.T) {
	// REQ-API-004 行为要求：重复调用不产生第二次决策，返回首次结果。
	// Console 的重复点击与网络重发不该变成一次 409。
	for _, action := range []approval.Action{
		approval.ActionDeny, approval.ActionAllowOnce,
		approval.ActionAllowUntilTaskEnd, approval.ActionAutoAllowInProject,
	} {
		t.Run(string(action), func(t *testing.T) {
			all := newHarness(t)
			item := open(t, all)

			first, err := all.manager.Settle(t.Context(), settleRequest(item.ID, action))
			if err != nil {
				t.Fatalf("首次提交失败：%v", err)
			}
			if first.Replayed {
				t.Error("首次提交被标记成了重放")
			}

			second, err := all.manager.Settle(t.Context(), settleRequest(item.ID, action))
			if err != nil {
				t.Fatalf("重复提交失败：%v", err)
			}
			if !second.Replayed {
				t.Error("重复提交没有被标记为重放，调用方会据此再做一次后果")
			}
			if second.Allowed != first.Allowed {
				t.Errorf("重放的结论是 %v，首次是 %v", second.Allowed, first.Allowed)
			}
			if second.Approval.Status != first.Approval.Status {
				t.Errorf("重放的状态是 %s，首次是 %s",
					second.Approval.Status, first.Approval.Status)
			}
			if second.Approval.DecidedAt != first.Approval.DecidedAt {
				t.Error("重放改写了决定时刻")
			}
		})
	}
}

func TestSettle_SwitchingToAnotherActionIsStillAConflict(t *testing.T) {
	// 重放只对**同一个**操作成立。换一个操作仍然是冲突，
	// 否则「拒绝之后不能被改成允许」就没了。
	cases := []struct {
		first  approval.Action
		second approval.Action
	}{
		{approval.ActionAllowOnce, approval.ActionDeny},
		{approval.ActionAllowOnce, approval.ActionAllowUntilTaskEnd},
		{approval.ActionDeny, approval.ActionAllowOnce},
		{approval.ActionAutoAllowInProject, approval.ActionAllowOnce},
	}

	for _, testCase := range cases {
		t.Run(string(testCase.first)+" 之后 "+string(testCase.second), func(t *testing.T) {
			all := newHarness(t)
			item := open(t, all)

			if _, err := all.manager.Settle(
				t.Context(), settleRequest(item.ID, testCase.first)); err != nil {
				t.Fatalf("首次提交失败：%v", err)
			}

			_, err := all.manager.Settle(t.Context(), settleRequest(item.ID, testCase.second))
			assertCode(t, err, apperr.CodeConflict)
		})
	}
}

func TestSettle_OnAnExpiredApprovalIsAConflictEvenForTheSameAction(t *testing.T) {
	// 超时不是任何人做出的决定。把它当成「你刚才提交过的那次拒绝」
	// 会让账本上少掉「没人来处理」这个事实，而超时也不产生 Lease。
	all := newHarness(t)
	item := open(t, all)

	all.clock.Advance(approval.DefaultTimeout + time.Second)
	if _, err := all.manager.Expire(t.Context(), 10); err != nil {
		t.Fatalf("超时清扫失败：%v", err)
	}

	// Expire 用的正是 ActionDeny，所以这是「同一个操作」那条路径。
	_, err := all.manager.Settle(t.Context(), settleRequest(item.ID, approval.ActionDeny))
	assertCode(t, err, apperr.CodeConflict)

	still, err := all.approvals.ApprovalByID(t.Context(), item.ID)
	if err != nil {
		t.Fatalf("读取审批项失败：%v", err)
	}
	if still.Status != approval.StatusExpired {
		t.Errorf("状态被改成了 %s", still.Status)
	}
}
